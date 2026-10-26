package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/store"
)

// 检测项字段约束。
const (
	maxCheckNameRunes     = 128
	maxCheckTargetRunes   = 1024
	maxScriptBytes        = 64 << 10
	minCheckTimeoutS      = 1
	maxCheckTimeoutS      = 600
	defaultCheckTimeoutS  = 10
	checkRunConcurrency   = 4
	checkResultsDefault   = 100
	checkResultsMax       = 1000
	availabilityWindowHrs = 24
)

// checkKinds 是契约第 13 节允许的检测类型。
var checkKinds = map[string]struct{}{
	"tcp": {}, "http": {}, "ping": {}, "process": {}, "script": {},
}

// CheckRunResult 是一次手动/调度执行的探测结果（对外形态）。
type CheckRunResult struct {
	OK         bool   `json:"ok"`
	LatencyMS  int64  `json:"latency_ms"`
	StatusCode int    `json:"status_code,omitempty"`
	Message    string `json:"message,omitempty"`
	TS         int64  `json:"ts"`
}

// CheckRunner 是「立即执行检测」与「唤醒调度器」的端口。
//
// 由 internal/checker.Scheduler 经薄适配器实现（见 checks_wiring.go）；
// 为 nil 时 /checks/{id}/run 返回 503，其余接口仍可用。
type CheckRunner interface {
	RunOnce(ctx context.Context, checkID, serverID string) (CheckRunResult, error)
	Wake()
}

// checkPayload 是检测项的对外表示（含聚合信息）。
type checkPayload struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	Target      string         `json:"target"`
	IntervalS   int            `json:"interval_s"`
	TimeoutS    int            `json:"timeout_s"`
	Params      map[string]any `json:"params"`
	Enabled     bool           `json:"enabled"`
	CreatedAt   int64          `json:"created_at"`
	TargetCount int            `json:"target_count"`
	ServerCount int            `json:"server_count"`
	ServerIDs   []string       `json:"server_ids"`
	Status      string         `json:"status"`
	// State 与 Status 同值：前端 检测 约定用 state，后端历史字段是 status，两个都发以免任何一方改造。
	State string `json:"state"`
	// ConsecutiveFail 是该检测项当前的连续失败次数（多目标取最大值），供前端展示「1/3」进度。
	ConsecutiveFail int                 `json:"consecutive_fail"`
	Availability24h float64             `json:"availability_24h"`
	Results24h      int                 `json:"results_24h"`
	LastResult      *checkResultPayload `json:"last_result"`
}

// checkResultPayload 是一条检测结果的对外表示。
//
// message 恒输出（成功时为空串，见契约 §15.1）；status_code 仅 http 有意义，无则省略。
type checkResultPayload struct {
	ID         int64  `json:"id"`
	CheckID    string `json:"check_id"`
	ServerID   string `json:"server_id"`
	ServerName string `json:"server_name,omitempty"`
	TS         int64  `json:"ts"`
	OK         bool   `json:"ok"`
	LatencyMS  int64  `json:"latency_ms"`
	StatusCode *int   `json:"status_code,omitempty"`
	Message    string `json:"message"`
}

// checkTargetStatePayload 是「检测项 × 服务器」的状态。
type checkTargetStatePayload struct {
	ServerID        string  `json:"server_id"`
	Status          string  `json:"status"`
	OK              bool    `json:"ok"`
	ConsecutiveFail int     `json:"consecutive_fail"`
	Since           int64   `json:"since"`
	LastTS          int64   `json:"last_ts"`
	LastMessage     *string `json:"last_message,omitempty"`
}

// createCheckRequest 是创建检测项请求体。
type createCheckRequest struct {
	Name      string         `json:"name"`
	Kind      string         `json:"kind"`
	Target    string         `json:"target"`
	IntervalS int            `json:"interval_s"`
	TimeoutS  int            `json:"timeout_s"`
	Params    map[string]any `json:"params"`
	Enabled   *bool          `json:"enabled"`
	ServerIDs []string       `json:"server_ids"`
}

