package alerting

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// baseTS 是假时钟的起点：2026-01-01T00:00:00Z。
const baseTS int64 = 1767225600000

// newTestEngine 构造一个带假时钟与通知记录器的引擎。
func newTestEngine(t *testing.T, store Store, clock *fakeClock) (*Engine, *notifyRecorder) {
	t.Helper()
	rec := &notifyRecorder{}
	e := New(Deps{
		Store:  store,
		Notify: rec.fn,
		Now:    clock.Now,
	})
	if err := e.Reload(context.Background()); err != nil {
		t.Fatalf("Reload 失败: %v", err)
	}
	return e, rec
}

func checkRule() Rule {
	return Rule{
		ID:           "rule-check-1",
		Name:         "检测项告警",
		Enabled:      true,
		Source:       SourceCheck,
		Severity:     SeverityCritical,
		ForDurationS: 0,
		DedupWindowS: 300,
		ChannelIDs:   []string{"ch-1"},
	}
}

func activeEvent(ts int64) Event {
	return Event{
		Source:     SourceCheck,
		ServerID:   "srv-1",
		ServerName: "web-01",
		CheckID:    "chk-1",
		CheckName:  "HTTP 探活",
		Active:     true,
		Value:      1,
		TS:         ts,
	}
}

func recoverEvent(ts int64) Event {
	ev := activeEvent(ts)
	ev.Active = false
	return ev
}

// ---- for_duration ----

func TestForDurationNotReachedNoAlert(t *testing.T) {
	rule := checkRule()
	rule.ForDurationS = 60
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	// 首次观测到异常：只记 pending，不建告警。
	if err := eng.HandleEvent(context.Background(), activeEvent(clock.Now())); err != nil {
		t.Fatalf("HandleEvent 失败: %v", err)
	}
	if got := store.alertCount(); got != 0 {
		t.Fatalf("未达 for_duration 不应建告警，实际 %d 条", got)
	}
	if got := rec.count(); got != 0 {
		t.Fatalf("未达 for_duration 不应通知，实际 %d 次", got)
	}

	// 推进 59s：仍未达 60s 门槛。
	clock.Advance(59_000)
	if err := eng.HandleEvent(context.Background(), activeEvent(clock.Now())); err != nil {
		t.Fatalf("HandleEvent 失败: %v", err)
	}
	if got := store.alertCount(); got != 0 {
		t.Fatalf("59s < 60s 不应建告警，实际 %d 条", got)
	}
}

func TestForDurationReachedFires(t *testing.T) {
	rule := checkRule()
	rule.ForDurationS = 60
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	clock.Advance(60_000) // 恰好达到 60s 门槛 → 触发
	if err := eng.HandleEvent(context.Background(), activeEvent(clock.Now())); err != nil {
		t.Fatalf("HandleEvent 失败: %v", err)
	}

	alerts := store.allAlerts()
	if len(alerts) != 1 {
		t.Fatalf("达到 for_duration 应建 1 条告警，实际 %d 条", len(alerts))
	}
	a := alerts[0]
	if a.Status != StatusFiring {
		t.Fatalf("告警状态应为 firing，实际 %q", a.Status)
	}
	if a.StartedAt != baseTS+60_000 {
		t.Fatalf("StartedAt 应为触发时刻，实际 %d", a.StartedAt)
	}
	if a.NotifyCount != 1 {
		t.Fatalf("NotifyCount 应为 1，实际 %d", a.NotifyCount)
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("应通知 1 次，实际 %d 次", got)
	}
}

func TestForDurationRecoveredDuringPendingNoAlert(t *testing.T) {
	rule := checkRule()
	rule.ForDurationS = 60
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	clock.Advance(30_000)
	// 期间恢复：清掉 pending，不告警。
	if err := eng.HandleEvent(context.Background(), recoverEvent(clock.Now())); err != nil {
		t.Fatalf("HandleEvent 失败: %v", err)
	}
	if got := store.alertCount(); got != 0 {
		t.Fatalf("pending 期间恢复不应建告警，实际 %d 条", got)
	}
	if got := rec.count(); got != 0 {
		t.Fatalf("pending 期间恢复不应通知，实际 %d 次", got)
	}

	// 恢复后再次异常，计时必须从新起点重新开始（旧 pending 已清除）。
	clock.Advance(1_000)
	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	clock.Advance(59_000) // 距离新起点 59s
	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	if got := store.alertCount(); got != 0 {
		t.Fatalf("计时未重新开始：59s 就告警了（说明旧 pending 未清）")
	}
	clock.Advance(1_000) // 距离新起点 60s
	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	if got := store.alertCount(); got != 1 {
		t.Fatalf("计时重新开始后达到 60s 应告警，实际 %d 条", got)
	}
}

func TestForDurationZeroFiresImmediately(t *testing.T) {
	store := newFakeStore(checkRule()) // ForDurationS=0
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	if err := eng.HandleEvent(context.Background(), activeEvent(clock.Now())); err != nil {
		t.Fatalf("HandleEvent 失败: %v", err)
	}
	if got := store.alertCount(); got != 1 {
		t.Fatalf("for_duration=0 应立刻告警，实际 %d 条", got)
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("应立刻通知 1 次，实际 %d 次", got)
	}
}

// ---- 去重 ----

func TestDedupDoesNotCreateNewAlert(t *testing.T) {
	store := newFakeStore(checkRule())
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	for i := 0; i < 5; i++ {
		if err := eng.HandleEvent(context.Background(), activeEvent(clock.Now())); err != nil {
			t.Fatalf("HandleEvent 失败: %v", err)
		}
		clock.Advance(1_000)
	}
	if got := store.alertCount(); got != 1 {
		t.Fatalf("重复触发不得新增告警，实际 %d 条", got)
	}
	if got := store.createCount(); got != 1 {
		t.Fatalf("CreateAlert 应只调用 1 次，实际 %d 次", got)
	}
	// dedup_window=300s，窗口内不重复通知。
	if got := rec.count(); got != 1 {
		t.Fatalf("去重窗口内不应重复通知，实际 %d 次", got)
	}
	a := store.allAlerts()[0]
	if a.UpdatedAt != baseTS+4_000 {
		t.Fatalf("重复触发应更新 UpdatedAt，实际 %d", a.UpdatedAt)
	}
}

