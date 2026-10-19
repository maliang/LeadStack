package store

import (
	"context"
	"errors"
	"testing"
)

// TestAlertRuleCRUDAndJSONColumns 覆盖规则 CRUD 与 JSON 列往返。
func TestAlertRuleCRUDAndJSONColumns(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	metric := "cpu.pct"
	comparator := ">"
	threshold := 90.5
	rule := &AlertRule{
		ID: "rule-1", Name: "CPU 过高", Enabled: true, Source: "metric",
		CheckIDs: []string{"chk-a", "chk-b"}, ServerIDs: []string{"srv-1"},
		Groups: []string{"prod"}, Tags: []string{"web", "bj"},
		Metric: &metric, Comparator: &comparator, Threshold: &threshold,
		ForDurationS: 60, Severity: "critical", DedupWindowS: 300,
		ChannelIDs: []string{"ch-1", "ch-2"}, CreatedAt: NowMS(),
	}
	if err := st.CreateAlertRule(ctx, rule); err != nil {
		t.Fatalf("创建规则失败: %v", err)
	}

	got, err := st.GetAlertRule(ctx, rule.ID)
	if err != nil {
		t.Fatalf("查询规则失败: %v", err)
	}
	// JSON 列必须完整往返
	if len(got.CheckIDs) != 2 || got.CheckIDs[0] != "chk-a" || got.CheckIDs[1] != "chk-b" {
		t.Fatalf("check_ids 往返失败: %v", got.CheckIDs)
	}
	if len(got.ServerIDs) != 1 || got.ServerIDs[0] != "srv-1" {
		t.Fatalf("server_ids 往返失败: %v", got.ServerIDs)
	}
	if len(got.Groups) != 1 || got.Groups[0] != "prod" {
		t.Fatalf("groups 往返失败: %v", got.Groups)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "web" {
		t.Fatalf("tags 往返失败: %v", got.Tags)
	}
	if len(got.ChannelIDs) != 2 || got.ChannelIDs[1] != "ch-2" {
		t.Fatalf("channel_ids 往返失败: %v", got.ChannelIDs)
	}
	if got.Metric == nil || *got.Metric != metric || got.Comparator == nil || *got.Comparator != comparator {
		t.Fatalf("metric/comparator 往返失败: %v / %v", got.Metric, got.Comparator)
	}
	if got.Threshold == nil || *got.Threshold != threshold {
		t.Fatalf("threshold 往返失败: %v", got.Threshold)
	}

	// 空切片也要落成 [] 而不是 null
	if err := st.CreateAlertRule(ctx, &AlertRule{
		ID: "rule-2", Name: "无目标", Enabled: false, Source: "check",
		ForDurationS: 0, Severity: "warning", DedupWindowS: 300, CreatedAt: NowMS(),
	}); err != nil {
		t.Fatalf("创建规则失败: %v", err)
	}
	empty, _ := st.GetAlertRule(ctx, "rule-2")
	if empty.CheckIDs == nil || len(empty.CheckIDs) != 0 {
		t.Fatalf("空 JSON 列应解成空切片，实际 %#v", empty.CheckIDs)
	}

	// 部分更新：只改 enabled 与 channel_ids，其余不动
	newName := "改名"
	enabled := false
	channels := []string{"ch-9"}
	updated, err := st.UpdateAlertRule(ctx, rule.ID, AlertRuleUpdate{
		Name: &newName, Enabled: &enabled, ChannelIDs: &channels,
	})
	if err != nil {
		t.Fatalf("更新规则失败: %v", err)
	}
	if updated.Name != newName || updated.Enabled || len(updated.ChannelIDs) != 1 {
		t.Fatalf("更新结果不正确: %+v", updated)
	}
	if updated.Metric == nil || *updated.Metric != metric {
		t.Fatalf("未提供的字段不应被清空: %v", updated.Metric)
	}

	// 静默
	until := NowMS() + 60000
	silenced, err := st.UpdateAlertRule(ctx, rule.ID, AlertRuleUpdate{SilenceUntil: &until})
	if err != nil {
		t.Fatalf("设置静默失败: %v", err)
	}
	if silenced.SilenceUntil == nil || *silenced.SilenceUntil != until {
		t.Fatalf("silence_until 未生效: %v", silenced.SilenceUntil)
	}

	// 启用列表只含启用规则
	enabledList, err := st.ListEnabledAlertRules(ctx)
	if err != nil {
		t.Fatalf("查询启用规则失败: %v", err)
	}
	if len(enabledList) != 1 || enabledList[0].ID != "rule-1" {
		// rule-1 已被置为 disabled，启用列表应只剩……没有
		if len(enabledList) != 0 {
			t.Fatalf("启用规则列表不正确: %+v", enabledList)
		}
	}

	if _, err := st.GetAlertRule(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("查询不存在的规则期望 ErrNotFound，实际 %v", err)
	}
	if err := st.DeleteAlertRule(ctx, rule.ID); err != nil {
		t.Fatalf("删除规则失败: %v", err)
	}
	if err := st.DeleteAlertRule(ctx, rule.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除期望 ErrNotFound，实际 %v", err)
	}
}

// TestAlertChannelStorage 覆盖渠道存取与发送结果回写。
func TestAlertChannelStorage(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	channel := &AlertChannel{
		ID: "ch-1", Name: "运维群", Kind: "dingtalk",
		ConfigEnc: []byte{1, 2, 3, 4}, Enabled: true, CreatedAt: NowMS(),
	}
	if err := st.CreateAlertChannel(ctx, channel); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	// 契约 §21 的 alert_channels 表没有 name 唯一约束，因此重名允许（按 id 区分）。
	if err := st.CreateAlertChannel(ctx, &AlertChannel{
		ID: "ch-2", Name: "运维群", Kind: "webhook", ConfigEnc: []byte{5}, CreatedAt: NowMS(),
	}); err != nil {
		t.Fatalf("同名渠道应允许创建: %v", err)
	}
	if err := st.DeleteAlertChannel(ctx, "ch-2"); err != nil {
		t.Fatalf("清理临时渠道失败: %v", err)
	}

	got, err := st.GetAlertChannel(ctx, "ch-1")
	if err != nil {
		t.Fatalf("查询渠道失败: %v", err)
	}
	if string(got.ConfigEnc) != string([]byte{1, 2, 3, 4}) || !got.Enabled {
		t.Fatalf("渠道内容不正确: %+v", got)
	}

	// 失败：写 last_error，last_ok_at 不变
	reason := "HTTP 非 2xx: 500"
	if err := st.UpdateAlertChannelResult(ctx, "ch-1", nil, &reason); err != nil {
		t.Fatalf("回写失败结果出错: %v", err)
	}
	got, _ = st.GetAlertChannel(ctx, "ch-1")
	if got.LastError == nil || *got.LastError != reason {
		t.Fatalf("失败原因未写入: %v", got.LastError)
	}
	if got.LastOKAt != nil {
		t.Fatalf("失败不应写 last_ok_at: %v", got.LastOKAt)
	}

	// 成功：清空 last_error 并写 last_ok_at
	okAt := NowMS()
	if err := st.UpdateAlertChannelResult(ctx, "ch-1", &okAt, nil); err != nil {
		t.Fatalf("回写成功结果出错: %v", err)
	}
	got, _ = st.GetAlertChannel(ctx, "ch-1")
	if got.LastError != nil {
		t.Fatalf("成功后 last_error 应被清空，实际 %q", *got.LastError)
	}
	if got.LastOKAt == nil || *got.LastOKAt != okAt {
		t.Fatalf("last_ok_at 未写入: %v", got.LastOKAt)
	}

	// 更新密文与启用状态
	newName := "运维群2"
	disabled := false
	updated, err := st.UpdateAlertChannel(ctx, "ch-1", &newName, nil, []byte{9, 9}, &disabled)
	if err != nil {
		t.Fatalf("更新渠道失败: %v", err)
	}
	if updated.Name != newName || updated.Enabled || string(updated.ConfigEnc) != string([]byte{9, 9}) {
		t.Fatalf("更新结果不正确: %+v", updated)
	}

	list, err := st.ListAlertChannels(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("渠道列表不正确: %v / %v", list, err)
	}

	if err := st.DeleteAlertChannel(ctx, "ch-1"); err != nil {
		t.Fatalf("删除渠道失败: %v", err)
	}
	if err := st.DeleteAlertChannel(ctx, "ch-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除期望 ErrNotFound，实际 %v", err)
	}
}

