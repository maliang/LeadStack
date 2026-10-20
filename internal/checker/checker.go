// Package checker 实现 LeadStack 检测 的检测引擎：五种探测器（tcp/http/ping/process/script）
// 与按 (check, server) 目标调度的 Scheduler。
//
// 契约见 docs/ARCHITECTURE.md 第 13 节（检测模型与执行位置）与 14.1 节（包 API，签名已冻结）。
// 本包的公开签名不得改名；backend 通过实现 Store/Executor 消费本包，checker 不 import store。
package checker

import "context"

// Kind 是检测类型。
type Kind string

const (
	KindTCP     Kind = "tcp"     // host:port，控制端直连
	KindHTTP    Kind = "http"    // 完整 URL，控制端直连
	KindPing    Kind = "ping"    // host，控制端非特权 ICMP
	KindProcess Kind = "process" // 进程名，目标机执行
	KindScript  Kind = "script"  // 脚本内容，目标机执行
)

// 契约默认值（docs/ARCHITECTURE.md 第 13/14.1 节）。
const (
	MinIntervalS       = 10 // IntervalS 最小 10
	DefaultIntervalS   = 10
	DefaultTimeoutS    = 10
	DefaultConcurrency = 8
	DefaultMinCount    = 1
	DefaultFailThresh  = 3
)

// Spec 是一个检测项的不可变描述。
type Spec struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Kind      Kind           `json:"kind"`
	Target    string         `json:"target"`
	IntervalS int            `json:"interval_s"` // 最小 10
	TimeoutS  int            `json:"timeout_s"`  // 默认 10
	Params    map[string]any `json:"params,omitempty"`
}

// Result 是一次探测的结果。TS 为 Unix 毫秒。
type Result struct {
	OK         bool   `json:"ok"`
	LatencyMS  int64  `json:"latency_ms"`
	StatusCode int    `json:"status_code,omitempty"` // http 专用
	Message    string `json:"message,omitempty"`     // 失败原因 / 摘要
	TS         int64  `json:"ts"`                    // Unix 毫秒
}

// Executor 是「在目标机上执行一条命令」的抽象：agent 模式由 jobs.Dispatcher 实现，
// ssh 模式由 backend 用 sshx.Client 包装。checker 不感知底层通道。
type Executor interface {
	Exec(ctx context.Context, serverID, command string, timeoutS int) (stdout, stderr string, exitCode int, err error)
}

// Store 是调度器对持久化的最小依赖，由 backend 实现（避免 checker import store）。
type Store interface {
	ListEnabledTargets(ctx context.Context) ([]Target, error)
	SaveResult(ctx context.Context, checkID, serverID string, r Result) error
	UpdateState(ctx context.Context, checkID, serverID string, ok bool, ts int64) error
}

// Target 是调度器的最小调度单元：(检测项, 服务器)。
type Target struct {
	CheckID  string
	ServerID string
	Mode     string // "agent" | "ssh"
	Spec     Spec
}

// Deps 是 Scheduler 的依赖。
type Deps struct {
	Store         Store
	Exec          Executor
	Concurrency   int                                                  // 默认 8
	OnStateChange func(checkID, serverID string, ok bool, since int64) // 可选，告警 告警挂这里
}