func TestDedupWindowBoundary(t *testing.T) {
	rule := checkRule()
	rule.DedupWindowS = 300
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now())) // 首次通知 @baseTS
	if got := rec.count(); got != 1 {
		t.Fatalf("首次应通知 1 次，实际 %d", got)
	}

	// 窗口内（299.999s）：不重复通知。
	clock.Set(baseTS + 299_999)
	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	if got := rec.count(); got != 1 {
		t.Fatalf("窗口内(299.999s)不应重复通知，实际 %d 次", got)
	}

	// 恰好等于窗口（300s）：语义是「多久没通知过才重新通知」，边界取 >=，应重发。
	clock.Set(baseTS + 300_000)
	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	if got := rec.count(); got != 2 {
		t.Fatalf("恰好到窗口(300s)应重新通知，实际 %d 次", got)
	}

	a := store.allAlerts()[0]
	if a.NotifyCount != 2 {
		t.Fatalf("NotifyCount 应为 2，实际 %d", a.NotifyCount)
	}
	if a.LastNotifyAt != baseTS+300_000 {
		t.Fatalf("LastNotifyAt 应更新为最近通知时刻，实际 %d", a.LastNotifyAt)
	}
	if store.alertCount() != 1 {
		t.Fatalf("重新通知不得新增告警")
	}

	// 再次进入窗口内：不通知。
	clock.Set(baseTS + 400_000)
	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	if got := rec.count(); got != 2 {
		t.Fatalf("新一轮窗口内不应通知，实际 %d 次", got)
	}
}

// ---- 静默 ----

func TestSilenceStillCreatesAlertAndEventButNoNotify(t *testing.T) {
	rule := checkRule()
	rule.SilenceUntil = baseTS + 3_600_000 // 静默 1 小时
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	if err := eng.HandleEvent(context.Background(), activeEvent(clock.Now())); err != nil {
		t.Fatalf("HandleEvent 失败: %v", err)
	}

	// 静默不等于不记录：仍然建告警。
	if got := store.alertCount(); got != 1 {
		t.Fatalf("静默期仍应建告警，实际 %d 条", got)
	}
	a := store.allAlerts()[0]
	if a.Status != StatusFiring {
		t.Fatalf("静默期告警状态应为 firing，实际 %q", a.Status)
	}
	if a.SilenceUntil != rule.SilenceUntil {
		t.Fatalf("告警应带上规则的 SilenceUntil，实际 %d", a.SilenceUntil)
	}
	// 仍然写事件（trigger 事件必须有）。
	if got := len(store.eventsOfKind(EventKindTrigger)); got != 1 {
		t.Fatalf("静默期仍应写 trigger 事件，实际 %d 条", got)
	}
	// 只是不通知。
	if got := rec.count(); got != 0 {
		t.Fatalf("静默期不应发送通知，实际 %d 次", got)
	}
	if a.NotifyCount != 0 {
		t.Fatalf("静默期 NotifyCount 不应累加，实际 %d", a.NotifyCount)
	}
}

func TestSilenceExpiredNotifies(t *testing.T) {
	rule := checkRule()
	rule.SilenceUntil = baseTS + 60_000
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	if rec.count() != 0 {
		t.Fatalf("静默期内不应通知")
	}

	// 静默过期后再次触发：窗口内（LastNotifyAt=0）应立即补一次通知。
	clock.Set(baseTS + 61_000)
	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	if got := rec.count(); got != 1 {
		t.Fatalf("静默过期后应通知 1 次，实际 %d 次", got)
	}
}

func TestSilenceSuppressesFiringButNotRecoveryNotify(t *testing.T) {
	// 静默的语义是「不要再打扰我新发生的故障」，而不是「让我永远不知道服务已经好了」。
	// 因此静默期：firing 通知被抑制，恢复通知必须穿透静默照发。
	rule := checkRule()
	rule.SilenceUntil = baseTS + 3_600_000
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	if got := rec.firingCalls(); len(got) != 0 {
		t.Fatalf("静默期不应发送 firing 通知，实际 %d 次", len(got))
	}

	clock.Advance(10_000)
	_ = eng.HandleEvent(context.Background(), recoverEvent(clock.Now()))

	if got := store.allAlerts()[0].Status; got != StatusResolved {
		t.Fatalf("静默期恢复仍应置 resolved，实际 %q", got)
	}
	// 恢复通知必须穿透静默。
	recCalls := rec.recoveredCalls()
	if len(recCalls) != 1 {
		t.Fatalf("静默期恢复通知必须穿透静默发出 1 次，实际 %d 次", len(recCalls))
	}
	if !recCalls[0].Recovered {
		t.Fatalf("穿透静默的通知必须标记 recovered=true")
	}
	if len(recCalls[0].Channels) != 1 || recCalls[0].Channels[0] != "ch-1" {
		t.Fatalf("恢复通知应走规则配置的渠道，实际 %v", recCalls[0].Channels)
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("静默期总共只应有 1 次通知（恢复），实际 %d 次", got)
	}
}

// ---- 恢复 ----

func TestRecoveryResolvesAndNotifiesOnce(t *testing.T) {
	store := newFakeStore(checkRule())
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	clock.Advance(10_000)
	if err := eng.HandleEvent(context.Background(), recoverEvent(clock.Now())); err != nil {
		t.Fatalf("HandleEvent 失败: %v", err)
	}

	a := store.allAlerts()[0]
	if a.Status != StatusResolved {
		t.Fatalf("恢复后状态应为 resolved，实际 %q", a.Status)
	}
	if a.ResolvedAt != baseTS+10_000 {
		t.Fatalf("ResolvedAt 应记录恢复时刻，实际 %d", a.ResolvedAt)
	}
	recCalls := rec.recoveredCalls()
	if len(recCalls) != 1 {
		t.Fatalf("恢复应产生恰好 1 次恢复通知，实际 %d 次", len(recCalls))
	}
	if !recCalls[0].Recovered {
		t.Fatalf("恢复通知的 recovered 必须为 true")
	}
	if len(recCalls[0].Channels) != 1 || recCalls[0].Channels[0] != "ch-1" {
		t.Fatalf("恢复通知应走规则配置的渠道，实际 %v", recCalls[0].Channels)
	}
	if got := len(store.eventsOfKind(EventKindResolve)); got != 1 {
		t.Fatalf("应写 1 条 resolve 事件，实际 %d 条", got)
	}

	// 重复收到恢复事件：不再重复通知。
	clock.Advance(1_000)
	_ = eng.HandleEvent(context.Background(), recoverEvent(clock.Now()))
	if got := len(rec.recoveredCalls()); got != 1 {
		t.Fatalf("重复恢复事件不应重复通知，实际 %d 次", got)
	}
}

