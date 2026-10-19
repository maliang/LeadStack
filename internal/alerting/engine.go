package alerting

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// 默认值与内部常量。
const (
	// DefaultDedupWindowS 默认去重窗口（秒）：距上次通知超过该时长才允许再次通知。
	DefaultDedupWindowS = 300
	// DefaultForDurationS 默认持续时长（秒）：0 表示首次观测到异常立即触发。
	DefaultForDurationS = 0

	// numStripes 去重键互斥锁的分片数：按 dedup_key 哈希取一片，
	// 保证「同一 key 串行、不同 key 并行」，且内存占用有界（不随 key 数量增长）。
	numStripes = 256

	// pendingPruneThreshold 是触发 pending 惰性清扫的条目数阈值。
	pendingPruneThreshold = 4096
	// pendingTTL 是 pending 条目的最长存活时间：超过该时长仍未达到 for_duration
	// （例如检测项被删除、再没有后续事件）则视为失效。
	pendingTTL = 24 * 60 * 60 * 1000 // 毫秒，与观测时间戳单位一致
)

var (
	errNoStore        = errors.New("alerting: 未注入 Store")
	errMetricViaEvent = errors.New("alerting: HandleEvent 不支持 source=metric，指标规则请走 EvaluateMetrics")
)

// Engine 是告警规则评估与告警状态机引擎。零值不可用，必须用 New 构造。并发安全。
type Engine struct {
	deps Deps
	now  func() int64

	// rules 是规则缓存，Reload 时整体原子替换（读侧无锁）。
	rules atomic.Pointer[[]Rule]

	// stripes 按 dedup_key 分片的互斥锁，保证同一 key 的状态流转串行。
	stripes [numStripes]sync.Mutex

	// mu 保护 pending（for_duration 的待触发观测）。
	//
	// 【已知限制 · 有意为之】pending 是**纯内存态**，不落库：
	// 控制端重启后，尚未满足 for_duration 的计时会丢失，异常若仍在持续则从重启后重新计时。
	// 这是可接受的行为——重启本身就可能错过异常窗口，为它引入 pending 持久化（需要改
	// alerts 相关 schema）不划算。Lead 已将此写入 M3 已知限制。
	mu      sync.Mutex
	pending map[string]*pendingEntry
}

// pendingEntry 是一次「已观测到异常、但尚未满足 for_duration」的待触发记录。
//
// 保存完整 observation 而不是只存时间戳，是因为检测项/服务器事件只在**状态翻转**时
// 到达一次：若只存时间戳，达到 for_duration 时没有任何后续事件来推进状态，
// 告警将永远不会触发。保存观测后可由周期 tick（EvaluateMetrics）重新评估并提升为 firing。
type pendingEntry struct {
	firstSeen int64 // 首次观测到异常的时刻（for_duration 计时起点）
	lastSeen  int64 // 最近一次观测到异常的时刻
	obs       observation
}

// notification 是一次待发送的通知（在锁内决定、在锁外调用 Notify）。
type notification struct {
	alert     Alert
	channels  []string
	recovered bool
}

// observation 把「检测/服务器事件」与「指标样本」归一成同一种观测，复用同一套状态机。
type observation struct {
	rule       Rule
	key        string
	ts         int64
	source     Source
	serverID   string
	serverName string
	group      string
	tags       []string
	checkID    string
	checkName  string
	metric     string
	value      float64
	// hasValue 区分「真实观测值 0」与「未提供值」，避免把已有的 Value 误清成 0。
	hasValue bool
	message  string
}

// New 构造 Engine。Deps.Now 为空时使用 time.Now().UnixMilli()。
func New(d Deps) *Engine {
	e := &Engine{deps: d, pending: make(map[string]*pendingEntry)}
	e.now = d.Now
	if e.now == nil {
		e.now = func() int64 { return time.Now().UnixMilli() }
	}
	return e
}

// HandleEvent 处理一次状态翻转：负责 for_duration 判定、去重、建/销告警、触发通知。
func (e *Engine) HandleEvent(ctx context.Context, ev Event) error {
	if e.deps.Store == nil {
		return errNoStore
	}
	key, err := dedupKeyForEvent(ev)
	if err != nil {
		return err
	}
	ts := ev.TS
	if ts <= 0 {
		ts = e.now()
	}
	rules, err := e.currentRules(ctx)
	if err != nil {
		return err
	}
	matched := matchRules(rules, ev)
	obs := observation{
		key:        key,
		ts:         ts,
		source:     ev.Source,
		serverID:   ev.ServerID,
		serverName: ev.ServerName,
		group:      ev.Group,
		tags:       ev.Tags,
		checkID:    ev.CheckID,
		checkName:  ev.CheckName,
		value:      ev.Value,
		hasValue:   true,
		message:    ev.Message,
	}
	return e.dispatch(ctx, obs, rules, matched, ev.Active)
}

