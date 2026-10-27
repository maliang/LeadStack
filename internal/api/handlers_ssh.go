package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/maliang/leadstack/internal/store"
)

// 凭证字段长度约束。
const (
	maxCredentialNameRunes = 128
	maxCredentialUserRunes = 128
)

// handleTestConnection 处理 POST /api/v1/servers/{id}/test-connection。
//
// SSH 层失败属于「测试结果」：返回 200 + ok=false + error 文案，便于前端直接展示原因；
// 只有服务器不存在（404）或服务未启用（503）才是错误响应。
func (a *API) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "serverID")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}
	if a.ssh == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "SSH 服务未启用")
		return
	}

	result, err := a.ssh.TestConnection(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, result)
}

// handleRefreshHostInfo 处理 POST /api/v1/servers/{id}/refresh-host-info。
//
// 成功返回 {data:{host_info:{...}}}；SSH 采集失败返回 502。
func (a *API) handleRefreshHostInfo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "serverID")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}
	if a.ssh == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "SSH 服务未启用")
		return
	}

	info, err := a.ssh.RefreshHostInfo(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.writeStoreError(w, r, err)
			return
		}
		// SSH 握手/采集失败属于远端失败，统一 502 + remote_failure（A19）。
		a.writeRemoteError(w, r, err, "SSH 采集")
		return
	}
	writeData(w, http.StatusOK, map[string]any{"host_info": info})
}
