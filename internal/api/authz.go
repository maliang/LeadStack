package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/go-chi/chi/v5"

	"github.com/maliang/leadstack/internal/auth"
	"github.com/maliang/leadstack/internal/store"
)

// accessScope 是角色的资源范围（契约 §53.2）。
type accessScope struct {
	Mode      string   `json:"mode"`
	GroupIDs  []string `json:"group_ids"`
	ServerIDs []string `json:"server_ids"`
}

// authz 是一次请求的授权上下文：权限 + 范围 + 预解析的可见服务器集合。
type authz struct {
	Role        string
	RoleName    string
	Permissions []string
	Scope       accessScope
	// scopeAll 为 true 表示不受范围限制（allowed 为空表示全部）。
	scopeAll bool
	// allowed 是范围内服务器集合；scopeAll 时为空。
	allowed map[string]struct{}
}

// authzEntry 是角色授权信息的缓存项（gen 变化即失效）。
type authzEntry struct {
	gen int64
	az  *authz
}

// invalidateAuthz 让角色/分组/成员变更后立即失效缓存。
func (a *API) invalidateAuthz() {
	a.authzGen.Add(1)
}

// authzForRole 解析角色授权信息（内置角色走常量，自定义角色读库），带进程内缓存。
func (a *API) authzForRole(ctx context.Context, roleKey string) (*authz, error) {
	key := strings.ToLower(strings.TrimSpace(roleKey))
	gen := a.authzGen.Load()
	if v, ok := a.authzCache.Load(key); ok {
		if e, ok := v.(*authzEntry); ok && e.gen == gen {
			return e.az, nil
		}
	}
	az, err := a.buildAuthz(ctx, key)
	if err != nil {
		return nil, err
	}
	a.authzCache.Store(key, &authzEntry{gen: gen, az: az})
	return az, nil
}

// buildAuthz 从常量或数据库构建授权信息。
func (a *API) buildAuthz(ctx context.Context, roleKey string) (*authz, error) {
	if builtin, ok := auth.BuiltinRole(roleKey); ok {
		return &authz{
			Role: builtin.Key, RoleName: builtin.Name, Permissions: builtin.Permissions,
			Scope:    accessScope{Mode: auth.ScopeAll, GroupIDs: []string{}, ServerIDs: []string{}},
			scopeAll: true,
		}, nil
	}
	role, err := a.st.GetRole(ctx, roleKey)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// 角色被删或写错：不给任何权限（拒绝优先）。
			return &authz{
				Role: roleKey, RoleName: roleKey,
				Scope: accessScope{Mode: auth.ScopeGroups, GroupIDs: []string{}, ServerIDs: []string{}},
			}, nil
		}
		return nil, fmt.Errorf("查询角色 %s: %w", roleKey, err)
	}
	az := &authz{
		Role:        role.Key,
		RoleName:    role.Name,
		Permissions: role.Permissions,
		Scope: accessScope{
			Mode:      role.ScopeMode,
			GroupIDs:  role.GroupIDs,
			ServerIDs: role.ServerIDs,
		},
	}
	if az.Scope.Mode == "" {
		az.Scope.Mode = auth.ScopeAll
	}
	if az.Scope.Mode == auth.ScopeAll {
		az.scopeAll = true
		return az, nil
	}
	ids, err := a.st.ServerIDsInScope(ctx, az.Scope.GroupIDs, az.Scope.ServerIDs)
	if err != nil {
		return nil, err
	}
	az.allowed = make(map[string]struct{}, len(ids))
	for _, id := range ids {
		az.allowed[id] = struct{}{}
	}
	return az, nil
}

// authzCtxKey 是授权上下文的键。
type authzCtxKey struct{}

// authzFromRequest 取出当前请求的授权上下文。
func authzFromRequest(r *http.Request) (*authz, bool) {
	if r == nil {
		return nil, false
	}
	az, ok := r.Context().Value(authzCtxKey{}).(*authz)
	return az, ok
}

// withAuthz 把授权上下文写入请求。
func withAuthz(ctx context.Context, az *authz) context.Context {
	return context.WithValue(ctx, authzCtxKey{}, az)
}

// hasPerm 判断当前调用者是否具备某权限。
func (a *API) hasPerm(r *http.Request, perm string) bool {
	az, ok := authzFromRequest(r)
	if !ok {
		return false
	}
	return auth.HasPermission(az.Permissions, perm)
}

// scopeIsAll 判断当前调用者是否不受资源范围限制。
func (a *API) scopeIsAll(r *http.Request) bool {
	az, ok := authzFromRequest(r)
	return ok && az.scopeAll
}

// allowedServers 返回范围内服务器集合；nil 表示全部可见。
func (a *API) allowedServers(r *http.Request) map[string]struct{} {
	az, ok := authzFromRequest(r)
	if !ok {
		return map[string]struct{}{}
	}
	if az.scopeAll {
		return nil
	}
	return az.allowed
}

// canAccessServer 判断服务器是否在范围内。
func (a *API) canAccessServer(r *http.Request, serverID string) bool {
	allowed := a.allowedServers(r)
	if allowed == nil {
		return true
	}
	_, ok := allowed[serverID]
	return ok
}