func TestRecoveryThenRetriggerCreatesNewAlert(t *testing.T) {
	store := newFakeStore(checkRule())
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	clock.Advance(10_000)
	_ = eng.HandleEvent(context.Background(), recoverEvent(clock.Now()))

	// 恢复后同一 key 再次触发：必须建一条新告警。
	clock.Advance(10_000)
	if err := eng.HandleEvent(context.Background(), activeEvent(clock.Now())); err != nil {
		t.Fatalf("HandleEvent 失败: %v", err)
	}
	if got := store.alertCount(); got != 2 {
		t.Fatalf("恢复后再次触发应建新告警（共 2 条），实际 %d 条", got)
	}
	if got := store.createCount(); got != 2 {
		t.Fatalf("CreateAlert 应调用 2 次，实际 %d 次", got)
	}
	firing := rec.firingCalls()
	if len(firing) != 2 {
		t.Fatalf("应有 2 次故障通知，实际 %d 次", len(firing))
	}
	// 新告警的 ID 必须与旧告警不同。
	ids := map[string]bool{}
	for _, a := range store.allAlerts() {
		ids[a.ID] = true
	}
	if len(ids) != 2 {
		t.Fatalf("两条告警的 ID 必须不同")
	}
}

func TestRecoverWithoutActiveAlertIsNoop(t *testing.T) {
	store := newFakeStore(checkRule())
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	if err := eng.HandleEvent(context.Background(), recoverEvent(clock.Now())); err != nil {
		t.Fatalf("无活跃告警时恢复不应报错: %v", err)
	}
	if got := store.alertCount(); got != 0 {
		t.Fatalf("无活跃告警时恢复不应建告警")
	}
	if got := rec.count(); got != 0 {
		t.Fatalf("无活跃告警时恢复不应通知")
	}
}

// ---- acked 状态 ----

func TestAckedAlertStillDedups(t *testing.T) {
	store := newFakeStore(checkRule())
	clock := newFakeClock(baseTS)
	eng, _ := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	// 模拟 backend 把告警置为 acked（FindActiveAlert 仍应视其为未解决）。
	a := store.allAlerts()[0]
	a.Status = StatusAcked
	a.AckedAt = baseTS + 5_000
	_ = store.UpdateAlert(context.Background(), a)

	clock.Set(baseTS + 10_000)
	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	if got := store.alertCount(); got != 1 {
		t.Fatalf("acked 也是未解决告警，不得新建，实际 %d 条", got)
	}
}

// ---- 规则匹配 ----

func TestRuleMatching(t *testing.T) {
	ev := activeEvent(baseTS)
	ev.Group = "prod"
	ev.Tags = []string{"web", "edge"}

	cases := []struct {
		name  string
		rule  Rule
		match bool
	}{
		{"空条件匹配所有", Rule{ID: "r", Enabled: true, Source: SourceCheck}, true},
		{"按 checkID 命中", Rule{ID: "r", Enabled: true, Source: SourceCheck, CheckIDs: []string{"chk-1"}}, true},
		{"按 checkID 未命中", Rule{ID: "r", Enabled: true, Source: SourceCheck, CheckIDs: []string{"chk-other"}}, false},
		{"按 serverID 命中", Rule{ID: "r", Enabled: true, Source: SourceCheck, ServerIDs: []string{"srv-1"}}, true},
		{"按 group 命中", Rule{ID: "r", Enabled: true, Source: SourceCheck, Groups: []string{"prod"}}, true},
		{"按 group 未命中", Rule{ID: "r", Enabled: true, Source: SourceCheck, Groups: []string{"staging"}}, false},
		{"按 tag 命中", Rule{ID: "r", Enabled: true, Source: SourceCheck, Tags: []string{"edge"}}, true},
		{"按 tag 未命中", Rule{ID: "r", Enabled: true, Source: SourceCheck, Tags: []string{"db"}}, false},
		{"并集语义: server 未命中但 tag 命中", Rule{ID: "r", Enabled: true, Source: SourceCheck, ServerIDs: []string{"srv-x"}, Tags: []string{"web"}}, true},
		{"来源不匹配", Rule{ID: "r", Enabled: true, Source: SourceServer}, false},
		{"禁用规则不参与", Rule{ID: "r", Enabled: false, Source: SourceCheck}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := matchRules([]Rule{normalizeRule(c.rule)}, ev)
			if (len(got) > 0) != c.match {
				t.Fatalf("规则匹配结果不符：期望 %v，实际 %d 条命中", c.match, len(got))
			}
		})
	}
}

func TestDisabledRuleDoesNotAlert(t *testing.T) {
	rule := checkRule()
	rule.Enabled = false
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	if err := eng.HandleEvent(context.Background(), activeEvent(clock.Now())); err != nil {
		t.Fatalf("HandleEvent 失败: %v", err)
	}
	if store.alertCount() != 0 {
		t.Fatalf("禁用规则不得建告警")
	}
	if rec.count() != 0 {
		t.Fatalf("禁用规则不得通知")
	}
}

func TestServerSourceEvent(t *testing.T) {
	rule := Rule{
		ID: "rule-server", Name: "服务器离线", Enabled: true, Source: SourceServer,
		Severity: SeverityCritical, DedupWindowS: 300, ChannelIDs: []string{"ch-2"},
	}
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	ev := Event{Source: SourceServer, ServerID: "srv-9", ServerName: "db-01", Active: true, TS: clock.Now()}
	if err := eng.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("HandleEvent 失败: %v", err)
	}
	alerts := store.allAlerts()
	if len(alerts) != 1 {
		t.Fatalf("应建 1 条告警，实际 %d", len(alerts))
	}
	if alerts[0].DedupKey != "server:srv-9" {
		t.Fatalf("dedup_key 应为 server:srv-9，实际 %q", alerts[0].DedupKey)
	}
	if !strings.Contains(alerts[0].Title, "db-01") {
		t.Fatalf("标题应含服务器名，实际 %q", alerts[0].Title)
	}
	if rec.count() != 1 {
		t.Fatalf("应通知 1 次")
	}
}

func TestCheckDedupKeyFormat(t *testing.T) {
	store := newFakeStore(checkRule())
	clock := newFakeClock(baseTS)
	eng, _ := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	if got := store.allAlerts()[0].DedupKey; got != "check:chk-1:srv-1" {
		t.Fatalf("dedup_key 格式应为 check:<checkID>:<serverID>，实际 %q", got)
	}
}