// TestAlertLifecycle 覆盖告警状态流转、去重查询、过滤与 summary。
func TestAlertLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := NowMS()

	serverID, checkID := "srv-1", "chk-1"
	alert := Alert{
		ID: "alert-1", RuleID: "rule-1", DedupKey: "check:chk-1:srv-1",
		ServerID: &serverID, CheckID: &checkID,
		Status: AlertStatusFiring, Severity: "critical", Title: "检测项异常",
		StartedAt: now, UpdatedAt: now,
	}
	if err := st.CreateAlert(ctx, alert); err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}

	// 同一 dedup_key 查活跃告警
	active, err := st.FindActiveAlert(ctx, "check:chk-1:srv-1")
	if err != nil {
		t.Fatalf("查询活跃告警失败: %v", err)
	}
	if active.ID != alert.ID || active.Status != AlertStatusFiring {
		t.Fatalf("活跃告警不正确: %+v", active)
	}
	if _, err := st.FindActiveAlert(ctx, "server:nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的 dedup_key 期望 ErrNotFound，实际 %v", err)
	}

	// 去重累加：更新通知计数
	alert.NotifyCount = 2
	alert.LastNotifyAt = &now
	alert.UpdatedAt = now + 1000
	if err := st.UpdateAlert(ctx, alert); err != nil {
		t.Fatalf("更新告警失败: %v", err)
	}
	updated, _ := st.GetAlert(ctx, alert.ID)
	if updated.NotifyCount != 2 || updated.LastNotifyAt == nil {
		t.Fatalf("去重累加未生效: %+v", updated)
	}

	// 事件轨迹
	if err := st.AppendAlertEvent(ctx, alert.ID, AlertEventFiring, now, "规则命中"); err != nil {
		t.Fatalf("追加事件失败: %v", err)
	}
	if err := st.AppendAlertEvent(ctx, alert.ID, AlertEventNotify, now+1, "已通知渠道 A"); err != nil {
		t.Fatalf("追加事件失败: %v", err)
	}
	events, err := st.ListAlertEvents(ctx, alert.ID, 10)
	if err != nil {
		t.Fatalf("查询事件失败: %v", err)
	}
	if len(events) != 2 || events[0].Kind != AlertEventFiring || events[1].Kind != AlertEventNotify {
		t.Fatalf("事件轨迹不正确: %+v", events)
	}

	// 确认
	acked, err := st.AckAlert(ctx, alert.ID, "admin", now+2)
	if err != nil {
		t.Fatalf("确认告警失败: %v", err)
	}
	if acked.Status != AlertStatusAcked || acked.AckedAt == nil || acked.AckedBy == nil || *acked.AckedBy != "admin" {
		t.Fatalf("确认结果不正确: %+v", acked)
	}
	// acked 仍算活跃
	if _, err := st.FindActiveAlert(ctx, alert.DedupKey); err != nil {
		t.Fatalf("acked 应仍算活跃告警: %v", err)
	}

	// 过滤
	firing := AlertFilter{Status: AlertStatusAcked, Limit: 10}
	list, err := st.ListAlerts(ctx, firing)
	if err != nil || len(list) != 1 {
		t.Fatalf("按状态过滤失败: %v / %v", list, err)
	}
	if got, err := st.ListAlerts(ctx, AlertFilter{Status: AlertStatusFiring}); err != nil || len(got) != 0 {
		t.Fatalf("firing 过滤应无结果: %v / %v", got, err)
	}
	if got, err := st.ListAlerts(ctx, AlertFilter{RuleID: "rule-1", ServerID: serverID, Severity: "critical"}); err != nil || len(got) != 1 {
		t.Fatalf("组合过滤失败: %v / %v", got, err)
	}
	if got, err := st.ListAlerts(ctx, AlertFilter{RuleID: "rule-x"}); err != nil || len(got) != 0 {
		t.Fatalf("rule_id 过滤失败: %v / %v", got, err)
	}
	if got, err := st.ListAlerts(ctx, AlertFilter{From: now + 100000}); err != nil || len(got) != 0 {
		t.Fatalf("时间过滤失败: %v / %v", got, err)
	}

	// summary
	summary, err := st.AlertSummary(ctx, now)
	if err != nil {
		t.Fatalf("统计概览失败: %v", err)
	}
	if summary.Firing != 0 || summary.Acked != 1 || summary.BySeverity["critical"] != 1 {
		t.Fatalf("概览不正确: %+v", summary)
	}

	// 恢复
	resolveTS := now + 3
	if err := st.ResolveAlert(ctx, alert.ID, resolveTS); err != nil {
		t.Fatalf("恢复告警失败: %v", err)
	}
	resolved, _ := st.GetAlert(ctx, alert.ID)
	if resolved.Status != AlertStatusResolved || resolved.ResolvedAt == nil || *resolved.ResolvedAt != resolveTS {
		t.Fatalf("恢复结果不正确: %+v", resolved)
	}
	if _, err := st.FindActiveAlert(ctx, alert.DedupKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("已恢复告警不应再算活跃: %v", err)
	}
	// 已恢复的告警不能确认
	if _, err := st.AckAlert(ctx, alert.ID, "admin", now+4); !errors.Is(err, ErrConflict) {
		t.Fatalf("已恢复告警确认期望 ErrConflict，实际 %v", err)
	}

	summary, _ = st.AlertSummary(ctx, now)
	if summary.Resolved24h != 1 {
		t.Fatalf("resolved_24h 期望 1，实际 %d", summary.Resolved24h)
	}

	// 保留策略：清理更早的已恢复告警
	if n, err := st.DeleteResolvedAlertsBefore(ctx, resolveTS+1); err != nil {
		t.Fatalf("清理历史告警失败: %v", err)
	} else if n != 1 {
		t.Fatalf("期望清理 1 条，实际 %d", n)
	}
	if events, err := st.ListAlertEvents(ctx, alert.ID, 10); err != nil || len(events) != 0 {
		t.Fatalf("清理告警应连带清理事件: %v / %v", events, err)
	}
}
