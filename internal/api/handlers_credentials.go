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
	"time"

	"github.com/maliang/leadstack/internal/panel/baota"
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
	// 宝塔面板凭据的可回显字段（api_key 永不返回）。
	PanelURL           string `json:"panel_url,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
}

// createCredentialRequest 是创建凭证请求体。
type createCredentialRequest struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	PrivateKey string `json:"private_key"`
	Passphrase string `json:"passphrase"`

	// 宝塔面板凭据（kind=baota）：面板地址 + API 密钥 + 是否跳过证书校验。
	PanelURL           string `json:"panel_url"`
	APIKey             string `json:"api_key"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
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
		out = append(out, a.enrichCredentialPayload(item))
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
	switch kind {
	case string(credential.KindPassword), string(credential.KindKey), string(credential.KindBaota):
	default:
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "kind 只能是 password / key / baota")
		return
	}
	username := strings.TrimSpace(req.Username)
	if kind == string(credential.KindBaota) {
		// 宝塔面板凭据不使用 SSH 用户名；缺省填面板占位值以满足表结构。
		if username == "" {
			username = "baota"
		}
	} else if err := validateText("username", username, maxCredentialUserRunes, true); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}

	secret := credential.Secret{
		Kind:       credential.Kind(kind),
		Username:   username,
		Password:   req.Password,
		Passphrase: req.Passphrase,
	}
	if kind == string(credential.KindBaota) {
		secret.PanelURL = strings.TrimSpace(req.PanelURL)
		secret.APIKey = strings.TrimSpace(req.APIKey)
		secret.InsecureSkipVerify = req.InsecureSkipVerify
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
	writeData(w, http.StatusCreated, a.enrichCredentialPayload(store.CredentialSummary{Credential: *item, ServerCount: 0}))
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

// enrichCredentialPayload 在通用 payload 上补充宝塔凭据的可回显字段。
//
// 只回显面板地址与「是否跳过证书校验」；api_key 属于机密，任何接口都不返回。
func (a *API) enrichCredentialPayload(item store.CredentialSummary) credentialPayload {
	out := toCredentialPayload(item)
	if out.Kind != string(credential.KindBaota) || a.cipher == nil {
		return out
	}
	plain, err := a.cipher.Decrypt(item.SecretEnc)
	if err != nil {
		return out
	}
	secret, err := credential.Unmarshal(plain)
	if err != nil {
		return out
	}
	out.PanelURL = secret.PanelURL
	out.InsecureSkipVerify = secret.InsecureSkipVerify
	return out
}

// derefString 安全解引用可空字符串（仅用于日志里的非敏感字段）。
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// handleTestCredential 处理 POST /api/v1/credentials/{credentialID}/test。
//
// 目前支持宝塔面板凭据的连通性测试（用真实面板 API 做一次 GetSystemTotal）；
// SSH 凭据请用「服务器 → 测试连通性」，因为那还涉及主机密钥策略。
func (a *API) handleTestCredential(w http.ResponseWriter, r *http.Request) {
	if a.cipher == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "凭证加密未启用（缺少主密钥）")
		return
	}
	id := chi.URLParam(r, "credentialID")
	cred, err := a.st.GetCredential(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if cred.Kind != string(credential.KindBaota) {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			"该凭证不是宝塔面板类型；SSH 凭证请到服务器详情做「测试连通性」")
		return
	}
	plain, err := a.cipher.Decrypt(cred.SecretEnc)
	if err != nil {
		a.internalError(w, r, fmt.Errorf("解密凭证: %w", err))
		return
	}
	secret, err := credential.Unmarshal(plain)
	if err != nil {
		a.internalError(w, r, fmt.Errorf("解析凭证: %w", err))
		return
	}
	client, err := baota.New(baota.Config{
		BaseURL:            secret.PanelURL,
		APIKey:             secret.APIKey,
		InsecureSkipVerify: secret.InsecureSkipVerify,
		Timeout:            12 * time.Second,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	if err := client.Ping(r.Context()); err != nil {
		a.logger.Warn("宝塔面板连通性测试失败", "credential_id", cred.ID, "err", err)
		writeData(w, http.StatusOK, map[string]any{"ok": false, "message": "面板连接失败：" + err.Error()})
		return
	}
	writeData(w, http.StatusOK, map[string]any{"ok": true, "message": "面板连接正常"})
}
