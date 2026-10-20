package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/alerting"
	"github.com/maliang/leadstack/internal/auth"
	"github.com/maliang/leadstack/internal/notify"
	"github.com/maliang/leadstack/internal/store"
)

// 告警模块的字段约束。
const (
	maxAlertNameRunes    = 128
	alertListDefault     = 100
	alertListMax         = 1000
	maxSilenceSeconds    = 30 * 24 * 3600
	alertEventsDefault   = 200
	maskedSecretKeepRune = 4
	// maskedSuffix 是脱敏后的固定后缀。
	maskedSuffix = "***"
)

// secretConfigKeys 是需要脱敏的渠道配置字段（契约第 19.2 节 + 常见等价命名）。
var secretConfigKeys = map[string]struct{}{
	"secret": {}, "password": {}, "bot_token": {}, "token": {},
	"api_key": {}, "apikey": {}, "access_token": {}, "webhook_secret": {},
}

// sensitiveHeaderNames 是常见敏感请求头（headers map 里的值同样脱敏）。
var sensitiveHeaderNames = map[string]struct{}{
	"authorization": {}, "x-api-key": {}, "x-auth-token": {}, "cookie": {}, "x-token": {},
}

// alertRulePayload 直接复用存储模型的 JSON 形态（字段与契约 §20 一致）。
type alertRulePayload = store.AlertRule

// alertChannelPayload 是渠道的对外表示：config 已脱敏。
type alertChannelPayload struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Kind      string         `json:"kind"`
	Enabled   bool           `json:"enabled"`
	Config    map[string]any `json:"config"`
	LastError *string        `json:"last_error"`
	LastOKAt  *int64         `json:"last_ok_at"`
	CreatedAt int64          `json:"created_at"`
}

// alertPayload 是告警的对外表示。
type alertPayload struct {
	store.Alert
	RuleName   string             `json:"rule_name,omitempty"`
	ServerName string             `json:"server_name,omitempty"`
	CheckName  string             `json:"check_name,omitempty"`
	Events     []store.AlertEvent `json:"events,omitempty"`
}

// createAlertRuleRequest 是创建规则请求体。
type createAlertRuleRequest struct {
	Name         string   `json:"name"`
	Enabled      *bool    `json:"enabled"`
	Source       string   `json:"source"`
	CheckIDs     []string `json:"check_ids"`
	ServerIDs    []string `json:"server_ids"`
	Groups       []string `json:"groups"`
	Tags         []string `json:"tags"`
	Metric       string   `json:"metric"`
	Comparator   string   `json:"comparator"`
	Threshold    *float64 `json:"threshold"`
	ForDurationS int      `json:"for_duration_s"`
	Severity     string   `json:"severity"`
	DedupWindowS *int     `json:"dedup_window_s"`
	ChannelIDs   []string `json:"channel_ids"`
}

// patchAlertRuleRequest 是更新规则请求体；nil 表示不修改。
type patchAlertRuleRequest struct {
	Name         *string   `json:"name"`
	Enabled      *bool     `json:"enabled"`
	Source       *string   `json:"source"`
	CheckIDs     *[]string `json:"check_ids"`
	ServerIDs    *[]string `json:"server_ids"`
	Groups       *[]string `json:"groups"`
	Tags         *[]string `json:"tags"`
	Metric       *string   `json:"metric"`
	Comparator   *string   `json:"comparator"`
	Threshold    *float64  `json:"threshold"`
	ForDurationS *int      `json:"for_duration_s"`
	Severity     *string   `json:"severity"`
	DedupWindowS *int      `json:"dedup_window_s"`
	ChannelIDs   *[]string `json:"channel_ids"`
}

// silenceRuleRequest 是静默请求体。
type silenceRuleRequest struct {
	DurationS int `json:"duration_s"`
}

// alertChannelRequest 是创建/更新渠道请求体。
type alertChannelRequest struct {
	Name    string         `json:"name"`
	Kind    string         `json:"kind"`
	Enabled *bool          `json:"enabled"`
	Config  map[string]any `json:"config"`
}

// channelTestResult 是渠道测试响应体（probe 为新增字段，既有字段不变）。
type channelTestResult struct {
	OK        bool              `json:"ok"`
	Error     string            `json:"error,omitempty"`
	ErrorKind string            `json:"error_kind,omitempty"`
	Probe     *channelProbeInfo `json:"probe,omitempty"`
}