// patchCheckRequest 是更新检测项请求体；nil 表示不修改。
type patchCheckRequest struct {
	Name      *string         `json:"name"`
	Target    *string         `json:"target"`
	IntervalS *int            `json:"interval_s"`
	TimeoutS  *int            `json:"timeout_s"`
	Params    *map[string]any `json:"params"`
	Enabled   *bool           `json:"enabled"`
}

// setTargetsRequest 是覆盖式设置目标的请求体。
type setTargetsRequest struct {
	ServerIDs []string `json:"server_ids"`
}

// checkRunResponse 是立即执行的响应体。
type checkRunResponse struct {
	Results []checkRunItem  `json:"results"`
	Summary checkRunSummary `json:"summary"`
}

// checkRunSummary 是立即执行的汇总计数。
type checkRunSummary struct {
	OK     int `json:"ok"`
	Failed int `json:"failed"`
}

// checkRunItem 是单台服务器的执行结果（§15.1：含 status_code）。
type checkRunItem struct {
	ServerID   string `json:"server_id"`
	OK         bool   `json:"ok"`
	LatencyMS  int64  `json:"latency_ms"`
	StatusCode *int   `json:"status_code,omitempty"`
	Message    string `json:"message"`
}

// handleListChecks 处理 GET /api/v1/checks。
func (a *API) handleListChecks(w http.ResponseWriter, r *http.Request) {
	checks, err := a.st.ListChecks(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	payloads, err := a.buildCheckPayloads(r.Context(), checks)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	// 资源范围过滤（先过滤后分页，契约 §53.3/§53.4）。
	payloads = a.filterCheckPayloadsByScope(r, payloads)
	params, perr := parsePage(r)
	if perr != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, perr.Error())
		return
	}
	start, end, meta := params.window(len(payloads))
	if !params.Paged {
		start, end = 0, len(payloads)
	}
	writePagedList(w, params, payloads[start:end], meta)
}

// handleGetCheck 处理 GET /api/v1/checks/{id}。
func (a *API) handleGetCheck(w http.ResponseWriter, r *http.Request) {
	c, err := a.loadCheck(w, r)
	if err != nil {
		return // loadCheck 已写响应
	}
	payloads, err := a.buildCheckPayloads(r.Context(), []store.Check{*c})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if !writeNotFoundIfInvisible(w, a.canAccessAnyServer(r, payloads[0].ServerIDs)) {
		return
	}
	writeData(w, http.StatusOK, payloads[0])
}

// handleCreateCheck 处理 POST /api/v1/checks。
func (a *API) handleCreateCheck(w http.ResponseWriter, r *http.Request) {
	var req createCheckRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}

	kind := strings.TrimSpace(req.Kind)
	if _, ok := checkKinds[kind]; !ok {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "kind 只能是 tcp|http|ping|process|script")
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := validateText("name", name, maxCheckNameRunes, true); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	target := strings.TrimSpace(req.Target)
	if err := validateCheckTarget(kind, target); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	intervalS, err := a.normalizeIntervalS(req.IntervalS, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	timeoutS, err := normalizeTimeoutS(req.TimeoutS)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	params, err := normalizeCheckParams(kind, req.Params)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	encodedParams, err := json.Marshal(params)
	if err != nil {
		a.internalError(w, r, fmt.Errorf("序列化检测参数: %w", err))
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	ctx := r.Context()
	if len(req.ServerIDs) > maxBatchIDs {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			fmt.Sprintf("一次最多绑定 %d 台服务器", maxBatchIDs))
		return
	}
	serverIDs, err := a.validateServerIDs(ctx, req.ServerIDs)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if !a.requireServersInScope(w, r, serverIDs) {
		return
	}

	check := &store.Check{
		ID:         uuid.NewString(),
		Name:       name,
		Kind:       kind,
		Target:     target,
		IntervalS:  intervalS,
		TimeoutS:   timeoutS,
		ParamsJSON: string(encodedParams),
		Enabled:    enabled,
		CreatedAt:  a.nowMS(),
	}
	if err := a.st.CreateCheck(ctx, check); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if len(serverIDs) > 0 {
		if err := a.st.SetCheckTargets(ctx, check.ID, serverIDs); err != nil {
			a.writeStoreError(w, r, err)
			return
		}
	}
	a.wakeChecks()

	a.logger.Info("创建检测项",
		"check_id", check.ID, "name", check.Name, "kind", check.Kind,
		"target", check.Target, "interval_s", check.IntervalS, "targets", len(serverIDs))
	payloads, err := a.buildCheckPayloads(ctx, []store.Check{*check})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, payloads[0])
}

