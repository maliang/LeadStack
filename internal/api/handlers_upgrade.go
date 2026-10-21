package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/store"
	"github.com/maliang/leadstack/pkg/protocol"
)

// upgradeRequest 是 Agent 升级请求体（契约第 44 节）。
type upgradeRequest struct {
	Version   string   `json:"version"`
	URL       string   `json:"url"`
	SHA256    string   `json:"sha256"`
	ServerIDs []string `json:"server_ids"`
}

// upgradeResult 是单台服务器的下发结果。
type upgradeResult struct {
	ServerID string `json:"server_id"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

// sendAgentUpgrade 向一台在线 agent 下发升级指令。
func (a *API) sendAgentUpgrade(ctx context.Context, serverID, version, rawURL, sha string) error {
	conn, ok := a.hub.Get(serverID)
	if !ok {
		return fmt.Errorf("该服务器的 agent 未在线")
	}
	payload, err := json.Marshal(protocol.AgentUpgrade{
		Version: version, URL: rawURL, SHA256: sha, Restart: true,
	})
	if err != nil {
		return fmt.Errorf("序列化升级指令: %w", err)
	}
	if err := conn.writeEnvelope(protocol.Envelope{
		Type: protocol.TypeAgentUpgrade, ID: uuid.NewString(), TS: a.nowMS(), Payload: payload,
	}); err != nil {
		return fmt.Errorf("下发升级指令失败: %w", err)
	}
	return nil
}

// handleAgentUpgrade 处理 POST /api/v1/servers/{id}/agent-upgrade（admin）。
func (a *API) handleAgentUpgrade(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	if _, err := a.st.GetServer(r.Context(), serverID); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	var req upgradeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	if err := validateUpgradeRequest(&req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	if err := a.sendAgentUpgrade(r.Context(), serverID, req.Version, req.URL, req.SHA256); err != nil {
		a.authorizeUpgradeFailure(w, r, serverID, err)
		return
	}
	a.auditManual(r, "agent.upgrade", "server", serverID,
		fmt.Sprintf(`{"target_version":%q,"url":%q}`, req.Version, req.URL))
	a.logger.Info("已下发 Agent 升级指令", "server_id", serverID, "version", req.Version)
	writeData(w, http.StatusAccepted, upgradeResult{ServerID: serverID, OK: true})
}

// handleBatchAgentUpgrade 处理 POST /api/v1/agents/upgrade（admin）。
//
// 未指定 server_ids 时对所有「在线且版本低于目标版本」的 agent 下发。
func (a *API) handleBatchAgentUpgrade(w http.ResponseWriter, r *http.Request) {
	var req upgradeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	if err := validateUpgradeRequest(&req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	ctx := r.Context()
	servers, err := a.st.ListServers(ctx)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	filter := map[string]bool{}
	for _, id := range normalizeIDList(req.ServerIDs) {
		filter[id] = true
	}
	// 资源范围：显式指定的目标必须在范围内；未指定时只升级范围内的服务器。
	if !a.scopeIsAll(r) {
		if len(filter) > 0 {
			if !a.requireServersInScope(w, r, normalizeIDList(req.ServerIDs)) {
				return
			}
		}
	}

	results := make([]upgradeResult, 0, len(servers))
	for _, srv := range servers {
		if srv.Mode != store.ModeAgent {
			continue
		}
		if len(filter) > 0 && !filter[srv.ID] {
			continue
		}
		if _, online := a.hub.Get(srv.ID); !online {
			if len(filter) > 0 {
				results = append(results, upgradeResult{ServerID: srv.ID, Error: "agent 未在线"})
			}
			continue
		}
		if !a.canAccessServer(r, srv.ID) {
			continue // 范围外：不升级也不报错（不泄露存在性）
		}
		if len(filter) == 0 && srv.AgentVersion != nil && *srv.AgentVersion == req.Version {
			continue // 已是目标版本，跳过
		}
		if err := a.sendAgentUpgrade(ctx, srv.ID, req.Version, req.URL, req.SHA256); err != nil {
			results = append(results, upgradeResult{ServerID: srv.ID, Error: err.Error()})
			continue
		}
		results = append(results, upgradeResult{ServerID: srv.ID, OK: true})
	}
	okCount := 0
	for _, item := range results {
		if item.OK {
			okCount++
		}
	}
	a.auditManual(r, "agent.upgrade", "agent", "",
		fmt.Sprintf(`{"target_version":%q,"queued":%d,"failed":%d}`, req.Version, okCount, len(results)-okCount))
	a.logger.Info("批量下发 Agent 升级", "target_version", req.Version, "queued", okCount, "failed", len(results)-okCount)
	writeData(w, http.StatusAccepted, map[string]any{
		"results": results, "queued": okCount, "failed": len(results) - okCount,
	})
}

// validateUpgradeRequest 校验升级参数。
func validateUpgradeRequest(req *upgradeRequest) error {
	req.Version = strings.TrimSpace(req.Version)
	req.URL = strings.TrimSpace(req.URL)
	req.SHA256 = strings.ToLower(strings.TrimSpace(req.SHA256))
	if req.Version == "" {
		return fmt.Errorf("version 不能为空")
	}
	if !versionPattern.MatchString(req.Version) {
		return fmt.Errorf("version 只能是字母数字与 . _ -")
	}
	normalized, err := normalizeServerURL(req.URL)
	if err != nil {
		return fmt.Errorf("url 不合法: %v", err)
	}
	req.URL = normalized
	if !isHex64(req.SHA256) {
		return fmt.Errorf("sha256 必须是 64 位十六进制字符串")
	}
	return nil
}

// isHex64 判断是否为 64 位十六进制。
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

// authorizeUpgradeFailure 把「agent 不在线」映射成 409，其余 502。
func (a *API) authorizeUpgradeFailure(w http.ResponseWriter, r *http.Request, serverID string, err error) {
	if strings.Contains(err.Error(), "未在线") {
		writeError(w, http.StatusConflict, codeConflict, err.Error())
		return
	}
	a.writeRemoteError(w, r, err, "下发升级指令")
}