// ------------------------------------------------ 规则 ------------------------------------------------

// handleListAlertRules 处理 GET /api/v1/alert-rules。
func (a *API) handleListAlertRules(w http.ResponseWriter, r *http.Request) {
	rules, err := a.st.ListAlertRules(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if rules == nil {
		rules = []store.AlertRule{}
	}
	writeData(w, http.StatusOK, rules)
}

// handleCreateAlertRule 处理 POST /api/v1/alert-rules。
func (a *API) handleCreateAlertRule(w http.ResponseWriter, r *http.Request) {
	var req createAlertRuleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	rule, err := a.buildAlertRule(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	ctx := r.Context()
	// 校验引用的检测项与渠道存在，避免留下悬空引用。
	if err := a.validateRuleRefs(ctx, rule); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if err := a.st.CreateAlertRule(ctx, rule); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.reloadAlerts()

	a.logger.Info("创建告警规则",
		"rule_id", rule.ID, "name", rule.Name, "source", rule.Source, "severity", rule.Severity,
		"channels", len(rule.ChannelIDs))
	writeData(w, http.StatusCreated, rule)
}

// handleGetAlertRule 处理 GET /api/v1/alert-rules/{id}。
func (a *API) handleGetAlertRule(w http.ResponseWriter, r *http.Request) {
	rule, err := a.loadAlertRule(w, r)
	if err != nil {
		return
	}
	writeData(w, http.StatusOK, rule)
}

// handlePatchAlertRule 处理 PATCH /api/v1/alert-rules/{id}。
func (a *API) handlePatchAlertRule(w http.ResponseWriter, r *http.Request) {
	rule, err := a.loadAlertRule(w, r)
	if err != nil {
		return
	}
	var req patchAlertRuleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	if req.Name == nil && req.Enabled == nil && req.Source == nil && req.CheckIDs == nil &&
		req.ServerIDs == nil && req.Groups == nil && req.Tags == nil && req.Metric == nil &&
		req.Comparator == nil && req.Threshold == nil && req.ForDurationS == nil &&
		req.Severity == nil && req.DedupWindowS == nil && req.ChannelIDs == nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "至少需要提供一个待更新字段")
		return
	}

	upd := store.AlertRuleUpdate{
		Name:         req.Name,
		Enabled:      req.Enabled,
		Source:       req.Source,
		CheckIDs:     req.CheckIDs,
		ServerIDs:    req.ServerIDs,
		Groups:       req.Groups,
		Tags:         req.Tags,
		Metric:       req.Metric,
		Comparator:   req.Comparator,
		Threshold:    req.Threshold,
		ForDurationS: req.ForDurationS,
		DedupWindowS: req.DedupWindowS,
		Severity:     req.Severity,
		ChannelIDs:   req.ChannelIDs,
	}
	// 校验合并后的有效规则（source 决定必填项）。
	merged := *rule
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if err := validateText("name", name, maxAlertNameRunes, true); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		merged.Name = name
	}
	if req.Source != nil {
		merged.Source = strings.TrimSpace(*req.Source)
	}
	if req.Severity != nil {
		merged.Severity = strings.TrimSpace(*req.Severity)
	}
	if req.ForDurationS != nil {
		merged.ForDurationS = *req.ForDurationS
	}
	if req.DedupWindowS != nil {
		merged.DedupWindowS = *req.DedupWindowS
	}
	if req.Metric != nil {
		merged.Metric = strPtrOrNil(strings.TrimSpace(*req.Metric))
	}
	if req.Comparator != nil {
		merged.Comparator = strPtrOrNil(strings.TrimSpace(*req.Comparator))
	}
	if req.Threshold != nil {
		merged.Threshold = req.Threshold
	}
	if req.CheckIDs != nil {
		merged.CheckIDs = *req.CheckIDs
	}
	if req.ServerIDs != nil {
		merged.ServerIDs = *req.ServerIDs
	}
	if req.ChannelIDs != nil {
		merged.ChannelIDs = *req.ChannelIDs
	}
	if err := validateAlertRule(&merged); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	if err := a.validateRuleRefs(r.Context(), &merged); err != nil {
		a.writeStoreError(w, r, err)
		return
	}

	updated, err := a.st.UpdateAlertRule(r.Context(), rule.ID, upd)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.reloadAlerts()
	a.logger.Info("更新告警规则", "rule_id", updated.ID, "enabled", updated.Enabled)
	writeData(w, http.StatusOK, updated)
}

