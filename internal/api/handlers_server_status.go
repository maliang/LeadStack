package api

import (
	"net/http"

	"github.com/maliang/leadstack/internal/store"
)

type serverStatusPayload struct {
	ID             string             `json:"id"`
	Status         string             `json:"status"`
	LastSeenAt     *int64             `json:"last_seen_at"`
	AgentVersion   *string            `json:"agent_version"`
	LastError      *string            `json:"last_error"`
	MetricsSummary map[string]float64 `json:"metrics_summary,omitempty"`
}

// handleListServerStatuses 处理 GET /api/v1/servers/status。
//
// 列表页高频轮询专用：只查 id/mode/status/last_seen/agent_version/last_error
// 与最新指标，不读 tags/分组/主机信息，也不做 host_info 联表；服务器规模变大时
// 轮询成本仍然与「状态字段数」成正比，而不是整页列表载荷。
func (a *API) handleListServerStatuses(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := a.st.ListServerStatuses(ctx)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	allowed := a.allowedServers(r)
	if allowed != nil {
		filtered := rows[:0]
		for _, row := range rows {
			if _, ok := allowed[row.ID]; ok {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	metrics, err := a.st.LatestMetrics(ctx, ids)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	now := a.nowMS()
	out := make([]serverStatusPayload, 0, len(rows))
	for _, row := range rows {
		status := store.EffectiveStatus(store.Server{
			ID: row.ID, Mode: row.Mode, Status: row.Status, LastSeenAt: row.LastSeenAt,
		}, now)
		out = append(out, serverStatusPayload{
			ID:             row.ID,
			Status:         status,
			LastSeenAt:     row.LastSeenAt,
			AgentVersion:   row.AgentVersion,
			LastError:      row.LastError,
			MetricsSummary: metrics[row.ID],
		})
	}
	writeData(w, http.StatusOK, out)
}