func TestTitleContainsRuleAndServerName(t *testing.T) {
	store := newFakeStore(checkRule())
	clock := newFakeClock(baseTS)
	eng, _ := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	title := store.allAlerts()[0].Title
	if !strings.Contains(title, "检测项告警") {
		t.Fatalf("标题应含规则名，实际 %q", title)
	}
	if !strings.Contains(title, "web-01") {
		t.Fatalf("标题应含服务器名，实际 %q", title)
	}
}

// ---- 指标规则 ----

func TestEvaluateMetricsComparatorBoundary(t *testing.T) {
	cases := []struct {
		name       string
		comparator string
		threshold  float64
		value      float64
		wantFiring bool
	}{
		{"大于-高于阈值", ">", 80, 80.1, true},
		{"大于-等于阈值不触发", ">", 80, 80, false},
		{"大于-低于阈值", ">", 80, 79.9, false},
		{"大于等于-等于阈值触发", ">=", 80, 80, true},
		{"大于等于-低于阈值", ">=", 80, 79.9, false},
		{"小于-低于阈值", "<", 10, 9.9, true},
		{"小于-等于阈值不触发", "<", 10, 10, false},
		{"小于等于-等于阈值触发", "<=", 10, 10, true},
		{"小于等于-高于阈值", "<=", 10, 10.1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rule := Rule{
				ID: "rule-metric", Name: "CPU 高", Enabled: true, Source: SourceMetric,
				Metric: "cpu.pct", Comparator: c.comparator, Threshold: c.threshold,
				Severity: SeverityWarning, DedupWindowS: 300, ChannelIDs: []string{"ch-3"},
			}
			store := newFakeStore(rule)
			clock := newFakeClock(baseTS)
			eng, rec := newTestEngine(t, store, clock)

			samples := []MetricSample{{
				ServerID: "srv-1", ServerName: "web-01", Metric: "cpu.pct",
				Value: c.value, TS: clock.Now(),
			}}
			if err := eng.EvaluateMetrics(context.Background(), samples); err != nil {
				t.Fatalf("EvaluateMetrics 失败: %v", err)
			}
			firing := store.alertCount() > 0
			if firing != c.wantFiring {
				t.Fatalf("value=%g %s %g 期望 firing=%v，实际 %v", c.value, c.comparator, c.threshold, c.wantFiring, firing)
			}
			if c.wantFiring && rec.count() != 1 {
				t.Fatalf("触发时应通知 1 次，实际 %d", rec.count())
			}
			if c.wantFiring {
				a := store.allAlerts()[0]
				if a.DedupKey != "metric:rule-metric:srv-1" {
					t.Fatalf("指标 dedup_key 格式错误: %q", a.DedupKey)
				}
				if a.Severity != SeverityWarning {
					t.Fatalf("严重级别应来自规则，实际 %q", a.Severity)
				}
			}
		})
	}
}

func TestEvaluateMetricsResolvesWhenBackToNormal(t *testing.T) {
	rule := Rule{
		ID: "rule-metric", Name: "CPU 高", Enabled: true, Source: SourceMetric,
		Metric: "cpu.pct", Comparator: ">", Threshold: 80,
		Severity: SeverityWarning, DedupWindowS: 300, ChannelIDs: []string{"ch-3"},
	}
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	hi := []MetricSample{{ServerID: "srv-1", ServerName: "web-01", Metric: "cpu.pct", Value: 95, TS: clock.Now()}}
	if err := eng.EvaluateMetrics(context.Background(), hi); err != nil {
		t.Fatalf("EvaluateMetrics 失败: %v", err)
	}
	if store.alertCount() != 1 {
		t.Fatalf("超阈值应建告警")
	}

	clock.Advance(10_000)
	lo := []MetricSample{{ServerID: "srv-1", ServerName: "web-01", Metric: "cpu.pct", Value: 20, TS: clock.Now()}}
	if err := eng.EvaluateMetrics(context.Background(), lo); err != nil {
		t.Fatalf("EvaluateMetrics 失败: %v", err)
	}
	if got := store.allAlerts()[0].Status; got != StatusResolved {
		t.Fatalf("指标回落应置 resolved，实际 %q", got)
	}
	if got := len(rec.recoveredCalls()); got != 1 {
		t.Fatalf("指标恢复应产生 1 次恢复通知，实际 %d 次", got)
	}
}

func TestEvaluateMetricsIgnoresUnmatchedMetricAndRule(t *testing.T) {
	rule := Rule{
		ID: "rule-metric", Name: "CPU 高", Enabled: true, Source: SourceMetric,
		Metric: "cpu.pct", Comparator: ">", Threshold: 80, Severity: SeverityWarning,
	}
	disabled := rule
	disabled.ID = "rule-disabled"
	disabled.Enabled = false

	store := newFakeStore(rule, disabled)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	// 指标名不匹配
	if err := eng.EvaluateMetrics(context.Background(), []MetricSample{
		{ServerID: "srv-1", Metric: "mem.used_pct", Value: 99, TS: clock.Now()},
	}); err != nil {
		t.Fatalf("EvaluateMetrics 失败: %v", err)
	}
	// 阈值未越过
	if err := eng.EvaluateMetrics(context.Background(), []MetricSample{
		{ServerID: "srv-1", Metric: "cpu.pct", Value: 10, TS: clock.Now()},
	}); err != nil {
		t.Fatalf("EvaluateMetrics 失败: %v", err)
	}
	if store.alertCount() != 0 {
		t.Fatalf("不匹配的样本不应建告警，实际 %d 条", store.alertCount())
	}
	if rec.count() != 0 {
		t.Fatalf("不匹配的样本不应通知")
	}
}

func TestEvaluateMetricsForDuration(t *testing.T) {
	rule := Rule{
		ID: "rule-metric", Name: "CPU 高", Enabled: true, Source: SourceMetric,
		Metric: "cpu.pct", Comparator: ">", Threshold: 80, ForDurationS: 30,
		Severity: SeverityWarning, DedupWindowS: 300,
	}
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, _ := newTestEngine(t, store, clock)

	sample := func() []MetricSample {
		return []MetricSample{{ServerID: "srv-1", Metric: "cpu.pct", Value: 95, TS: clock.Now()}}
	}
	_ = eng.EvaluateMetrics(context.Background(), sample())
	clock.Advance(29_000)
	_ = eng.EvaluateMetrics(context.Background(), sample())
	if store.alertCount() != 0 {
		t.Fatalf("指标规则未达 for_duration 不应告警")
	}
	clock.Advance(1_000)
	_ = eng.EvaluateMetrics(context.Background(), sample())
	if store.alertCount() != 1 {
		t.Fatalf("指标规则达到 for_duration 应告警")
	}
}

