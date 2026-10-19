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
	// M1 扩展列（契约第 11 节）。
	CredentialID  *string `json:"credential_id"`
	Group         *string `json:"group"`
	HostKeyPolicy string  `json:"host_key_policy"`
	LastError     *string `json:"last_error"`
}

// 服务器接入模式。
const (
	// ModeAgent 表示由节点上的 leadstack-agent 主动上报。
	ModeAgent = "agent"
	// ModeSSH 表示由控制端通过 SSH 主动采集（agentless）。
	ModeSSH = "ssh"
)

// 主机密钥校验策略，取值与 internal/sshx.HostKeyPolicy 一致。
const (
	HostKeyPolicyInsecure  = "insecure"
	HostKeyPolicyAcceptNew = "accept-new"
	HostKeyPolicyStrict    = "strict"
)

// Credential 是一条 SSH 凭证；库内只存密文，明文永不落库。
type Credential struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	Username    string  `json:"username"`
	SecretEnc   []byte  `json:"-"`
	Fingerprint *string `json:"fingerprint"`
	CreatedAt   int64   `json:"created_at"`
}

// CredentialSummary 是凭证列表项，附带引用它的服务器数量。
type CredentialSummary struct {
	Credential
	ServerCount int `json:"server_count"`
}

// HostInfoSnapshot 是主机信息的落库形态（InfoJSON 为 hostinfo.Info 的 JSON）。
type HostInfoSnapshot struct {
	ServerID    string `json:"server_id"`
	InfoJSON    string `json:"info_json"`
	CollectedAt int64  `json:"collected_at"`
}

// RollupPoint 是一条降采样汇总。
type RollupPoint struct {
	ServerID string  `json:"server_id"`
	Metric   string  `json:"metric"`
	Bucket   string  `json:"bucket"`
	TS       int64   `json:"ts"`
	Avg      float64 `json:"avg"`
	Min      float64 `json:"min"`
	Max      float64 `json:"max"`
	Cnt      int64   `json:"cnt"`
}

// GroupCount 是分组的聚合结果。
type GroupCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
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
