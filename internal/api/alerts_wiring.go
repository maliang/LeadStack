package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/maliang/leadstack/internal/alerting"
	"github.com/maliang/leadstack/internal/credential"
	"github.com/maliang/leadstack/internal/notify"
	"github.com/maliang/leadstack/internal/store"
)

// 通知发送相关参数。
const (
	// notifySendTimeout 是单渠道发送超时（契约第 19.2 节：单渠道 10s）。
	notifySendTimeout = 10 * time.Second
	// alertEventWriteTimeout 是写事件/回写渠道结果的操作超时。
	alertEventWriteTimeout = 5 * time.Second
)

// ------------------------------------------------ alerting.Store 适配器 ------------------------------------------------

// AlertStoreAdapter 把 store 适配成 alerting.Store（契约第 19.1 节）。
//
// 规则里的 check_ids/server_ids/groups/tags/channel_ids 在库里是 JSON 文本，
// 适配器负责解码成结构体，alerting 因此不需要 import store。
type AlertStoreAdapter struct {
	st     *store.Store
	logger *slog.Logger
}

// 编译期确认接口实现。
var _ alerting.Store = (*AlertStoreAdapter)(nil)

// NewAlertStore 创建 alerting.Store 适配器。
func NewAlertStore(st *store.Store, logger *slog.Logger) *AlertStoreAdapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &AlertStoreAdapter{st: st, logger: logger}
}

// ListRules 返回启用中的规则（JSON 列已解码）。
func (a *AlertStoreAdapter) ListRules(ctx context.Context) ([]alerting.Rule, error) {
	rows, err := a.st.ListEnabledAlertRules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]alerting.Rule, 0, len(rows))
	for _, r := range rows {
		out = append(out, toAlertingRule(r))
	}
	return out, nil
}

// FindActiveAlert 查未解决告警；不存在时返回 (nil, nil)，与引擎约定一致。
func (a *AlertStoreAdapter) FindActiveAlert(ctx context.Context, dedupKey string) (*alerting.Alert, error) {
	found, err := a.st.FindActiveAlert(ctx, dedupKey)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	converted := toAlertingAlert(*found)
	return &converted, nil
}

// CreateAlert 写入新告警。
func (a *AlertStoreAdapter) CreateAlert(ctx context.Context, item alerting.Alert) error {
	converted := fromAlertingAlert(item)
	return a.st.CreateAlert(ctx, converted)
}

// UpdateAlert 更新告警（去重累加、通知计数等）。
func (a *AlertStoreAdapter) UpdateAlert(ctx context.Context, item alerting.Alert) error {
	converted := fromAlertingAlert(item)
	return a.st.UpdateAlert(ctx, converted)
}

// ResolveAlert 把告警置为 resolved。
func (a *AlertStoreAdapter) ResolveAlert(ctx context.Context, id string, ts int64) error {
	return a.st.ResolveAlert(ctx, id, ts)
}

// AppendAlertEvent 追加事件轨迹。
func (a *AlertStoreAdapter) AppendAlertEvent(ctx context.Context, alertID, kind string, ts int64, detail string) error {
	return a.st.AppendAlertEvent(ctx, alertID, kind, ts, detail)
}

// toAlertingRule 把存储模型转成引擎模型。
func toAlertingRule(r store.AlertRule) alerting.Rule {
	rule := alerting.Rule{
		ID:           r.ID,
		Name:         r.Name,
		Enabled:      r.Enabled,
		Source:       alerting.Source(r.Source),
		CheckIDs:     r.CheckIDs,
		ServerIDs:    r.ServerIDs,
		Groups:       r.Groups,
		Tags:         r.Tags,
		ForDurationS: r.ForDurationS,
		Severity:     alerting.Severity(r.Severity),
		DedupWindowS: r.DedupWindowS,
		ChannelIDs:   r.ChannelIDs,
	}
	if r.Metric != nil {
		rule.Metric = *r.Metric
	}
	if r.Comparator != nil {
		rule.Comparator = *r.Comparator
	}
	if r.Threshold != nil {
		rule.Threshold = *r.Threshold
	}
	if r.SilenceUntil != nil {
		rule.SilenceUntil = *r.SilenceUntil
	}
	return rule
}