// handlePatchCheck 处理 PATCH /api/v1/checks/{id}。
func (a *API) handlePatchCheck(w http.ResponseWriter, r *http.Request) {
	c, err := a.loadCheck(w, r)
	if err != nil {
		return
	}
	targets, terr := a.st.ListCheckTargets(r.Context(), c.ID)
	if terr != nil {
		a.internalError(w, r, terr)
		return
	}
	if !writeNotFoundIfInvisible(w, a.canAccessAnyServer(r, targets)) {
		return
	}
	var req patchCheckRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	if req.Name == nil && req.Target == nil && req.IntervalS == nil &&
		req.TimeoutS == nil && req.Params == nil && req.Enabled == nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			"至少需要提供 name/target/interval_s/timeout_s/params/enabled 之一")
		return
	}

	upd := store.CheckUpdate{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if err := validateText("name", name, maxCheckNameRunes, true); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		upd.Name = &name
	}
	if req.Target != nil {
		target := strings.TrimSpace(*req.Target)
		if err := validateCheckTarget(c.Kind, target); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		upd.Target = &target
	}
	if req.IntervalS != nil {
		intervalS, err := a.normalizeIntervalS(*req.IntervalS, true)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		upd.IntervalS = &intervalS
	}
	if req.TimeoutS != nil {
		timeoutS, err := normalizeTimeoutS(*req.TimeoutS)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		upd.TimeoutS = &timeoutS
	}
	if req.Params != nil {
		params, err := normalizeCheckParams(c.Kind, *req.Params)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		encoded, err := json.Marshal(params)
		if err != nil {
			a.internalError(w, r, fmt.Errorf("序列化检测参数: %w", err))
			return
		}
		s := string(encoded)
		upd.ParamsJSON = &s
	}
	if req.Enabled != nil {
		upd.Enabled = req.Enabled
	}

	updated, err := a.st.UpdateCheck(r.Context(), c.ID, upd)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	// 间隔、启用状态、目标参数都可能影响调度，统一唤醒重排。
	a.wakeChecks()

	a.logger.Info("更新检测项", "check_id", updated.ID, "interval_s", updated.IntervalS, "enabled", updated.Enabled)
	payloads, err := a.buildCheckPayloads(r.Context(), []store.Check{*updated})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, payloads[0])
}

// handleDeleteCheck 处理 DELETE /api/v1/checks/{id}。
func (a *API) handleDeleteCheck(w http.ResponseWriter, r *http.Request) {
	c, err := a.loadCheck(w, r)
	if err != nil {
		return
	}
	if !writeNotFoundIfInvisible(w, a.checkVisible(r, c.ID)) {
		return
	}
	if err := a.st.DeleteCheck(r.Context(), c.ID); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.wakeChecks()
	a.logger.Info("删除检测项", "check_id", c.ID, "name", c.Name)
	writeData(w, http.StatusOK, map[string]any{"id": c.ID})
}

// handleSetCheckTargets 处理 POST /api/v1/checks/{id}/targets（覆盖式）。
func (a *API) handleSetCheckTargets(w http.ResponseWriter, r *http.Request) {
	c, err := a.loadCheck(w, r)
	if err != nil {
		return
	}
	if !writeNotFoundIfInvisible(w, a.checkVisible(r, c.ID)) {
		return
	}
	var req setTargetsRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	ctx := r.Context()
	serverIDs, err := a.validateServerIDs(ctx, req.ServerIDs)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if !a.requireServersInScope(w, r, serverIDs) {
		return
	}
	if err := a.st.SetCheckTargets(ctx, c.ID, serverIDs); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.wakeChecks()

	a.logger.Info("设置检测项目标", "check_id", c.ID, "targets", len(serverIDs))
	writeData(w, http.StatusOK, map[string]any{"id": c.ID, "server_ids": serverIDs})
}

