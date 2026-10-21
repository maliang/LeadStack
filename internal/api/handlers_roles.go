package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/maliang/leadstack/internal/auth"
	"github.com/maliang/leadstack/internal/store"
)

// 角色字段约束（契约 §52.3）。
const (
	maxRoleNameRunes        = 32
	maxRoleDescriptionRunes = 200
)

// scopePayload 是角色资源范围的 JSON 形态。
type scopePayload struct {
	Mode      string   `json:"mode"`
	GroupIDs  []string `json:"group_ids"`
	ServerIDs []string `json:"server_ids"`
}

// rolePayload 是角色的对外表示（内置与自定义统一）。
type rolePayload struct {
	Key         string       `json:"key"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Permissions []string     `json:"permissions"`
	Builtin     bool         `json:"builtin"`
	Scope       scopePayload `json:"scope"`
	UserCount   int          `json:"user_count"`
	CreatedAt   int64        `json:"created_at,omitempty"`
	UpdatedAt   int64        `json:"updated_at,omitempty"`
}

// handleListPermissions 处理 GET /api/v1/permissions（role.manage）。
func (a *API) handleListPermissions(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, auth.Permissions())
}

// handleListRoles 处理 GET /api/v1/roles（role.manage）。
//
// 内置角色来自代码常量（scope 恒为 all），自定义角色来自 roles 表。
func (a *API) handleListRoles(w http.ResponseWriter, r *http.Request) {
	custom, err := a.st.ListRoles(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	roleUsers, err := a.roleUserCounts(r)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]rolePayload, 0, len(custom)+3)
	for _, builtin := range auth.BuiltinRoles() {
		out = append(out, rolePayload{
			Key:         builtin.Key,
			Name:        builtin.Name,
			Description: builtin.Description,
			Permissions: builtin.Permissions,
			Builtin:     true,
			Scope:       scopePayload{Mode: auth.ScopeAll, GroupIDs: []string{}, ServerIDs: []string{}},
			UserCount:   roleUsers[builtin.Key],
		})
	}
	for _, role := range custom {
		out = append(out, rolePayload{
			Key:         role.Key,
			Name:        role.Name,
			Description: role.Description,
			Permissions: role.Permissions,
			Builtin:     false,
			Scope: scopePayload{
				Mode:      role.ScopeMode,
				GroupIDs:  nonNilStrings(role.GroupIDs),
				ServerIDs: nonNilStrings(role.ServerIDs),
			},
			UserCount: roleUsers[role.Key],
			CreatedAt: role.CreatedAt,
			UpdatedAt: role.UpdatedAt,
		})
	}
	writeData(w, http.StatusOK, out)
}

// roleUserCounts 统计每个角色的用户数。
func (a *API) roleUserCounts(r *http.Request) (map[string]int, error) {
	users, err := a.st.ListUsers(r.Context())
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(users))
	for _, u := range users {
		out[u.Role]++
	}
	return out, nil
}

// handleCreateRole 处理 POST /api/v1/roles（role.manage）。
func (a *API) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string       `json:"name"`
		Description string       `json:"description"`
		Permissions []string     `json:"permissions"`
		Scope       scopePayload `json:"scope"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := validateText("name", name, maxRoleNameRunes, true); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	description := strings.TrimSpace(req.Description)
	if len([]rune(description)) > maxRoleDescriptionRunes {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			fmt.Sprintf("描述不能超过 %d 个字符", maxRoleDescriptionRunes))
		return
	}
	perms, err := auth.NormalizePermissions(req.Permissions)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	scope, err := a.validateScope(r, req.Scope)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	now := a.nowMS()
	role := &store.Role{
		Key:         newRoleKey(),
		Name:        name,
		Description: description,
		Permissions: perms,
		ScopeMode:   scope.Mode,
		GroupIDs:    scope.GroupIDs,
		ServerIDs:   scope.ServerIDs,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := a.st.CreateRole(r.Context(), role); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.invalidateAuthz()
	a.auditManual(r, "role.create", "role", role.Key,
		fmt.Sprintf(`{"name":%q,"permissions":%d,"scope":%q}`, role.Name, len(role.Permissions), role.ScopeMode))
	stored, err := a.st.GetRole(r.Context(), role.Key)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, rolePayload{
		Key: stored.Key, Name: stored.Name, Description: stored.Description,
		Permissions: stored.Permissions, Builtin: false,
		Scope:     scopePayload{Mode: stored.ScopeMode, GroupIDs: nonNilStrings(stored.GroupIDs), ServerIDs: nonNilStrings(stored.ServerIDs)},
		CreatedAt: stored.CreatedAt, UpdatedAt: stored.UpdatedAt,
	})
}

