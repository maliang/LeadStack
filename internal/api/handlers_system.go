package api

import "net/http"

// healthResponse 是 /healthz 的响应体。
type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	UptimeS int64  `json:"uptime_s"`
}

// systemInfoResponse 是控制端信息响应体。
type systemInfoResponse struct {
	Version     string `json:"version"`
	UptimeS     int64  `json:"uptime_s"`
	ServerCount int    `json:"server_count"`
	OnlineCount int    `json:"online_count"`
}

// handleHealthz 处理 GET /healthz（免鉴权存活探针）。
func (a *API) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, healthResponse{
		Status:  "ok",
		Version: a.version,
		UptimeS: a.uptimeSeconds(),
	})
}

// handleSystemInfo 处理 GET /api/v1/system/info。
func (a *API) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	// 在线数按“最后心跳在离线阈值内”实时推导，不受后台清扫周期影响。
	onlineSince := a.nowMS() - a.offlineThresholdMS()
	total, online, err := a.st.CountServers(r.Context(), onlineSince)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, systemInfoResponse{
		Version:     a.version,
		UptimeS:     a.uptimeSeconds(),
		ServerCount: total,
		OnlineCount: online,
	})
}