// handleRunCheck 处理 POST /api/v1/checks/{id}/run：对全部目标立即执行一次。
func (a *API) handleRunCheck(w http.ResponseWriter, r *http.Request) {
	c, err := a.loadCheck(w, r)
	if err != nil {
		return
	}
	if a.checks == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "检测调度器未启用")
		return
	}
	ctx := r.Context()
	serverIDs, err := a.st.ListCheckTargets(ctx, c.ID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if !writeNotFoundIfInvisible(w, a.canAccessAnyServer(r, serverIDs)) {
		return
	}
	serverIDs = a.filterServerIDsByScope(r, serverIDs)

	results := make([]checkRunItem, len(serverIDs))
	sem := make(chan struct{}, checkRunConcurrency)
	var wg sync.WaitGroup
	for i, serverID := range serverIDs {
		wg.Add(1)
		go func(i int, serverID string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			res, err := a.checks.RunOnce(ctx, c.ID, serverID)
			if err != nil {
				results[i] = checkRunItem{ServerID: serverID, OK: false, Message: err.Error()}
				return
			}
			item := checkRunItem{
				ServerID: serverID, OK: res.OK, LatencyMS: res.LatencyMS, Message: res.Message,
			}
			if res.StatusCode > 0 {
				code := res.StatusCode
				item.StatusCode = &code
			}
			results[i] = item
		}(i, serverID)
	}
	wg.Wait()

	summary := checkRunSummary{}
	for _, item := range results {
		if item.OK {
			summary.OK++
		} else {
			summary.Failed++
		}
	}
	a.logger.Info("立即执行检测项", "check_id", c.ID, "targets", len(results), "ok", summary.OK)
	writeData(w, http.StatusOK, checkRunResponse{Results: results, Summary: summary})
}

// handleListCheckResults 处理 GET /api/v1/checks/{id}/results。
func (a *API) handleListCheckResults(w http.ResponseWriter, r *http.Request) {
	c, err := a.loadCheck(w, r)
	if err != nil {
		return
	}
	if !writeNotFoundIfInvisible(w, a.checkVisible(r, c.ID)) {
		return
	}

	limit := checkResultsDefault
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "limit 必须是正整数")
			return
		}
		if v > checkResultsMax {
			v = checkResultsMax
		}
		limit = v
	}
	from, err := parseQueryInt(r, "from", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "from 必须是 Unix 毫秒整数")
		return
	}
	to, err := parseQueryInt(r, "to", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "to 必须是 Unix 毫秒整数")
		return
	}
	serverID := strings.TrimSpace(r.URL.Query().Get("server_id"))
	if serverID != "" {
		if _, err := a.st.GetServer(r.Context(), serverID); err != nil {
			a.writeStoreError(w, r, err)
			return
		}
	}

	rows, err := a.st.ListCheckResults(r.Context(), store.CheckResultFilter{
		CheckID: c.ID, ServerID: serverID, From: from, To: to, Limit: limit,
	})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	names, err := a.serverNameIndex(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]checkResultPayload, 0, len(rows))
	for _, row := range rows {
		item := toCheckResultPayload(row)
		item.ServerName = names[row.ServerID]
		out = append(out, item)
	}
	writeData(w, http.StatusOK, out)
}

