package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/maliang/leadstack/internal/auth"
	"github.com/maliang/leadstack/internal/store"
)

// loginRequest 是登录请求体。
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// userPayload 是对外暴露的用户信息（不含密码哈希）。
type userPayload struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	RoleName  string `json:"role_name,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`
	IsSelf    bool   `json:"is_self,omitempty"`
	// 登录保护状态（契约 §54）。
	Locked         bool  `json:"locked"`
	LockedUntil    int64 `json:"locked_until"`
	FailedAttempts int   `json:"failed_attempts"`
	// 仅 /auth/me 与登录响应携带：当前角色的权限与资源范围（契约 §55.6）。
	Permissions []string      `json:"permissions,omitempty"`
	Scope       *scopePayload `json:"scope,omitempty"`
}

// loginResponse 是登录成功响应。
type loginResponse struct {
	Token     string      `json:"token"`
	ExpiresAt int64       `json:"expires_at"`
	User      userPayload `json:"user"`
}

// handleLogin 处理 POST /api/v1/auth/login。
//
// 安全顺序（契约 §54）：IP 限流 → 进程内用户名临时锁定 → 账号持久化锁定 →
// 密码校验（失败计数/触发锁定）→ 成功清零并签发令牌。
func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "用户名与密码不能为空")
		return
	}
	if len(req.Password) > auth.MaxPasswordBytes {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "密码长度超出上限")
		return
	}

	ctx := r.Context()
	remote := clientIP(r)

	// 1) IP 维度限流（进程内滑动窗口）。
	if ok, retry := a.login.allowIP(remote); !ok {
		a.auditLoginSecurity(r, req.Username, "auth.login_rate_limited",
			fmt.Sprintf(`{"ip":%q,"retry_after_s":%d}`, remote, int(retry.Seconds())))
		a.logger.Warn("登录被限流", "username", req.Username, "remote", remote)
		writeRateLimited(w, "rate_limited", "登录请求过于频繁，请稍后再试", retry, nil)
		return
	}

	// 2) 未知用户名也做进程内临时锁定（不落库，仅防爆破与用户名枚举）。
	if locked, retry := a.login.unknownLocked(req.Username); locked {
		a.auditLoginSecurity(r, req.Username, "auth.login_locked",
			fmt.Sprintf(`{"reason":"unknown_user","retry_after_s":%d}`, int(retry.Seconds())))
		writeRateLimited(w, "login_locked", "账号已锁定，请稍后再试", retry, nil)
		return
	}

	user, err := a.st.GetUserRecordByUsername(ctx, req.Username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// 不区分“用户不存在”与“密码错误”，并补齐哈希耗时的差值，避免用户名枚举。
			auth.WastePasswordTime(req.Password)
			locked, remaining := a.login.noteUnknownFailure(req.Username)
			a.logger.Warn("登录失败", "username", req.Username, "remote", remote, "reason", "用户不存在")
			a.auditLogin(r, req.Username, false, "用户不存在")
			if locked {
				retry := a.cfg.LoginLockDuration
				a.auditLoginSecurity(r, req.Username, "auth.login_locked",
					fmt.Sprintf(`{"reason":"unknown_user","retry_after_s":%d}`, int(retry.Seconds())))
				writeRateLimited(w, "login_locked", "账号已锁定，请稍后再试", retry, nil)
				return
			}
			writeLoginFailure(w, "用户名或密码错误", remaining)
			return
		}
		a.internalError(w, r, err)
		return
	}

	nowMS := a.nowMS()

	// 3) 账号持久化锁定：锁定期内即使密码正确也拒绝。
	if user.LockedUntil > nowMS {
		retry := time.Duration(user.LockedUntil-nowMS) * time.Millisecond
		a.logger.Warn("登录失败", "username", req.Username, "remote", remote, "reason", "账号锁定中")
		a.auditLogin(r, req.Username, false, "账号锁定中")
		a.auditLoginSecurity(r, req.Username, "auth.login_locked",
			fmt.Sprintf(`{"reason":"locked","locked_until":%d}`, user.LockedUntil))
		writeRateLimited(w, "login_locked", "账号已锁定，请稍后再试", retry, map[string]any{
			"locked_until": user.LockedUntil,
		})
		return
	}

	if !auth.VerifyPassword(user.PasswordHash, req.Password) {
		failed, lockedUntil, ferr := a.st.RecordLoginFailure(ctx, user.ID, nowMS,
			a.cfg.LoginMaxAttempts, int64(a.cfg.LoginLockDuration/time.Millisecond))
		if ferr != nil {
			a.internalError(w, r, ferr)
			return
		}
		a.logger.Warn("登录失败", "username", req.Username, "remote", remote, "reason", "密码错误")
		a.auditLogin(r, req.Username, false, "密码错误")
		if lockedUntil > nowMS {
			retry := time.Duration(lockedUntil-nowMS) * time.Millisecond
			a.auditLoginSecurity(r, req.Username, "auth.login_locked",
				fmt.Sprintf(`{"reason":"wrong_password","failed":%d,"locked_until":%d}`, failed, lockedUntil))
			a.logger.Warn("账号已锁定", "username", req.Username, "locked_until", lockedUntil)
			writeRateLimited(w, "login_locked", "账号已锁定，请稍后再试", retry, map[string]any{
				"locked_until": lockedUntil,
				"failed":       failed,
			})
			return
		}
		remaining := a.cfg.LoginMaxAttempts - failed
		if a.cfg.LoginMaxAttempts <= 0 {
			remaining = 0
		}
		writeLoginFailure(w, "用户名或密码错误", remaining)
		return
	}

	// 4) 成功：清零失败计数与锁定。
	if err := a.st.ResetLoginState(ctx, user.ID, nowMS); err != nil {
		a.logger.Warn("登录成功后清理失败计数失败", "user_id", user.ID, "err", err)
	}
	a.login.resetUnknown(req.Username)

	token, expiresAt, err := a.authMgr.Sign(user.ID, user.Username, user.Role)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	az, azErr := a.authzForRole(ctx, user.Role)
	if azErr != nil {
		a.internalError(w, r, azErr)
		return
	}

	a.logger.Info("登录成功", "username", user.Username, "remote", remote)
	a.auditLogin(r, user.Username, true, "")
	writeData(w, http.StatusOK, loginResponse{
		Token:     token,
		ExpiresAt: expiresAt.UnixMilli(),
		User:      userPayloadWithAuthz(user, az),
	})
}

