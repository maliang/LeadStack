package api

import (
	"errors"
	"net/http"
	"strings"

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
	Disabled  bool   `json:"disabled,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`
	IsSelf    bool   `json:"is_self,omitempty"`
}

// loginResponse 是登录成功响应。
type loginResponse struct {
	Token     string      `json:"token"`
	ExpiresAt int64       `json:"expires_at"`
	User      userPayload `json:"user"`
}

// handleLogin 处理 POST /api/v1/auth/login。
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
	user, err := a.st.GetUserByUsername(ctx, req.Username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// 不区分“用户不存在”与“密码错误”，并补齐哈希耗时的差值，避免用户名枚举。
			auth.WastePasswordTime(req.Password)
			a.logger.Warn("登录失败", "username", req.Username, "remote", remoteHost(r), "reason", "用户不存在")
			a.auditLogin(r, req.Username, false, "用户不存在")
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "用户名或密码错误")
			return
		}
		a.internalError(w, r, err)
		return
	}
	if !auth.VerifyPassword(user.PasswordHash, req.Password) {
		a.logger.Warn("登录失败", "username", req.Username, "remote", remoteHost(r), "reason", "密码错误")
		a.auditLogin(r, req.Username, false, "密码错误")
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "用户名或密码错误")
		return
	}

	token, expiresAt, err := a.authMgr.Sign(user.ID, user.Username, user.Role)
	if err != nil {
		a.internalError(w, r, err)
		return
	}

	a.logger.Info("登录成功", "username", user.Username, "remote", remoteHost(r))
	a.auditLogin(r, user.Username, true, "")
	writeData(w, http.StatusOK, loginResponse{
		Token:     token,
		ExpiresAt: expiresAt.UnixMilli(),
		User:      userPayload{ID: user.ID, Username: user.Username, Role: user.Role},
	})
}

// handleMe 处理 GET /api/v1/auth/me。
func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	if actor, ok := actorFromRequest(r); ok {
		writeData(w, http.StatusOK, userPayload{ID: actor.ID, Username: actor.Username, Role: actor.Role})
		return
	}
	claims, ok := auth.ClaimsFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "缺少有效的登录凭证")
		return
	}
	user, err := a.st.GetUserByID(r.Context(), claims.Subject)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// 用户已被删除，令牌随之失效。
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "用户不存在或已被删除")
			return
		}
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, userPayload{ID: user.ID, Username: user.Username, Role: user.Role})
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