// handleCheckSummary 处理 GET /api/v1/checks/summary。
func (a *API) handleCheckSummary(w http.ResponseWriter, r *http.Request) {
	checks, err := a.st.ListChecks(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	payloads, err := a.buildCheckPayloads(r.Context(), checks)
	if err != nil {
		a.internalError(w, r, err)
		return
	}

	summary := struct {
		Total   int            `json:"total"`
		Up      int            `json:"up"`
		Down    int            `json:"down"`
		Unknown int            `json:"unknown"`
		Checks  []checkPayload `json:"checks"`
	}{Total: len(payloads), Checks: payloads}
	for _, c := range payloads {
		switch c.Status {
		case store.CheckStatusUp:
			summary.Up++
		case store.CheckStatusDown:
			summary.Down++
		default:
			summary.Unknown++
		}
	}
	writeData(w, http.StatusOK, summary)
}

// handleServerChecks 处理 GET /api/v1/servers/{id}/checks。
func (a *API) handleServerChecks(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	if serverID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}
	ctx := r.Context()
	if _, err := a.st.GetServer(ctx, serverID); err != nil {
		a.writeStoreError(w, r, err)
		return
	}

	checkIDs, err := a.st.ListCheckIDsForServer(ctx, serverID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	checks := make([]store.Check, 0, len(checkIDs))
	for _, id := range checkIDs {
		c, err := a.st.GetCheck(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue // 检测项刚被删除
			}
			a.internalError(w, r, err)
			return
		}
		checks = append(checks, *c)
	}

	payloads, err := a.buildCheckPayloads(ctx, checks)
	if err != nil {
		a.internalError(w, r, err)
		return
	}

	// 覆盖为「该服务器视角」的状态、可用率与最近结果；元素形态与 GET /checks 保持一致。
	since := a.nowMS() - int64(availabilityWindowHrs)*3600*1000
	out := make([]checkPayload, 0, len(payloads))
	for i, c := range checks {
		item := payloads[i]
		threshold := failThresholdOf(c.ParamsJSON)
		status := store.CheckStatusUnknown
		consecutiveFail := 0
		if st, err := a.st.GetCheckState(ctx, c.ID, serverID); err == nil {
			status = statusForState(*st, threshold)
			consecutiveFail = st.ConsecutiveFail
		} else if !errors.Is(err, store.ErrNotFound) {
			a.internalError(w, r, err)
			return
		}
		avail, err := a.st.Availability(ctx, c.ID, serverID, since)
		if err != nil {
			a.internalError(w, r, err)
			return
		}
		resultRows, err := a.st.ListCheckResults(ctx, store.CheckResultFilter{
			CheckID: c.ID, ServerID: serverID, Limit: 1,
		})
		if err != nil {
			a.internalError(w, r, err)
			return
		}
		item.Status = status
		item.State = status
		item.ConsecutiveFail = consecutiveFail
		item.Availability24h = round2(avail.Ratio() * 100)
		item.Results24h = avail.Total
		item.LastResult = nil
		if len(resultRows) > 0 {
			payload := toCheckResultPayload(resultRows[0])
			item.LastResult = &payload
		}
		out = append(out, item)
	}
	writeData(w, http.StatusOK, out)
}

// buildCheckPayloads 组装检测项列表/详情的聚合信息。
func (a *API) buildCheckPayloads(ctx context.Context, checks []store.Check) ([]checkPayload, error) {
	if len(checks) == 0 {
		return []checkPayload{}, nil
	}
	ids := make([]string, 0, len(checks))
	for _, c := range checks {
		ids = append(ids, c.ID)
	}

	targetIndex, err := a.st.ListAllCheckTargets(ctx)
	if err != nil {
		return nil, err
	}
	states, err := a.st.ListCheckStates(ctx, ids)
	if err != nil {
		return nil, err
	}
	latest, err := a.st.ListLatestCheckResults(ctx, ids)
	if err != nil {
		return nil, err
	}
	availability, err := a.st.ListAvailability(ctx, a.nowMS()-int64(availabilityWindowHrs)*3600*1000)
	if err != nil {
		return nil, err
	}

	out := make([]checkPayload, 0, len(checks))
	for _, c := range checks {
		params := decodeParams(c.ParamsJSON)
		targetServerIDs := targetIndex[c.ID]
		if targetServerIDs == nil {
			targetServerIDs = []string{}
		}
		threshold := failThresholdOf(c.ParamsJSON)
		avail := availability[c.ID]

		item := checkPayload{
			ID:              c.ID,
			Name:            c.Name,
			Kind:            c.Kind,
			Target:          c.Target,
			IntervalS:       c.IntervalS,
			TimeoutS:        c.TimeoutS,
			Params:          params,
			Enabled:         c.Enabled,
			CreatedAt:       c.CreatedAt,
			TargetCount:     len(targetServerIDs),
			ServerCount:     len(targetServerIDs),
			ServerIDs:       targetServerIDs,
			Status:          statusForStates(states[c.ID], threshold),
			Availability24h: round2(avail.Ratio() * 100),
			Results24h:      avail.Total,
			LastResult:      nil,
		}
		item.State = item.Status
		item.ConsecutiveFail = maxConsecutiveFail(states[c.ID])
		if r, ok := latest[c.ID]; ok {
			payload := toCheckResultPayload(r)
			item.LastResult = &payload
		}
		out = append(out, item)
	}
	return out, nil
}