// toAlertingAlert 把存储模型转成引擎模型。
func toAlertingAlert(a store.Alert) alerting.Alert {
	out := alerting.Alert{
		ID:           a.ID,
		RuleID:       a.RuleID,
		DedupKey:     a.DedupKey,
		Status:       a.Status,
		Severity:     alerting.Severity(a.Severity),
		Title:        a.Title,
		StartedAt:    a.StartedAt,
		UpdatedAt:    a.UpdatedAt,
		NotifyCount:  a.NotifyCount,
		LastNotifyAt: derefInt64(a.LastNotifyAt),
		SilenceUntil: derefInt64(a.SilenceUntil),
		ResolvedAt:   derefInt64(a.ResolvedAt),
		AckedAt:      derefInt64(a.AckedAt),
	}
	if a.ServerID != nil {
		out.ServerID = *a.ServerID
	}
	if a.CheckID != nil {
		out.CheckID = *a.CheckID
	}
	if a.Message != nil {
		out.Message = *a.Message
	}
	if a.Value != nil {
		out.Value = *a.Value
	}
	return out
}

// fromAlertingAlert 把引擎模型转回存储模型。
func fromAlertingAlert(a alerting.Alert) store.Alert {
	out := store.Alert{
		ID:          a.ID,
		RuleID:      a.RuleID,
		DedupKey:    a.DedupKey,
		Status:      a.Status,
		Severity:    string(a.Severity),
		Title:       a.Title,
		StartedAt:   a.StartedAt,
		UpdatedAt:   a.UpdatedAt,
		NotifyCount: a.NotifyCount,
	}
	out.ServerID = strPtrOrNil(a.ServerID)
	out.CheckID = strPtrOrNil(a.CheckID)
	out.Message = strPtrOrNil(a.Message)
	// 引擎的 Value 是「有值」的（0 也是合法观测值），必须原样落库，不能当成 NULL 丢信息。
	value := a.Value
	out.Value = &value
	out.ResolvedAt = int64PtrOrNil(a.ResolvedAt)
	out.AckedAt = int64PtrOrNil(a.AckedAt)
	out.LastNotifyAt = int64PtrOrNil(a.LastNotifyAt)
	out.SilenceUntil = int64PtrOrNil(a.SilenceUntil)
	return out
}

// ------------------------------------------------ Notify 实现 ------------------------------------------------

// AlertNotifier 实现 alerting.Deps.Notify：逐个渠道发送，**单个渠道失败不影响其它渠道**。
type AlertNotifier struct {
	st     *store.Store
	cipher *credential.Cipher
	sender Notifier
	logger *slog.Logger
}

// NewAlertNotifier 创建通知发送器；cipher 或 sender 为空时返回 nil（引擎会得到空实现）。
func NewAlertNotifier(st *store.Store, cipher *credential.Cipher, sender Notifier, logger *slog.Logger) *AlertNotifier {
	if st == nil || cipher == nil || sender == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &AlertNotifier{st: st, cipher: cipher, sender: sender, logger: logger}
}

// Notify 按渠道列表发送；无论成功失败都会给告警追加一条 notify 事件。
func (n *AlertNotifier) Notify(ctx context.Context, a alerting.Alert, channels []string, recovered bool) {
	if n == nil {
		return
	}
	if len(channels) == 0 {
		n.appendEvent(ctx, a.ID, "规则未配置通知渠道，跳过发送")
		return
	}
	for _, channelID := range channels {
		// 每个渠道独立处理：任何一个出错都不会中断循环。
		n.notifyOne(ctx, a, channelID, recovered)
	}
}