// handleDeleteAlertRule 处理 DELETE /api/v1/alert-rules/{id}。
func (a *API) handleDeleteAlertRule(w http.ResponseWriter, r *http.Request) {
	rule, err := a.loadAlertRule(w, r)
	if err != nil {
		return
	}
	if err := a.st.DeleteAlertRule(r.Context(), rule.ID); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.reloadAlerts()
	a.logger.Info("删除告警规则", "rule_id", rule.ID)
	writeData(w, http.StatusOK, map[string]any{"id": rule.ID})
}

// handleSilenceAlertRule 处理 POST /api/v1/alert-rules/{id}/silence。
func (a *API) handleSilenceAlertRule(w http.ResponseWriter, r *http.Request) {
	rule, err := a.loadAlertRule(w, r)
	if err != nil {
		return
	}
	var req silenceRuleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	if req.DurationS < 0 || req.DurationS > maxSilenceSeconds {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			fmt.Sprintf("duration_s 必须在 0-%d 秒之间（0 表示取消静默）", maxSilenceSeconds))
		return
	}

	// duration_s=0：取消静默（清空 silence_until），前端「取消静默」按钮走这条路径。
	if req.DurationS == 0 {
		clear := true
		updated, err := a.st.UpdateAlertRule(r.Context(), rule.ID, store.AlertRuleUpdate{ClearSilence: &clear})
		if err != nil {
			a.writeStoreError(w, r, err)
			return
		}
		a.reloadAlerts()
		a.logger.Info("取消告警规则静默", "rule_id", rule.ID)
		writeData(w, http.StatusOK, updated)
		return
	}

	until := a.nowMS() + int64(req.DurationS)*1000
	updated, err := a.st.UpdateAlertRule(r.Context(), rule.ID, store.AlertRuleUpdate{SilenceUntil: &until})
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.reloadAlerts()
	a.logger.Info("静默告警规则", "rule_id", rule.ID, "until", until, "duration_s", req.DurationS)
	writeData(w, http.StatusOK, updated)
}

// ------------------------------------------------ 渠道 ------------------------------------------------

// handleListAlertChannels 处理 GET /api/v1/alert-channels（密钥字段脱敏）。
func (a *API) handleListAlertChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := a.st.ListAlertChannels(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]alertChannelPayload, 0, len(channels))
	for _, ch := range channels {
		item, err := a.toAlertChannelPayload(ch)
		if err != nil {
			a.logger.Warn("渠道配置解密失败，按空配置返回", "channel_id", ch.ID, "err", err)
			item = alertChannelPayload{
				ID: ch.ID, Name: ch.Name, Kind: ch.Kind, Enabled: ch.Enabled,
				Config: map[string]any{}, LastError: ch.LastError, LastOKAt: ch.LastOKAt,
				CreatedAt: ch.CreatedAt,
			}
		}
		out = append(out, item)
	}
	writeData(w, http.StatusOK, out)
}

// handleCreateAlertChannel 处理 POST /api/v1/alert-channels。
func (a *API) handleCreateAlertChannel(w http.ResponseWriter, r *http.Request) {
	if a.cipher == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "渠道配置加密未启用（缺少主密钥）")
		return
	}
	var req alertChannelRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := validateText("name", name, maxAlertNameRunes, true); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "kind 不能为空")
		return
	}
	if req.Config == nil {
		req.Config = map[string]any{}
	}
	if err := a.validateChannelConfig(r.Context(), kind, req.Config); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}

	encrypted, err := a.encryptChannelConfig(req.Config)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	channel := &store.AlertChannel{
		ID:        uuid.NewString(),
		Name:      name,
		Kind:      kind,
		ConfigEnc: encrypted,
		Enabled:   enabled,
		CreatedAt: a.nowMS(),
	}
	if err := a.st.CreateAlertChannel(r.Context(), channel); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.reloadAlerts()

	// 审计日志只记渠道名与类型，绝不记录配置内容。
	a.logger.Info("创建通知渠道", "channel_id", channel.ID, "name", channel.Name, "kind", channel.Kind)
	payload, err := a.toAlertChannelPayload(*channel)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, payload)
}

