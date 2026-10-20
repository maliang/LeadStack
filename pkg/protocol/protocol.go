// Package protocol 定义控制端与 agent 之间的 WebSocket 消息契约。
//
// 该包是两侧共享的唯一事实来源，规则见 docs/ARCHITECTURE.md 第 4 节：
//   - 信封固定为 {type, id, ts, payload}，JSON 文本帧。
//   - 时间戳统一 Unix 毫秒。
//   - 新增消息类型必须向后兼容，未识别的 type 必须被忽略而不是断开连接。
package protocol

import "encoding/json"

// 消息类型常量。
const (
	// agent -> 控制端
	TypeHello     = "hello"
	TypeHeartbeat = "heartbeat"
	TypeMetrics   = "metrics"
	TypeHostInfo  = "host_info"
	TypeJobAck    = "job.ack"
	TypeJobLog    = "job.log"
	TypeJobDone   = "job.done"
	TypeTermData  = "term.data"

	// 控制端 -> agent
	TypeHelloAck   = "hello_ack"
	TypeConfigPush = "config.push"
	TypeJobExec    = "job.exec"
	TypeTermOpen   = "term.open"
	TypeTermResize = "term.resize"
	TypeTermClose  = "term.close"
)

// 指标名（点分层级）。M0 只要求前 5 个。
const (
	MetricCPUPct     = "cpu.pct"
	MetricMemUsedPct = "mem.used_pct"
	MetricMemUsedB   = "mem.used_bytes"
	MetricLoad1      = "load.1"
	MetricDiskUsedPt = "disk.used_pct"
	MetricNetRxBytes = "net.rx_bytes"
	MetricNetTxBytes = "net.tx_bytes"
	MetricProcCount  = "proc.count"
)

// 服务器在线状态。
const (
	StatusOnline  = "online"
	StatusOffline = "offline"
	StatusUnknown = "unknown"
)

// 默认节奏（秒）。
const (
	DefaultHeartbeatIntervalS = 20
	DefaultMetricsIntervalS   = 15
	// OfflineAfterS 服务端超过该秒数未收到心跳即视为离线。
	OfflineAfterS = 45
)

// Envelope 是所有 WebSocket 帧的统一信封。
type Envelope struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	TS      int64           `json:"ts"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Hello 由 agent 在连接建立后立即发送。
type Hello struct {
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Kernel       string `json:"kernel"`
	AgentVersion string `json:"agent_version"`
	BootTime     int64  `json:"boot_time"`
}

// HelloAck 由控制端返回，携带服务端下发的节奏配置。
type HelloAck struct {
	ServerID           string `json:"server_id"`
	HeartbeatIntervalS int    `json:"heartbeat_interval_s"`
	MetricsIntervalS   int    `json:"metrics_interval_s"`
}

// Heartbeat 是轻量存活信号，用于 UI 与离线判定。
type Heartbeat struct {
	CPUPct     float64 `json:"cpu_pct"`
	MemUsedPct float64 `json:"mem_used_pct"`
	DiskUsedPt float64 `json:"disk_used_pct"`
	Load1      float64 `json:"load1"`
	UptimeS    int64   `json:"uptime_s"`
}

// Sample 是一条指标采样。
type Sample struct {
	Metric string  `json:"metric"`
	Value  float64 `json:"value"`
	TS     int64   `json:"ts"`
}

// Metrics 是批量指标上报。
type Metrics struct {
	Samples []Sample `json:"samples"`
}

// HostInfo 是 agent 上报的主机静态信息（上线后立即一次，之后每 6 小时一次）。
// 字段与 internal/hostinfo.Info 一一对应，json tag 必须保持一致。
type HostInfo struct {
	Hostname       string   `json:"hostname"`
	OS             string   `json:"os"`
	OSVersion      string   `json:"os_version"`
	Kernel         string   `json:"kernel"`
	Arch           string   `json:"arch"`
	CPUModel       string   `json:"cpu_model"`
	CPUCores       int      `json:"cpu_cores"`
	MemTotalBytes  uint64   `json:"mem_total_bytes"`
	DiskTotalBytes uint64   `json:"disk_total_bytes"`
	DiskUsedBytes  uint64   `json:"disk_used_bytes"`
	UptimeS        int64    `json:"uptime_s"`
	Timezone       string   `json:"timezone"`
	Docker         bool     `json:"docker"`
	DockerVersion  string   `json:"docker_version,omitempty"`
	IPAddrs        []string `json:"ip_addrs"`
	CollectedAt    int64    `json:"collected_at"`
}

// JobExec 是控制端下发的任务。
type JobExec struct {
	RunID    string `json:"run_id"`
	Kind     string `json:"kind"` // M0 固定 "command"
	Command  string `json:"command"`
	TimeoutS int    `json:"timeout_s"`
}

// JobAck 表示 agent 已接手任务。
type JobAck struct {
	RunID string `json:"run_id"`
}

// JobLog 是任务日志的分块流。
type JobLog struct {
	RunID  string `json:"run_id"`
	Seq    int    `json:"seq"`
	Stream string `json:"stream"` // stdout | stderr
	Chunk  string `json:"chunk"`
}

// JobDone 表示任务结束。
type JobDone struct {
	RunID    string `json:"run_id"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

// ConfigPush 下发给 agent 的运行参数。
type ConfigPush struct {
	HeartbeatIntervalS *int `json:"heartbeat_interval_s,omitempty"`
	MetricsIntervalS   *int `json:"metrics_interval_s,omitempty"`
}

// TermOpen 请求开启一个交互式终端会话。
type TermOpen struct {
	SessionID string `json:"session_id"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
	Shell     string `json:"shell,omitempty"`
}

// TermResize 调整终端尺寸。
type TermResize struct {
	SessionID string `json:"session_id"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

// TermClose 关闭终端会话。
type TermClose struct {
	SessionID string `json:"session_id"`
}

// TermData 携带终端输入/输出的 base64 数据。
type TermData struct {
	SessionID string `json:"session_id"`
	B64       string `json:"b64"`
}

// TypeAgentUpgrade 是控制端 → agent 的升级指令（契约第 44 节）。
// agent 收到后下载并校验 sha256，使用 A/B 替换策略更新自身，失败必须保留旧版本。
const TypeAgentUpgrade = "agent.upgrade"

// AgentUpgrade 描述一次 agent 升级请求。
type AgentUpgrade struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	// Restart 为 true 时 agent 在替换完成后重启自身（或交由服务管理器重启）。
	Restart bool `json:"restart"`
}
