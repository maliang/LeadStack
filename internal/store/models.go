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
	// 纳管 扩展列（契约第 11 节）。
	CredentialID *string `json:"credential_id"`
	// PanelCredentialID 是宝塔面板凭据（kind=baota），与 SSH 凭据相互独立。
	PanelCredentialID *string `json:"panel_credential_id"`
	Group             *string `json:"group"`
	HostKeyPolicy     string  `json:"host_key_policy"`
	LastError         *string `json:"last_error"`
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

// ------------------------------------------------ 检测：检测 ------------------------------------------------

// Check 是一个检测项定义（契约第 16 节 checks 表）。
type Check struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Target     string `json:"target"`
	IntervalS  int    `json:"interval_s"`
	TimeoutS   int    `json:"timeout_s"`
	ParamsJSON string `json:"-"`
	Enabled    bool   `json:"enabled"`
	CreatedAt  int64  `json:"created_at"`
}

// CheckResult 是一条检测结果。
type CheckResult struct {
	ID         int64   `json:"id"`
	CheckID    string  `json:"check_id"`
	ServerID   string  `json:"server_id"`
	TS         int64   `json:"ts"`
	OK         bool    `json:"ok"`
	LatencyMS  int64   `json:"latency_ms"`
	StatusCode *int    `json:"status_code,omitempty"`
	Message    *string `json:"message,omitempty"`
}

// CheckState 是一个 (检测项, 服务器) 的当前状态（连续失败计数供 告警 告警使用）。
type CheckState struct {
	CheckID         string  `json:"check_id"`
	ServerID        string  `json:"server_id"`
	OK              bool    `json:"ok"`
	ConsecutiveFail int     `json:"consecutive_fail"`
	Since           int64   `json:"since"`
	LastTS          int64   `json:"last_ts"`
	LastMessage     *string `json:"last_message,omitempty"`
}

// AvailabilityStat 是可用率聚合（up 次数 / 总次数）。
type AvailabilityStat struct {
	Total int `json:"total"`
	Up    int `json:"up"`
}

// Ratio 返回可用率（0-1）；没有样本时返回 0。
func (a AvailabilityStat) Ratio() float64 {
	if a.Total <= 0 {
		return 0
	}
	return float64(a.Up) / float64(a.Total)
}

// 检测项状态。
const (
	CheckStatusUp      = "up"
	CheckStatusDown    = "down"
	CheckStatusUnknown = "unknown"
)

// DefaultFailThreshold 是连续失败多少次判 down（可由 params.fail_threshold 覆盖）。
const DefaultFailThreshold = 3

// ------------------------------------------------ 告警：告警 ------------------------------------------------

// AlertRule 是一条告警规则（契约第 21 节 alert_rules 表）。
//
// check_ids/server_ids/groups/tags/channel_ids 在库里是 JSON 文本，模型里直接暴露为切片。
type AlertRule struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Enabled      bool     `json:"enabled"`
	Source       string   `json:"source"`
	CheckIDs     []string `json:"check_ids"`
	ServerIDs    []string `json:"server_ids"`
	Groups       []string `json:"groups"`
	Tags         []string `json:"tags"`
	Metric       *string  `json:"metric"`
	Comparator   *string  `json:"comparator"`
	Threshold    *float64 `json:"threshold"`
	ForDurationS int      `json:"for_duration_s"`
	Severity     string   `json:"severity"`
	DedupWindowS int      `json:"dedup_window_s"`
	ChannelIDs   []string `json:"channel_ids"`
	SilenceUntil *int64   `json:"silence_until"`
	CreatedAt    int64    `json:"created_at"`
}

// AlertChannel 是一个通知渠道；ConfigEnc 为 credential.Cipher 加密后的配置 JSON。
type AlertChannel struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	ConfigEnc []byte  `json:"-"`
	Enabled   bool    `json:"enabled"`
	LastError *string `json:"last_error"`
	LastOKAt  *int64  `json:"last_ok_at"`
	CreatedAt int64   `json:"created_at"`
}

