package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/credential"
	"github.com/maliang/leadstack/internal/store"
)

// credentialPayload 是凭证的对外表示。
//
// 安全红线：**绝不包含** secret_enc、密码、私钥或 passphrase 的任何形态。
type credentialPayload struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	Username    string  `json:"username"`
	Fingerprint *string `json:"fingerprint"`
	CreatedAt   int64   `json:"created_at"`
	ServerCount int     `json:"server_count"`
}

// createCredentialRequest 是创建凭证请求体。
type createCredentialRequest struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	PrivateKey string `json:"private_key"`
	Passphrase string `json:"passphrase"`
}

// handleListCredentials 处理 GET /api/v1/credentials。
func (a *API) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	items, err := a.st.ListCredentials(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]credentialPayload, 0, len(items))
	for _, item := range items {
		out = append(out, toCredentialPayload(item))
	}
	writeData(w, http.StatusOK, out)
}

// handleCreateCredential 处理 POST /api/v1/credentials。
func (a *API) handleCreateCredential(w http.ResponseWriter, r *http.Request) {
	if a.cipher == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "凭证加密未启用（缺少主密钥）")
		return
	}

	var req createCredentialRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}

	name := strings.TrimSpace(req.Name)
	if err := validateText("name", name, maxCredentialNameRunes, true); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if kind != string(credential.KindPassword) && kind != string(credential.KindKey) {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "kind 只能是 password 或 key")
		return
	}
	username := strings.TrimSpace(req.Username)
	if err := validateText("username", username, maxCredentialUserRunes, true); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}

	secret := credential.Secret{
		Kind:       credential.Kind(kind),
		Username:   username,
		Password:   req.Password,
		Passphrase: req.Passphrase,
	}
	if strings.TrimSpace(req.PrivateKey) != "" {
		secret.PrivateKey = []byte(req.PrivateKey)
	}
	if err := credential.Validate(secret); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "凭证内容不合法: "+err.Error())
		return
	}

	// 指纹只对私钥有意义；password 类型为 NULL。
	var fingerprint *string
	if secret.Kind == credential.KindKey {
		fp, err := credential.Fingerprint(secret.PrivateKey)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "私钥指纹计算失败: "+err.Error())
			return
		}
		fingerprint = &fp
	}

	plain, err := credential.Marshal(secret)
	if err != nil {
		a.internalError(w, r, fmt.Errorf("序列化凭证: %w", err))
		return
	}
	encrypted, err := a.cipher.Encrypt(plain)
	if err != nil {
		a.internalError(w, r, fmt.Errorf("加密凭证: %w", err))
		return
	}
	// 明文用完即弃，不保留引用（Go 无法保证立即清零，但至少不写入日志/响应）。
	plain = nil

	item := &store.Credential{
		ID:          uuid.NewString(),
		Name:        name,
		Kind:        kind,
		Username:    username,
		SecretEnc:   encrypted,
		Fingerprint: fingerprint,
		CreatedAt:   a.nowMS(),
	}
	if err := a.st.CreateCredential(r.Context(), item); err != nil {
		a.writeStoreError(w, r, err)
		return
	}

	a.logger.Info("创建凭证", "credential_id", item.ID, "name", item.Name, "kind", item.Kind, "fingerprint", derefString(fingerprint))
	writeData(w, http.StatusCreated, toCredentialPayload(store.CredentialSummary{Credential: *item, ServerCount: 0}))
}

// handleDeleteCredential 处理 DELETE /api/v1/credentials/{id}。
//
// 仍被服务器引用时返回 409 conflict（HTTP 层由 writeStoreError 映射）。
func (a *API) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "credentialID")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少凭证 ID")
		return
	}
	if err := a.st.DeleteCredential(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrConflict) {
			a.logger.Warn("删除凭证被拒绝：仍被引用", "credential_id", id)
		}
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("删除凭证", "credential_id", id)
	writeData(w, http.StatusOK, map[string]any{"id": id})
}

// toCredentialPayload 把存储模型转换为对外结构（不含任何密钥material）。
func toCredentialPayload(item store.CredentialSummary) credentialPayload {
	return credentialPayload{
		ID:          item.ID,
		Name:        item.Name,
		Kind:        item.Kind,
		Username:    item.Username,
		Fingerprint: item.Fingerprint,
		CreatedAt:   item.CreatedAt,
		ServerCount: item.ServerCount,
	}
}

// derefString 安全解引用可空字符串（仅用于日志里的非敏感字段）。
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