// notifyOne 向单个渠道发送并回写结果。
func (n *AlertNotifier) notifyOne(ctx context.Context, a alerting.Alert, channelID string, recovered bool) {
	channel, err := n.st.GetAlertChannel(ctx, channelID)
	if err != nil {
		reason := "渠道不存在或已删除"
		if !errors.Is(err, store.ErrNotFound) {
			reason = "读取渠道失败: " + err.Error()
		}
		n.logger.Warn("告警通知跳过渠道", "alert_id", a.ID, "channel_id", channelID, "reason", reason)
		n.appendEvent(ctx, a.ID, fmt.Sprintf("渠道 %s 未发送：%s", channelID, reason))
		return
	}
	if !channel.Enabled {
		// notify 客户端不拦截 Enabled，必须由调用方判断（lead §M3 对齐事项）。
		n.logger.Debug("告警通知跳过已禁用渠道", "alert_id", a.ID, "channel_id", channelID)
		n.appendEvent(ctx, a.ID, fmt.Sprintf("渠道「%s」已禁用，跳过发送", channel.Name))
		return
	}

	config, err := n.decryptConfig(*channel)
	if err != nil {
		n.logger.Warn("渠道配置解密失败", "alert_id", a.ID, "channel_id", channelID, "err", err)
		n.writeChannelResult(ctx, channel.ID, nil, "渠道配置解密失败")
		n.appendEvent(ctx, a.ID, fmt.Sprintf("渠道「%s」发送失败：配置解密失败", channel.Name))
		return
	}

	msg := buildNotifyMessage(a, recovered)
	sendCtx, cancel := context.WithTimeout(ctx, notifySendTimeout)
	defer cancel()

	sendErr := n.sender.Send(sendCtx, notify.Channel{
		ID: channel.ID, Name: channel.Name, Kind: notify.Kind(channel.Kind),
		Enabled: channel.Enabled, Config: config,
	}, msg)

	kindLabel := "告警"
	if recovered {
		kindLabel = "恢复"
	}
	if sendErr != nil {
		reason := sendErr.Error()
		n.writeChannelResult(ctx, channel.ID, nil, classifySendError(reason, nil))
		n.logger.Warn("告警通知发送失败",
			"alert_id", a.ID, "channel_id", channel.ID, "kind", channel.Kind, "recovered", recovered, "err", reason)
		n.appendEvent(ctx, a.ID, fmt.Sprintf("%s通知经渠道「%s」(%s) 发送失败：%s",
			kindLabel, channel.Name, channel.Kind, reason))
		return
	}

	nowMS := store.NowMS()
	n.writeChannelResult(ctx, channel.ID, &nowMS, "")
	n.logger.Info("告警通知已发送",
		"alert_id", a.ID, "channel_id", channel.ID, "kind", channel.Kind, "recovered", recovered)
	n.appendEvent(ctx, a.ID, fmt.Sprintf("%s通知经渠道「%s」(%s) 发送成功",
		kindLabel, channel.Name, channel.Kind))
}

// appendEvent 追加一条通知类事件（失败只记日志）。
func (n *AlertNotifier) appendEvent(ctx context.Context, alertID, detail string) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), alertEventWriteTimeout)
	defer cancel()
	if err := n.st.AppendAlertEvent(writeCtx, alertID, store.AlertEventNotify, store.NowMS(), detail); err != nil {
		n.logger.Warn("写入通知事件失败", "alert_id", alertID, "err", err)
	}
}

// writeChannelResult 回写渠道发送结果（成功清空 last_error，失败记录原因）。
func (n *AlertNotifier) writeChannelResult(ctx context.Context, channelID string, okAt *int64, lastError string) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), alertEventWriteTimeout)
	defer cancel()
	var errPtr *string
	if strings.TrimSpace(lastError) != "" {
		errPtr = &lastError
	}
	if err := n.st.UpdateAlertChannelResult(writeCtx, channelID, okAt, errPtr); err != nil {
		n.logger.Warn("回写渠道发送结果失败", "channel_id", channelID, "err", err)
	}
}

// decryptConfig 解密渠道配置。
func (n *AlertNotifier) decryptConfig(channel store.AlertChannel) (map[string]any, error) {
	plain, err := n.cipher.Decrypt(channel.ConfigEnc)
	if err != nil {
		return nil, err
	}
	config := map[string]any{}
	if err := json.Unmarshal(plain, &config); err != nil {
		return nil, err
	}
	return config, nil
}

