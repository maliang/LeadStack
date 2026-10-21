package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/auth"
	"github.com/maliang/leadstack/internal/store"
)

// 用户管理字段约束。
const (
	maxUsernameRunes = 64
	minPasswordBytes = 8
)

// handleListUsers 处理 GET /api/v1/users（admin）。
func (a *API) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.st.ListUsers(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	self := actorID(r)
	names := make([]string, 0, len(users))
	for _, u := range users {
		names = append(names, u.Role)
	}
	roleNames := a.roleDisplayNames(r.Context(), names)
	nowMS := a.nowMS()
	out := make([]userPayload, 0, len(users))
	for _, u := range users {
		out = append(out, userPayload{
			ID: u.ID, Username: u.Username, Role: u.Role, RoleName: roleNames[u.Role],
			Disabled: u.Disabled, CreatedAt: u.CreatedAt, IsSelf: u.ID == self,
			Locked:         u.LockedUntil > nowMS,
			LockedUntil:    u.LockedUntil,
			FailedAttempts: u.FailedAttempts,
		})
	}
	writeData(w, http.StatusOK, out)
}

// handleCreateUser 处理 POST /api/v1/users（admin）。
func (a *API) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	username := strings.TrimSpace(req.Username)
	if err := validateText("username", username, maxUsernameRunes, true); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	if len(req.Password) < minPasswordBytes {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			fmt.Sprintf("密码长度不能少于 %d 个字符", minPasswordBytes))
		return
	}
	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = roleViewer
	}
	exists, rerr := a.roleExists(r.Context(), role)
	if rerr != nil {
		a.internalError(w, r, rerr)
		return
	}
	if !exists {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "role 不存在（须为内置角色或已创建的自定义角色）")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "密码不合法: "+err.Error())
		return
	}
	user := &store.User{
		ID: uuid.NewString(), Username: username, PasswordHash: hash,
		Role: role, CreatedAt: a.nowMS(),
	}
	if err := a.st.CreateUser(r.Context(), user); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("创建用户", "user_id", user.ID, "username", user.Username, "role", user.Role)
	writeData(w, http.StatusCreated, userPayload{
		ID: user.ID, Username: user.Username, Role: user.Role, CreatedAt: user.CreatedAt,
	})
}

// handlePatchUser 处理 PATCH /api/v1/users/{id}（admin）。
func (a *API) handlePatchUser(w http.ResponseWriter, r *http.Request) {
	target, err := a.loadUser(w, r)
	if err != nil {
		return
	}
	var req struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
		Password *string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	if req.Role == nil && req.Disabled == nil && req.Password == nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "至少需要提供 role/disabled/password 之一")
		return
	}
	upd := store.UserUpdate{}
	if req.Role != nil {
		role := strings.TrimSpace(*req.Role)
		exists, rerr := a.roleExists(r.Context(), role)
		if rerr != nil {
			a.internalError(w, r, rerr)
			return
		}
		if !exists {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "role 不存在（须为内置角色或已创建的自定义角色）")
			return
		}
		// 降级最后一个 admin 会锁死系统，直接拒绝。
		if target.Role == roleAdmin && role != roleAdmin {
			if err := a.ensureNotLastAdmin(r, target.ID, "不能降级最后一个管理员"); err != nil {
				a.writeStoreError(w, r, err)
				return
			}
		}
		upd.Role = &role
	}
	if req.Disabled != nil {
		if *req.Disabled {
			if target.ID == actorID(r) {
				writeError(w, http.StatusBadRequest, codeInvalidArgument, "不能禁用自己")
				return
			}
			if target.Role == roleAdmin {
				if err := a.ensureNotLastAdmin(r, target.ID, "不能禁用最后一个管理员"); err != nil {
					a.writeStoreError(w, r, err)
					return
				}
			}
		}
		upd.Disabled = req.Disabled
	}
	if req.Password != nil {
		if len(*req.Password) < minPasswordBytes {
			writeError(w, http.StatusBadRequest, codeInvalidArgument,
				fmt.Sprintf("密码长度不能少于 %d 个字符", minPasswordBytes))
			return
		}
		hash, herr := auth.HashPassword(*req.Password)
		if herr != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "密码不合法: "+herr.Error())
			return
		}
		upd.PasswordHash = &hash
	}
	updated, err := a.st.UpdateUser(r.Context(), target.ID, upd, a.nowMS())
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if req.Password != nil {
		// 管理员重置密码后同时解除锁定，避免「密码换了还被锁着」。
		if err := a.st.ResetLoginState(r.Context(), target.ID, a.nowMS()); err != nil {
			a.logger.Warn("重置密码后清理登录锁定失败", "user_id", target.ID, "err", err)
		}
		a.login.resetUnknown(updated.Username)
		updated.FailedAttempts = 0
		updated.LockedUntil = 0
	}
	a.logger.Info("更新用户", "user_id", updated.ID, "role", updated.Role, "disabled", updated.Disabled)
	roleNames := a.roleDisplayNames(r.Context(), []string{updated.Role})
	writeData(w, http.StatusOK, userPayload{
		ID: updated.ID, Username: updated.Username, Role: updated.Role, RoleName: roleNames[updated.Role],
		Disabled: updated.Disabled, CreatedAt: updated.CreatedAt,
		Locked:         updated.LockedUntil > a.nowMS(),
		LockedUntil:    updated.LockedUntil,
		FailedAttempts: updated.FailedAttempts,
	})
}