// handlePatchAlertChannel 处理 PATCH /api/v1/alert-channels/{id}。
func (a *API) handlePatchAlertChannel(w http.ResponseWriter, r *http.Request) {
	channel, err := a.loadAlertChannel(w, r)
	if err != nil {
		return
	}
	var req alertChannelRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}

	var namePtr, kindPtr *string
	if strings.TrimSpace(req.Name) != "" {
		name := strings.TrimSpace(req.Name)
		if err := validateText("name", name, maxAlertNameRunes, true); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		namePtr = &name
	}
	effectiveKind := channel.Kind
	if strings.TrimSpace(req.Kind) != "" {
		k := strings.TrimSpace(req.Kind)
		kindPtr = &k
		effectiveKind = k
	}

	var encrypted []byte
	if req.Config != nil {
		// 合并式更新：前端 PATCH 只发用户新填的字段，未出现的 key 保持原值；
		// 若把脱敏串（***/•••）回传，则视为「不修改」，避免把掩码写进库里。
		merged, err := a.mergeChannelConfig(*channel, req.Config)
		if err != nil {
			a.internalError(w, r, err)
			return
		}
		if err := a.validateChannelConfig(r.Context(), effectiveKind, merged); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		encrypted, err = a.encryptChannelConfig(merged)
		if err != nil {
			a.internalError(w, r, err)
			return
		}
	}
	if namePtr == nil && kindPtr == nil && encrypted == nil && req.Enabled == nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "至少需要提供 name/kind/config/enabled 之一")
		return
	}

	updated, err := a.st.UpdateAlertChannel(r.Context(), channel.ID, namePtr, kindPtr, encrypted, req.Enabled)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.reloadAlerts()
	a.logger.Info("更新通知渠道", "channel_id", updated.ID, "kind", updated.Kind)
	payload, err := a.toAlertChannelPayload(*updated)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, payload)
}

// handleDeleteAlertChannel 处理 DELETE /api/v1/alert-channels/{id}。
func (a *API) handleDeleteAlertChannel(w http.ResponseWriter, r *http.Request) {
	channel, err := a.loadAlertChannel(w, r)
	if err != nil {
		return
	}
	if err := a.st.DeleteAlertChannel(r.Context(), channel.ID); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.reloadAlerts()
	a.logger.Info("删除通知渠道", "channel_id", channel.ID)
	writeData(w, http.StatusOK, map[string]any{"id": channel.ID})
}

// handleTestAlertChannel 处理 POST /api/v1/alert-channels/{id}/test。
//
// 约定（lead 告警 对齐事项 2）：HTTP 恒 200，结果放 {data:{ok,error?}}；渠道不存在仍 404。
func (a *API) handleTestAlertChannel(w http.ResponseWriter, r *http.Request) {
	channel, err := a.loadAlertChannel(w, r)
	if err != nil {
		return
	}
	if a.notifier == nil {
		writeData(w, http.StatusOK, channelTestResult{OK: false, Error: "通知模块未启用"})
		return
	}
	config, derr := a.decryptChannelConfig(*channel)
	if derr != nil {
		a.logger.Warn("渠道配置解密失败", "channel_id", channel.ID, "err", derr)
		writeData(w, http.StatusOK, channelTestResult{OK: false, Error: "渠道配置解密失败"})
		return
	}

	msg := notify.Message{
		Title:    "LeadStack 测试通知",
		Body:     fmt.Sprintf("这是一条来自 LeadStack 的测试消息（渠道 %s，类型 %s）。", channel.Name, channel.Kind),
		Severity: notify.SeverityInfo,
		TS:       a.nowMS(),
	}
	sendErr := a.notifier.Send(r.Context(), notify.Channel{
		ID: channel.ID, Name: channel.Name, Kind: notify.Kind(channel.Kind),
		Enabled: channel.Enabled, Config: config,
	}, msg)

	// 回写发送结果，便于排障。
	nowMS := a.nowMS()
	if sendErr != nil {
		errText := classifySendError(sendErr.Error(), sendErr)
		if uerr := a.st.UpdateAlertChannelResult(r.Context(), channel.ID, nil, &errText); uerr != nil {
			a.logger.Warn("回写渠道失败原因出错", "channel_id", channel.ID, "err", uerr)
		}
		a.logger.Warn("渠道测试失败", "channel_id", channel.ID, "kind", channel.Kind, "err", errText)
		result := channelTestResult{OK: false, Error: errText}
		if se, ok := notify.AsSendError(sendErr); ok {
			result.ErrorKind = string(se.Kind)
		}
		// 真实发送失败时额外做一次端点探测，区分「配置错」与「网络不通」。
		result.Probe = a.probeAlertChannel(r.Context(), notify.Channel{
			ID: channel.ID, Name: channel.Name, Kind: notify.Kind(channel.Kind),
			Enabled: channel.Enabled, Config: config,
		})
		writeData(w, http.StatusOK, result)
		return
	}
	if uerr := a.st.UpdateAlertChannelResult(r.Context(), channel.ID, &nowMS, nil); uerr != nil {
		a.logger.Warn("回写渠道成功时间出错", "channel_id", channel.ID, "err", uerr)
	}
	a.logger.Info("渠道测试成功", "channel_id", channel.ID, "kind", channel.Kind)
	writeData(w, http.StatusOK, channelTestResult{OK: true})
}