// handlePatchRole 处理 PATCH /api/v1/roles/{roleKey}（role.manage）。
func (a *API) handlePatchRole(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(chi.URLParam(r, "roleKey"))
	if _, ok := auth.BuiltinRole(key); ok {
		writeError(w, http.StatusForbidden, codeForbidden, "内置角色不可修改")
		return
	}
	var req struct {
		Name        *string       `json:"name"`
		Description *string       `json:"description"`
		Permissions []string      `json:"permissions"`
		Scope       *scopePayload `json:"scope"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	upd := store.RoleUpdate{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if err := validateText("name", name, maxRoleNameRunes, true); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		upd.Name = &name
	}
	if req.Description != nil {
		description := strings.TrimSpace(*req.Description)
		if len([]rune(description)) > maxRoleDescriptionRunes {
			writeError(w, http.StatusBadRequest, codeInvalidArgument,
				fmt.Sprintf("描述不能超过 %d 个字符", maxRoleDescriptionRunes))
			return
		}
		upd.Description = &description
	}
	if req.Permissions != nil {
		perms, perr := auth.NormalizePermissions(req.Permissions)
		if perr != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, perr.Error())
			return
		}
		upd.Permissions = perms
	}
	if req.Scope != nil {
		scope, serr := a.validateScope(r, *req.Scope)
		if serr != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, serr.Error())
			return
		}
		upd.ScopeMode = &scope.Mode
		upd.GroupIDs = scope.GroupIDs
		upd.ServerIDs = scope.ServerIDs
	}
	updated, err := a.st.UpdateRole(r.Context(), key, upd, a.nowMS())
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.invalidateAuthz()
	a.auditManual(r, "role.update", "role", key, fmt.Sprintf(`{"name":%q}`, updated.Name))
	writeData(w, http.StatusOK, rolePayload{
		Key: updated.Key, Name: updated.Name, Description: updated.Description,
		Permissions: updated.Permissions, Builtin: false,
		Scope:     scopePayload{Mode: updated.ScopeMode, GroupIDs: nonNilStrings(updated.GroupIDs), ServerIDs: nonNilStrings(updated.ServerIDs)},
		CreatedAt: updated.CreatedAt, UpdatedAt: updated.UpdatedAt,
	})
}

// handleDeleteRole 处理 DELETE /api/v1/roles/{roleKey}（role.manage）。
func (a *API) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(chi.URLParam(r, "roleKey"))
	if _, ok := auth.BuiltinRole(key); ok {
		writeError(w, http.StatusForbidden, codeForbidden, "内置角色不可删除")
		return
	}
	count, err := a.st.CountUsersByRole(r.Context(), key)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if count > 0 {
		writeError(w, http.StatusConflict, codeConflict,
			fmt.Sprintf("该角色仍被 %d 个用户使用，请先改派角色", count))
		return
	}
	if err := a.st.DeleteRole(r.Context(), key); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.invalidateAuthz()
	a.auditManual(r, "role.delete", "role", key, "")
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

// validateScope 校验并规整角色范围（契约 §53.2）。
func (a *API) validateScope(r *http.Request, in scopePayload) (scopePayload, error) {
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	if mode == "" {
		mode = auth.ScopeAll
	}
	if !auth.ValidScopeMode(mode) {
		return scopePayload{}, fmt.Errorf("scope.mode 只能是 all|groups|servers")
	}
	groupIDs := dedupeTrimmed(in.GroupIDs)
	serverIDs := dedupeTrimmed(in.ServerIDs)
	if mode == auth.ScopeAll {
		return scopePayload{Mode: auth.ScopeAll, GroupIDs: []string{}, ServerIDs: []string{}}, nil
	}
	if len(groupIDs)+len(serverIDs) == 0 {
		return scopePayload{}, errors.New("受限范围必须至少指定一个分组或服务器")
	}
	if len(groupIDs) > 0 {
		missing, err := a.st.GroupsExist(r.Context(), groupIDs)
		if err != nil {
			return scopePayload{}, err
		}
		if len(missing) > 0 {
			return scopePayload{}, fmt.Errorf("分组不存在: %s", strings.Join(missing, ", "))
		}
	}
	if len(serverIDs) > 0 {
		missing, err := a.st.ServersExist(r.Context(), serverIDs)
		if err != nil {
			return scopePayload{}, err
		}
		if len(missing) > 0 {
			return scopePayload{}, fmt.Errorf("服务器不存在: %s", strings.Join(missing, ", "))
		}
	}
	return scopePayload{Mode: mode, GroupIDs: groupIDs, ServerIDs: serverIDs}, nil
}

// newRoleKey 生成自定义角色 key：r_ + 8 位小写十六进制。
func newRoleKey() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		// 极少数情况下退回纳秒时间戳（仍然唯一到 32 位以上）。
		return fmt.Sprintf("r_%08x", uint32(time.Now().UnixNano()))
	}
	return "r_" + hex.EncodeToString(buf)
}

// dedupeTrimmed 去空白、去重并保持顺序。
func dedupeTrimmed(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
