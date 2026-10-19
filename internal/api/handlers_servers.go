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
)

// serverPayload 是服务器的对外表示，字段与契约第 3 节一致。
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
}

// createServerRequest 是新增服务器请求体。
type createServerRequest struct {
	Name string `json:"name"`
	Host string `json:"host"`
	Port *int   `json:"port"`
	Mode string `json:"mode"`
}

// patchServerRequest 是改名/改标签请求体；nil 表示不改。
type patchServerRequest struct {
	Name *string   `json:"name"`
	Tags *[]string `json:"tags"`
}

// handleListServers 处理 GET /api/v1/servers。
func (a *API) handleListServers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	servers, err := a.st.ListServers(ctx)
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

	nowMS := a.nowMS()
	out := make([]serverPayload, 0, len(servers))
	for _, srv := range servers {
		out = append(out, toServerPayload(srv, nowMS, metrics[srv.ID]))
	}
	writeData(w, http.StatusOK, out)
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
	writeData(w, http.StatusOK, toServerPayload(*srv, a.nowMS(), metrics[id]))
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
	port := 22
	if req.Port != nil {
		port = *req.Port
	}
	if port <= 0 || port > 65535 {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "port 必须在 1-65535 之间")
		return
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "agent"
	}
	if err := validateMode(mode); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}

	srv := &store.Server{
		ID:        uuid.NewString(),
		Name:      name,
		Host:      host,
		Port:      port,
		Mode:      mode,
		TagsJSON:  "[]",
		Status:    protocol.StatusUnknown,
		CreatedAt: a.nowMS(),
	}
	if err := a.st.CreateServer(r.Context(), srv); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("新增服务器", "server_id", srv.ID, "name", srv.Name, "host", srv.Host)
	writeData(w, http.StatusCreated, toServerPayload(*srv, a.nowMS(), nil))
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
	if req.Name == nil && req.Tags == nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "至少需要提供 name 或 tags 之一")
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

	srv, err := a.st.UpdateServerNameTags(r.Context(), id, name, tagsJSON)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	metrics, err := a.st.LatestMetrics(r.Context(), []string{id})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, toServerPayload(*srv, a.nowMS(), metrics[id]))
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
func toServerPayload(srv store.Server, nowMS int64, metrics map[string]float64) serverPayload {
	out := serverPayload{
		ID:           srv.ID,
		Name:         srv.Name,
		Host:         srv.Host,
		Port:         srv.Port,
		Mode:         srv.Mode,
		Status:       deriveStatus(srv, nowMS),
		LastSeenAt:   srv.LastSeenAt,
		AgentVersion: srv.AgentVersion,
		OS:           srv.OS,
		Arch:         srv.Arch,
		Kernel:       srv.Kernel,
		Tags:         parseTags(srv.TagsJSON),
		CreatedAt:    srv.CreatedAt,
	}
	if len(metrics) > 0 {
		out.MetricsSummary = metrics
	}
	return out
}

// deriveStatus 按契约第 3 节由 last_seen_at 推导状态。
func deriveStatus(srv store.Server, nowMS int64) string {
	if srv.LastSeenAt == nil {
		return protocol.StatusUnknown
	}
	if nowMS-*srv.LastSeenAt <= int64(protocol.OfflineAfterS)*1000 {
		return protocol.StatusOnline
	}
	return protocol.StatusOffline
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