// ------------------------------------------------ 告警 ------------------------------------------------

// handleListAlerts 处理 GET /api/v1/alerts。
func (a *API) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	limit := alertListDefault
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		v, err := parseQueryInt(r, "limit", alertListDefault)
		if err != nil || v <= 0 {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "limit 必须是正整数")
			return
		}
		if v > alertListMax {
			v = alertListMax
		}
		limit = int(v)
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

	params, perr := parsePage(r)
	if perr != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, perr.Error())
		return
	}
	if params.Paged {
		limit = params.fetchLimit(alertListDefault)
		if limit > alertListMax {
			limit = alertListMax
		}
	}
	filter := store.AlertFilter{
		Status:   strings.TrimSpace(r.URL.Query().Get("status")),
		Severity: strings.TrimSpace(r.URL.Query().Get("severity")),
		RuleID:   strings.TrimSpace(r.URL.Query().Get("rule_id")),
		ServerID: strings.TrimSpace(r.URL.Query().Get("server_id")),
		From:     from,
		To:       to,
		Limit:    limit,
	}
	for _, pair := range []struct{ field, value string }{
		{"status", filter.Status}, {"severity", filter.Severity},
	} {
		if pair.value == "" {
			continue
		}
		if !isValidAlertEnum(pair.field, pair.value) {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, pair.field+" 取值非法")
			return
		}
	}

	alerts, err := a.st.ListAlerts(r.Context(), filter)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if alerts == nil {
		alerts = []store.Alert{}
	}
	names, err := a.alertNameIndex(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]alertPayload, 0, len(alerts))
	for _, item := range alerts {
		out = append(out, a.toAlertPayload(item, names, nil))
	}
	start, end, meta := params.window(len(out))
	if !params.Paged {
		start, end = 0, len(out)
	}
	writePagedList(w, params, out[start:end], meta)
}

// handleGetAlert 处理 GET /api/v1/alerts/{id}（含 events）。
func (a *API) handleGetAlert(w http.ResponseWriter, r *http.Request) {
	alert, err := a.loadAlert(w, r)
	if err != nil {
		return
	}
	events, err := a.st.ListAlertEvents(r.Context(), alert.ID, alertEventsDefault)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if events == nil {
		events = []store.AlertEvent{}
	}
	names, err := a.alertNameIndex(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, a.toAlertPayload(*alert, names, events))
}

// handleAckAlert 处理 POST /api/v1/alerts/{id}/ack。
func (a *API) handleAckAlert(w http.ResponseWriter, r *http.Request) {
	alert, err := a.loadAlert(w, r)
	if err != nil {
		return
	}
	if alert.Status == store.AlertStatusResolved {
		writeError(w, http.StatusConflict, codeConflict, "告警已恢复，无需确认")
		return
	}
	by := "admin"
	if claims, ok := auth.ClaimsFromRequest(r); ok && claims.Username != "" {
		by = claims.Username
	}
	nowMS := a.nowMS()
	updated, err := a.st.AckAlert(r.Context(), alert.ID, by, nowMS)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if err := a.st.AppendAlertEvent(r.Context(), alert.ID, store.AlertEventAcked, nowMS, "由 "+by+" 确认"); err != nil {
		a.logger.Warn("写入确认事件失败", "alert_id", alert.ID, "err", err)
	}
	a.logger.Info("确认告警", "alert_id", alert.ID, "by", by)
	names, _ := a.alertNameIndex(r.Context())
	writeData(w, http.StatusOK, a.toAlertPayload(*updated, names, nil))
}

