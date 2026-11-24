package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/store"
	"github.com/maliang/leadstack/pkg/protocol"

	"github.com/maliang/leadstack/internal/credential"
	"github.com/maliang/leadstack/internal/deploy"
	"github.com/maliang/leadstack/internal/panel/baota"
)

// 服务器字段的长度约束。
const (
	maxServerNameRunes = 128
	maxServerHostRunes = 255
	maxTagRunes        = 64
	maxTagCount        = 32
	maxGroupRunes      = 64
	// maxBatchIDs 限制单次批量操作的服务器数量，避免一次请求打爆 SSH 连接池。
	maxBatchIDs = 200
	// maxLoginScriptRunes 限制服务器登录脚本长度，防止把整段程序塞进库里。
	maxLoginScriptRunes = 8192
	// maxLoginOutputBytes 限制登录脚本回显的字节数，避免一次响应塞进超大输出。
	maxLoginOutputBytes = 64 << 10
	// loginScriptTimeoutS 是登录脚本在目标机上的执行超时（秒）。
	loginScriptTimeoutS = 60
	// maxServerNotesRunes 限制服务器备注长度（迁移 0014）。
	maxServerNotesRunes = 2000
	// maxCustomFieldCount 限制自定义字段的键数量，避免把整张表塞进一个 JSON 列。
	maxCustomFieldCount = 32
	// maxCustomFieldKeyRunes 限制自定义字段的键长度。
	maxCustomFieldKeyRunes = 40
	// maxCustomFieldValueRunes 限制自定义字段的值长度。
	maxCustomFieldValueRunes = 200
	// maxCustomFieldsBytes 限制自定义字段序列化后的字节数。
	maxCustomFieldsBytes = 8 << 10
)

// serverPayload 是服务器的对外表示，字段与契约第 3/10 节一致。
type serverPayload struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	Host           string             `json:"host"`
	Port           int                `json:"port"`
	Mode           string             `json:"mode"`
	Status         string             `json:"status"`
	LastSeenAt     *int64             `json:"last_seen_at"`
	AgentVersion   *string            `json:"agent_version"`
	OS             *string            `json:"os"`
	Arch           *string            `json:"arch"`
	Kernel         *string            `json:"kernel"`
	Tags           []string           `json:"tags"`
	CreatedAt      int64              `json:"created_at"`
	MetricsSummary map[string]float64 `json:"metrics_summary,omitempty"`
	// 纳管 扩展字段。
	CredentialID  *string         `json:"credential_id"`
	Group         *string         `json:"group"`
	HostKeyPolicy string          `json:"host_key_policy"`
	LastError     *string         `json:"last_error"`
	HostInfo      json.RawMessage `json:"host_info"`
	// 一等公民分组（授权单元，契约 §53.1）；与上面的自由文本 group 并存。
	GroupIDs   []string `json:"group_ids"`
	GroupNames []string `json:"group_names"`
	// 宝塔面板凭据（kind=baota）：仅 provider=baota 的应用部署时使用。
	PanelCredentialID *string `json:"panel_credential_id"`
	// LoginScript 是服务器登录脚本（空串表示未配置）。
	LoginScript string `json:"login_script"`
}

// serverDetailPayload 是 GET /api/v1/servers/{id} 的返回体：在列表字段之外追加服务器档案。
//
// notes / custom_fields 只出现在详情接口（列表不返回），避免列表响应被大字段拖慢；
// 两个字段无 omitempty，详情里即使为空也稳定给出，调用方不必猜形状。
type serverDetailPayload struct {
	serverPayload
	Notes        string          `json:"notes"`
	CustomFields json.RawMessage `json:"custom_fields"`
}

// createServerRequest 是新增服务器请求体（纳管 扩展 credential_id/mode/group/tags/host_key_policy）。
type createServerRequest struct {
	Name              string    `json:"name"`
	GroupIDs          *[]string `json:"group_ids"`
	PanelCredentialID *string   `json:"panel_credential_id"`
	Host              string    `json:"host"`
	Port              *int      `json:"port"`
	Mode              string    `json:"mode"`
	CredentialID      *string   `json:"credential_id"`
	Group             *string   `json:"group"`
	Tags              *[]string `json:"tags"`
	HostKeyPolicy     string    `json:"host_key_policy"`
}

// patchServerRequest 是改名/改标签/改分组请求体；nil 表示不改。
type patchServerRequest struct {
	Name          *string   `json:"name"`
	Tags          *[]string `json:"tags"`
	Group         *string   `json:"group"`
	HostKeyPolicy *string   `json:"host_key_policy"`
	CredentialID  *string   `json:"credential_id"`
	// Host / Port 允许事后修改（契约 49.2）：改完需重新测试连通性。
	Host *string `json:"host"`
	Port *int    `json:"port"`
	// GroupIDs 整包替换服务器的一等公民分组归属（缺省表示不改）。
	GroupIDs *[]string `json:"group_ids"`
	// PanelCredentialID 绑定宝塔面板凭据（空串表示解绑）。
	PanelCredentialID *string `json:"panel_credential_id"`
	// LoginScript 设置服务器登录脚本（空串表示清除）。
	LoginScript *string `json:"login_script"`
	// Notes 设置运维备注（<=2000 个字符，允许换行；空串表示清除）。
	Notes *string `json:"notes"`
	// CustomFields 整包替换自定义字段（键值均为字符串；空对象表示清空）。
	CustomFields *map[string]string `json:"custom_fields"`
}