// ---- 并发 ----

func TestConcurrentHandleEventSameKeySingleAlert(t *testing.T) {
	store := newFakeStore(checkRule())
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	// 放大竞争窗口：让 FindActiveAlert 在返回前让出调度。
	store.findDelay = func() {
		// 空转一点点，制造交错；不 sleep 是为了保持测试快。
		for i := 0; i < 100; i++ {
			_ = i
		}
	}

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if err := eng.HandleEvent(context.Background(), activeEvent(clock.Now())); err != nil {
				t.Errorf("并发 HandleEvent 失败: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := store.createCount(); got != 1 {
		t.Fatalf("同一 dedup_key 并发触发必须只建 1 条告警，实际 CreateAlert 调用 %d 次", got)
	}
	if got := store.alertCount(); got != 1 {
		t.Fatalf("同一 dedup_key 并发触发必须只留 1 条告警，实际 %d 条", got)
	}
	// 去重窗口 300s，并发触发同一时刻，通知只能 1 次。
	if got := rec.count(); got != 1 {
		t.Fatalf("同一 dedup_key 并发触发只应通知 1 次，实际 %d 次", got)
	}
}

func TestConcurrentDifferentKeysIndependent(t *testing.T) {
	store := newFakeStore(checkRule())
	clock := newFakeClock(baseTS)
	eng, _ := newTestEngine(t, store, clock)

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			ev := activeEvent(clock.Now())
			ev.ServerID = "srv-" + string(rune('a'+i))
			ev.CheckID = "chk-1"
			if err := eng.HandleEvent(context.Background(), ev); err != nil {
				t.Errorf("并发 HandleEvent 失败: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if got := store.alertCount(); got != n {
		t.Fatalf("不同 dedup_key 应各自建告警（%d 条），实际 %d 条", n, got)
	}
}

// ---- 错误路径 ----

func TestStoreErrorsAreWrapped(t *testing.T) {
	rule := checkRule()
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, _ := newTestEngine(t, store, clock)

	store.createErr = errors.New("磁盘炸了")
	err := eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	if err == nil {
		t.Fatalf("CreateAlert 失败应向上返回 error")
	}
	if !strings.Contains(err.Error(), "创建告警") {
		t.Fatalf("错误信息应带上下文，实际 %q", err)
	}
	if !errors.Is(err, store.createErr) {
		t.Fatalf("错误应可 unwrap 回原始错误")
	}
}

func TestNilStore(t *testing.T) {
	eng := New(Deps{})
	if err := eng.HandleEvent(context.Background(), activeEvent(baseTS)); err == nil {
		t.Fatalf("未注入 Store 时 HandleEvent 应报错")
	}
	if err := eng.EvaluateMetrics(context.Background(), nil); err == nil {
		t.Fatalf("未注入 Store 时 EvaluateMetrics 应报错")
	}
	if err := eng.Reload(context.Background()); err == nil {
		t.Fatalf("未注入 Store 时 Reload 应报错")
	}
}

func TestInvalidEvent(t *testing.T) {
	store := newFakeStore(checkRule())
	clock := newFakeClock(baseTS)
	eng, _ := newTestEngine(t, store, clock)

	cases := []Event{
		{Source: SourceCheck, ServerID: "srv-1"},                // 缺 CheckID
		{Source: SourceCheck, CheckID: "chk-1"},                 // 缺 ServerID
		{Source: SourceServer},                                  // 缺 ServerID
		{Source: SourceMetric, ServerID: "srv-1", CheckID: "c"}, // 指标事件不走 HandleEvent
		{Source: "bogus", ServerID: "srv-1"},                    // 未知来源
	}
	for i, ev := range cases {
		if err := eng.HandleEvent(context.Background(), ev); err == nil {
			t.Fatalf("非法事件 #%d 应返回 error: %+v", i, ev)
		}
	}
	if store.alertCount() != 0 {
		t.Fatalf("非法事件不得建告警")
	}
}

func TestReloadReplacesRules(t *testing.T) {
	rule := checkRule()
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	if rec.count() != 1 {
		t.Fatalf("初始规则应生效")
	}

	// 规则删除后 Reload：不再命中，不建新告警。
	store.setRules()
	if err := eng.Reload(context.Background()); err != nil {
		t.Fatalf("Reload 失败: %v", err)
	}
	ev := activeEvent(clock.Now())
	ev.CheckID = "chk-2" // 新的 dedup_key，避免命中已有告警
	ev.ServerID = "srv-2"
	_ = eng.HandleEvent(context.Background(), ev)
	if got := store.alertCount(); got != 1 {
		t.Fatalf("Reload 后规则应已失效，不得新建告警，实际 %d 条", got)
	}
}

func TestReloadError(t *testing.T) {
	store := newFakeStore()
	store.listRulesErr = errors.New("db down")
	eng := New(Deps{Store: store, Now: func() int64 { return baseTS }})
	if err := eng.Reload(context.Background()); err == nil {
		t.Fatalf("ListRules 失败应返回 error")
	}
}

func TestLazyRuleLoadWithoutReload(t *testing.T) {
	store := newFakeStore(checkRule())
	clock := newFakeClock(baseTS)
	rec := &notifyRecorder{}
	// 不显式 Reload，首次 HandleEvent 应自动加载规则。
	eng := New(Deps{Store: store, Notify: rec.fn, Now: clock.Now})

	if err := eng.HandleEvent(context.Background(), activeEvent(clock.Now())); err != nil {
		t.Fatalf("HandleEvent 失败: %v", err)
	}
	if store.alertCount() != 1 {
		t.Fatalf("首次使用应自动加载规则并告警")
	}
}

func TestDefaultNowWhenNil(t *testing.T) {
	store := newFakeStore(checkRule())
	eng := New(Deps{Store: store}) // Now 为空
	if err := eng.HandleEvent(context.Background(), Event{
		Source: SourceCheck, ServerID: "srv-1", CheckID: "chk-1", Active: true,
	}); err != nil {
		t.Fatalf("默认时钟下 HandleEvent 失败: %v", err)
	}
	if store.alertCount() != 1 {
		t.Fatalf("应建告警")
	}
}

// ---- 事件轨迹 ----

func TestEventsWrittenOnTriggerNotifyResolve(t *testing.T) {
	rule := checkRule()
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, _ := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	clock.Advance(400_000) // 超过去重窗口 → 再通知一次
	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	clock.Advance(10_000)
	_ = eng.HandleEvent(context.Background(), recoverEvent(clock.Now()))

	alertID := store.allAlerts()[0].ID
	evs := store.eventsFor(alertID)
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	want := []string{EventKindTrigger, EventKindNotify, EventKindNotify, EventKindResolve}
	if len(kinds) != len(want) {
		t.Fatalf("事件轨迹应为 %v，实际 %v", want, kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("事件轨迹应为 %v，实际 %v", want, kinds)
		}
	}
}

func TestNoNotifyCallbackDoesNotPanic(t *testing.T) {
	store := newFakeStore(checkRule())
	clock := newFakeClock(baseTS)
	eng := New(Deps{Store: store, Now: clock.Now}) // 无 Notify

	if err := eng.HandleEvent(context.Background(), activeEvent(clock.Now())); err != nil {
		t.Fatalf("无 Notify 回调时不应报错: %v", err)
	}
	if store.alertCount() != 1 {
		t.Fatalf("无 Notify 回调时仍应建告警")
	}
	// 有 Notify 但静默时也不应调用（已由 TestSilence... 覆盖），此处验证计数仍会累加。
	if got := store.allAlerts()[0].NotifyCount; got != 1 {
		t.Fatalf("无 Notify 回调时 NotifyCount 仍应记录尝试次数，实际 %d", got)
	}
}

// ---- 数值更新语义 ----

func TestRepeatPreservesZeroValueWhenProvided(t *testing.T) {
	// Value=0 是合法观测值，必须能更新到告警上（不能被当成「未提供」跳过）。
	rule := checkRule()
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, _ := newTestEngine(t, store, clock)

	first := activeEvent(clock.Now())
	first.Value = 42
	_ = eng.HandleEvent(context.Background(), first)

	second := activeEvent(clock.Now())
	second.Value = 0
	_ = eng.HandleEvent(context.Background(), second)

	if got := store.allAlerts()[0].Value; got != 0 {
		t.Fatalf("真实观测值 0 应更新到告警，实际 %g", got)
	}
}

func TestRepeatUsesOwnerRuleChannels(t *testing.T) {
	// 两条规则都能命中同一 dedup_key 时，去重/重发应使用「建告警的那条规则」的渠道。
	owner := checkRule()
	owner.ID = "rule-a"
	owner.DedupWindowS = 60
	owner.ChannelIDs = []string{"ch-owner"}
	other := checkRule()
	other.ID = "rule-b"
	other.DedupWindowS = 99999
	other.ChannelIDs = []string{"ch-other"}

	store := newFakeStore(owner, other)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	clock.Advance(61_000) // 超过 owner 的 60s 窗口，但远小于 other 的窗口
	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))

	calls := rec.all()
	if len(calls) != 2 {
		t.Fatalf("按建告警规则的窗口应重发 1 次（共 2 次），实际 %d 次", len(calls))
	}
	if calls[1].Channels[0] != "ch-owner" {
		t.Fatalf("重发应走建告警规则的渠道，实际 %v", calls[1].Channels)
	}
}