// handleAlertSummary 处理 GET /api/v1/alerts/summary。
func (a *API) handleAlertSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := a.st.AlertSummary(r.Context(), a.nowMS())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, summary)
}

// ------------------------------------------------ 辅助 ------------------------------------------------

// buildAlertRule 校验并构造规则。
func (a *API) buildAlertRule(req createAlertRuleRequest) (*store.AlertRule, error) {
	name := strings.TrimSpace(req.Name)
	if err := validateText("name", name, maxAlertNameRunes, true); err != nil {
		return nil, err
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	dedup := alerting.DefaultDedupWindowS
	if req.DedupWindowS != nil {
		if *req.DedupWindowS < 0 || *req.DedupWindowS > 7*24*3600 {
			return nil, errors.New("dedup_window_s 必须在 0-604800 之间")
		}
		dedup = *req.DedupWindowS
	}
	rule := &store.AlertRule{
		ID:           uuid.NewString(),
		Name:         name,
		Enabled:      enabled,
		Source:       strings.TrimSpace(req.Source),
		CheckIDs:     nonNilStrings(req.CheckIDs),
		ServerIDs:    nonNilStrings(req.ServerIDs),
		Groups:       nonNilStrings(req.Groups),
		Tags:         nonNilStrings(req.Tags),
		ForDurationS: req.ForDurationS,
		Severity:     strings.TrimSpace(req.Severity),
		DedupWindowS: dedup,
		ChannelIDs:   nonNilStrings(req.ChannelIDs),
		CreatedAt:    a.nowMS(),
	}
	if s := strings.TrimSpace(req.Metric); s != "" {
		rule.Metric = &s
	}
	if s := strings.TrimSpace(req.Comparator); s != "" {
		rule.Comparator = &s
	}
	rule.Threshold = req.Threshold
	if err := validateAlertRule(rule); err != nil {
		return nil, err
	}
	return rule, nil
}

// validateAlertRule 校验规则的取值组合。
func validateAlertRule(rule *store.AlertRule) error {
	switch rule.Source {
	case "check", "server", "metric":
	default:
		return errors.New("source 只能是 check|server|metric")
	}
	switch rule.Severity {
	case "critical", "warning", "info":
	default:
		return errors.New("severity 只能是 critical|warning|info")
	}
	if rule.ForDurationS < 0 || rule.ForDurationS > 7*24*3600 {
		return errors.New("for_duration_s 必须在 0-604800 之间")
	}
	if rule.Source == "metric" {
		if rule.Metric == nil || *rule.Metric == "" {
			return errors.New("source=metric 时必须提供 metric")
		}
		if rule.Comparator == nil {
			return errors.New("source=metric 时必须提供 comparator")
		}
		switch *rule.Comparator {
		case ">", ">=", "<", "<=":
		default:
			return errors.New("comparator 只能是 > >= < <=")
		}
		if rule.Threshold == nil {
			return errors.New("source=metric 时必须提供 threshold")
		}
	}
	return nil
}

// validateRuleRefs 校验规则引用的检测项与渠道存在。
func (a *API) validateRuleRefs(ctx context.Context, rule *store.AlertRule) error {
	for _, checkID := range rule.CheckIDs {
		if _, err := a.st.GetCheck(ctx, checkID); err != nil {
			return err
		}
	}
	for _, channelID := range rule.ChannelIDs {
		if _, err := a.st.GetAlertChannel(ctx, channelID); err != nil {
			return err
		}
	}
	return nil
}

// loadAlertRule 读取路径上的规则；失败时已写响应。
func (a *API) loadAlertRule(w http.ResponseWriter, r *http.Request) (*store.AlertRule, error) {
	id := chi.URLParam(r, "ruleID")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少规则 ID")
		return nil, errors.New("缺少规则 ID")
	}
	rule, err := a.st.GetAlertRule(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return nil, err
	}
	return rule, nil
}