// handleListServers 处理 GET /api/v1/servers，支持 ?group=&tag=&status=&mode= 过滤。
func (a *API) handleListServers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := store.ServerFilter{
		Group:  strings.TrimSpace(r.URL.Query().Get("group")),
		Tag:    strings.TrimSpace(r.URL.Query().Get("tag")),
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Mode:   strings.TrimSpace(r.URL.Query().Get("mode")),
	}
	if filter.Status != "" && !isValidStatus(filter.Status) {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "status 只能是 online|offline|unknown")
		return
	}
	if filter.Mode != "" && filter.Mode != store.ModeAgent && filter.Mode != store.ModeSSH {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "mode 只能是 agent|ssh")
		return
	}

	params, perr := parsePage(r)
	if perr != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, perr.Error())
		return
	}
	nowMS := a.nowMS()
	servers, err := a.st.ListServersFiltered(ctx, filter, nowMS)
	if err != nil {
		a.internalError(w, r, err)
		return
	}

	ids := make([]string, 0, len(servers))
	for _, srv := range servers {
		ids = append(ids, srv.ID)
	}
	metrics, err := a.st.LatestMetrics(ctx, ids)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	// 注意：ListServerHostInfo 传空切片表示「全部」，列表为空时直接跳过查询。
	hostInfos := map[string]store.HostInfoSnapshot{}
	if len(ids) > 0 {
		hostInfos, err = a.st.ListServerHostInfo(ctx, ids)
		if err != nil {
			a.internalError(w, r, err)
			return
		}
	}

	// 资源范围过滤必须在分页之前（契约 §53.3/§53.4：先过滤后分页）。
	servers = a.filterServersByScope(r, servers)
	out := make([]serverPayload, 0, len(servers))
	for _, srv := range servers {
		out = append(out, toServerPayload(srv, nowMS, metrics[srv.ID], hostInfoRaw(hostInfos, srv.ID)))
	}
	if err := a.attachServerGroups(ctx, out); err != nil {
		a.internalError(w, r, err)
		return
	}
	start, end, meta := params.window(len(out))
	if !params.Paged {
		start, end = 0, len(out)
	}
	writePagedList(w, params, out[start:end], meta)
}

// handleGetServer 处理 GET /api/v1/servers/{id}。
func (a *API) handleGetServer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "serverID")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}

	ctx := r.Context()
	srv, err := a.st.GetServer(ctx, id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	metrics, err := a.st.LatestMetrics(ctx, []string{id})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	hostInfos, err := a.st.ListServerHostInfo(ctx, []string{id})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	payloads := []serverPayload{toServerPayload(*srv, a.nowMS(), metrics[id], hostInfoRaw(hostInfos, id))}
	if err := a.attachServerGroups(ctx, payloads); err != nil {
		a.internalError(w, r, err)
		return
	}
	// 详情接口在列表字段之上追加服务器档案（notes / custom_fields）。
	writeData(w, http.StatusOK, serverDetailPayload{
		serverPayload: payloads[0],
		Notes:         srv.Notes,
		CustomFields:  customFieldsRaw(srv.CustomFieldsJSON),
	})
}