// userPayloadWithAuthz 组装携带权限与范围的用户载荷。
func userPayloadWithAuthz(user *store.UserRecord, az *authz) userPayload {
	out := userPayload{
		ID: user.ID, Username: user.Username, Role: user.Role,
		RoleName: az.RoleName, Permissions: nonNilStrings(az.Permissions),
	}
	if az != nil {
		scope := scopePayload{
			Mode:      az.Scope.Mode,
			GroupIDs:  nonNilStrings(az.Scope.GroupIDs),
			ServerIDs: nonNilStrings(az.Scope.ServerIDs),
		}
		out.Scope = &scope
	}
	return out
}

// handleMe 处理 GET /api/v1/auth/me。
//
// 除身份外还返回当前角色的权限与资源范围（契约 §55.6），前端据此做门控。
func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "缺少有效的登录凭证")
		return
	}
	record, err := a.st.GetUserRecord(r.Context(), actor.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// API Token 没有对应 user 行：用 token 自身信息构造载荷。
			az, azErr := a.authzForRole(r.Context(), actor.Role)
			if azErr != nil {
				a.internalError(w, r, azErr)
				return
			}
			writeData(w, http.StatusOK, userPayload{
				ID: actor.ID, Username: actor.Username, Role: actor.Role,
				RoleName: az.RoleName, Permissions: nonNilStrings(az.Permissions),
				Scope: scopeOf(az),
			})
			return
		}
		a.internalError(w, r, err)
		return
	}
	az, err := a.authzForRole(r.Context(), record.Role)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, userPayloadWithAuthz(record, az))
}

// scopeOf 把授权范围转成对外结构。
func scopeOf(az *authz) *scopePayload {
	if az == nil {
		return nil
	}
	return &scopePayload{
		Mode:      az.Scope.Mode,
		GroupIDs:  nonNilStrings(az.Scope.GroupIDs),
		ServerIDs: nonNilStrings(az.Scope.ServerIDs),
	}
}

// writeLoginFailure 输出 401 登录失败，并在仍有剩余次数时给出 remaining_attempts。
func writeLoginFailure(w http.ResponseWriter, message string, remaining int) {
	details := map[string]any{}
	if remaining > 0 {
		details["remaining_attempts"] = remaining
	}
	writeErrorDetails(w, http.StatusUnauthorized, codeUnauthorized, message, details)
}

// writeRateLimited 输出 429 并带上 Retry-After 头。
func writeRateLimited(w http.ResponseWriter, code, message string, retry time.Duration, extra map[string]any) {
	seconds := int(retry.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", fmt.Sprintf("%d", seconds))
	details := map[string]any{"retry_after_s": seconds}
	for k, v := range extra {
		details[k] = v
	}
	writeErrorDetails(w, http.StatusTooManyRequests, code, message, details)
}

// writeErrorDetails 输出带 details 的错误体（契约 §54 需要剩余次数/重试秒数）。
//
// request_id 与 writeError 同源（响应头 X-Request-Id）。登录失败/限流是用户最常撞到的
// 错误路径，没有请求号等于断掉「用户报障 → 日志」这条排查链。
func writeErrorDetails(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	errBody := map[string]any{
		"code":       code,
		"message":    message,
		"request_id": errorRequestID(w),
	}
	if len(details) > 0 {
		errBody["details"] = details
	}
	writeJSON(w, status, map[string]any{"error": errBody})
}

// auditLoginSecurity 记录登录保护相关事件（限流/锁定），actor 为尝试登录的用户名。
func (a *API) auditLoginSecurity(r *http.Request, username, action, detail string) {
	entry := &store.AuditLog{TS: a.nowMS(), Actor: username, Action: action}
	if entry.Actor == "" {
		entry.Actor = "anonymous"
	}
	if method := r.Method; method != "" {
		entry.Method = &method
	}
	if path := r.URL.Path; path != "" {
		entry.Path = &path
	}
	if ip := clientIP(r); ip != "" {
		entry.IP = &ip
	}
	if ua := r.UserAgent(); ua != "" {
		entry.UserAgent = &ua
	}
	if detail != "" {
		entry.Detail = &detail
	}
	a.writeAudit(entry)
}

// handleLogout 处理 POST /api/v1/auth/logout。
//
// JWT 无状态，服务端只做记录，令牌由前端清除。
func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	if claims, ok := auth.ClaimsFromRequest(r); ok {
		a.logger.Info("用户登出", "username", claims.Username)
	}
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}