// loadAlertChannel 读取路径上的渠道；失败时已写响应。
func (a *API) loadAlertChannel(w http.ResponseWriter, r *http.Request) (*store.AlertChannel, error) {
	id := chi.URLParam(r, "channelID")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少渠道 ID")
		return nil, errors.New("缺少渠道 ID")
	}
	channel, err := a.st.GetAlertChannel(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return nil, err
	}
	return channel, nil
}

// loadAlert 读取路径上的告警；失败时已写响应。
func (a *API) loadAlert(w http.ResponseWriter, r *http.Request) (*store.Alert, error) {
	id := chi.URLParam(r, "alertID")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少告警 ID")
		return nil, errors.New("缺少告警 ID")
	}
	alert, err := a.st.GetAlert(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return nil, err
	}
	return alert, nil
}

// isValidAlertEnum 校验 status/severity 查询参数。
func isValidAlertEnum(field, value string) bool {
	switch field {
	case "status":
		return value == store.AlertStatusFiring || value == store.AlertStatusAcked || value == store.AlertStatusResolved
	case "severity":
		return value == "critical" || value == "warning" || value == "info"
	default:
		return true
	}
}

// encryptChannelConfig 序列化并用主密钥加密渠道配置。
func (a *API) encryptChannelConfig(config map[string]any) ([]byte, error) {
	plain, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("序列化渠道配置: %w", err)
	}
	encrypted, err := a.cipher.Encrypt(plain)
	if err != nil {
		return nil, fmt.Errorf("加密渠道配置: %w", err)
	}
	return encrypted, nil
}

// decryptChannelConfig 解密渠道配置。
func (a *API) decryptChannelConfig(channel store.AlertChannel) (map[string]any, error) {
	if a.cipher == nil {
		return nil, errors.New("渠道配置加密未启用")
	}
	plain, err := a.cipher.Decrypt(channel.ConfigEnc)
	if err != nil {
		return nil, fmt.Errorf("解密渠道配置: %w", err)
	}
	config := map[string]any{}
	if err := json.Unmarshal(plain, &config); err != nil {
		return nil, fmt.Errorf("解析渠道配置: %w", err)
	}
	return config, nil
}

// validateChannelConfig 校验渠道配置（交给 notify 的规则）。
func (a *API) validateChannelConfig(ctx context.Context, kind string, config map[string]any) error {
	if a.notifier == nil {
		return nil // 通知模块未装配时只做基本校验，避免阻塞录入
	}
	if err := a.notifier.Validate(ctx, kind, config); err != nil {
		return fmt.Errorf("渠道配置不合法: %w", err)
	}
	return nil
}

// toAlertChannelPayload 转成对外结构：**配置中的密钥类字段一律脱敏**。
func (a *API) toAlertChannelPayload(channel store.AlertChannel) (alertChannelPayload, error) {
	config, err := a.decryptChannelConfig(channel)
	if err != nil {
		return alertChannelPayload{}, err
	}
	return alertChannelPayload{
		ID:        channel.ID,
		Name:      channel.Name,
		Kind:      channel.Kind,
		Enabled:   channel.Enabled,
		Config:    maskChannelConfig(channel.Kind, config),
		LastError: channel.LastError,
		LastOKAt:  channel.LastOKAt,
		CreatedAt: channel.CreatedAt,
	}, nil
}

// mergeChannelConfig 把 PATCH 传来的部分配置合并到现有配置上。
//
// 规则：
//   - 未出现的 key：保持原值（前端「留空则不发」的语义）；
//   - 密钥类 key 传空串或脱敏串（***）：视为不修改，避免误清空或把掩码写进库；
//   - 其余情况：覆盖。
func (a *API) mergeChannelConfig(channel store.AlertChannel, patch map[string]any) (map[string]any, error) {
	current, err := a.decryptChannelConfig(channel)
	if err != nil {
		// 解密失败（例如主密钥换过）时按全新配置处理，让用户能重新录入。
		a.logger.Warn("渠道配置解密失败，按新配置覆盖", "channel_id", channel.ID, "err", err)
		current = map[string]any{}
	}
	for key, value := range patch {
		lower := strings.ToLower(key)
		_, isSecret := secretConfigKeys[lower]
		if s, ok := value.(string); ok {
			// 脱敏串（含 URL 脱敏后的形态）一律视为「不修改」，避免把掩码写进库里。
			if isMaskedSecretValue(s) {
				continue
			}
			// 密钥字段传空串同样视为「不修改」，防止误清空凭证。
			if isSecret && strings.TrimSpace(s) == "" {
				continue
			}
		}
		current[key] = value
	}
	return current, nil
}