// EvaluateMetrics 周期评估指标阈值规则（同一去重键同样走 for_duration 与去重）。
func (e *Engine) EvaluateMetrics(ctx context.Context, samples []MetricSample) error {
	if e.deps.Store == nil {
		return errNoStore
	}
	rules, err := e.currentRules(ctx)
	if err != nil {
		return err
	}
	var errs []error

	// 本方法同时承担「周期推进 for_duration」的职责：check/server 事件源只在状态翻转时
	// 回调一次，异常持续期间不会有第二次事件，必须靠周期 tick 把已达门槛的待触发记录
	// 提升为 firing。放在指标评估之前，保证纯 check/server 场景（没有指标规则）也能推进。
	if err := e.promoteDuePending(ctx); err != nil {
		errs = append(errs, err)
	}

	metricRules := make([]Rule, 0, len(rules))
	for _, r := range rules {
		if r.Enabled && r.Source == SourceMetric && r.Metric != "" && validComparator(r.Comparator) {
			metricRules = append(metricRules, r)
		}
	}
	if len(metricRules) == 0 {
		return errors.Join(errs...)
	}

	for _, s := range samples {
		ts := s.TS
		if ts <= 0 {
			ts = e.now()
		}
		for _, r := range metricRules {
			if r.Metric != s.Metric {
				continue
			}
			if !scopeMatches(r, "", s.ServerID, s.Group, s.Tags) {
				continue
			}
			obs := observation{
				rule:       r,
				key:        metricDedupKey(r.ID, s.ServerID),
				ts:         ts,
				source:     SourceMetric,
				serverID:   s.ServerID,
				serverName: s.ServerName,
				group:      s.Group,
				tags:       s.Tags,
				metric:     s.Metric,
				value:      s.Value,
				hasValue:   true,
				message:    metricMessage(r, s),
			}
			active := compare(s.Value, r.Comparator, r.Threshold)
			if err := e.dispatch(ctx, obs, rules, []Rule{r}, active); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Reload 规则增删改后刷新缓存（由 backend 在 API 写操作后调用）。
// 规则列表在锁内归一化后整体原子替换，读侧永远看到一致快照。
func (e *Engine) Reload(ctx context.Context) error {
	if e.deps.Store == nil {
		return errNoStore
	}
	rules, err := e.deps.Store.ListRules(ctx)
	if err != nil {
		return fmt.Errorf("alerting: 读取告警规则失败: %w", err)
	}
	normalized := make([]Rule, 0, len(rules))
	for _, r := range rules {
		normalized = append(normalized, normalizeRule(r))
	}
	// 排序决定「多规则命中同一 dedup_key」时选哪条规则作为主规则（渠道与去重窗口都跟它走）。
	// 口径：**严重级别最高的优先（critical > warning > info），同级再按 ID 升序**。
	//
	// 为什么按级别而不是按 ID：若同一目标同时配了 warning 与 critical 两条规则，
	// 按 ID 排序可能选中 warning，导致 critical 永远发不出去——这在告警产品里不可接受。
	sort.SliceStable(normalized, func(i, j int) bool {
		ri, rj := severityRank(normalized[i].Severity), severityRank(normalized[j].Severity)
		if ri != rj {
			return ri < rj // rank 越小越严重
		}
		return normalized[i].ID < normalized[j].ID
	})
	e.rules.Store(&normalized)
	return nil
}

// severityRank 把严重级别映射成可排序的优先级，越小越严重。
// 未知/空级别按 warning 处理（与 normalizeRule 的默认值一致）。
func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 0
	case SeverityWarning:
		return 1
	case SeverityInfo:
		return 2
	default:
		return 1
	}
}

// dispatch 是状态机的统一入口：active=true 走触发/去重，false 走恢复。
func (e *Engine) dispatch(ctx context.Context, obs observation, all, matched []Rule, active bool) error {
	mu := e.keyLock(obs.key)
	mu.Lock()
	var note *notification
	var err error
	if active {
		note, err = e.trigger(ctx, obs, all, matched)
	} else {
		note, err = e.resolve(ctx, obs, all, matched)
	}
	mu.Unlock()
	if err != nil {
		return err
	}
	// Notify 在锁外调用：状态与计数已落库，锁外发送不会产生重复告警；
	// backend 的实现若可能阻塞（HTTP/邮件），也不会拖住同一分片上的其它去重键。
	if note != nil && e.deps.Notify != nil {
		e.deps.Notify(ctx, note.alert, note.channels, note.recovered)
	}
	return nil
}

// trigger 处理「观测到异常」：for_duration 判定 → 建告警或去重累加。
func (e *Engine) trigger(ctx context.Context, obs observation, all, matched []Rule) (*notification, error) {
	active, err := e.deps.Store.FindActiveAlert(ctx, obs.key)
	if err != nil {
		return nil, fmt.Errorf("alerting: 查询活跃告警(%s)失败: %w", obs.key, err)
	}
	if len(matched) == 0 {
		// 没有启用中的规则命中：不建告警，清掉可能残留的 pending。
		e.clearPending(obs.key)
		return nil, nil
	}
	rule := matched[0]
	if active != nil {
		return e.repeat(ctx, obs, all, matched, active, rule)
	}

	// for_duration：从首次观测到异常起计时，持续满足才真正 firing。
	if fd := rule.ForDurationS; fd > 0 {
		// 记录命中的规则，供周期 tick 提升时识别（并复查该规则是否仍启用）。
		obs.rule = rule
		first, ok := e.observePending(obs)
		if !ok {
			return nil, nil
		}
		if obs.ts-first < int64(fd)*1000 {
			return nil, nil
		}
	}
	e.clearPending(obs.key)

	alert := buildAlert(rule, obs)
	if err := e.deps.Store.CreateAlert(ctx, alert); err != nil {
		return nil, fmt.Errorf("alerting: 创建告警(%s)失败: %w", obs.key, err)
	}
	if err := e.appendEvent(ctx, alert.ID, EventKindTrigger, obs.ts, triggerDetail(obs)); err != nil {
		return nil, err
	}

	// 静默期内仍然建告警、仍然写事件，只是不发送 firing 通知。
	// （恢复通知不受静默影响，见 resolve。）
	if silenced(rule.SilenceUntil, obs.ts) {
		return nil, nil
	}
	alert.NotifyCount++
	alert.LastNotifyAt = obs.ts
	alert.UpdatedAt = obs.ts
	if err := e.deps.Store.UpdateAlert(ctx, alert); err != nil {
		return nil, fmt.Errorf("alerting: 更新告警通知计数(%s)失败: %w", alert.ID, err)
	}
	if err := e.appendEvent(ctx, alert.ID, EventKindNotify, obs.ts, notifyDetail(rule.ChannelIDs, false)); err != nil {
		return nil, err
	}
	return &notification{alert: alert, channels: rule.ChannelIDs}, nil
}

// repeat 处理「告警已存在时再次观测到异常」：不新建，只更新 updated_at；
// 距上次通知超过 dedup_window_s 才再推一次。
func (e *Engine) repeat(ctx context.Context, obs observation, all, matched []Rule, active *Alert, fallback Rule) (*notification, error) {
	rule := fallback
	if owner := findRule(all, active.RuleID); owner != nil && owner.Enabled {
		rule = *owner
	}
	a := *active
	a.UpdatedAt = obs.ts
	if obs.hasValue {
		a.Value = obs.value
	}
	if obs.message != "" {
		a.Message = obs.message
	}
	a.SilenceUntil = rule.SilenceUntil

	// 规则被停用后不再打扰，但已有的告警记录仍然更新 updated_at（保持状态一致）。
	ownerEnabled := true
	if owner := findRule(all, active.RuleID); owner != nil {
		ownerEnabled = owner.Enabled
	}
	notify := ownerEnabled && !silenced(rule.SilenceUntil, obs.ts) && dedupWindowElapsed(a, rule, obs.ts)
	if notify {
		a.NotifyCount++
		a.LastNotifyAt = obs.ts
	}
	if err := e.deps.Store.UpdateAlert(ctx, a); err != nil {
		return nil, fmt.Errorf("alerting: 更新活跃告警(%s)失败: %w", a.ID, err)
	}
	if !notify {
		return nil, nil
	}
	if err := e.appendEvent(ctx, a.ID, EventKindNotify, obs.ts, notifyDetail(rule.ChannelIDs, false)); err != nil {
		return nil, err
	}
	return &notification{alert: a, channels: rule.ChannelIDs}, nil
}

// resolve 处理「异常解除」：置 resolved + 一次恢复通知；恢复后同一 key 可再次触发新告警。
func (e *Engine) resolve(ctx context.Context, obs observation, all, matched []Rule) (*notification, error) {
	e.clearPending(obs.key)
	active, err := e.deps.Store.FindActiveAlert(ctx, obs.key)
	if err != nil {
		return nil, fmt.Errorf("alerting: 查询活跃告警(%s)失败: %w", obs.key, err)
	}
	if active == nil {
		return nil, nil
	}
	if err := e.deps.Store.ResolveAlert(ctx, active.ID, obs.ts); err != nil {
		return nil, fmt.Errorf("alerting: 恢复告警(%s)失败: %w", active.ID, err)
	}
	a := *active
	a.Status = StatusResolved
	a.ResolvedAt = obs.ts
	a.UpdatedAt = obs.ts
	if err := e.appendEvent(ctx, a.ID, EventKindResolve, obs.ts, resolveDetail(obs)); err != nil {
		return nil, err
	}

	// 恢复通知走与故障通知同一渠道，且**必须穿透静默**。
	//
	// 静默的语义是「不要再打扰我新发生的故障」，而不是「让我永远不知道服务已经好了」；
	// 恢复通知是降噪信息而非噪声。因此这里只取渠道，不做 silenced 判定。
	// （静默期只抑制 firing 通知，见 trigger/repeat。）
	rule := findRule(all, active.RuleID)
	if rule == nil && len(matched) > 0 {
		rule = &matched[0]
	}
	channels := []string(nil)
	if rule != nil {
		channels = rule.ChannelIDs
	}
	return &notification{alert: a, channels: channels, recovered: true}, nil
}

// ---- 规则匹配 ----

// matchRules 返回所有启用且匹配该事件的规则（已按 ID 排序，首条为选主规则）。
func matchRules(rules []Rule, ev Event) []Rule {
	out := make([]Rule, 0, 1)
	for _, r := range rules {
		if !r.Enabled || r.Source != ev.Source {
			continue
		}
		if r.Source == SourceMetric {
			continue // 指标规则只由 EvaluateMetrics 驱动
		}
		if !scopeMatches(r, ev.CheckID, ev.ServerID, ev.Group, ev.Tags) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// scopeMatches 判断规则适用范围是否命中观测对象。
// 四个选择器全部为空 = 匹配所有；任一非空则按并集语义（命中任意一个选择器即算命中）。
func scopeMatches(r Rule, checkID, serverID, group string, tags []string) bool {
	if len(r.CheckIDs) == 0 && len(r.ServerIDs) == 0 && len(r.Groups) == 0 && len(r.Tags) == 0 {
		return true
	}
	if checkID != "" && containsString(r.CheckIDs, checkID) {
		return true
	}
	if serverID != "" && containsString(r.ServerIDs, serverID) {
		return true
	}
	if group != "" && containsString(r.Groups, group) {
		return true
	}
	for _, t := range tags {
		if t != "" && containsString(r.Tags, t) {
			return true
		}
	}
	return false
}

func findRule(rules []Rule, id string) *Rule {
	if id == "" {
		return nil
	}
	for i := range rules {
		if rules[i].ID == id {
			return &rules[i]
		}
	}
	return nil
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// ---- dedup_key ----

func dedupKeyForEvent(ev Event) (string, error) {
	switch ev.Source {
	case SourceCheck:
		if ev.CheckID == "" || ev.ServerID == "" {
			return "", fmt.Errorf("alerting: source=check 的事件必须带 CheckID 与 ServerID（check=%q server=%q）", ev.CheckID, ev.ServerID)
		}
		return "check:" + ev.CheckID + ":" + ev.ServerID, nil
	case SourceServer:
		if ev.ServerID == "" {
			return "", fmt.Errorf("alerting: source=server 的事件必须带 ServerID")
		}
		return "server:" + ev.ServerID, nil
	case SourceMetric:
		return "", errMetricViaEvent
	default:
		return "", fmt.Errorf("alerting: 未知事件来源 %q", ev.Source)
	}
}

func metricDedupKey(ruleID, serverID string) string {
	return "metric:" + ruleID + ":" + serverID
}

// ---- 时间/去重/静默判定 ----

func silenced(until, ts int64) bool {
	return until > 0 && until > ts
}

// dedupWindowElapsed 判断是否已超过去重窗口、可以再次通知。
// 语义是「距上次通知是否已过 dedup_window_s」：从未通知过则立即允许；
// 恰好等于窗口长度视为已过窗口（边界取 >=）。
func dedupWindowElapsed(a Alert, r Rule, ts int64) bool {
	if a.LastNotifyAt <= 0 {
		return true
	}
	windowS := r.DedupWindowS
	if windowS < 0 {
		windowS = 0
	}
	return ts-a.LastNotifyAt >= int64(windowS)*1000
}

func (e *Engine) keyLock(key string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return &e.stripes[h.Sum32()%numStripes]
}

// observePending 记录一次异常观测，并返回 for_duration 的计时起点。
//
// 返回 ok=false 表示「首次观测，尚未开始计时」（调用方不应建告警）。
// 同一 dedup_key 重复观测时保留最早的 firstSeen，只刷新 lastSeen 与观测内容。
func (e *Engine) observePending(obs observation) (firstSeen int64, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.pending) >= pendingPruneThreshold {
		e.pruneLocked(obs.ts)
	}
	if p, exists := e.pending[obs.key]; exists {
		p.lastSeen = obs.ts
		p.obs = obs
		return p.firstSeen, true
	}
	cp := obs
	e.pending[obs.key] = &pendingEntry{firstSeen: obs.ts, lastSeen: obs.ts, obs: cp}
	return 0, false
}

// pendingSnapshot 返回某个 dedup_key 的待触发记录副本。
func (e *Engine) pendingSnapshot(key string) (pendingEntry, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, ok := e.pending[key]
	if !ok {
		return pendingEntry{}, false
	}
	return *p, true
}

// pruneLocked 清掉超过 pendingTTL 未再被观测到的待触发条目，保证内存有界。
// 调用方必须持有 e.mu。
func (e *Engine) pruneLocked(nowTS int64) {
	cutoff := nowTS - pendingTTL
	for k, p := range e.pending {
		if p.lastSeen < cutoff {
			delete(e.pending, k)
		}
	}
}

// clearPending 删除某个 dedup_key 的待触发记录。
func (e *Engine) clearPending(key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.pending, key)
}

// promoteDuePending 把「已持续异常达到 for_duration」的待触发记录提升为 firing 告警。
//
// 为什么需要它：检测项/服务器事件源（checker.Deps.OnStateChange）只在**状态翻转**时
// 回调一次，异常持续期间不会有第二次事件。若仅靠 HandleEvent 推进 for_duration，
// for_duration_s > 0 的 check/server 规则将永远不会触发。因此周期 tick 调一次本方法，
// 用 pending 里保存的观测重新走一遍状态机（届时 obs.ts 已超过计时门槛）。
func (e *Engine) promoteDuePending(ctx context.Context) error {
	e.mu.Lock()
	keys := make([]string, 0, len(e.pending))
	for k := range e.pending {
		keys = append(keys, k)
	}
	e.mu.Unlock()
	if len(keys) == 0 {
		return nil
	}
	rules, err := e.currentRules(ctx)
	if err != nil {
		return err
	}

	var errs []error
	for _, key := range keys {
		entry, ok := e.pendingSnapshot(key)
		if !ok {
			continue // 并发路径已处理掉
		}
		// 指标规则的待触发状态由样本逐轮推进（每轮都有新观测），无需在此提升。
		if entry.obs.source == SourceMetric {
			continue
		}
		// 待触发记录的规则可能已被删除/停用：直接丢弃。
		owner := findRule(rules, entry.obs.rule.ID)
		if owner == nil || !owner.Enabled {
			e.clearPending(key)
			continue
		}
		// 计时口径：pending 只在「持续异常」期间存在（收到恢复事件会立即清除），
		// 因此 elapsed = 当前时刻 - 首次观测时刻。不能用 lastSeen：
		// 翻转型事件源在异常持续期间不会再有新事件，lastSeen 会一直停在首次观测上。
		nowTS := e.now()
		if nowTS-entry.firstSeen < int64(owner.ForDurationS)*1000 {
			continue // 仍未达到门槛，留给下一轮 tick
		}
		// 已达标：以当前时刻重新走一遍触发流程（此时 for_duration 必然满足）。
		obs := entry.obs
		obs.ts = nowTS
		if err := e.dispatch(ctx, obs, rules, []Rule{*owner}, true); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ---- 规则归一化 ----

func normalizeRule(r Rule) Rule {
	if r.ForDurationS < 0 {
		r.ForDurationS = 0
	}
	if r.DedupWindowS < 0 {
		r.DedupWindowS = 0
	}
	if r.Severity == "" {
		r.Severity = SeverityWarning
	}
	return r
}

func validComparator(op string) bool {
	switch strings.TrimSpace(op) {
	case ">", ">=", "<", "<=":
		return true
	}
	return false
}

func compare(v float64, op string, threshold float64) bool {
	switch strings.TrimSpace(op) {
	case ">":
		return v > threshold
	case ">=":
		return v >= threshold
	case "<":
		return v < threshold
	case "<=":
		return v <= threshold
	}
	return false
}

// ---- 告警构造与事件明细 ----

func buildAlert(r Rule, obs observation) Alert {
	sev := r.Severity
	if sev == "" {
		sev = SeverityWarning
	}
	return Alert{
		ID:           uuid.NewString(),
		RuleID:       r.ID,
		DedupKey:     obs.key,
		ServerID:     obs.serverID,
		CheckID:      obs.checkID,
		Status:       StatusFiring,
		Severity:     sev,
		Title:        alertTitle(r, obs),
		Message:      alertMessage(r, obs),
		Value:        obs.value,
		StartedAt:    obs.ts,
		UpdatedAt:    obs.ts,
		SilenceUntil: r.SilenceUntil,
	}
}

// alertTitle 标题必须含规则名与服务器名（M3 验收第 4 条要求）。
func alertTitle(r Rule, obs observation) string {
	server := obs.serverName
	if server == "" {
		server = obs.serverID
	}
	title := r.Name
	if title == "" {
		title = "告警"
	}
	title += " · " + server
	switch obs.source {
	case SourceCheck:
		if obs.checkName != "" {
			title += " / " + obs.checkName
		} else if obs.checkID != "" {
			title += " / " + obs.checkID
		}
	case SourceMetric:
		if obs.metric != "" {
			title += " / " + obs.metric
		}
	}
	return title
}

func alertMessage(r Rule, obs observation) string {
	if obs.message != "" {
		return obs.message
	}
	server := obs.serverName
	if server == "" {
		server = obs.serverID
	}
	switch obs.source {
	case SourceCheck:
		name := obs.checkName
		if name == "" {
			name = obs.checkID
		}
		return fmt.Sprintf("检测项 %s 在 %s 上持续异常", name, server)
	case SourceServer:
		return fmt.Sprintf("服务器 %s 异常", server)
	case SourceMetric:
		return fmt.Sprintf("指标 %s 当前值 %g 满足 %s %g", obs.metric, obs.value, r.Comparator, r.Threshold)
	}
	return "异常"
}

func metricMessage(r Rule, s MetricSample) string {
	server := s.ServerName
	if server == "" {
		server = s.ServerID
	}
	return fmt.Sprintf("服务器 %s 指标 %s 当前值 %g 满足 %s %g", server, s.Metric, s.Value, r.Comparator, r.Threshold)
}

func triggerDetail(obs observation) string {
	switch obs.source {
	case SourceCheck:
		return "检测项异常触发: check=" + obs.checkID + " server=" + obs.serverID
	case SourceServer:
		return "服务器异常触发: server=" + obs.serverID
	case SourceMetric:
		return "指标越界触发: metric=" + obs.metric + " server=" + obs.serverID
	}
	return "触发"
}

func resolveDetail(obs observation) string {
	switch obs.source {
	case SourceCheck:
		return "检测项恢复: check=" + obs.checkID + " server=" + obs.serverID
	case SourceServer:
		return "服务器恢复: server=" + obs.serverID
	case SourceMetric:
		return "指标恢复: metric=" + obs.metric + " server=" + obs.serverID
	}
	return "恢复"
}

func notifyDetail(channels []string, recovered bool) string {
	kind := "trigger"
	if recovered {
		kind = "recovered"
	}
	return "kind=" + kind + " channels=" + strings.Join(channels, ",")
}

func (e *Engine) appendEvent(ctx context.Context, alertID, kind string, ts int64, detail string) error {
	if err := e.deps.Store.AppendAlertEvent(ctx, alertID, kind, ts, detail); err != nil {
		return fmt.Errorf("alerting: 写入告警事件(%s/%s)失败: %w", alertID, kind, err)
	}
	return nil
}

// currentRules 返回规则缓存；从未 Reload 过则先加载一次。
func (e *Engine) currentRules(ctx context.Context) ([]Rule, error) {
	if p := e.rules.Load(); p != nil {
		return *p, nil
	}
	if err := e.Reload(ctx); err != nil {
		return nil, err
	}
	if p := e.rules.Load(); p != nil {
		return *p, nil
	}
	return nil, nil
}