// Alert 是一条告警。
type Alert struct {
	ID           string   `json:"id"`
	RuleID       string   `json:"rule_id"`
	DedupKey     string   `json:"dedup_key"`
	ServerID     *string  `json:"server_id"`
	CheckID      *string  `json:"check_id"`
	Status       string   `json:"status"`
	Severity     string   `json:"severity"`
	Title        string   `json:"title"`
	Message      *string  `json:"message"`
	Value        *float64 `json:"value"`
	StartedAt    int64    `json:"started_at"`
	UpdatedAt    int64    `json:"updated_at"`
	ResolvedAt   *int64   `json:"resolved_at"`
	AckedAt      *int64   `json:"acked_at"`
	AckedBy      *string  `json:"acked_by"`
	NotifyCount  int      `json:"notify_count"`
	LastNotifyAt *int64   `json:"last_notify_at"`
	SilenceUntil *int64   `json:"silence_until"`
}

// AlertEvent 是告警的状态变更轨迹与通知记录。
type AlertEvent struct {
	ID      int64   `json:"id"`
	AlertID string  `json:"alert_id"`
	Kind    string  `json:"kind"`
	TS      int64   `json:"ts"`
	Detail  *string `json:"detail"`
}

// AlertSummary 是告警概览。
type AlertSummary struct {
	Firing      int            `json:"firing"`
	Acked       int            `json:"acked"`
	Resolved24h int            `json:"resolved_24h"`
	BySeverity  map[string]int `json:"by_severity"`
}

// 告警状态与事件类型。
const (
	AlertStatusFiring   = "firing"
	AlertStatusAcked    = "acked"
	AlertStatusResolved = "resolved"

	AlertEventFiring   = "firing"
	AlertEventResolved = "resolved"
	AlertEventAcked    = "acked"
	AlertEventNotify   = "notify"
	AlertEventSilenced = "silenced"
)

// ------------------------------------------------ 部署：批量部署 ------------------------------------------------

// App 是一个被部署的业务应用。
type App struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Root        *string `json:"root"`
	Description *string `json:"description"`
	CreatedAt   int64   `json:"created_at"`
	// 部署方式与宝塔面板站点配置（provider=baota 时生效）。
	Provider        string  `json:"provider"`
	SiteName        *string `json:"site_name"`
	SitePort        *int    `json:"site_port"`
	SiteType        *string `json:"site_type"`
	HealthcheckPath *string `json:"healthcheck_path"`
}

// DeployTemplate 是部署模板（版本化文本，发布时冻结快照）。
type DeployTemplate struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// Artifact 是要部署的制品。
type Artifact struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Location  string  `json:"location"`
	SHA256    *string `json:"sha256"`
	SizeBytes int64   `json:"size_bytes"`
	CreatedAt int64   `json:"created_at"`
}

// Release 是一次发布的记录。
type Release struct {
	ID               string  `json:"id"`
	AppID            string  `json:"app_id"`
	TemplateID       string  `json:"template_id"`
	Version          string  `json:"version"`
	ArtifactID       *string `json:"artifact_id"`
	TemplateSnapshot string  `json:"template_snapshot"`
	Status           string  `json:"status"`
	CreatedBy        *string `json:"created_by"`
	CreatedAt        int64   `json:"created_at"`
	StartedAt        *int64  `json:"started_at"`
	FinishedAt       *int64  `json:"finished_at"`
	Concurrency      int     `json:"concurrency"`
}

// ReleaseRun 是一次发布在某台服务器上的执行单元。
type ReleaseRun struct {
	ID            string  `json:"id"`
	ReleaseID     string  `json:"release_id"`
	ServerID      string  `json:"server_id"`
	Status        string  `json:"status"`
	ExitCode      *int    `json:"exit_code"`
	Message       *string `json:"message"`
	FailedStep    *string `json:"failed_step"`
	PrevReleaseID *string `json:"prev_release_id"`
	StartedAt     *int64  `json:"started_at"`
	FinishedAt    *int64  `json:"finished_at"`
}

// ReleaseLog 是发布日志的一条分块。
type ReleaseLog struct {
	ReleaseRunID string `json:"release_run_id"`
	Seq          int64  `json:"seq"`
	TS           int64  `json:"ts"`
	Stream       string `json:"stream"`
	Chunk        string `json:"chunk"`
}

// 发布与执行单元的状态。
const (
	ReleaseStatusPending  = "pending"
	ReleaseStatusRunning  = "running"
	ReleaseStatusSuccess  = "success"
	ReleaseStatusFailed   = "failed"
	ReleaseStatusCanceled = "canceled"
)

// 制品类型。
const (
	ArtifactKindURL    = "url"
	ArtifactKindUpload = "upload"
	ArtifactKindImage  = "image"
)

// 模板类型。
const (
	TemplateKindCompose = "compose"
	TemplateKindSystemd = "systemd"
	TemplateKindBinary  = "binary"
)