// handleUnlockUser 处理 POST /api/v1/users/{id}/unlock（user.manage，契约 §54）。
func (a *API) handleUnlockUser(w http.ResponseWriter, r *http.Request) {
	target, err := a.loadUser(w, r)
	if err != nil {
		return
	}
	if err := a.st.UnlockUser(r.Context(), target.ID, a.nowMS()); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.login.resetUnknown(target.Username)
	a.auditManual(r, "user.unlock", "user", target.ID, fmt.Sprintf(`{"username":%q}`, target.Username))
	a.logger.Info("解锁用户", "user_id", target.ID, "username", target.Username)
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteUser 处理 DELETE /api/v1/users/{id}（admin）。
//
// 禁止自删、禁止删除最后一个 admin。
func (a *API) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	target, err := a.loadUser(w, r)
	if err != nil {
		return
	}
	if target.ID == actorID(r) {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "不能删除自己的账号")
		return
	}
	if target.Role == roleAdmin {
		if err := a.ensureNotLastAdmin(r, target.ID, "不能删除最后一个管理员"); err != nil {
			a.writeStoreError(w, r, err)
			return
		}
	}
	if err := a.st.DeleteUser(r.Context(), target.ID); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("删除用户", "user_id", target.ID, "username", target.Username)
	writeData(w, http.StatusOK, map[string]any{"id": target.ID})
}

// handleChangeOwnPassword 处理 POST /api/v1/users/me/password（所有角色）。
func (a *API) handleChangeOwnPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	if req.OldPassword == "" || req.NewPassword == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "old_password 与 new_password 不能为空")
		return
	}
	if len(req.NewPassword) < minPasswordBytes {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			fmt.Sprintf("新密码长度不能少于 %d 个字符", minPasswordBytes))
		return
	}
	if req.OldPassword == req.NewPassword {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "新密码不能与旧密码相同")
		return
	}
	record, err := a.st.GetUserRecord(r.Context(), actorID(r))
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if !auth.VerifyPassword(record.PasswordHash, req.OldPassword) {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "旧密码错误")
		return
	}
	hash, herr := auth.HashPassword(req.NewPassword)
	if herr != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "新密码不合法: "+herr.Error())
		return
	}
	if _, err := a.st.UpdateUser(r.Context(), record.ID, store.UserUpdate{PasswordHash: &hash}, a.nowMS()); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("用户修改密码", "user_id", record.ID)
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

// loadUser 读取路径上的用户。
func (a *API) loadUser(w http.ResponseWriter, r *http.Request) (*store.UserRecord, error) {
	id := chi.URLParam(r, "userID")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少用户 ID")
		return nil, errors.New("缺少用户 ID")
	}
	record, err := a.st.GetUserRecord(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return nil, err
	}
	return record, nil
}

// ensureNotLastAdmin 确认删除/降级该用户后仍有可用管理员。
func (a *API) ensureNotLastAdmin(r *http.Request, targetID, message string) error {
	admins, err := a.st.CountActiveAdmins(r.Context())
	if err != nil {
		return err
	}
	return ensureAdminRemains(admins, message)
}

// ensureAdminRemains 是纯逻辑判断（便于单测）。
func ensureAdminRemains(activeAdmins int, message string) error {
	if activeAdmins <= 1 {
		return fmt.Errorf("%s: %w", message, store.ErrConflict)
	}
	return nil
}
