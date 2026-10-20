package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/store"
)

// API Token 参数（契约第 38 节）。
const (
	apiTokenBytes  = 32
	apiTokenPrefix = "lsk_"
	// defaultTokenExpiresIn 是未指定有效期时的默认值（90 天）。
	defaultTokenExpiresIn = 90 * 24 * time.Hour
	maxTokenExpiresIn     = 5 * 365 * 24 * time.Hour
)

// apiTokenPayload 是 token 的对外表示（绝不包含明文或哈希）。
type apiTokenPayload struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Role       string  `json:"role"`
	CreatedBy  *string `json:"created_by"`
	CreatedAt  int64   `json:"created_at"`
	LastUsedAt *int64  `json:"last_used_at"`
	ExpiresAt  *int64  `json:"expires_at"`
	RevokedAt  *int64  `json:"revoked_at"`
}

// createTokenResponse 只在创建时返回一次明文。
type createTokenResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Token     string `json:"token"`
	ExpiresAt *int64 `json:"expires_at"`
	CreatedAt int64  `json:"created_at"`
}

// handleListAPITokens 处理 GET /api/v1/api-tokens（admin）。
func (a *API) handleListAPITokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := a.st.ListAPITokens(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]apiTokenPayload, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, apiTokenPayload{
			ID: t.ID, Name: t.Name, Role: t.Role, CreatedBy: t.CreatedBy,
			CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt,
			ExpiresAt: t.ExpiresAt, RevokedAt: t.RevokedAt,
		})
	}
	writeData(w, http.StatusOK, out)
}

// handleCreateAPIToken 处理 POST /api/v1/api-tokens（admin）。
func (a *API) handleCreateAPIToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		Role      string `json:"role"`
		ExpiresIn string `json:"expires_in"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := validateText("name", name, 128, true); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = roleOperator
	}
	if roleRank(role) == 0 {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "role 只能是 admin|operator|viewer")
		return
	}
	// Token 默认不允许高于创建者自身的角色（防止 operator 造 admin token 提权）。
	creator, _ := actorFromRequest(r)
	if creator != nil && roleRank(role) > roleRank(creator.Role) {
		writeError(w, http.StatusForbidden, codeForbidden, "不能创建高于自身角色的 API Token")
		return
	}

	ttl := defaultTokenExpiresIn
	if strings.TrimSpace(req.ExpiresIn) != "" {
		parsed, perr := time.ParseDuration(strings.TrimSpace(req.ExpiresIn))
		if perr != nil || parsed <= 0 || parsed > maxTokenExpiresIn {
			writeError(w, http.StatusBadRequest, codeInvalidArgument,
				"expires_in 必须是合法的 Go duration（如 720h），且不超过 5 年")
			return
		}
		ttl = parsed
	}

	plainBytes := make([]byte, apiTokenBytes)
	if _, err := rand.Read(plainBytes); err != nil {
		a.internalError(w, r, fmt.Errorf("生成 API Token: %w", err))
		return
	}
	plain := apiTokenPrefix + base64.RawURLEncoding.EncodeToString(plainBytes)
	sum := sha256.Sum256([]byte(plain))
	nowMS := a.nowMS()
	expiresAt := nowMS + ttl.Milliseconds()
	createdBy := actorName(r)
	token := &store.APIToken{
		ID: uuid.NewString(), Name: name, TokenHash: hex.EncodeToString(sum[:]), Role: role,
		CreatedBy: &createdBy, CreatedAt: nowMS, ExpiresAt: &expiresAt,
	}
	if err := a.st.CreateAPIToken(r.Context(), token); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("创建 API Token", "token_id", token.ID, "name", token.Name, "role", token.Role)
	// 明文只在这里返回一次。
	writeData(w, http.StatusCreated, createTokenResponse{
		ID: token.ID, Name: token.Name, Role: token.Role,
		Token: plain, ExpiresAt: token.ExpiresAt, CreatedAt: token.CreatedAt,
	})
}

// handleDeleteAPIToken 处理 DELETE /api/v1/api-tokens/{id}（admin）。
func (a *API) handleDeleteAPIToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "tokenID")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少 Token ID")
		return
	}
	if err := a.st.RevokeAPIToken(r.Context(), id, a.nowMS()); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("撤销 API Token", "token_id", id)
	writeData(w, http.StatusOK, map[string]any{"id": id, "revoked": true})
}
