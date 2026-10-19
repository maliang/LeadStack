package store

// 本文件集中定义持久化模型，字段与 docs/ARCHITECTURE.md 第 5 节的 schema 一一对应。
// 可空列在模型里用指针表达，避免调用方混淆“0 值”与“无值”。

// User 是控制台用户。
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"`
	CreatedAt    int64  `json:"created_at"`
}

// Server 是被纳管的服务器。
type Server struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Host         string  `json:"host"`
	Port         int     `json:"port"`
	Mode         string  `json:"mode"`
	TagsJSON     string  `json:"-"`
	Status       string  `json:"status"`
	LastSeenAt   *int64  `json:"last_seen_at"`
	AgentVersion *string `json:"agent_version"`
	OS           *string `json:"os"`
	Arch         *string `json:"arch"`
	Kernel       *string `json:"kernel"`
	CreatedAt    int64   `json:"created_at"`
}

// EnrollToken 是 agent 接入令牌；库内只保存哈希。
type EnrollToken struct {
	ID        string  `json:"id"`
	TokenHash string  `json:"-"`
	Note      string  `json:"note"`
	ExpiresAt int64   `json:"expires_at"`
	UsedAt    *int64  `json:"used_at"`
	ServerID  *string `json:"server_id"`
}

// MetricSample 是一条指标采样，主键为 (server_id, metric, ts)。
type MetricSample struct {
	ServerID string  `json:"server_id"`
	Metric   string  `json:"metric"`
	TS       int64   `json:"ts"`
	Value    float64 `json:"value"`
}

// Event 是控制端记录的事件（上线/离线等）。
type Event struct {
	ID       string  `json:"id"`
	ServerID *string `json:"server_id"`
	Kind     string  `json:"kind"`
	Message  string  `json:"message"`
	TS       int64   `json:"ts"`
}

// 事件类型。
const (
	EventKindOnline  = "online"
	EventKindOffline = "offline"
)