// serverAccessPayload 是 GET /api/v1/servers/{id}/access 的返回体。
//
// 这是唯一回显 SSH 登录密码明文的接口：库内只存密文，明文仅在本次响应里出现，
// 不写日志、不写审计 detail。
type serverAccessPayload struct {
	ServerID string `json:"server_id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	AuthType string `json:"auth_type"`
}

// handleServerAccess 处理 GET /api/v1/servers/{id}/access：返回登录信息（含明文密码）。
//
// 鉴权：server.read + 服务器在角色范围内（路由层已挂 inScope）。
// 审计：GET 不在自动审计范围，这里用 auditManual 显式补记 server.access（detail 不含密码）。
func (a *API) handleServerAccess(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "serverID"))
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}
	if a.cipher == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "凭证加密未启用（缺少主密钥）")
		return
	}

	ctx := r.Context()
	srv, err := a.st.GetServer(ctx, id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if srv.CredentialID == nil || strings.TrimSpace(*srv.CredentialID) == "" {
		writeError(w, http.StatusConflict, codeConflict, "服务器未绑定 SSH 凭证，无法获取登录信息")
		return
	}
	cred, err := a.st.GetCredential(ctx, *srv.CredentialID)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	// 宝塔面板凭据不用于 SSH 登录，不能把 api_key 当成密码回显。
	if cred.Kind == string(credential.KindBaota) {
		writeError(w, http.StatusConflict, codeConflict, "服务器绑定的凭证不是 SSH 凭证")
		return
	}
	plain, err := a.cipher.Decrypt(cred.SecretEnc)
	if err != nil {
		a.internalError(w, r, fmt.Errorf("解密服务器 %s 的凭证失败: %w", id, err))
		return
	}
	secret, err := credential.Unmarshal(plain)
	if err != nil {
		a.internalError(w, r, fmt.Errorf("解析服务器 %s 的凭证失败: %w", id, err))
		return
	}
	username := secret.Username
	if username == "" {
		username = cred.Username
	}

	// 敏感读取必须留痕：detail 只带凭证 id 与认证类型，绝不含密码/私钥。
	detail := fmt.Sprintf(`{"credential_id":%q,"auth_type":%q}`, cred.ID, string(secret.Kind))
	a.auditManual(r, auditActionServerAccess, "server", id, detail)

	writeData(w, http.StatusOK, serverAccessPayload{
		ServerID: srv.ID,
		Name:     srv.Name,
		Host:     srv.Host,
		Port:     srv.Port,
		Username: username,
		Password: secret.Password,
		AuthType: string(secret.Kind),
	})
}

// serverPanelPayload 是 GET /api/v1/servers/{id}/panel 的返回体。
//
// 只回显面板地址与认证类型：panel_url 供调用方展示，api_key 永不回显。
type serverPanelPayload struct {
	ServerID string `json:"server_id"`
	PanelURL string `json:"panel_url"`
	AuthType string `json:"auth_type"`
}

// handleServerPanel 处理 GET /api/v1/servers/{id}/panel：返回绑定的宝塔面板地址。
//
// 鉴权：server.read + 服务器在角色范围内（路由层已挂 inScope）。
// 审计：GET 不在自动审计范围，这里用 auditManual 显式补记 server.panel；
// detail 只带 credential_id，绝不含 panel_url 与 api_key。
func (a *API) handleServerPanel(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "serverID"))
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}
	srv, cred, secret, err := a.loadServerPanelSecret(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, errCipherDisabled):
			writeError(w, http.StatusServiceUnavailable, codeInternal, err.Error())
		case errors.Is(err, errPanelUnbound), errors.Is(err, errPanelCredentialKind), errors.Is(err, errPanelURLMissing):
			writeError(w, http.StatusConflict, codeConflict, err.Error())
		default:
			a.writeStoreError(w, r, err)
		}
		return
	}

	a.auditManual(r, auditActionServerPanel, "server", id,
		fmt.Sprintf(`{"credential_id":%q}`, cred.ID))

	writeData(w, http.StatusOK, serverPanelPayload{
		ServerID: srv.ID,
		PanelURL: strings.TrimSpace(secret.PanelURL),
		AuthType: string(secret.Kind),
	})
}

// loadServerPanelSecret 读取服务器绑定的宝塔凭据并解密。
func (a *API) loadServerPanelSecret(ctx context.Context, id string) (*store.Server, *store.Credential, credential.Secret, error) {
	if a.cipher == nil {
		return nil, nil, credential.Secret{}, errCipherDisabled
	}
	srv, err := a.st.GetServer(ctx, id)
	if err != nil {
		return nil, nil, credential.Secret{}, err
	}
	if srv.PanelCredentialID == nil || strings.TrimSpace(*srv.PanelCredentialID) == "" {
		return nil, nil, credential.Secret{}, errPanelUnbound
	}
	cred, err := a.st.GetCredential(ctx, *srv.PanelCredentialID)
	if err != nil {
		return nil, nil, credential.Secret{}, err
	}
	if cred.Kind != string(credential.KindBaota) {
		return nil, nil, credential.Secret{}, errPanelCredentialKind
	}
	plain, err := a.cipher.Decrypt(cred.SecretEnc)
	if err != nil {
		return nil, nil, credential.Secret{}, fmt.Errorf("解密服务器 %s 的面板凭证失败: %w", id, err)
	}
	secret, err := credential.Unmarshal(plain)
	if err != nil {
		return nil, nil, credential.Secret{}, fmt.Errorf("解析服务器 %s 的面板凭证失败: %w", id, err)
	}
	if secret.Kind != credential.KindBaota || strings.TrimSpace(secret.PanelURL) == "" {
		return nil, nil, credential.Secret{}, errPanelURLMissing
	}
	return srv, cred, secret, nil
}

var (
	errCipherDisabled      = errors.New("凭证加密未启用（缺少主密钥）")
	errPanelUnbound        = errors.New("服务器未绑定面板凭证")
	errPanelCredentialKind = errors.New("服务器绑定的不是宝塔面板凭证")
	errPanelURLMissing     = errors.New("服务器绑定的宝塔面板凭证缺少面板地址")
)

// isPanelCertificateError 判断是否为面板自签/未知 CA 的 TLS 校验失败。
func isPanelCertificateError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "x509:") ||
		strings.Contains(msg, "certificate signed by unknown authority") ||
		strings.Contains(msg, "certificate is not trusted")
}

// handleServerPanelLogin 处理 POST /api/v1/servers/{id}/panel-login。
//
// 用面板 API 生成一次性 tmp_token 登录地址（有效期 120 秒），前端立即打开；
// api_key 与 tmp_token 都不写日志、不进响应缓存。
func (a *API) handleServerPanelLogin(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "serverID"))
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}
	_, cred, secret, err := a.loadServerPanelSecret(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, errCipherDisabled):
			writeError(w, http.StatusServiceUnavailable, codeInternal, err.Error())
		case errors.Is(err, errPanelUnbound), errors.Is(err, errPanelCredentialKind), errors.Is(err, errPanelURLMissing):
			writeError(w, http.StatusConflict, codeConflict, err.Error())
		default:
			a.writeStoreError(w, r, err)
		}
		return
	}
	client, err := baota.New(baota.Config{
		BaseURL:            secret.PanelURL,
		APIKey:             secret.APIKey,
		InsecureSkipVerify: secret.InsecureSkipVerify,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	loginURL, err := client.LoginURL(r.Context())
	insecureFallback := false
	tryLogin := func(baseURL string, insecure bool) bool {
		attempt, berr := baota.New(baota.Config{
			BaseURL:            baseURL,
			APIKey:             secret.APIKey,
			InsecureSkipVerify: insecure,
		})
		if berr != nil {
			return false
		}
		nextURL, lerr := attempt.LoginURL(r.Context())
		if lerr != nil {
			return false
		}
		loginURL, insecureFallback, err = nextURL, insecure, nil
		return true
	}
	// 常见现场：面板实际只监听 HTTPS，但录入的是 http:// 地址，表现为
	// connection reset by peer。仅登录场景自动尝试同主机同端口的 HTTPS。
	if err != nil && strings.HasPrefix(strings.ToLower(secret.PanelURL), "http://") {
		httpsURL := "https://" + strings.TrimPrefix(secret.PanelURL, "http://")
		for _, insecure := range []bool{secret.InsecureSkipVerify, true} {
			if tryLogin(httpsURL, insecure) {
				a.logger.Warn("宝塔面板 HTTP 登录失败，已自动改用 HTTPS", "server_id", id)
				break
			}
		}
	}
	// 宝塔默认使用自签证书。登录只是生成一次性 URL，不应因为控制端没有
	// 安装面板 CA 就直接失败；这里做一次「仅本次登录」的跳过校验重试。
	if err != nil && !secret.InsecureSkipVerify && isPanelCertificateError(err) {
		if tryLogin(secret.PanelURL, true) {
			a.logger.Warn("宝塔面板证书校验失败，已为本次登录跳过校验", "server_id", id)
		}
	}
	if err != nil {
		a.logger.Warn("宝塔面板临时登录失败", "server_id", id, "error", err)
		writeError(w, http.StatusBadGateway, codeInternal, "宝塔面板登录失败: "+err.Error())
		return
	}
	a.auditManual(r, "server.panel_login", "server", id,
		fmt.Sprintf(`{"credential_id":%q,"insecure_fallback":%t}`, cred.ID, insecureFallback))
	writeData(w, http.StatusOK, map[string]any{
		"url": loginURL, "expires_in": 120, "insecure_skip_verify": insecureFallback,
	})
}

// handleCreateServer 处理 POST /api/v1/servers。
func (a *API) handleCreateServer(w http.ResponseWriter, r *http.Request) {
	var req createServerRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}

	name := strings.TrimSpace(req.Name)
	if err := validateText("name", name, maxServerNameRunes, true); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	host := strings.TrimSpace(req.Host)
	if err := validateText("host", host, maxServerHostRunes, true); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	// 未显式给端口时用设置里的默认 SSH 端口（servers.default_ssh_port，默认 22）。
	port := a.defaultSSHPortForCreate(r.Context())
	if req.Port != nil {
		port = *req.Port
	}
	if port <= 0 || port > 65535 {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "port 必须在 1-65535 之间")
		return
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = store.ModeAgent
	}
	if mode != store.ModeAgent && mode != store.ModeSSH {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "mode 只能是 agent 或 ssh")
		return
	}

	policy := strings.TrimSpace(req.HostKeyPolicy)
	if policy == "" {
		policy = store.HostKeyPolicyAcceptNew
	}
	if !isValidHostKeyPolicy(policy) {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "host_key_policy 只能是 insecure|accept-new|strict")
		return
	}

	ctx := r.Context()
	// 可选：绑定凭证（mode=ssh 时建议绑定；不存在则直接报错，避免留下悬空引用）。
	var credentialID *string
	if req.CredentialID != nil && strings.TrimSpace(*req.CredentialID) != "" {
		cid := strings.TrimSpace(*req.CredentialID)
		if _, err := a.st.GetCredential(ctx, cid); err != nil {
			a.writeStoreError(w, r, err)
			return
		}
		credentialID = &cid
	}
	// 可选：绑定宝塔面板凭据（provider=baota 的应用部署时使用）。
	panelCredentialID, err := a.validatePanelCredential(ctx, req.PanelCredentialID)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}

	tagsJSON := "[]"
	if req.Tags != nil {
		tags, err := normalizeTags(*req.Tags)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		encoded, err := json.Marshal(tags)
		if err != nil {
			a.internalError(w, r, fmt.Errorf("序列化标签: %w", err))
			return
		}
		tagsJSON = string(encoded)
	}

	var group *string
	if req.Group != nil {
		if g := strings.TrimSpace(*req.Group); g != "" {
			if err := validateText("group", g, maxGroupRunes, false); err != nil {
				writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
				return
			}
			group = &g
		}
	}

	srv := &store.Server{
		ID:                uuid.NewString(),
		Name:              name,
		Host:              host,
		Port:              port,
		Mode:              mode,
		TagsJSON:          tagsJSON,
		Status:            protocol.StatusUnknown,
		CreatedAt:         a.nowMS(),
		CredentialID:      credentialID,
		PanelCredentialID: panelCredentialID,
		Group:             group,
		HostKeyPolicy:     policy,
	}
	if err := a.st.CreateServer(ctx, srv); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if !a.applyServerGroups(ctx, w, r, srv.ID, req.GroupIDs) {
		return
	}
	a.logger.Info("新增服务器",
		"server_id", srv.ID, "name", srv.Name, "host", srv.Host, "mode", srv.Mode, "group", req.Group)
	payloads := []serverPayload{toServerPayload(*srv, a.nowMS(), nil, nil)}
	if err := a.attachServerGroups(ctx, payloads); err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, payloads[0])
}

// handlePatchServer 处理 PATCH /api/v1/servers/{id}。
func (a *API) handlePatchServer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "serverID")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}

	var req patchServerRequest
	if err := decodeJSON(w, r, &req); err != nil {
		// 请求体里可能有 notes 正文或 custom_fields 的值：审计只记「请求体非法」。
		a.setAuditDetail(r, "server.update", `{"invalid_body":true}`)
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	if req.Name == nil && req.Tags == nil && req.Group == nil && req.GroupIDs == nil && req.PanelCredentialID == nil &&
		req.HostKeyPolicy == nil && req.CredentialID == nil &&
		req.Host == nil && req.Port == nil && req.LoginScript == nil &&
		req.Notes == nil && req.CustomFields == nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			"至少需要提供 name/tags/group/host_key_policy/credential_id/host/port/login_script/notes/custom_fields 之一")
		return
	}

	var name *string
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if err := validateText("name", trimmed, maxServerNameRunes, true); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		name = &trimmed
	}

	var tagsJSON *string
	if req.Tags != nil {
		tags, err := normalizeTags(*req.Tags)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		encoded, err := json.Marshal(tags)
		if err != nil {
			a.internalError(w, r, fmt.Errorf("序列化标签: %w", err))
			return
		}
		s := string(encoded)
		tagsJSON = &s
	}

	var policy *string
	if req.HostKeyPolicy != nil {
		p := strings.TrimSpace(*req.HostKeyPolicy)
		if !isValidHostKeyPolicy(p) {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "host_key_policy 只能是 insecure|accept-new|strict")
			return
		}
		policy = &p
	}

	var group *string
	if req.Group != nil {
		g := strings.TrimSpace(*req.Group)
		if err := validateText("group", g, maxGroupRunes, false); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		group = &g
	}

	var loginScript *string
	if req.LoginScript != nil {
		script := strings.TrimSpace(*req.LoginScript)
		if err := validateLoginScript(script); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		loginScript = &script
	}

	// notes / custom_fields：先校验后落盘，非法输入在写任何库之前拒绝。
	var notes *string
	if req.Notes != nil {
		n := strings.TrimSpace(*req.Notes)
		if err := validateServerNotes(n); err != nil {
			// 非法备注不落库，也不把正文写进审计 detail。
			a.setAuditDetail(r, "server.update", `{"invalid_fields":["notes"]}`)
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		notes = &n
	}
	var customFieldsJSON *string
	var customFieldKeys []string
	if req.CustomFields != nil {
		encoded, keys, err := normalizeCustomFields(*req.CustomFields)
		if err != nil {
			// 非法自定义字段不落库，也不把值写进审计 detail。
			a.setAuditDetail(r, "server.update", `{"invalid_fields":["custom_fields"]}`)
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		customFieldsJSON = &encoded
		customFieldKeys = keys
	}

	ctx := r.Context()
	var credentialID *string
	if req.CredentialID != nil {
		cid := strings.TrimSpace(*req.CredentialID)
		if cid != "" {
			if _, err := a.st.GetCredential(ctx, cid); err != nil {
				a.writeStoreError(w, r, err)
				return
			}
		}
		credentialID = &cid // 空字符串表示解绑
	}
	panelCredentialID, err := a.validatePanelCredential(ctx, req.PanelCredentialID)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}

	// host/port：允许事后修改（契约 49.2）。
	current, err := a.st.GetServer(ctx, id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	var hostPtr *string
	if req.Host != nil {
		h := strings.TrimSpace(*req.Host)
		if err := validateText("host", h, maxServerHostRunes, true); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		hostPtr = &h
	}
	var portPtr *int
	if req.Port != nil {
		if err := ValidatePort(*req.Port); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "port "+err.Error())
			return
		}
		portPtr = req.Port
	}
	oldHost, oldPort := current.Host, current.Port
	newHost, newPort := oldHost, oldPort
	if hostPtr != nil {
		newHost = *hostPtr
	}
	if portPtr != nil {
		newPort = *portPtr
	}
	endpointChanged := newHost != oldHost || newPort != oldPort
	if endpointChanged {
		// host:port 变了，旧主机密钥不再可信：清掉该机记录，下次连接重新学习/校验。
		a.forgetKnownHost(ctx, id, oldHost, oldPort)
	}

	srv, err := a.st.UpdateServerFields(ctx, id, store.ServerUpdate{
		Name:              name,
		TagsJSON:          tagsJSON,
		Group:             group,
		HostKeyPolicy:     policy,
		CredentialID:      credentialID,
		PanelCredentialID: panelCredentialID,
		Host:              hostPtr,
		Port:              portPtr,
		LoginScript:       loginScript,
		Notes:             notes,
		CustomFieldsJSON:  customFieldsJSON,
	})
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	metrics, err := a.st.LatestMetrics(ctx, []string{id})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	hostInfos, err := a.st.ListServerHostInfo(ctx, []string{id})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	// 审计：继续用 server.update，但 detail 只记键名不记值（notes 正文与
	// custom_fields 的值都不落审计，避免备注/自定义字段里的敏感信息外泄）。
	if endpointChanged || notes != nil || customFieldsJSON != nil {
		detail := serverUpdateAuditDetail(endpointChanged, oldHost, oldPort, newHost, newPort, notes != nil, customFieldKeys)
		a.setAuditDetail(r, "server.update", detail)
		if endpointChanged {
			a.logger.Info("服务器地址已变更，已清除旧主机密钥",
				"server_id", id, "old", fmt.Sprintf("%s:%d", oldHost, oldPort),
				"new", fmt.Sprintf("%s:%d", newHost, newPort))
		}
	}
	if !a.applyServerGroups(ctx, w, r, id, req.GroupIDs) {
		return
	}
	payloads := []serverPayload{toServerPayload(*srv, a.nowMS(), metrics[id], hostInfoRaw(hostInfos, id))}
	if err := a.attachServerGroups(ctx, payloads); err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, payloads[0])
}

// handleDeleteServer 处理 DELETE /api/v1/servers/{id}。
func (a *API) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "serverID")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}
	if err := a.st.DeleteServer(r.Context(), id); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("删除服务器", "server_id", id)
	writeData(w, http.StatusOK, map[string]any{"id": id})
}

// toServerPayload 把存储模型转换为对外结构，并实时推导在线状态。
func toServerPayload(srv store.Server, nowMS int64, metrics map[string]float64, hostInfo json.RawMessage) serverPayload {
	out := serverPayload{
		ID:                srv.ID,
		Name:              srv.Name,
		Host:              srv.Host,
		Port:              srv.Port,
		Mode:              srv.Mode,
		Status:            deriveStatus(srv, nowMS),
		LastSeenAt:        srv.LastSeenAt,
		AgentVersion:      srv.AgentVersion,
		OS:                srv.OS,
		Arch:              srv.Arch,
		Kernel:            srv.Kernel,
		Tags:              parseTags(srv.TagsJSON),
		CreatedAt:         srv.CreatedAt,
		CredentialID:      srv.CredentialID,
		Group:             srv.Group,
		HostKeyPolicy:     srv.HostKeyPolicy,
		LastError:         srv.LastError,
		PanelCredentialID: srv.PanelCredentialID,
		LoginScript:       srv.LoginScript,
		HostInfo:          hostInfo,
	}
	if len(metrics) > 0 {
		out.MetricsSummary = metrics
	}
	return out
}

// hostInfoRaw 从快照 map 中取出某台服务器的主机信息 JSON；不存在返回 nil（JSON 序列化为 null）。
func hostInfoRaw(snapshots map[string]store.HostInfoSnapshot, serverID string) json.RawMessage {
	snap, ok := snapshots[serverID]
	if !ok || strings.TrimSpace(snap.InfoJSON) == "" {
		return nil
	}
	raw := json.RawMessage(snap.InfoJSON)
	if !json.Valid(raw) {
		return nil
	}
	return raw
}

// deriveStatus 按契约第 10 节推导状态（ssh 模式以采集器裁决为准，agent 模式按心跳阈值）。
func deriveStatus(srv store.Server, nowMS int64) string {
	return store.EffectiveStatus(srv, nowMS)
}

// isValidStatus 校验状态过滤值。
func isValidStatus(s string) bool {
	switch s {
	case protocol.StatusOnline, protocol.StatusOffline, protocol.StatusUnknown:
		return true
	default:
		return false
	}
}

// isValidHostKeyPolicy 校验主机密钥策略。
func isValidHostKeyPolicy(s string) bool {
	switch s {
	case store.HostKeyPolicyInsecure, store.HostKeyPolicyAcceptNew, store.HostKeyPolicyStrict:
		return true
	default:
		return false
	}
}

// parseTags 把库里的 JSON 文本还原为字符串切片；异常数据按空标签处理。
func parseTags(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return []string{}
	}
	if tags == nil {
		return []string{}
	}
	return tags
}

// normalizeTags 校验并归一化标签：去空白、去重、限制条数与长度。
func normalizeTags(in []string) ([]string, error) {
	if len(in) > maxTagCount {
		return nil, fmt.Errorf("tags 最多 %d 个", maxTagCount)
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, tag := range in {
		t := strings.TrimSpace(tag)
		if t == "" {
			continue
		}
		if utf8.RuneCountInString(t) > maxTagRunes {
			return nil, fmt.Errorf("标签 %q 长度超过 %d 个字符", t, maxTagRunes)
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out, nil
}

// validateText 校验文本字段长度，required 为 true 时不允许为空。
func validateText(field, value string, maxRunes int, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s 不能为空", field)
		}
		return nil
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%s 长度超过 %d 个字符", field, maxRunes)
	}
	if strings.ContainsAny(value, "\r\n\t") {
		return fmt.Errorf("%s 不能包含控制字符", field)
	}
	return nil
}

// validateMode 校验连接模式，限制为小写字母/数字/下划线/短横线。
func validateMode(mode string) error {
	if len(mode) > 32 {
		return errors.New("mode 长度超过 32 个字符")
	}
	for _, r := range mode {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return fmt.Errorf("mode %q 含非法字符（只允许小写字母、数字、_ 与 -）", mode)
		}
	}
	return nil
}

// validatePanelCredential 校验服务器要绑定的宝塔面板凭据。
//
// 语义：nil 表示不改；空串表示解绑；非空则必须是已存在的 kind=baota 凭据。
func (a *API) validatePanelCredential(ctx context.Context, raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	id := strings.TrimSpace(*raw)
	if id == "" {
		empty := ""
		return &empty, nil
	}
	cred, err := a.st.GetCredential(ctx, id)
	if err != nil {
		return nil, errors.New("面板凭据不存在")
	}
	if cred.Kind != string(credential.KindBaota) {
		return nil, errors.New("只能绑定宝塔面板类型（baota）的凭据")
	}
	return &id, nil
}

// handleDiscoverServer 处理 POST /api/v1/servers/{serverID}/discover（接管向导的只读探测）。
func (a *API) handleDiscoverServer(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	if serverID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}
	srv, err := a.st.GetServer(r.Context(), serverID)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if !a.requireServersInScope(w, r, []string{srv.ID}) {
		return
	}
	prober, ok := a.deploy.(interface {
		Discover(context.Context, string) (*DiscoveryReport, error)
	})
	if !ok {
		// 控制端本地没装配执行器：503 unavailable，不是目标机的 502。
		writeUnavailable(w, "发布执行器未启用，无法探测目标机")
		return
	}
	report, err := prober.Discover(r.Context(), serverID)
	if err != nil {
		if errors.Is(err, errOrchestratorDisabled) {
			// 本地装配缺陷（执行器存在但执行端为空），同样不能算远端失败。
			writeUnavailable(w, "发布执行器未启用，无法探测目标机")
			return
		}
		// 目标机不可达/拒绝连接属于远端失败，统一 502 + remote_failure（A19）。
		a.writeRemoteError(w, r, err, "探测")
		return
	}
	writeData(w, http.StatusOK, report)
}

// serverLoginPayload 是 POST /api/v1/servers/{id}/login 的返回体。
//
// 脚本约定输出一个 JSON 对象；解析失败时整段输出落到 output，url/terminal 为空。
type serverLoginPayload struct {
	ServerID string `json:"server_id"`
	URL      string `json:"url"`
	Terminal string `json:"terminal"`
	Output   string `json:"output"`
}

// loginScriptRunner 是「在目标机执行任意脚本」的可选能力，由发布编排器实现。
type loginScriptRunner interface {
	RunLoginScript(ctx context.Context, serverID, script string) (string, error)
}

// RunLoginScript 在目标机上执行已配置的登录脚本并返回 stdout。
//
// 与 RunDiagnose 一样复用发布执行器：脚本以单步 plan 下发，超时 loginScriptTimeoutS 秒。
func (o *deployOrchestrator) RunLoginScript(ctx context.Context, serverID, script string) (string, error) {
	if o == nil || o.exec == nil {
		return "", errOrchestratorDisabled
	}
	plan := deploy.Plan{
		AppSlug: "login", ReleaseID: "login", Version: "0", Root: "/tmp",
		Steps: []deploy.Step{{Name: "login", Command: script, TimeoutS: loginScriptTimeoutS}},
	}
	var sb strings.Builder
	outcome, err := o.exec.RunPlan(ctx, serverID, plan, deploy.Hooks{OnLog: func(stream, chunk string) {
		if stream == "stdout" {
			sb.WriteString(chunk)
		}
	}})
	if err != nil && outcome.Status == "" {
		return "", fmt.Errorf("执行登录脚本失败: %w", err)
	}
	if outcome.Status != "" && outcome.Status != deploy.StatusSuccess {
		return "", fmt.Errorf("执行登录脚本失败: %s", strings.TrimSpace(outcome.Message))
	}
	return sb.String(), nil
}

// RunLoginScript 见 handler。
func (h *DeployOrchestratorHandle) RunLoginScript(ctx context.Context, serverID, script string) (string, error) {
	if h == nil || h.inner == nil {
		return "", errOrchestratorDisabled
	}
	return h.inner.RunLoginScript(ctx, serverID, script)
}

// validateLoginScript 校验服务器登录脚本：允许换行/制表符，禁止 NUL，长度受限。
func validateLoginScript(script string) error {
	if script == "" {
		return nil
	}
	if strings.ContainsRune(script, 0) {
		return errors.New("login_script 不能包含 NUL 字符")
	}
	if utf8.RuneCountInString(script) > maxLoginScriptRunes {
		return fmt.Errorf("login_script 长度超过 %d 个字符", maxLoginScriptRunes)
	}
	return nil
}

// validateServerNotes 校验服务器备注：<=maxServerNotesRunes 个字符，允许换行/制表符，
// 禁止其它控制字符（避免把终端控制序列写进库再回显）。
func validateServerNotes(notes string) error {
	if notes == "" {
		return nil
	}
	if utf8.RuneCountInString(notes) > maxServerNotesRunes {
		return fmt.Errorf("notes 长度超过 %d 个字符", maxServerNotesRunes)
	}
	for _, r := range notes {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
		case r < 0x20 || r == 0x7f:
			return errors.New("notes 不能包含控制字符")
		}
	}
	return nil
}

// normalizeCustomFields 校验并规范化自定义字段，返回落库用的 JSON 文本与排序后的键名。
//
// 约束（契约）：<=32 个键、键 1-40 个字符且不含控制字符、值 <=200 个字符、
// 序列化后 <=8KB。键名排序只为让审计 detail 与落库文本稳定可比。
func normalizeCustomFields(fields map[string]string) (string, []string, error) {
	if len(fields) > maxCustomFieldCount {
		return "", nil, fmt.Errorf("custom_fields 最多 %d 个键", maxCustomFieldCount)
	}
	keys := make([]string, 0, len(fields))
	for k, v := range fields {
		if strings.TrimSpace(k) == "" {
			return "", nil, errors.New("custom_fields 的键不能为空")
		}
		if utf8.RuneCountInString(k) > maxCustomFieldKeyRunes {
			return "", nil, fmt.Errorf("custom_fields 的键 %q 长度超过 %d 个字符", k, maxCustomFieldKeyRunes)
		}
		for _, r := range k {
			if unicode.IsControl(r) {
				return "", nil, fmt.Errorf("custom_fields 的键 %q 不能包含控制字符", k)
			}
		}
		if utf8.RuneCountInString(v) > maxCustomFieldValueRunes {
			return "", nil, fmt.Errorf("custom_fields 的键 %q 的值长度超过 %d 个字符", k, maxCustomFieldValueRunes)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	encoded, err := json.Marshal(fields)
	if err != nil {
		return "", nil, fmt.Errorf("序列化 custom_fields: %w", err)
	}
	if len(encoded) > maxCustomFieldsBytes {
		return "", nil, fmt.Errorf("custom_fields 序列化后超过 %d 字节", maxCustomFieldsBytes)
	}
	return string(encoded), keys, nil
}

// customFieldsRaw 把库内的 custom_fields 文本转成响应里的 JSON 对象。
//
// 库内只写合法对象（写入路径已校验），这里对历史/手改数据做兜底：非法或为空时回 "{}"，
// 保证详情接口的 custom_fields 永远是对象而非 null。
func customFieldsRaw(raw string) json.RawMessage {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !json.Valid([]byte(trimmed)) || trimmed[0] != '{' {
		return json.RawMessage("{}")
	}
	return json.RawMessage(trimmed)
}

// serverUpdateAuditDetail 构造 PATCH /servers/{id} 的审计 detail。
//
// 只记「哪些字段变了」与自定义字段的键名：notes 正文与 custom_fields 的值一律不落审计。
func serverUpdateAuditDetail(endpointChanged bool, oldHost string, oldPort int, newHost string, newPort int, notesChanged bool, customFieldKeys []string) string {
	parts := make([]string, 0, 3)
	if endpointChanged {
		parts = append(parts, fmt.Sprintf(`"endpoint":{"old":{"host":%q,"port":%d},"new":{"host":%q,"port":%d},"host_key_cleared":true}`,
			oldHost, oldPort, newHost, newPort))
	}
	if notesChanged {
		parts = append(parts, `"notes_changed":true`)
	}
	if customFieldKeys != nil {
		encoded, err := json.Marshal(customFieldKeys)
		if err != nil {
			encoded = []byte("[]")
		}
		parts = append(parts, `"custom_field_keys":`+string(encoded))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// clampLoginOutput 按字节上限截断输出，并回退到 rune 边界避免产生非法 UTF-8。
func clampLoginOutput(s string) string {
	if len(s) <= maxLoginOutputBytes {
		return s
	}
	cut := maxLoginOutputBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…(已截断)"
}

// parseServerLoginOutput 把脚本 stdout 解析成 url/terminal/output。
//
// 只接受 JSON 对象；字段缺失为空串，非字符串字段或非 JSON 输出整体落到 output。
func parseServerLoginOutput(serverID, raw string) serverLoginPayload {
	out := serverLoginPayload{ServerID: serverID}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return out
	}
	if strings.HasPrefix(trimmed, "{") {
		var parsed struct {
			URL      string `json:"url"`
			Terminal string `json:"terminal"`
			Output   string `json:"output"`
		}
		if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
			out.URL = clampLoginOutput(parsed.URL)
			out.Terminal = clampLoginOutput(parsed.Terminal)
			out.Output = clampLoginOutput(parsed.Output)
			return out
		}
	}
	out.Output = clampLoginOutput(trimmed)
	return out
}

// handleServerLogin 处理 POST /api/v1/servers/{id}/login：执行该机配置的登录脚本，
// 按 JSON 返回 url/terminal/output。
//
// 鉴权：server.write + 服务器在角色范围内（路由层已挂 inScope）。
// 失败边界：未配置脚本 → 409；执行器未装配 → 503；目标机不可达/脚本失败 → 502 remote_failure。
func (a *API) handleServerLogin(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "serverID"))
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}
	ctx := r.Context()
	srv, err := a.st.GetServer(ctx, id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if !a.requireServersInScope(w, r, []string{srv.ID}) {
		return
	}
	script := strings.TrimSpace(srv.LoginScript)
	if script == "" {
		writeError(w, http.StatusConflict, codeConflict, "服务器 "+srv.ID+" 未配置登录脚本")
		return
	}
	runner, ok := a.deploy.(loginScriptRunner)
	if !ok {
		writeUnavailable(w, "发布执行器未启用，无法执行登录脚本")
		return
	}
	raw, err := runner.RunLoginScript(ctx, srv.ID, script)
	if err != nil {
		if errors.Is(err, errOrchestratorDisabled) {
			writeUnavailable(w, "发布执行器未启用，无法执行登录脚本")
			return
		}
		a.writeRemoteError(w, r, err, "执行登录脚本")
		return
	}
	payload := parseServerLoginOutput(srv.ID, raw)
	// 脚本输出可能带一次性登录 URL：detail 只记长度与是否产出 url，绝不落明文。
	detail := fmt.Sprintf(`{"server_id":%q,"script_bytes":%d,"has_url":%t}`,
		srv.ID, len(script), payload.URL != "")
	a.setAuditDetail(r, "server.login", detail)
	a.logger.Info("执行服务器登录脚本", "server_id", srv.ID, "script_bytes", len(script))
	writeData(w, http.StatusOK, payload)
}
