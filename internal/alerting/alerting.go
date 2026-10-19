// Package alerting 实现告警规则评估与告警状态机（契约见 docs/ARCHITECTURE.md 第 18/19.1 节）。
//
// 本包不 import internal/store / internal/api / internal/notify：持久化与通知均通过
// Deps 注入的接口完成，因此可脱离数据库与网络独立单测。
//
// 状态机要点（对应契约第 18 节）：
//
//   - for_duration_s：首次观测到异常时在内存 pending 表按 dedup_key 记录该次观测，
//     持续异常达到该时长才建 firing 告警；期间收到恢复事件则清掉 pending，不建告警。
//     由于事件源（checker.OnStateChange）只在状态翻转时回调一次，达标判定由周期 tick
//     （EvaluateMetrics）推进。pending 是纯内存态，控制端重启后从头计时（已知限制，有意为之）。
//   - dedup_key：check:<checkID>:<serverID> / server:<serverID> / metric:<ruleID>:<serverID>。
//     同一 key 同时只允许一条未解决（firing 或 acked）告警，由进程内按 key 的互斥锁 + Store
//     查询共同保证（并发 HandleEvent 不会建出第二条）。
//   - 去重窗口：告警已未解决时再次触发不新建告警，只更新 updated_at/value；
//     距上次通知（LastNotifyAt）超过 dedup_window_s 才再推送一次并累加 notify_count。
//   - 静默：silence_until 未过期时**仍然建告警、仍然写事件**，只是不发送 firing 通知；
//     **恢复通知必须穿透静默**（恢复是降噪信息，不是噪声）。
//   - 恢复：异常解除 → ResolveAlert + 一次 recovered=true 的通知；恢复后同一 key 可再次触发新告警。
//   - 多规则命中同一 dedup_key：选**严重级别最高**的规则作为主规则（critical > warning > info），
//     同级再按 ID 升序，保证确定性。
package alerting

import "context"

// Severity 告警严重级别。
type Severity string

// 严重级别取值（契约冻结）。
const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// Source 规则/事件来源。
type Source string

// 来源取值（契约冻结）。
const (
	SourceCheck  Source = "check"
	SourceServer Source = "server"
	SourceMetric Source = "metric"
)

// 告警状态取值（契约冻结）。
const (
	StatusFiring   = "firing"
	StatusResolved = "resolved"
	StatusAcked    = "acked"
)

// AppendAlertEvent 使用的 kind 取值。
//
// 这些字面量与 internal/store 的 AlertEvent* 常量保持一致（本包不得 import
// internal/store，故在此重复定义）：alert_events.kind 在库中是同一取值域，
// 引擎写入的 firing/notify/resolved 与 backend 写入的 acked/silenced 由前端统一渲染。
// 改动时需与 backend 同步。
const (
	// EventKindTrigger 告警首次建立（含静默期内建立的告警）。
	EventKindTrigger = "firing"
	// EventKindNotify 一次通知实际发出（detail 记录渠道；恢复通知同样使用该 kind，detail 注明 recovered）。
	EventKindNotify = "notify"
	// EventKindResolve 告警恢复。
	EventKindResolve = "resolved"
)

// Rule 是一条告警规则（字段与契约第 19.1 节一致）。
type Rule struct {
	ID           string
	Name         string
	Enabled      bool
	Source       Source
	CheckIDs     []string // 适用范围；全部为空 = 匹配所有
	ServerIDs    []string
	Groups       []string
	Tags         []string
	Metric       string // source=metric 时使用，如 "cpu.pct"
	Comparator   string // ">" ">=" "<" "<="
	Threshold    float64
	ForDurationS int
	Severity     Severity
	DedupWindowS int
	ChannelIDs   []string
	SilenceUntil int64
}

// Event 是一次观测到的状态变化（由 backend 从检测回调/服务器上下线转成）。
//
// Source=check 时 CheckID 与 ServerID 必填；Source=server 时 ServerID 必填。
// 指标规则不走 HandleEvent，而由 EvaluateMetrics 评估。
type Event struct {
	Source     Source
	ServerID   string
	ServerName string
	Group      string
	Tags       []string
	CheckID    string
	CheckName  string
	Active     bool // true=异常发生, false=恢复
	Value      float64
	Message    string
	TS         int64
}

// MetricSample 是周期评估的输入（backend 从最新指标快照构造）。
type MetricSample struct {
	ServerID   string
	ServerName string
	Group      string
	Tags       []string
	Metric     string
	Value      float64
	TS         int64
}

// Alert 是一条告警记录（字段与契约第 19.1 节一致）。
type Alert struct {
	ID, RuleID, DedupKey string
	ServerID, CheckID    string
	Status               string // "firing" | "resolved" | "acked"
	Severity             Severity
	Title, Message       string
	Value                float64
	StartedAt, UpdatedAt int64
	ResolvedAt, AckedAt  int64
	NotifyCount          int
	LastNotifyAt         int64
	SilenceUntil         int64
}

// Store 由 backend 实现。
//
// FindActiveAlert 必须返回该 dedup_key 下**未解决**（status 为 firing 或 acked）的告警，
// 没有则返回 (nil, nil)。
type Store interface {
	ListRules(ctx context.Context) ([]Rule, error)
	FindActiveAlert(ctx context.Context, dedupKey string) (*Alert, error)
	CreateAlert(ctx context.Context, a Alert) error
	UpdateAlert(ctx context.Context, a Alert) error
	ResolveAlert(ctx context.Context, id string, ts int64) error
	AppendAlertEvent(ctx context.Context, alertID, kind string, ts int64, detail string) error
}

// Deps 是 Engine 的依赖。
type Deps struct {
	Store Store
	// Notify 由 backend 注入：按渠道发送（内部接 notify.Client）。
	// channels 为规则配置的渠道 ID；recovered 为 true 时表示恢复通知。
	// 该回调无返回值：发送失败由 backend 自行记录，不影响引擎状态机。
	Notify func(ctx context.Context, a Alert, channels []string, recovered bool)
	Now    func() int64 // 可注入便于测试，默认 time.Now().UnixMilli()
}