func TestDisabledOwnerRuleStopsNotifyingButKeepsAlert(t *testing.T) {
	rule := checkRule()
	rule.DedupWindowS = 60
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	if rec.count() != 1 {
		t.Fatalf("首次应通知")
	}

	// 规则被停用后再触发：不新建告警、也不再通知。
	disabled := rule
	disabled.Enabled = false
	store.setRules(disabled)
	if err := eng.Reload(context.Background()); err != nil {
		t.Fatalf("Reload 失败: %v", err)
	}
	clock.Advance(120_000)
	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))

	if got := rec.count(); got != 1 {
		t.Fatalf("规则停用后不应继续通知，实际 %d 次", got)
	}
	if got := store.alertCount(); got != 1 {
		t.Fatalf("已有告警不应被删除，实际 %d 条", got)
	}
	if got := store.allAlerts()[0].Status; got != StatusFiring {
		t.Fatalf("已有告警状态应保持 firing，实际 %q", got)
	}
}

// ---- pending 内存有界 ----

func TestPendingPrunedWhenExceedsThreshold(t *testing.T) {
	rule := checkRule()
	rule.ForDurationS = 3600
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, _ := newTestEngine(t, store, clock)

	// 造一批「早已过期」的 pending：远超 pendingTTL。
	eng.mu.Lock()
	for i := 0; i < pendingPruneThreshold; i++ {
		key := "check:stale:" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26))
		stale := baseTS - pendingTTL - 1
		eng.pending[key] = &pendingEntry{firstSeen: stale, lastSeen: stale}
	}
	eng.mu.Unlock()

	// 触发一次 observePending，应清理掉过期条目。
	eng.observePending(observation{key: "check:chk-new:srv-new", ts: baseTS})

	eng.mu.Lock()
	size := len(eng.pending)
	eng.mu.Unlock()
	if size > pendingPruneThreshold {
		t.Fatalf("pending 应被清扫到阈值以内，实际 %d 条", size)
	}
}

func TestPendingKeptWhenStillFresh(t *testing.T) {
	rule := checkRule()
	rule.ForDurationS = 3600
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, _ := newTestEngine(t, store, clock)

	// 未过期的 pending 不能被误删（否则 for_duration 计时会被重置）。
	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	if _, ok := eng.pendingSnapshot("check:chk-1:srv-1"); !ok {
		t.Fatalf("首次异常应留下 pending 记录")
	}
	clock.Advance(1800_000) // 30 分钟，未达 for_duration 也未过 TTL
	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	if _, ok := eng.pendingSnapshot("check:chk-1:srv-1"); !ok {
		t.Fatalf("未过期的 pending 不应被清理")
	}
}

// ---- 无规则场景 ----

func TestNoRulesNoAlertNoError(t *testing.T) {
	store := newFakeStore() // 空规则
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	if err := eng.HandleEvent(context.Background(), activeEvent(clock.Now())); err != nil {
		t.Fatalf("无规则时不应报错: %v", err)
	}
	if store.alertCount() != 0 {
		t.Fatalf("无规则时不应建告警")
	}
	if rec.count() != 0 {
		t.Fatalf("无规则时不应通知")
	}
	// 空样本集合也应正常返回。
	if err := eng.EvaluateMetrics(context.Background(), nil); err != nil {
		t.Fatalf("空样本不应报错: %v", err)
	}
}