// isMaskedSecretValue 判断一个值是否是我们返回的脱敏串。
func isMaskedSecretValue(s string) bool {
	return strings.Contains(s, maskedSuffix) || strings.Contains(s, "•••")
}

// maskChannelConfig 返回脱敏后的配置副本。
//
// 选择「掩码」而非「整字段不返回」：前端需要知道「该字段已配置」，否则编辑弹窗无法提示用户。
// 具体规则复用 internal/notify 的 MaskConfig（单一事实来源）：密钥类字段保留前 4 位 + "***"，
// **webhook/api_base 这类本身即凭证的 URL 也会脱敏**（钉钉/企微/飞书/slack 的整条 URL 就是密钥），
// headers 里的 Authorization 等同样脱敏。
func maskChannelConfig(kind string, config map[string]any) map[string]any {
	masked := notify.MaskConfig(notify.Channel{Kind: notify.Kind(kind), Config: config})

	// 兜底：MaskConfig 只处理字符串值，非字符串的敏感字段（畸形配置）在这里统一抹掉。
	for key, value := range masked {
		lower := strings.ToLower(key)
		if _, secret := secretConfigKeys[lower]; !secret {
			continue
		}
		if _, isStr := value.(string); !isStr && value != nil {
			masked[key] = maskedSuffix
		}
	}
	return masked
}

// maskSecretValue 对单个密钥值脱敏：保留前 4 个字符，其余用 *** 代替。
func maskSecretValue(value any) any {
	s, ok := value.(string)
	if !ok || s == "" {
		if value == nil {
			return nil
		}
		// 非字符串密钥（罕见）：统一替换为掩码，避免任何形式的泄露。
		return maskedSuffix
	}
	runes := []rune(s)
	if len(runes) <= maskedSecretKeepRune {
		return maskedSuffix
	}
	return string(runes[:maskedSecretKeepRune]) + maskedSuffix
}

// alertNameIndex 返回告警展示需要的名称索引（规则名/服务器名/检测项名）。
type alertNames struct {
	rules   map[string]string
	servers map[string]string
	checks  map[string]string
}

// alertNameIndex 构造名称索引。
func (a *API) alertNameIndex(ctx context.Context) (alertNames, error) {
	out := alertNames{rules: map[string]string{}, servers: map[string]string{}, checks: map[string]string{}}

	rules, err := a.st.ListAlertRules(ctx)
	if err != nil {
		return out, err
	}
	for _, rule := range rules {
		out.rules[rule.ID] = rule.Name
	}
	servers, err := a.st.ListServers(ctx)
	if err != nil {
		return out, err
	}
	for _, srv := range servers {
		out.servers[srv.ID] = srv.Name
	}
	checks, err := a.st.ListChecks(ctx)
	if err != nil {
		return out, err
	}
	for _, check := range checks {
		out.checks[check.ID] = check.Name
	}
	return out, nil
}

// toAlertPayload 转成对外结构（附带可读名称与事件）。
func (a *API) toAlertPayload(alert store.Alert, names alertNames, events []store.AlertEvent) alertPayload {
	out := alertPayload{Alert: alert, Events: events}
	out.RuleName = names.rules[alert.RuleID]
	if alert.ServerID != nil {
		out.ServerName = names.servers[*alert.ServerID]
	}
	if alert.CheckID != nil {
		out.CheckName = names.checks[*alert.CheckID]
	}
	return out
}

// reloadAlerts 通知引擎刷新规则缓存（规则/渠道增删改后必须调用）。
func (a *API) reloadAlerts() {
	if a.alerts == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.alerts.Reload(ctx); err != nil {
		a.logger.Warn("刷新告警规则缓存失败", "err", err)
	}
}

// nonNilStrings 保证切片非 nil（JSON 输出 []）。
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// strPtrOrNil 空串返回 nil，便于表达「清空该字段」。
func strPtrOrNil(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
