package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/store"
)

// 接入令牌参数约束。
const (
	// enrollTokenBytes 是令牌明文的随机字节数（base64url 后 43 个字符）。
	enrollTokenBytes = 32
	// defaultEnrollTTLS 是请求未指定 ttl_s 时的默认有效期（1 小时）。
	defaultEnrollTTLS = 3600
	// maxEnrollTTLS 是令牌有效期上限（30 天）。
	maxEnrollTTLS = 30 * 24 * 3600
	// enrollTokensListLimit 是列表接口的返回上限。
	enrollTokensListLimit = 200
)

// enrollTokenPayload 是接入令牌的对外表示。
type enrollTokenPayload struct {
	ID        string  `json:"id"`
	Note      string  `json:"note"`
	ExpiresAt int64   `json:"expires_at"`
	UsedAt    *int64  `json:"used_at"`
	ServerID  *string `json:"server_id"`
}

// createEnrollTokenRequest 是新建令牌请求体。
//
// ServerID 是契约之外的可选扩展：填了就预绑定到已有服务器（「先添加服务器、再接入」的流程），
// 不填则由 agent 首次 hello 自动创建服务器记录。
type createEnrollTokenRequest struct {
	Note     string `json:"note"`
	TTLS     int    `json:"ttl_s"`
	ServerID string `json:"server_id"`
}

// createEnrollTokenResponse 只在创建时返回一次明文令牌。
type createEnrollTokenResponse struct {
	ID        string  `json:"id"`
	Token     string  `json:"token"`
	Note      string  `json:"note"`
	ExpiresAt int64   `json:"expires_at"`
	ServerID  *string `json:"server_id"`
}

// installScriptRequest 是生成安装命令的请求体。
type installScriptRequest struct {
	Token     string `json:"token"`
	ServerURL string `json:"server_url"`
}

// installScriptResponse 返回一行可复制的安装命令。
type installScriptResponse struct {
	Command string `json:"command"`
}

// handleListEnrollTokens 处理 GET /api/v1/enroll-tokens。
func (a *API) handleListEnrollTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := a.st.ListEnrollTokens(r.Context(), enrollTokensListLimit)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]enrollTokenPayload, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, enrollTokenPayload{
			ID:        t.ID,
			Note:      t.Note,
			ExpiresAt: t.ExpiresAt,
			UsedAt:    t.UsedAt,
			ServerID:  t.ServerID,
		})
	}
	writeData(w, http.StatusOK, out)
}

// handleCreateEnrollToken 处理 POST /api/v1/enroll-tokens。
func (a *API) handleCreateEnrollToken(w http.ResponseWriter, r *http.Request) {
	var req createEnrollTokenRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}

	note := strings.TrimSpace(req.Note)
	if err := validateText("note", note, 200, false); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}

	ttl := req.TTLS
	if ttl <= 0 {
		ttl = defaultEnrollTTLS
	}
	if ttl < 60 || ttl > maxEnrollTTLS {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			fmt.Sprintf("ttl_s 必须在 60-%d 秒之间", maxEnrollTTLS))
		return
	}

	plain, err := newEnrollToken()
	if err != nil {
		a.internalError(w, r, err)
		return
	}

	nowMS := a.nowMS()
	token := &store.EnrollToken{
		ID:        uuid.NewString(),
		TokenHash: hashEnrollToken(plain),
		Note:      note,
		ExpiresAt: nowMS + int64(ttl)*1000,
	}

	// 可选：预绑定到已有服务器。
	if serverID := strings.TrimSpace(req.ServerID); serverID != "" {
		if _, err := a.st.GetServer(r.Context(), serverID); err != nil {
			a.writeStoreError(w, r, err)
			return
		}
		token.ServerID = &serverID
	}

	if err := a.st.CreateEnrollToken(r.Context(), token); err != nil {
		a.writeStoreError(w, r, err)
		return
	}

	a.logger.Info("生成接入令牌", "token_id", token.ID, "expires_at", token.ExpiresAt, "note", note)
	// 明文只在此处返回一次，库内只留哈希。
	writeData(w, http.StatusCreated, createEnrollTokenResponse{
		ID:        token.ID,
		Token:     plain,
		Note:      token.Note,
		ExpiresAt: token.ExpiresAt,
		ServerID:  token.ServerID,
	})
}

// handleInstallScript 处理 POST /api/v1/agent/install-script。
//
// 生成的命令调用部署脚本（deploy/install.sh），由 devops 侧提供：
//
//	curl -fsSL '<server>/install.sh' | sh -s -- --server '<server>' --token '<token>'
func (a *API) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	var req installScriptRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}

	token := strings.TrimSpace(req.Token)
	if token == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "token 不能为空")
		return
	}
	if !isSafeTokenString(token) {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "token 含非法字符")
		return
	}

	// 对外地址优先级：请求体显式指定 > 设置里的 public_url（改完立即生效）> 请求 Host。
	serverURL := strings.TrimSpace(req.ServerURL)
	if serverURL == "" {
		serverURL = a.publicURL(r.Context())
	}
	if serverURL == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		serverURL = scheme + "://" + r.Host
	}
	base, err := normalizeServerURL(serverURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}

	command := fmt.Sprintf("curl -fsSL '%s/install.sh' | sh -s -- --server '%s' --token '%s'", base, base, token)
	writeData(w, http.StatusOK, installScriptResponse{Command: command})
}

// newEnrollToken 生成随机令牌明文（base64url，无填充）。
func newEnrollToken() (string, error) {
	buf := make([]byte, enrollTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成接入令牌: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashEnrollToken 计算令牌的 SHA-256 十六进制摘要，用于入库与查找。
func hashEnrollToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// isSafeTokenString 限制令牌字符集为 base64url，杜绝命令注入。
func isSafeTokenString(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// normalizeServerURL 校验并归一化控制端地址：仅允许 http/https，去掉末尾斜杠。
func normalizeServerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("server_url 不能为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("server_url 不是合法 URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("server_url 必须以 http:// 或 https:// 开头")
	}
	if u.Host == "" {
		return "", fmt.Errorf("server_url 缺少主机名")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("server_url 不应包含查询串或锚点")
	}
	if !isSafeURLHost(u.Host) {
		return "", fmt.Errorf("server_url 主机名含非法字符")
	}
	return strings.TrimSuffix(u.Scheme+"://"+u.Host+strings.TrimSuffix(u.Path, "/"), "/"), nil
}

// isSafeURLHost 校验 host[:port] 只包含安全字符，避免拼进 shell 命令时注入。
func isSafeURLHost(host string) bool {
	if host == "" || len(host) > 255 {
		return false
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == ':', r == '[', r == ']', r == '_':
		default:
			return false
		}
	}
	return true
}
