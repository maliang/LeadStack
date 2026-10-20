package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/store"
	"github.com/maliang/leadstack/pkg/protocol"
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
}

// createServerRequest 是新增服务器请求体（纳管 扩展 credential_id/mode/group/tags/host_key_policy）。
type createServerRequest struct {
	Name          string    `json:"name"`
	Host          string    `json:"host"`
	Port          *int      `json:"port"`
	Mode          string    `json:"mode"`
	CredentialID  *string   `json:"credential_id"`
	Group         *string   `json:"group"`
	Tags          *[]string `json:"tags"`
	HostKeyPolicy string    `json:"host_key_policy"`
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

	out := make([]serverPayload, 0, len(servers))
	for _, srv := range servers {
		out = append(out, toServerPayload(srv, nowMS, metrics[srv.ID], hostInfoRaw(hostInfos, srv.ID)))
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
	writeData(w, http.StatusOK, toServerPayload(*srv, a.nowMS(), metrics[id], hostInfoRaw(hostInfos, id)))
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
		ID:            uuid.NewString(),
		Name:          name,
		Host:          host,
		Port:          port,
		Mode:          mode,
		TagsJSON:      tagsJSON,
		Status:        protocol.StatusUnknown,
		CreatedAt:     a.nowMS(),
		CredentialID:  credentialID,
		Group:         group,
		HostKeyPolicy: policy,
	}
	if err := a.st.CreateServer(ctx, srv); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("新增服务器",
		"server_id", srv.ID, "name", srv.Name, "host", srv.Host, "mode", srv.Mode, "group", req.Group)
	writeData(w, http.StatusCreated, toServerPayload(*srv, a.nowMS(), nil, nil))
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
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	if req.Name == nil && req.Tags == nil && req.Group == nil &&
		req.HostKeyPolicy == nil && req.CredentialID == nil &&
		req.Host == nil && req.Port == nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			"至少需要提供 name/tags/group/host_key_policy/credential_id/host/port 之一")
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
		Name:          name,
		TagsJSON:      tagsJSON,
		Group:         group,
		HostKeyPolicy: policy,
		CredentialID:  credentialID,
		Host:          hostPtr,
		Port:          portPtr,
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
	if endpointChanged {
		detail := fmt.Sprintf(`{"old":{"host":%q,"port":%d},"new":{"host":%q,"port":%d},"host_key_cleared":true}`,
			oldHost, oldPort, newHost, newPort)
		a.setAuditDetail(r, "server.update", detail)
		a.logger.Info("服务器地址已变更，已清除旧主机密钥",
			"server_id", id, "old", fmt.Sprintf("%s:%d", oldHost, oldPort),
			"new", fmt.Sprintf("%s:%d", newHost, newPort))
	}
	writeData(w, http.StatusOK, toServerPayload(*srv, a.nowMS(), metrics[id], hostInfoRaw(hostInfos, id)))
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
		ID:            srv.ID,
		Name:          srv.Name,
		Host:          srv.Host,
		Port:          srv.Port,
		Mode:          srv.Mode,
		Status:        deriveStatus(srv, nowMS),
		LastSeenAt:    srv.LastSeenAt,
		AgentVersion:  srv.AgentVersion,
		OS:            srv.OS,
		Arch:          srv.Arch,
		Kernel:        srv.Kernel,
		Tags:          parseTags(srv.TagsJSON),
		CreatedAt:     srv.CreatedAt,
		CredentialID:  srv.CredentialID,
		Group:         srv.Group,
		HostKeyPolicy: srv.HostKeyPolicy,
		LastError:     srv.LastError,
		HostInfo:      hostInfo,
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