// RequirePerm 要求调用者具备指定权限；缺失返回 403（契约 §52.4）。
//
// 与 RequireRole 不同：这里判断的是能力，而不是角色等级。
func (a *API) RequirePerm(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			az, ok := authzFromRequest(r)
			if !ok {
				writeError(w, http.StatusUnauthorized, codeUnauthorized, "缺少有效的登录凭证")
				return
			}
			if !auth.HasPermission(az.Permissions, perm) {
				a.logger.Warn("越权访问被拒绝",
					"user", actorName(r), "role", az.Role, "required_perm", perm, "path", r.URL.Path)
				writeError(w, http.StatusForbidden, codeForbidden,
					fmt.Sprintf("当前角色 %s 缺少权限 %s", az.RoleName, perm))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireGlobalScope 要求调用者不受资源范围限制（备份、系统设置等全局资源用）。
func (a *API) RequireGlobalScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.scopeIsAll(r) {
			writeError(w, http.StatusForbidden, codeForbidden, "该操作需要全局资源范围，当前角色被限制在部分服务器")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireServerInScope 是服务器子资源路由的统一中间件：
// 目标服务器不在调用者范围内时返回 404（不泄露存在性，契约 §53.3）。
func (a *API) requireServerInScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverID := chi.URLParam(r, "serverID")
		if serverID != "" && !a.canAccessServer(r, serverID) {
			writeError(w, http.StatusNotFound, codeNotFound, "服务器不存在")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requirePermInScope 在写操作上组合「权限 + 服务器范围」校验：
// 目标不在范围内返回 403（写越界，契约 §53.3）。
func (a *API) requireServersInScope(w http.ResponseWriter, r *http.Request, serverIDs []string) bool {
	allowed := a.allowedServers(r)
	if allowed == nil {
		return true
	}
	for _, id := range serverIDs {
		if _, ok := allowed[id]; !ok {
			writeError(w, http.StatusForbidden, codeForbidden,
				"目标服务器不在当前角色的资源范围内")
			return false
		}
	}
	return true
}

// authzGen 是授权缓存的版本号（新增字段见 api.go）。

// roleDisplayNames 返回 role key -> 展示名（内置走常量，自定义读库）。
func (a *API) roleDisplayNames(ctx context.Context, keys []string) map[string]string {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if _, ok := out[key]; ok {
			continue
		}
		if az, err := a.authzForRole(ctx, key); err == nil {
			out[key] = az.RoleName
			continue
		}
		out[key] = key
	}
	return out
}

// bumpAuthzGen 在改变权限/范围数据后失效缓存（供 handler 调用）。
var _ = atomic.Int64{}

// roleExists 判断角色 key 是否存在（内置或自定义）。
func (a *API) roleExists(ctx context.Context, key string) (bool, error) {
	if _, ok := auth.BuiltinRole(key); ok {
		return true, nil
	}
	if _, err := a.st.GetRole(ctx, key); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// canAccessAnyServer 判断给定服务器集合里是否有任一在调用者范围内。
//
// 空集合 + 范围非全局 → 不可见（规则级对象只对全局范围可见，契约 §53.3）。
func (a *API) canAccessAnyServer(r *http.Request, serverIDs []string) bool {
	allowed := a.allowedServers(r)
	if allowed == nil {
		return true
	}
	for _, id := range serverIDs {
		if _, ok := allowed[id]; ok {
			return true
		}
	}
	return false
}

// filterServerIDsByScope 返回集合里可见的服务器 ID（保持原顺序）。
func (a *API) filterServerIDsByScope(r *http.Request, serverIDs []string) []string {
	allowed := a.allowedServers(r)
	if allowed == nil {
		return serverIDs
	}
	out := make([]string, 0, len(serverIDs))
	for _, id := range serverIDs {
		if _, ok := allowed[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// serverVisibleByScope 判断单个可选服务器 ID 是否可见（nil 表示规则级/全局对象）。
func (a *API) optionalServerVisible(r *http.Request, serverID *string) bool {
	if a.scopeIsAll(r) {
		return true
	}
	if serverID == nil || strings.TrimSpace(*serverID) == "" {
		return false
	}
	return a.canAccessServer(r, *serverID)
}

// writeNotFoundIfInvisible 在不可见时写 404 并返回 false（供对象级校验使用）。
func writeNotFoundIfInvisible(w http.ResponseWriter, visible bool) bool {
	if !visible {
		writeError(w, http.StatusNotFound, codeNotFound, "对象不存在")
		return false
	}
	return true
}

// checkVisible 判断检测项是否与调用者资源范围有交集（范围=all 恒 true）。
func (a *API) checkVisible(r *http.Request, checkID string) bool {
	if a.scopeIsAll(r) {
		return true
	}
	targets, err := a.st.ListCheckTargets(r.Context(), checkID)
	if err != nil {
		a.logger.Warn("查询检测项目标失败", "check_id", checkID, "err", err)
		return false
	}
	return a.canAccessAnyServer(r, targets)
}

// taskVisible 判断定时任务是否与调用者资源范围有交集。
func (a *API) taskVisible(r *http.Request, serverIDs []string) bool {
	return a.canAccessAnyServer(r, serverIDs)
}