// buildNotifyMessage 组装通知内容（标题含规则名/服务器名，恢复时追加标记）。
func buildNotifyMessage(a alerting.Alert, recovered bool) notify.Message {
	title := a.Title
	if recovered {
		title = "[已恢复] " + title
	}
	body := a.Message
	if strings.TrimSpace(body) == "" {
		body = a.Title
	}
	return notify.Message{
		Title:     title,
		Body:      body,
		Severity:  notify.Severity(a.Severity),
		Value:     a.Value,
		TS:        store.NowMS(),
		Recovered: recovered,
		AlertID:   a.ID,
	}
}

// ------------------------------------------------ 事件接线 ------------------------------------------------

// AlertEventBridge 把三类触发源转成 alerting.Event / MetricSample 喂给引擎。
//
//  1. 检测项状态翻转（checker.Deps.OnStateChange）；
//  2. 服务器上下线（周期比对，见 PollServerStatuses）；
//  3. 指标阈值（周期把每台机器的最新指标快照交给 EvaluateMetrics）。
type AlertEventBridge struct {
	st     *store.Store
	engine AlertEngine
	logger *slog.Logger

	mu         sync.Mutex
	lastStatus map[string]string
}

// NewAlertEventBridge 创建事件桥。
func NewAlertEventBridge(st *store.Store, engine AlertEngine, logger *slog.Logger) *AlertEventBridge {
	if logger == nil {
		logger = slog.Default()
	}
	return &AlertEventBridge{st: st, engine: engine, logger: logger, lastStatus: map[string]string{}}
}

// OnCheckStateChange 是 checker.Deps.OnStateChange 的挂载点：检测项翻转 → 事件。
//
// active=true 表示异常发生（ok=false），false 表示恢复。
func (b *AlertEventBridge) OnCheckStateChange(checkID, serverID string, ok bool, since int64) {
	if b == nil || b.engine == nil || checkID == "" || serverID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), alertEventWriteTimeout)
	defer cancel()

	ev := alerting.Event{
		Source:   alerting.SourceCheck,
		CheckID:  checkID,
		ServerID: serverID,
		Active:   !ok,
		TS:       since,
	}
	if ev.TS <= 0 {
		ev.TS = store.NowMS()
	}

	if srv, err := b.st.GetServer(ctx, serverID); err == nil {
		ev.ServerName = srv.Name
		if srv.Group != nil {
			ev.Group = *srv.Group
		}
		ev.Tags = parseTags(srv.TagsJSON)
	} else {
		b.logger.Warn("检测事件缺少服务器信息", "server_id", serverID, "err", err)
	}
	if check, err := b.st.GetCheck(ctx, checkID); err == nil {
		ev.CheckName = check.Name
		if !ok {
			ev.Message = fmt.Sprintf("检测项「%s」异常（目标 %s）", check.Name, check.Target)
		} else {
			ev.Message = fmt.Sprintf("检测项「%s」已恢复", check.Name)
		}
	} else {
		b.logger.Warn("检测事件缺少检测项信息", "check_id", checkID, "err", err)
	}

	if err := b.engine.HandleEvent(ctx, ev); err != nil {
		b.logger.Warn("处理检测事件失败", "check_id", checkID, "server_id", serverID, "err", err)
	}
}

// PollServerStatuses 比对服务器在线状态并产生上下线事件。
//
// 采用「周期比对」而非在每个采集/心跳点回调：agent 与 ssh 两种模式的判定逻辑不同，
// 统一在这里用 EffectiveStatus 读一次全量状态即可覆盖，代价是事件延迟不超过一个评估周期。
func (b *AlertEventBridge) PollServerStatuses(ctx context.Context) {
	if b == nil || b.engine == nil {
		return
	}
	servers, err := b.st.ListServers(ctx)
	if err != nil {
		b.logger.Warn("巡检服务器状态失败", "err", err)
		return
	}
	nowMS := store.NowMS()

	for _, srv := range servers {
		status := store.EffectiveStatus(srv, nowMS)
		b.mu.Lock()
		prev, seen := b.lastStatus[srv.ID]
		b.lastStatus[srv.ID] = status
		b.mu.Unlock()

		// 首次只建立基线（不在重启后补发历史事件）；状态没变则无需处理。
		if !seen || prev == status {
			continue
		}
		// 只用 online/offline 两种确定状态参与告警；unknown 视为「未知」，不触发也不恢复。
		if status == "unknown" || prev == "unknown" {
			continue
		}
		b.emitServerEvent(ctx, srv, status == "online", nowMS)
	}
}