// ---- 与 backend 的事件类型取值域对齐 ----

func TestEventKindValuesMatchStoreConvention(t *testing.T) {
	// 这些字面量与 internal/store 的 AlertEvent* 常量必须一致，否则前端事件轨迹会显示原始英文。
	// 本包不能 import internal/store（契约禁止），因此用硬编码断言锁死取值。
	cases := map[string]string{
		"EventKindTrigger": EventKindTrigger,
		"EventKindNotify":  EventKindNotify,
		"EventKindResolve": EventKindResolve,
	}
	want := map[string]string{
		"EventKindTrigger": "firing",
		"EventKindNotify":  "notify",
		"EventKindResolve": "resolved",
	}
	for name, got := range cases {
		if got != want[name] {
			t.Fatalf("%s 应为 %q（与 store.AlertEvent* 对齐），实际 %q", name, want[name], got)
		}
	}
}

// ---- 翻转事件源 + for_duration：周期 tick 必须能推进 ----
//
// checker.Deps.OnStateChange 只在**状态翻转**时回调一次（见 internal/checker/scheduler.go
// record()）。因此 for_duration_s > 0 的 check/server 规则永远收不到第二次事件，
// 必须由周期 tick（backend 调用 EvaluateMetrics）把待触发记录提升为 firing。
// 若回归，告警 验收第 4 条（for_duration 告警）在真实链路上会永远不触发。

func TestPendingPromotedByPeriodicTick(t *testing.T) {
	rule := checkRule()
	rule.ForDurationS = 60
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	// 只有一次翻转事件（首次异常）：仅记录 pending。
	if err := eng.HandleEvent(context.Background(), activeEvent(clock.Now())); err != nil {
		t.Fatalf("HandleEvent 失败: %v", err)
	}
	if store.alertCount() != 0 {
		t.Fatalf("首次翻转不应立即告警")
	}

	// 周期 tick 但未达 60s：仍不告警。
	clock.Advance(30_000)
	if err := eng.EvaluateMetrics(context.Background(), nil); err != nil {
		t.Fatalf("EvaluateMetrics 失败: %v", err)
	}
	if store.alertCount() != 0 {
		t.Fatalf("30s < 60s 周期 tick 不应告警，实际 %d 条", store.alertCount())
	}

	// 再推进到超过 60s：周期 tick 必须把 pending 提升为 firing。
	clock.Advance(31_000)
	if err := eng.EvaluateMetrics(context.Background(), nil); err != nil {
		t.Fatalf("EvaluateMetrics 失败: %v", err)
	}
	alerts := store.allAlerts()
	if len(alerts) != 1 {
		t.Fatalf("周期 tick 应把待触发记录提升为 1 条告警，实际 %d 条", len(alerts))
	}
	if alerts[0].Status != StatusFiring {
		t.Fatalf("提升后状态应为 firing，实际 %q", alerts[0].Status)
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("提升后应通知 1 次，实际 %d 次", got)
	}
	// pending 已被消费。
	if _, ok := eng.pendingSnapshot("check:chk-1:srv-1"); ok {
		t.Fatalf("提升后 pending 应被清除")
	}
}

func TestPendingPromotionSkippedWhenRuleDisabled(t *testing.T) {
	rule := checkRule()
	rule.ForDurationS = 60
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	// 规则在计时期间被停用。
	disabled := rule
	disabled.Enabled = false
	store.setRules(disabled)
	if err := eng.Reload(context.Background()); err != nil {
		t.Fatalf("Reload 失败: %v", err)
	}

	clock.Advance(120_000)
	if err := eng.EvaluateMetrics(context.Background(), nil); err != nil {
		t.Fatalf("EvaluateMetrics 失败: %v", err)
	}
	if store.alertCount() != 0 {
		t.Fatalf("规则已停用不应提升为告警，实际 %d 条", store.alertCount())
	}
	if rec.count() != 0 {
		t.Fatalf("规则已停用不应通知")
	}
}

func TestPendingPromotionWorksForServerSource(t *testing.T) {
	rule := Rule{
		ID: "rule-server", Name: "服务器离线", Enabled: true, Source: SourceServer,
		Severity: SeverityCritical, ForDurationS: 30, DedupWindowS: 300, ChannelIDs: []string{"ch-2"},
	}
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	ev := Event{Source: SourceServer, ServerID: "srv-9", ServerName: "db-01", Active: true, TS: clock.Now()}
	_ = eng.HandleEvent(context.Background(), ev)
	if store.alertCount() != 0 {
		t.Fatalf("首次翻转不应立即告警")
	}

	clock.Advance(31_000)
	_ = eng.EvaluateMetrics(context.Background(), nil)
	if got := store.alertCount(); got != 1 {
		t.Fatalf("server 来源的待触发记录也应被周期 tick 提升，实际 %d 条", got)
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("应通知 1 次，实际 %d 次", got)
	}
}

func TestRecoveryClearsPendingBeforePromotion(t *testing.T) {
	rule := checkRule()
	rule.ForDurationS = 60
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, _ := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	// 恢复先到：pending 必须被清掉，之后 tick 不得再补一条告警。
	clock.Advance(10_000)
	_ = eng.HandleEvent(context.Background(), recoverEvent(clock.Now()))

	clock.Advance(120_000)
	_ = eng.EvaluateMetrics(context.Background(), nil)
	if store.alertCount() != 0 {
		t.Fatalf("恢复后不应再被 tick 提升出告警，实际 %d 条", store.alertCount())
	}
}

// ---- 多规则命中同一 dedup_key：严重级别最高者优先 ----
//
// 裁决：critical > warning > info，同级再按 ID。
// 若按 ID 排序，warning 可能被选中，导致 critical 永远发不出去。

func TestSeverityRankOrder(t *testing.T) {
	// rank 越小越严重。
	if !(severityRank(SeverityCritical) < severityRank(SeverityWarning)) {
		t.Fatalf("critical 必须优先于 warning")
	}
	if !(severityRank(SeverityWarning) < severityRank(SeverityInfo)) {
		t.Fatalf("warning 必须优先于 info")
	}
	// 未知/空级别按 warning 处理，与 normalizeRule 默认值一致。
	if severityRank("") != severityRank(SeverityWarning) {
		t.Fatalf("空级别应按 warning 处理")
	}
	if severityRank("bogus") != severityRank(SeverityWarning) {
		t.Fatalf("未知级别应按 warning 处理")
	}
}

