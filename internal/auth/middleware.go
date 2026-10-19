package auth

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// ctxKey 是私有上下文键类型，避免与其他包冲突。
type ctxKey int

const claimsCtxKey ctxKey = iota

// BearerToken 从 Authorization 头解析 Bearer 令牌；大小写不敏感。
func BearerToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if raw == "" {
		return ""
	}
	const prefix = "bearer "
	if len(raw) <= len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(raw[len(prefix):])
}

// ClaimsFromRequest 取出 RequireAuth 放进上下文的令牌载荷。
func ClaimsFromRequest(r *http.Request) (*Claims, bool) {
	if r == nil {
		return nil, false
	}
	claims, ok := r.Context().Value(claimsCtxKey).(*Claims)
	return claims, ok
}

// WithClaims 把令牌载荷写入上下文（供中间件与测试使用）。
func WithClaims(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, claimsCtxKey, claims)
}

// RequireAuth 是校验 Bearer JWT 的中间件；失败返回 401 与统一错误体。
func (m *Manager) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := BearerToken(r)
		if token == "" {
			WriteJSONError(w, http.StatusUnauthorized, "unauthorized", "缺少 Authorization: Bearer 令牌")
			return
		}
		claims, err := m.Parse(token)
		if err != nil {
			slog.Debug("令牌校验失败", "path", r.URL.Path, "err", err)
			WriteJSONError(w, http.StatusUnauthorized, "unauthorized", "令牌无效或已过期")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithClaims(r.Context(), claims)))
	})
}

// RequireRole 是 RBAC 中间件：要求当前用户角色在允许集合内。
//
// 必须挂在 RequireAuth 之后；授权信息缺失时按 401 处理，角色不匹配按 403 处理。
func RequireRole(allowed ...string) func(http.Handler) http.Handler {
	allowSet := make(map[string]struct{}, len(allowed))
	for _, role := range allowed {
		allowSet[role] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFromRequest(r)
			if !ok {
				WriteJSONError(w, http.StatusUnauthorized, "unauthorized", "缺少有效的登录凭证")
				return
			}
			if _, ok := allowSet[claims.Role]; !ok {
				WriteJSONError(w, http.StatusForbidden, "forbidden", "当前角色无权执行该操作")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WriteJSONError 输出契约约定的错误体 {"error":{"code","message"}}。
func WriteJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	body := map[string]any{"error": map[string]string{"code": code, "message": message}}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Warn("写入错误响应失败", "err", err)
	}
}