// loadCheck 解析路径参数并加载检测项；失败时已写好响应，返回 error 供调用方直接 return。
func (a *API) loadCheck(w http.ResponseWriter, r *http.Request) (*store.Check, error) {
	id := chi.URLParam(r, "checkID")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少检测项 ID")
		return nil, errors.New("缺少检测项 ID")
	}
	c, err := a.st.GetCheck(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return nil, err
	}
	return c, nil
}

// validateServerIDs 去重并校验服务器存在。
func (a *API) validateServerIDs(ctx context.Context, ids []string) ([]string, error) {
	out := normalizeIDList(ids)
	for _, id := range out {
		if _, err := a.st.GetServer(ctx, id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// wakeChecks 通知调度器重读目标（检测项增删改后必须调用）。
func (a *API) wakeChecks() {
	if a.checks != nil {
		a.checks.Wake()
	}
}

// serverNameIndex 返回 server_id → name 的索引。
func (a *API) serverNameIndex(ctx context.Context) (map[string]string, error) {
	servers, err := a.st.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(servers))
	for _, srv := range servers {
		out[srv.ID] = srv.Name
	}
	return out, nil
}

// toCheckResultPayload 把存储模型转换为对外结构。
func toCheckResultPayload(r store.CheckResult) checkResultPayload {
	return checkResultPayload{
		ID:         r.ID,
		CheckID:    r.CheckID,
		ServerID:   r.ServerID,
		TS:         r.TS,
		OK:         r.OK,
		LatencyMS:  r.LatencyMS,
		StatusCode: r.StatusCode,
		Message:    derefString(r.Message),
	}
}

// statusForState 按契约口径计算单个目标的检测状态。
func statusForState(st store.CheckState, failThreshold int) string {
	if st.OK {
		return store.CheckStatusUp
	}
	if st.ConsecutiveFail >= failThreshold {
		return store.CheckStatusDown
	}
	return store.CheckStatusUp
}

// statusForStates 汇总一个检测项所有目标的状态：任一 down 即 down，否则有状态即 up。
func statusForStates(states []store.CheckState, failThreshold int) string {
	if len(states) == 0 {
		return store.CheckStatusUnknown
	}
	anyUp := false
	for _, st := range states {
		switch statusForState(st, failThreshold) {
		case store.CheckStatusDown:
			return store.CheckStatusDown
		case store.CheckStatusUp:
			anyUp = true
		}
	}
	if anyUp {
		return store.CheckStatusUp
	}
	return store.CheckStatusUnknown
}

// maxConsecutiveFail 返回该检测项所有目标里最大的连续失败次数（多目标时前端展示最严重的那台）。
func maxConsecutiveFail(states []store.CheckState) int {
	max := 0
	for _, st := range states {
		if st.ConsecutiveFail > max {
			max = st.ConsecutiveFail
		}
	}
	return max
}

// failThresholdOf 读取 params.fail_threshold，缺省为 3。
func failThresholdOf(paramsJSON string) int {
	params := decodeParams(paramsJSON)
	raw, ok := params["fail_threshold"]
	if !ok {
		return store.DefaultFailThreshold
	}
	switch v := raw.(type) {
	case float64:
		if int(v) >= 1 {
			return int(v)
		}
	case int:
		if v >= 1 {
			return v
		}
	case json.Number:
		if n, err := v.Int64(); err == nil && n >= 1 {
			return int(n)
		}
	}
	return store.DefaultFailThreshold
}

// decodeParams 解析 params_json；异常数据按空对象处理。
func decodeParams(paramsJSON string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(paramsJSON) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(paramsJSON), &out); err != nil {
		return map[string]any{}
	}
	if out == nil {
		out = map[string]any{}
	}
	return out
}

// normalizeIntervalS 校验检测间隔；required 为 true 且未提供时使用配置下限。
func (a *API) normalizeIntervalS(value int, required bool) (int, error) {
	floor := int(a.cfg.CheckIntervalMin.Seconds())
	if floor <= 0 {
		floor = 10
	}
	if value <= 0 {
		if required {
			return floor, nil
		}
		return 0, errors.New("interval_s 必须为正数")
	}
	if value < floor {
		return 0, fmt.Errorf("interval_s 不能小于 %d 秒", floor)
	}
	if value > 24*3600 {
		return 0, errors.New("interval_s 不能超过 86400 秒")
	}
	return value, nil
}

// normalizeTimeoutS 校验超时时间。
func normalizeTimeoutS(value int) (int, error) {
	if value <= 0 {
		return defaultCheckTimeoutS, nil
	}
	if value < minCheckTimeoutS || value > maxCheckTimeoutS {
		return 0, fmt.Errorf("timeout_s 必须在 %d-%d 之间", minCheckTimeoutS, maxCheckTimeoutS)
	}
	return value, nil
}

// validateCheckTarget 按检测类型校验 target。
func validateCheckTarget(kind, target string) error {
	if target == "" {
		return errors.New("target 不能为空")
	}
	if len(target) > maxCheckTargetRunes {
		return fmt.Errorf("target 长度超过 %d 字节", maxCheckTargetRunes)
	}
	switch kind {
	case "tcp":
		host, portStr, err := splitHostPortLoose(target)
		if err != nil {
			return fmt.Errorf("tcp target 必须是 host:port 形式: %v", err)
		}
		if host == "" {
			return errors.New("tcp target 缺少主机名")
		}
		if portStr < 1 || portStr > 65535 {
			return errors.New("tcp target 端口必须在 1-65535 之间")
		}
	case "http":
		u, err := url.Parse(target)
		if err != nil {
			return fmt.Errorf("http target 不是合法 URL: %v", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return errors.New("http target 必须以 http:// 或 https:// 开头")
		}
		if u.Host == "" {
			return errors.New("http target 缺少主机名")
		}
	case "ping":
		if strings.ContainsAny(target, " \t\r\n/") {
			return errors.New("ping target 只能是主机名或 IP")
		}
	case "process":
		if err := validateText("target", target, 128, true); err != nil {
			return err
		}
	case "script":
		if len(target) > maxScriptBytes {
			return fmt.Errorf("script target 超过 %d 字节", maxScriptBytes)
		}
	default:
		return fmt.Errorf("未知检测类型 %q", kind)
	}
	return nil
}

// splitHostPortLoose 解析 host:port，支持 IPv6 字面量。
func splitHostPortLoose(target string) (string, int, error) {
	idx := strings.LastIndex(target, ":")
	if idx <= 0 || idx == len(target)-1 {
		return "", 0, errors.New("缺少端口")
	}
	host := strings.Trim(target[:idx], "[]")
	port, err := strconv.Atoi(target[idx+1:])
	if err != nil {
		return "", 0, errors.New("端口不是数字")
	}
	return host, port, nil
}

// normalizeCheckParams 按检测类型校验并归一化参数。
func normalizeCheckParams(kind string, in map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range in {
		if v == nil {
			continue
		}
		out[k] = v
	}

	if raw, ok := out["fail_threshold"]; ok {
		n, err := toIntParam(raw)
		if err != nil || n < 1 || n > 100 {
			return nil, errors.New("params.fail_threshold 必须是 1-100 的整数")
		}
		out["fail_threshold"] = n
	}

	// probe_via：探测通道（control=控制端直连，agent=目标机代检）。
	// 只有 tcp/http/ping 有意义——process/script 本来就在目标机执行。
	if raw, ok := out["probe_via"]; ok {
		via, isStr := raw.(string)
		if !isStr {
			return nil, errors.New("params.probe_via 只能是 control 或 agent")
		}
		via = strings.ToLower(strings.TrimSpace(via))
		switch via {
		case "control", "agent":
		case "":
			delete(out, "probe_via")
			via = ""
		default:
			return nil, errors.New("params.probe_via 只能是 control 或 agent")
		}
		if via != "" {
			if kind != "tcp" && kind != "http" && kind != "ping" {
				return nil, errors.New("params.probe_via 只适用于 tcp/http/ping（process/script 本身就在目标机执行）")
			}
			out["probe_via"] = via
		}
	}

	switch kind {
	case "tcp":
		if raw, ok := out["timeout_ms"]; ok {
			n, err := toIntParam(raw)
			if err != nil || n <= 0 || n > 600000 {
				return nil, errors.New("params.timeout_ms 必须是 1-600000 的整数")
			}
			out["timeout_ms"] = n
		}
	case "http":
		if raw, ok := out["expect_status"]; ok {
			if _, isStr := raw.(string); !isStr {
				n, err := toIntParam(raw)
				if err != nil || n < 100 || n > 599 {
					return nil, errors.New("params.expect_status 必须是状态码或形如 2xx 的字符串")
				}
				out["expect_status"] = n
			}
		}
		if raw, ok := out["method"]; ok {
			method, isStr := raw.(string)
			if !isStr || strings.TrimSpace(method) == "" {
				return nil, errors.New("params.method 必须是非空字符串")
			}
			out["method"] = strings.ToUpper(strings.TrimSpace(method))
		}
		if raw, ok := out["keyword"]; ok {
			if _, isStr := raw.(string); !isStr {
				return nil, errors.New("params.keyword 必须是字符串")
			}
		}
		if raw, ok := out["insecure"]; ok {
			if _, isBool := raw.(bool); !isBool {
				return nil, errors.New("params.insecure 必须是布尔值")
			}
		}
	case "process":
		if raw, ok := out["min_count"]; ok {
			n, err := toIntParam(raw)
			if err != nil || n < 1 || n > 100000 {
				return nil, errors.New("params.min_count 必须是正整数")
			}
			out["min_count"] = n
		}
		if raw, ok := out["user"]; ok {
			if _, isStr := raw.(string); !isStr {
				return nil, errors.New("params.user 必须是字符串")
			}
		}
	case "script":
		if raw, ok := out["exit_zero_ok"]; ok {
			if _, isBool := raw.(bool); !isBool {
				return nil, errors.New("params.exit_zero_ok 必须是布尔值")
			}
		}
		if raw, ok := out["expect_stdout"]; ok {
			if _, isStr := raw.(string); !isStr {
				return nil, errors.New("params.expect_stdout 必须是字符串")
			}
		}
	}
	return out, nil
}

// toIntParam 把 JSON 数字（float64/json.Number/int）转成 int。
func toIntParam(v any) (int, error) {
	switch n := v.(type) {
	case float64:
		if n != float64(int(n)) {
			return 0, errors.New("不是整数")
		}
		return int(n), nil
	case int:
		return n, nil
	case json.Number:
		i, err := n.Int64()
		return int(i), err
	case string:
		return strconv.Atoi(strings.TrimSpace(n))
	default:
		return 0, errors.New("不是数字")
	}
}

// round2 四舍五入保留两位小数。
func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

// filterCheckPayloadsByScope 按资源范围过滤检测项：只要有任一目标服务器在范围内即可见。
func (a *API) filterCheckPayloadsByScope(r *http.Request, in []checkPayload) []checkPayload {
	allowed := a.allowedServers(r)
	if allowed == nil {
		return in
	}
	out := make([]checkPayload, 0, len(in))
	for _, p := range in {
		for _, sid := range p.ServerIDs {
			if _, ok := allowed[sid]; ok {
				out = append(out, p)
				break
			}
		}
	}
	return out
}
