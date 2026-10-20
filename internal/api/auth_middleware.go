package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/maliang/leadstack/internal/auth"
	"github.com/maliang/leadstack/internal/store"
)

// 角色等级（契约第 37 节）：admin > operator > viewer。
const (
	roleAdmin    = "admin"
	roleOperator = "operator"
	roleViewer   = "viewer"
)

// roleRank 返回角色的权限等级，未知角色为 0（无权限）。
func roleRank(role string) int {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case roleAdmin:
		return 3
	case roleOperator:
		return 2
	case roleViewer:
		return 1
	default:
		return 0
	}
}

// actorClaims 是当前请求的调用者身份（JWT 用户或 API Token）。
type actorClaims struct {
	ID       string
	Username string
	Role     string
}

// requireAuth 是统一认证中间件：同时支持登录 JWT 与 API Token。
//
//   - `Bearer lsk_...` → API Token（库内 sha256 比对，校验撤销/过期，更新 last_used_at）；
//   - 其他 Bearer → JWT（校验签名/过期；用户被禁用则拒绝）。
func (a *API) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := auth.BearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "缺少 Authorization: Bearer 令牌")
			return
		}
		if strings.HasPrefix(token, "lsk_") {
			claims, err := a.authenticateAPIToken(r.Context(), token)
			if err != nil {
				a.logger.Debug("API Token 校验失败", "path", r.URL.Path, "err", err)
				writeError(w, http.StatusUnauthorized, codeUnauthorized, err.Error())
				return
			}
			next.ServeHTTP(w, r.WithContext(withActor(r.Context(), claims)))
			return
		}

		claims, err := a.authMgr.Parse(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "令牌无效或已过期")
			return
		}
		// 用户可能已被禁用或删除：JWT 仍有效也不放行。
		record, err := a.st.GetUserRecord(r.Context(), claims.Subject)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusUnauthorized, codeUnauthorized, "用户不存在或已被删除")
				return
			}
			a.internalError(w, r, err)
			return
		}
		if record.Disabled {
			writeError(w, http.StatusForbidden, codeForbidden, "该用户已被禁用，请联系管理员")
			return
		}
		next.ServeHTTP(w, r.WithContext(withActor(r.Context(), &actorClaims{
			ID: record.ID, Username: record.Username, Role: record.Role,
		})))
	})
}

// authenticateAPIToken 校验 API Token 并更新使用时间。
func (a *API) authenticateAPIToken(ctx context.Context, plain string) (*actorClaims, error) {
	sum := sha256.Sum256([]byte(plain))
	hash := hex.EncodeToString(sum[:])
	token, err := a.st.FindAPITokenByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errors.New("API Token 无效")
		}
		return nil, fmt.Errorf("查询 API Token 失败")
	}
	nowMS := a.nowMS()
	if token.RevokedAt != nil {
		return nil, errors.New("API Token 已被撤销")
	}
	if token.ExpiresAt != nil && *token.ExpiresAt <= nowMS {
		return nil, errors.New("API Token 已过期")
	}
	if err := a.st.TouchAPIToken(ctx, token.ID, nowMS); err != nil {
		a.logger.Warn("更新 API Token 使用时间失败", "token_id", token.ID, "err", err)
	}
	return &actorClaims{ID: token.ID, Username: "token:" + token.Name, Role: token.Role}, nil
}

// actorCtxKey 是当前调用者身份的上下文键。
type actorCtxKey struct{}

// actorHolder 让「外层中间件（审计）」也能拿到内层认证后的调用者身份。
type actorHolder struct {
	mu     sync.Mutex
	claims *actorClaims
}

// set 记录调用者身份。
func (h *actorHolder) set(claims *actorClaims) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.claims = claims
	h.mu.Unlock()
}

// get 读取调用者身份。
func (h *actorHolder) get() *actorClaims {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.claims
}

// actorHolderCtxKey 是 actorHolder 的上下文键。
type actorHolderCtxKey struct{}

// withActor 把调用者身份写入上下文（同时回填 holder，供审计中间件读取）。
func withActor(ctx context.Context, claims *actorClaims) context.Context {
	if holder, ok := ctx.Value(actorHolderCtxKey{}).(*actorHolder); ok {
		holder.set(claims)
	}
	return context.WithValue(ctx, actorCtxKey{}, claims)
}

// actorFromRequest 取出当前调用者身份。
func actorFromRequest(r *http.Request) (*actorClaims, bool) {
	if r == nil {
		return nil, false
	}
	claims, ok := r.Context().Value(actorCtxKey{}).(*actorClaims)
	return claims, ok
}

// RequireRole 要求调用者角色等级不低于 minRole；越权返回 403。
func (a *API) RequireRole(minRole string) func(http.Handler) http.Handler {
	minRank := roleRank(minRole)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := actorFromRequest(r)
			if !ok {
				writeError(w, http.StatusUnauthorized, codeUnauthorized, "缺少有效的登录凭证")
				return
			}
			if roleRank(claims.Role) < minRank {
				a.logger.Warn("越权访问被拒绝",
					"user", claims.Username, "role", claims.Role, "required", minRole, "path", r.URL.Path)
				writeError(w, http.StatusForbidden, codeForbidden,
					fmt.Sprintf("当前角色 %s 无权执行该操作（需要 %s 及以上）", claims.Role, minRole))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// actorName 返回当前调用者用户名（无身份时返回 system）。
func actorName(r *http.Request) string {
	if claims, ok := actorFromRequest(r); ok && claims.Username != "" {
		return claims.Username
	}
	return "system"
}

// actorRole 返回当前调用者角色。
func actorRole(r *http.Request) string {
	if claims, ok := actorFromRequest(r); ok {
		return claims.Role
	}
	return ""
}

// actorID 返回当前调用者 ID。
func actorID(r *http.Request) string {
	if claims, ok := actorFromRequest(r); ok {
		return claims.ID
	}
	return ""
}