func TestHighestSeverityRuleWinsSameDedupKey(t *testing.T) {
	// 故意让 ID 顺序与严重级别顺序相反：若按 ID 排序会选中 warning（rule-a）。
	warning := checkRule()
	warning.ID = "rule-a"
	warning.Name = "warning 规则"
	warning.Severity = SeverityWarning
	warning.ChannelIDs = []string{"ch-warning"}

	critical := checkRule()
	critical.ID = "rule-z"
	critical.Name = "critical 规则"
	critical.Severity = SeverityCritical
	critical.ChannelIDs = []string{"ch-critical"}

	store := newFakeStore(warning, critical)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	if err := eng.HandleEvent(context.Background(), activeEvent(clock.Now())); err != nil {
		t.Fatalf("HandleEvent 失败: %v", err)
	}

	alerts := store.allAlerts()
	if len(alerts) != 1 {
		t.Fatalf("同一 dedup_key 只应有 1 条告警，实际 %d 条", len(alerts))
	}
	a := alerts[0]
	if a.Severity != SeverityCritical {
		t.Fatalf("critical 必须胜过 warning，实际选中的是 %q（规则 %s）", a.Severity, a.RuleID)
	}
	if a.RuleID != "rule-z" {
		t.Fatalf("应选主规则 rule-z(critical)，实际 %q", a.RuleID)
	}
	// 通知也必须走 critical 规则的渠道。
	calls := rec.all()
	if len(calls) != 1 {
		t.Fatalf("应通知 1 次，实际 %d 次", len(calls))
	}
	if calls[0].Channels[0] != "ch-critical" {
		t.Fatalf("通知应走 critical 规则的渠道，实际 %v", calls[0].Channels)
	}
}

func TestSameSeverityFallsBackToIDOrder(t *testing.T) {
	// 同级时按 ID 升序，保证确定性。
	ruleB := checkRule()
	ruleB.ID = "rule-b"
	ruleB.Severity = SeverityWarning
	ruleB.ChannelIDs = []string{"ch-b"}

	ruleA := checkRule()
	ruleA.ID = "rule-a"
	ruleA.Severity = SeverityWarning
	ruleA.ChannelIDs = []string{"ch-a"}

	// 传入顺序故意与 ID 顺序相反。
	store := newFakeStore(ruleB, ruleA)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	if got := store.allAlerts()[0].RuleID; got != "rule-a" {
		t.Fatalf("同级应按 ID 升序选 rule-a，实际 %q", got)
	}
	if got := rec.all()[0].Channels[0]; got != "ch-a" {
		t.Fatalf("应走 rule-a 的渠道，实际 %q", got)
	}
}

func TestSeverityPriorityAcrossAllThreeLevels(t *testing.T) {
	info := checkRule()
	info.ID = "rule-1-info"
	info.Severity = SeverityInfo

	warning := checkRule()
	warning.ID = "rule-2-warning"
	warning.Severity = SeverityWarning

	critical := checkRule()
	critical.ID = "rule-3-critical"
	critical.Severity = SeverityCritical

	store := newFakeStore(info, warning, critical)
	clock := newFakeClock(baseTS)
	eng, _ := newTestEngine(t, store, clock)

	_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
	if got := store.allAlerts()[0].Severity; got != SeverityCritical {
		t.Fatalf("三级同时命中应选 critical，实际 %q", got)
	}
}

// ---- 静默期只抑制 firing，不抑制恢复 ----

func TestSilenceBlocksRepeatedFiringNotifyOnly(t *testing.T) {
	rule := checkRule()
	rule.SilenceUntil = baseTS + 3_600_000
	rule.DedupWindowS = 60
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	// 静默期内多次触发：全部不通知。
	for i := 0; i < 3; i++ {
		_ = eng.HandleEvent(context.Background(), activeEvent(clock.Now()))
		clock.Advance(120_000) // 每轮都超过去重窗口
	}
	if got := rec.count(); got != 0 {
		t.Fatalf("静默期内不应有任何 firing 通知，实际 %d 次", got)
	}
	if got := store.alertCount(); got != 1 {
		t.Fatalf("静默期仍应只建 1 条告警，实际 %d 条", got)
	}

	// 恢复：必须穿透静默发出。
	_ = eng.HandleEvent(context.Background(), recoverEvent(clock.Now()))
	if got := len(rec.recoveredCalls()); got != 1 {
		t.Fatalf("恢复通知必须穿透静默，实际 %d 次", got)
	}
	if got := len(rec.firingCalls()); got != 0 {
		t.Fatalf("整个过程中不应有 firing 通知，实际 %d 次", got)
	}
}

func TestRecoveryNotifyPassesThroughSilenceForMetricRule(t *testing.T) {
	rule := Rule{
		ID: "rule-metric", Name: "CPU 高", Enabled: true, Source: SourceMetric,
		Metric: "cpu.pct", Comparator: ">", Threshold: 80,
		Severity: SeverityWarning, DedupWindowS: 300, ChannelIDs: []string{"ch-m"},
		SilenceUntil: baseTS + 3_600_000,
	}
	store := newFakeStore(rule)
	clock := newFakeClock(baseTS)
	eng, rec := newTestEngine(t, store, clock)

	hi := []MetricSample{{ServerID: "srv-1", ServerName: "web-01", Metric: "cpu.pct", Value: 95, TS: clock.Now()}}
	_ = eng.EvaluateMetrics(context.Background(), hi)
	if got := rec.count(); got != 0 {
		t.Fatalf("静默期不应发送 firing 通知，实际 %d 次", got)
	}

	clock.Advance(10_000)
	lo := []MetricSample{{ServerID: "srv-1", ServerName: "web-01", Metric: "cpu.pct", Value: 20, TS: clock.Now()}}
	_ = eng.EvaluateMetrics(context.Background(), lo)

	if got := store.allAlerts()[0].Status; got != StatusResolved {
		t.Fatalf("指标恢复应置 resolved，实际 %q", got)
	}
	if got := len(rec.recoveredCalls()); got != 1 {
		t.Fatalf("指标恢复通知也必须穿透静默，实际 %d 次", got)
	}
	if got := len(rec.firingCalls()); got != 0 {
		t.Fatalf("不应有 firing 通知，实际 %d 次", got)
	}
}
