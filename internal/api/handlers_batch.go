package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/maliang/leadstack/internal/store"
)

// 批量操作支持的 action。
const (
	batchActionTestConnection  = "test-connection"
	batchActionRefreshHostInfo = "refresh-host-info"
	batchActionAddTags         = "add-tags"
	batchActionSetGroup        = "set-group"
	batchActionDelete          = "delete"
)

// batchSSHConcurrency 是批量 SSH 操作的并发上限。
const batchSSHConcurrency = 8

// batchRequest 是批量操作请求体。
type batchRequest struct {
	IDs    []string        `json:"ids"`
	Action string          `json:"action"`
	Params json.RawMessage `json:"params"`
}

// batchResultItem 是单台服务器的执行结果。
type batchResultItem struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Detail any    `json:"detail,omitempty"`
}

// batchSummary 是汇总计数。
type batchSummary struct {
	OK     int `json:"ok"`
	Failed int `json:"failed"`
}

// batchResponse 是批量操作响应体。
type batchResponse struct {
	Results []batchResultItem `json:"results"`
	Summary batchSummary      `json:"summary"`
}

// batchTagsParams 是 add-tags 的参数。
type batchTagsParams struct {
	Tags []string `json:"tags"`
}

// batchGroupParams 是 set-group 的参数。
type batchGroupParams struct {
	Group string `json:"group"`
}

// handleBatchServers 处理 POST /api/v1/servers/batch。
func (a *API) handleBatchServers(w http.ResponseWriter, r *http.Request) {
	var req batchRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}

	ids := normalizeIDList(req.IDs)
	if len(ids) == 0 {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "ids 不能为空")
		return
	}
	if len(ids) > maxBatchIDs {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			fmt.Sprintf("一次最多操作 %d 台服务器", maxBatchIDs))
		return
	}

	action := strings.TrimSpace(req.Action)
	ctx := r.Context()

	var results []batchResultItem
	switch action {
	case batchActionTestConnection, batchActionRefreshHostInfo:
		if a.ssh == nil {
			writeError(w, http.StatusServiceUnavailable, codeInternal, "SSH 服务未启用")
			return
		}
		results = a.runBatchSSH(ctx, ids, action)
	case batchActionAddTags:
		var params batchTagsParams
		if len(req.Params) == 0 {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "add-tags 需要 params.tags")
			return
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "params 解析失败: "+err.Error())
			return
		}
		tags, err := normalizeTags(params.Tags)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		if len(tags) == 0 {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "params.tags 不能为空")
			return
		}
		results = a.runBatchAddTags(ctx, ids, tags)
	case batchActionSetGroup:
		var params batchGroupParams
		if len(req.Params) == 0 {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "set-group 需要 params.group")
			return
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "params 解析失败: "+err.Error())
			return
		}
		group := strings.TrimSpace(params.Group)
		if err := validateText("group", group, maxGroupRunes, false); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		results = a.runBatchSetGroup(ctx, ids, group)
	case batchActionDelete:
		results = a.runBatchDelete(ctx, ids)
	default:
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			"action 只能是 test-connection|refresh-host-info|add-tags|set-group|delete")
		return
	}

	summary := batchSummary{}
	for _, item := range results {
		if item.OK {
			summary.OK++
		} else {
			summary.Failed++
		}
	}
	a.logger.Info("批量操作完成",
		"action", action, "total", len(results), "ok", summary.OK, "failed", summary.Failed)
	writeData(w, http.StatusOK, batchResponse{Results: results, Summary: summary})
}

// runBatchSSH 以有界并发执行批量 SSH 操作，结果顺序与 ids 一致。
func (a *API) runBatchSSH(ctx context.Context, ids []string, action string) []batchResultItem {
	results := make([]batchResultItem, len(ids))
	sem := make(chan struct{}, batchSSHConcurrency)
	var wg sync.WaitGroup

	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = batchResultItem{ID: id, OK: false, Error: ctx.Err().Error()}
				return
			}
			results[i] = a.runOneSSHAction(ctx, id, action)
		}(i, id)
	}
	wg.Wait()
	return results
}

// runOneSSHAction 执行单台服务器的 SSH 动作。
func (a *API) runOneSSHAction(ctx context.Context, id, action string) batchResultItem {
	switch action {
	case batchActionTestConnection:
		result, err := a.ssh.TestConnection(ctx, id)
		if err != nil {
			return batchResultItem{ID: id, OK: false, Error: err.Error()}
		}
		if !result.OK {
			return batchResultItem{ID: id, OK: false, Error: result.Error, Detail: result}
		}
		return batchResultItem{ID: id, OK: true, Detail: result}
	default:
		info, err := a.ssh.RefreshHostInfo(ctx, id)
		if err != nil {
			return batchResultItem{ID: id, OK: false, Error: err.Error()}
		}
		return batchResultItem{ID: id, OK: true, Detail: map[string]any{"hostname": info.Hostname, "os": info.OS}}
	}
}

// runBatchAddTags 给多台服务器追加标签（合并去重）。
func (a *API) runBatchAddTags(ctx context.Context, ids []string, tags []string) []batchResultItem {
	results := make([]batchResultItem, 0, len(ids))
	for _, id := range ids {
		srv, err := a.st.GetServer(ctx, id)
		if err != nil {
			results = append(results, batchResultItem{ID: id, OK: false, Error: err.Error()})
			continue
		}
		merged, err := normalizeTags(append(parseTags(srv.TagsJSON), tags...))
		if err != nil {
			results = append(results, batchResultItem{ID: id, OK: false, Error: err.Error()})
			continue
		}
		encoded, err := json.Marshal(merged)
		if err != nil {
			results = append(results, batchResultItem{ID: id, OK: false, Error: "序列化标签失败"})
			continue
		}
		encodedStr := string(encoded)
		if _, err := a.st.UpdateServerFields(ctx, id, store.ServerUpdate{TagsJSON: &encodedStr}); err != nil {
			results = append(results, batchResultItem{ID: id, OK: false, Error: err.Error()})
			continue
		}
		results = append(results, batchResultItem{ID: id, OK: true, Detail: map[string]any{"tags": merged}})
	}
	return results
}

// runBatchSetGroup 批量设置分组（空字符串表示清除分组）。
func (a *API) runBatchSetGroup(ctx context.Context, ids []string, group string) []batchResultItem {
	results := make([]batchResultItem, 0, len(ids))
	g := group
	for _, id := range ids {
		if _, err := a.st.UpdateServerFields(ctx, id, store.ServerUpdate{Group: &g}); err != nil {
			results = append(results, batchResultItem{ID: id, OK: false, Error: err.Error()})
			continue
		}
		results = append(results, batchResultItem{ID: id, OK: true, Detail: map[string]any{"group": g}})
	}
	return results
}

// runBatchDelete 批量删除服务器。
func (a *API) runBatchDelete(ctx context.Context, ids []string) []batchResultItem {
	results := make([]batchResultItem, 0, len(ids))
	for _, id := range ids {
		if err := a.st.DeleteServer(ctx, id); err != nil {
			results = append(results, batchResultItem{ID: id, OK: false, Error: err.Error()})
			continue
		}
		results = append(results, batchResultItem{ID: id, OK: true})
	}
	return results
}

// normalizeIDList 去空白、去重并保持顺序。
func normalizeIDList(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		v := strings.TrimSpace(id)
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