// emitServerEvent 把一次服务器状态变化转成事件。
func (b *AlertEventBridge) emitServerEvent(ctx context.Context, srv store.Server, online bool, ts int64) {
	ev := alerting.Event{
		Source:     alerting.SourceServer,
		ServerID:   srv.ID,
		ServerName: srv.Name,
		Active:     !online,
		TS:         ts,
	}
	if srv.Group != nil {
		ev.Group = *srv.Group
	}
	ev.Tags = parseTags(srv.TagsJSON)
	if online {
		ev.Message = fmt.Sprintf("服务器「%s」已恢复在线", srv.Name)
	} else {
		ev.Message = fmt.Sprintf("服务器「%s」已离线", srv.Name)
	}
	if err := b.engine.HandleEvent(ctx, ev); err != nil {
		b.logger.Warn("处理服务器状态事件失败", "server_id", srv.ID, "online", online, "err", err)
		return
	}
	b.logger.Info("服务器状态变化已转告警事件", "server_id", srv.ID, "online", online)
}

// EvaluateMetrics 把每台机器的最新指标快照喂给引擎做阈值评估。
func (b *AlertEventBridge) EvaluateMetrics(ctx context.Context) {
	if b == nil || b.engine == nil {
		return
	}
	servers, err := b.st.ListServers(ctx)
	if err != nil {
		b.logger.Warn("指标评估读取服务器失败", "err", err)
		return
	}
	// 注意：即使没有任何服务器/样本也要调用 EvaluateMetrics ——
	// 引擎在其中推进 for_duration 到期的 pending 规则，提前返回会让这类告警永远不触发。
	ids := make([]string, 0, len(servers))
	for _, srv := range servers {
		ids = append(ids, srv.ID)
	}
	metrics, err := b.st.LatestMetrics(ctx, ids)
	if err != nil {
		b.logger.Warn("指标评估读取最新指标失败", "err", err)
		return
	}

	nowMS := store.NowMS()
	samples := make([]alerting.MetricSample, 0, len(servers)*5)
	for _, srv := range servers {
		values := metrics[srv.ID]
		if len(values) == 0 {
			continue
		}
		base := alerting.MetricSample{ServerID: srv.ID, ServerName: srv.Name, TS: nowMS}
		if srv.Group != nil {
			base.Group = *srv.Group
		}
		base.Tags = parseTags(srv.TagsJSON)
		for metric, value := range values {
			sample := base
			sample.Metric = metric
			sample.Value = value
			samples = append(samples, sample)
		}
	}
	if err := b.engine.EvaluateMetrics(ctx, samples); err != nil {
		b.logger.Warn("指标阈值评估失败", "samples", len(samples), "err", err)
	}
}

// Run 周期执行「服务器状态巡检 + 指标阈值评估」，直到 ctx 取消。
func (b *AlertEventBridge) Run(ctx context.Context, interval time.Duration) {
	if b == nil || b.engine == nil {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	b.logger.Info("告警评估循环启动", "interval", interval.String())

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	b.PollServerStatuses(ctx) // 先建立状态基线
	b.EvaluateMetrics(ctx)

	for {
		select {
		case <-ctx.Done():
			b.logger.Info("告警评估循环退出")
			return
		case <-ticker.C:
			b.PollServerStatuses(ctx)
			b.EvaluateMetrics(ctx)
		}
	}
}

// derefInt64 解引用 int64 指针（nil → 0）。
func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// int64PtrOrNil 0 值返回 nil（库里存 NULL，语义为「未发生」）。
func int64PtrOrNil(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

// floatPtrOrNil 0 值返回 nil。
func floatPtrOrNil(v float64) *float64 {
	if v == 0 {
		return nil
	}
	return &v
}
