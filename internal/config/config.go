// Package config 负责控制端的配置加载。
//
// 优先级：命令行 flag > 环境变量 > 内置默认值。
// 详见 docs/ARCHITECTURE.md 第 2 节：默认监听 127.0.0.1:8080，数据默认落在 ./data。
package config

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 环境变量名。命令行 flag 同名覆盖环境变量。
const (
	EnvListen        = "LEADSTACK_LISTEN"
	EnvDataDir       = "LEADSTACK_DATA_DIR"
	EnvAdminPassword = "LEADSTACK_ADMIN_PASSWORD"
	EnvLogLevel      = "LEADSTACK_LOG_LEVEL"
	// EnvMasterKey 直接提供 32 字节 hex 主密钥；设置后不再读取 master.key 文件。
	EnvMasterKey = "LEADSTACK_MASTER_KEY"
	// EnvSSHInterval 是 agentless（mode=ssh）采集间隔，支持 Go duration 字符串（如 30s、3s）。
	EnvSSHInterval = "LEADSTACK_SSH_INTERVAL"
	// EnvCheckConcurrency 是检测调度器的最大并发探测数。
	EnvCheckConcurrency = "LEADSTACK_CHECK_CONCURRENCY"
	// EnvCheckIntervalMin 是检测项 interval_s 的防呆下限，支持 Go duration 字符串。
	EnvCheckIntervalMin = "LEADSTACK_CHECK_INTERVAL_MIN"
	// EnvAlertEvalInterval 是告警指标评估与服务器状态巡检周期。
	EnvAlertEvalInterval = "LEADSTACK_ALERT_EVAL_INTERVAL"
	// EnvAlertRetentionDays 是已解除告警的保留天数。
	EnvAlertRetentionDays = "LEADSTACK_ALERT_RETENTION_DAYS"
	// EnvDeployConcurrency 是批量发布的最大并发数。
	EnvDeployConcurrency = "LEADSTACK_DEPLOY_CONCURRENCY"
	// EnvArtifactMaxMB 是上传制品的最大体积（MB）。
	EnvArtifactMaxMB = "LEADSTACK_ARTIFACT_MAX_MB"
	// EnvDeployRoot 是目标机上的部署根目录。
	EnvDeployRoot = "LEADSTACK_DEPLOY_ROOT"
	// EnvPublicURL 是控制端对目标机可达的基地址（upload 制品下载用）。
	EnvPublicURL = "LEADSTACK_PUBLIC_URL"
	// EnvBackupDir 是备份归档目录。
	EnvBackupDir = "LEADSTACK_BACKUP_DIR"
	// EnvBackupInterval 是定时备份周期（Go duration 字符串）。
	EnvBackupInterval = "LEADSTACK_BACKUP_INTERVAL"
	// EnvBackupRetention 是保留的备份份数。
	EnvBackupRetention = "LEADSTACK_BACKUP_RETENTION"
	// EnvBackupIncludeArtifacts 是备份是否包含上传制品。
	EnvBackupIncludeArtifacts = "LEADSTACK_BACKUP_INCLUDE_ARTIFACTS"
	// EnvBackupPassphrase 是备份加密口令（设置后不再读取 backup.pass）。
	EnvBackupPassphrase = "LEADSTACK_BACKUP_PASSPHRASE"
)

// 默认值。
const (
	DefaultListen     = "127.0.0.1:8080"
	DefaultDataDir    = "./data"
	DefaultDBFileName = "leadstack.db"
	DefaultLogLevel   = "info"
	// DefaultTokenTTL 是登录 JWT 的有效期。
	DefaultTokenTTL = 12 * time.Hour
	// DefaultShutdownTimeout 是优雅关闭的最长等待时间。
	DefaultShutdownTimeout = 10 * time.Second
	// DefaultSSHInterval 是 agentless 采集的默认间隔。
	DefaultSSHInterval = 30 * time.Second
	// DefaultSSHConcurrency 是 agentless 采集的最大并发数。
	DefaultSSHConcurrency = 8
	// DefaultCheckConcurrency 是检测调度的默认最大并发数。
	DefaultCheckConcurrency = 8
	// DefaultCheckIntervalMin 是检测项间隔的默认下限（契约第 14.1 节：interval_s 最小 10）。
	DefaultCheckIntervalMin = 10 * time.Second
	// DefaultAlertEvalInterval 是告警指标评估周期。
	DefaultAlertEvalInterval = 30 * time.Second
	// DefaultAlertRetentionDays 是已解除告警的默认保留天数。
	DefaultAlertRetentionDays = 90
	// DefaultDeployConcurrency 是批量发布的默认并发上限。
	DefaultDeployConcurrency = 4
	// DefaultArtifactMaxMB 是上传制品的默认体积上限（MB）。
	DefaultArtifactMaxMB = 512
	// DefaultDeployRoot 是目标机上的默认部署根目录。
	DefaultDeployRoot = "/opt/leadstack/apps"
	// ArtifactsDirName 是控制端存放上传制品的目录名（位于 DataDir 下）。
	ArtifactsDirName = "artifacts"
	// BackupsDirName 是备份归档的默认目录名（位于 DataDir 下）。
	BackupsDirName = "backups"
	// DefaultBackupInterval 是定时备份的默认周期。
	DefaultBackupInterval = 24 * time.Hour
	// DefaultBackupRetention 是默认保留份数。
	DefaultBackupRetention = 7
	// MasterKeyFileName 是主密钥文件名（位于 DataDir 下）。
	MasterKeyFileName = "master.key"
	// MasterKeySize 是主密钥字节数（AES-256）。
	MasterKeySize = 32
)

// Config 是控制端的运行配置。
type Config struct {
	// Listen 是 HTTP 监听地址，例如 127.0.0.1:8080。
	Listen string
	// DataDir 是数据目录，SQLite 文件放在其中。
	DataDir string
	// DBPath 是 SQLite 文件的绝对/相对路径（由 DataDir 推导）。
	DBPath string
	// AdminPassword 可选；设置时用于初始化管理员，不再随机生成。
	AdminPassword string
	// LogLevel 是 slog 的最低输出级别。
	LogLevel slog.Level
	// TokenTTL 是登录 JWT 的有效期。
	TokenTTL time.Duration
	// ShutdownTimeout 是优雅关闭的最长等待时间。
	ShutdownTimeout time.Duration
	// Version 是构建版本号，由 main 传入（-ldflags 注入）。
	Version string
	// MasterKey 是 32 字节主密钥；来自 LEADSTACK_MASTER_KEY 时为非空。
	// 该字段**绝不能**写入日志。
	MasterKey []byte
	// SSHInterval 是 agentless 采集间隔。
	SSHInterval time.Duration
	// SSHConcurrency 是 agentless 采集最大并发数。
	SSHConcurrency int
	// CheckConcurrency 是检测调度最大并发数。
	CheckConcurrency int
	// CheckIntervalMin 是检测项 interval_s 的防呆下限。
	CheckIntervalMin time.Duration
	// AlertEvalInterval 是告警指标评估与服务器状态巡检周期。
	AlertEvalInterval time.Duration
	// AlertRetentionDays 是已解除告警的保留天数。
	AlertRetentionDays int
	// DeployConcurrency 是批量发布的最大并发数。
	DeployConcurrency int
	// ArtifactMaxMB 是上传制品的最大体积（MB）。
	ArtifactMaxMB int
	// DeployRoot 是目标机上的部署根目录。
	DeployRoot string
	// PublicURL 是控制端对目标机可达的基地址；upload 制品部署时需要。
	PublicURL string
	// BackupDir 是备份归档目录（默认 <data_dir>/backups）。
	BackupDir string
	// BackupInterval 是定时备份周期。
	BackupInterval time.Duration
	// BackupRetention 是保留的备份份数。
	BackupRetention int
	// BackupIncludeArtifacts 表示备份是否默认包含上传制品。
	BackupIncludeArtifacts bool
	// BackupPassphrase 是备份加密口令（可选；空则读 backup.pass 或自动生成）。
	BackupPassphrase string
}

// Load 解析配置。version 由调用方（main）传入，args 通常为 os.Args[1:]。
func Load(version string, args []string) (*Config, error) {
	fs := flag.NewFlagSet("leadstack", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	listen := fs.String("listen", envOr(EnvListen, DefaultListen), "HTTP 监听地址（环境变量 "+EnvListen+"）")
	dataDir := fs.String("data-dir", envOr(EnvDataDir, DefaultDataDir), "数据目录（环境变量 "+EnvDataDir+"）")
	adminPassword := fs.String("admin-password", os.Getenv(EnvAdminPassword), "初始管理员密码；留空则随机生成（环境变量 "+EnvAdminPassword+"）")
	logLevel := fs.String("log-level", envOr(EnvLogLevel, DefaultLogLevel), "日志级别：debug|info|warn|error")
	tokenTTL := fs.Duration("token-ttl", DefaultTokenTTL, "登录令牌有效期")
	shutdownTimeout := fs.Duration("shutdown-timeout", DefaultShutdownTimeout, "优雅关闭最长等待时间")
	masterKeyHex := fs.String("master-key", os.Getenv(EnvMasterKey), "32 字节 hex 主密钥；留空则读取 <data-dir>/master.key（环境变量 "+EnvMasterKey+"）")
	sshInterval := fs.Duration("ssh-interval", defaultSSHInterval(), "agentless 采集间隔，如 30s（环境变量 "+EnvSSHInterval+"）")
	sshConcurrency := fs.Int("ssh-concurrency", DefaultSSHConcurrency, "agentless 采集最大并发数")
	checkConcurrency := fs.Int("check-concurrency", envIntOr(EnvCheckConcurrency, DefaultCheckConcurrency), "检测调度最大并发数（环境变量 "+EnvCheckConcurrency+"）")
	checkIntervalMin := fs.Duration("check-interval-min", defaultCheckIntervalMin(), "检测项 interval_s 的下限，如 10s（环境变量 "+EnvCheckIntervalMin+"）")
	alertEvalInterval := fs.Duration("alert-eval-interval", defaultDurationOr(EnvAlertEvalInterval, DefaultAlertEvalInterval), "告警指标评估周期，如 30s（环境变量 "+EnvAlertEvalInterval+"）")
	alertRetentionDays := fs.Int("alert-retention-days", envIntOr(EnvAlertRetentionDays, DefaultAlertRetentionDays), "已解除告警保留天数（环境变量 "+EnvAlertRetentionDays+"）")
	deployConcurrency := fs.Int("deploy-concurrency", envIntOr(EnvDeployConcurrency, DefaultDeployConcurrency), "批量发布并发上限（环境变量 "+EnvDeployConcurrency+"）")
	artifactMaxMB := fs.Int("artifact-max-mb", envIntOr(EnvArtifactMaxMB, DefaultArtifactMaxMB), "上传制品体积上限 MB（环境变量 "+EnvArtifactMaxMB+"）")
	deployRoot := fs.String("deploy-root", envOr(EnvDeployRoot, DefaultDeployRoot), "目标机部署根目录（环境变量 "+EnvDeployRoot+"）")
	publicURL := fs.String("public-url", strings.TrimSpace(os.Getenv(EnvPublicURL)), "控制端可达基地址，如 http://10.0.0.1:8080（环境变量 "+EnvPublicURL+"）")
	backupDir := fs.String("backup-dir", envOr(EnvBackupDir, ""), "备份归档目录，默认 <data-dir>/backups（环境变量 "+EnvBackupDir+"）")
	backupInterval := fs.Duration("backup-interval", defaultDurationOr(EnvBackupInterval, DefaultBackupInterval), "定时备份周期，如 24h（环境变量 "+EnvBackupInterval+"）")
	backupRetention := fs.Int("backup-retention", envIntOr(EnvBackupRetention, DefaultBackupRetention), "保留备份份数（环境变量 "+EnvBackupRetention+"）")
	backupIncludeArtifacts := fs.Bool("backup-include-artifacts", envBoolOr(EnvBackupIncludeArtifacts, false), "备份是否包含上传制品（环境变量 "+EnvBackupIncludeArtifacts+"）")
	backupPassphrase := fs.String("backup-passphrase", os.Getenv(EnvBackupPassphrase), "备份加密口令；留空则读 <data-dir>/backup.pass 或自动生成（环境变量 "+EnvBackupPassphrase+"）")
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("解析命令行参数: %w", err)
	}

	cfg := &Config{
		Listen:                 strings.TrimSpace(*listen),
		DataDir:                strings.TrimSpace(*dataDir),
		AdminPassword:          *adminPassword,
		TokenTTL:               *tokenTTL,
		ShutdownTimeout:        *shutdownTimeout,
		Version:                version,
		SSHInterval:            *sshInterval,
		SSHConcurrency:         *sshConcurrency,
		CheckConcurrency:       *checkConcurrency,
		CheckIntervalMin:       *checkIntervalMin,
		AlertEvalInterval:      *alertEvalInterval,
		AlertRetentionDays:     *alertRetentionDays,
		DeployConcurrency:      *deployConcurrency,
		ArtifactMaxMB:          *artifactMaxMB,
		DeployRoot:             strings.TrimSpace(*deployRoot),
		PublicURL:              strings.TrimSuffix(strings.TrimSpace(*publicURL), "/"),
		BackupDir:              strings.TrimSpace(*backupDir),
		BackupInterval:         *backupInterval,
		BackupRetention:        *backupRetention,
		BackupIncludeArtifacts: *backupIncludeArtifacts,
		BackupPassphrase:       *backupPassphrase,
	}

	if cfg.Listen == "" {
		return nil, errors.New("配置错误: 监听地址不能为空")
	}
	if cfg.DataDir == "" {
		return nil, errors.New("配置错误: 数据目录不能为空")
	}
	if cfg.TokenTTL <= 0 {
		return nil, errors.New("配置错误: token-ttl 必须为正数")
	}
	if cfg.ShutdownTimeout <= 0 {
		return nil, errors.New("配置错误: shutdown-timeout 必须为正数")
	}

	if cfg.SSHInterval < time.Second || cfg.SSHInterval > 24*time.Hour {
		return nil, errors.New("配置错误: ssh-interval 必须在 1s-24h 之间")
	}
	if cfg.SSHConcurrency < 1 || cfg.SSHConcurrency > 64 {
		return nil, errors.New("配置错误: ssh-concurrency 必须在 1-64 之间")
	}
	if cfg.CheckConcurrency < 1 || cfg.CheckConcurrency > 64 {
		return nil, errors.New("配置错误: check-concurrency 必须在 1-64 之间")
	}
	if cfg.CheckIntervalMin < time.Second || cfg.CheckIntervalMin > 24*time.Hour {
		return nil, errors.New("配置错误: check-interval-min 必须在 1s-24h 之间")
	}
	if cfg.AlertEvalInterval < time.Second || cfg.AlertEvalInterval > 24*time.Hour {
		return nil, errors.New("配置错误: alert-eval-interval 必须在 1s-24h 之间")
	}
	if cfg.AlertRetentionDays < 1 || cfg.AlertRetentionDays > 3650 {
		return nil, errors.New("配置错误: alert-retention-days 必须在 1-3650 之间")
	}
	if cfg.DeployConcurrency < 1 || cfg.DeployConcurrency > 64 {
		return nil, errors.New("配置错误: deploy-concurrency 必须在 1-64 之间")
	}
	if cfg.ArtifactMaxMB < 1 || cfg.ArtifactMaxMB > 10240 {
		return nil, errors.New("配置错误: artifact-max-mb 必须在 1-10240 之间")
	}
	if cfg.DeployRoot == "" || !strings.HasPrefix(cfg.DeployRoot, "/") {
		return nil, errors.New("配置错误: deploy-root 必须是非空的绝对路径")
	}
	if cfg.PublicURL != "" && !strings.HasPrefix(cfg.PublicURL, "http://") && !strings.HasPrefix(cfg.PublicURL, "https://") {
		return nil, errors.New("配置错误: public-url 必须以 http:// 或 https:// 开头")
	}
	if cfg.BackupInterval < time.Second || cfg.BackupInterval > 365*24*time.Hour {
		return nil, errors.New("配置错误: backup-interval 必须在 1s-8760h 之间")
	}
	if cfg.BackupRetention < 1 || cfg.BackupRetention > 1000 {
		return nil, errors.New("配置错误: backup-retention 必须在 1-1000 之间")
	}

	if raw := strings.TrimSpace(*masterKeyHex); raw != "" {
		key, err := DecodeMasterKey(raw)
		if err != nil {
			return nil, err
		}
		cfg.MasterKey = key
	}

	absDataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("解析数据目录 %q: %w", cfg.DataDir, err)
	}
	cfg.DataDir = absDataDir
	cfg.DBPath = filepath.Join(absDataDir, DefaultDBFileName)
	if cfg.BackupDir == "" {
		cfg.BackupDir = filepath.Join(absDataDir, BackupsDirName)
	} else {
		absBackupDir, berr := filepath.Abs(cfg.BackupDir)
		if berr != nil {
			return nil, fmt.Errorf("解析备份目录 %q: %w", cfg.BackupDir, berr)
		}
		cfg.BackupDir = absBackupDir
	}

	lvl, err := ParseLogLevel(*logLevel)
	if err != nil {
		return nil, err
	}
	cfg.LogLevel = lvl

	return cfg, nil
}

// ParseLogLevel 把字符串转换为 slog.Level。
func ParseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("配置错误: 未知日志级别 %q（可选 debug|info|warn|error）", s)
	}
}

// EnsureDataDir 创建数据目录（含所有父目录）。
func (c *Config) EnsureDataDir() error {
	if err := os.MkdirAll(c.DataDir, 0o750); err != nil {
		return fmt.Errorf("创建数据目录 %s: %w", c.DataDir, err)
	}
	return nil
}

// envOr 返回环境变量的值，未设置或为空白时返回 fallback。
func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// BackupPassFile 返回备份口令文件路径（<data-dir>/backup.pass）。
func (c *Config) BackupPassFile() string {
	return filepath.Join(c.DataDir, "backup.pass")
}

// RestorePendingDir 返回「暂存恢复」目录（<data-dir>/.restore-pending）。
func (c *Config) RestorePendingDir() string {
	return filepath.Join(c.DataDir, ".restore-pending")
}

// KnownHostsPath 返回 known_hosts 文件路径（<data-dir>/known_hosts）。
func (c *Config) KnownHostsPath() string {
	return filepath.Join(c.DataDir, "known_hosts")
}

// ArtifactsDir 返回上传制品的存放目录（<data-dir>/artifacts）。
func (c *Config) ArtifactsDir() string {
	return filepath.Join(c.DataDir, ArtifactsDirName)
}

// ArtifactMaxBytes 返回上传制品的字节上限。
func (c *Config) ArtifactMaxBytes() int64 {
	return int64(c.ArtifactMaxMB) << 20
}

// MasterKeyPath 返回主密钥文件路径（<data-dir>/master.key）。
func (c *Config) MasterKeyPath() string {
	return filepath.Join(c.DataDir, MasterKeyFileName)
}

// DecodeMasterKey 解析 32 字节 hex 主密钥；允许可选的 0x 前缀与空白分隔。
func DecodeMasterKey(raw string) ([]byte, error) {
	cleaned := strings.TrimSpace(raw)
	cleaned = strings.TrimPrefix(cleaned, "0x")
	cleaned = strings.TrimPrefix(cleaned, "0X")
	cleaned = strings.NewReplacer(" ", "", ":", "", "-", "", "\n", "").Replace(cleaned)

	key, err := hex.DecodeString(cleaned)
	if err != nil {
		return nil, fmt.Errorf("配置错误: %s 不是合法的 hex 字符串: %w", EnvMasterKey, err)
	}
	if len(key) != MasterKeySize {
		return nil, fmt.Errorf("配置错误: %s 必须是 %d 字节（%d 个 hex 字符），实际 %d 字节",
			EnvMasterKey, MasterKeySize, MasterKeySize*2, len(key))
	}
	return key, nil
}

// defaultSSHInterval 解析环境变量里的采集间隔。
//
// 契约要求支持 Go duration 字符串（如 "3s"）；为容错也接受纯数字（按秒解释）。
// 解析失败时返回默认值，真正的报错交给 Load 的校验环节。
func defaultSSHInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv(EnvSSHInterval))
	if raw == "" {
		return DefaultSSHInterval
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		return time.Duration(secs) * time.Second
	}
	return DefaultSSHInterval
}

// defaultCheckIntervalMin 解析检测间隔下限；支持 Go duration 与纯数字（按秒）。
func defaultCheckIntervalMin() time.Duration {
	raw := strings.TrimSpace(os.Getenv(EnvCheckIntervalMin))
	if raw == "" {
		return DefaultCheckIntervalMin
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		return time.Duration(secs) * time.Second
	}
	return DefaultCheckIntervalMin
}

// envIntOr 读取整数环境变量，非法或缺失时返回 fallback。
func envIntOr(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

// defaultDurationOr 解析 duration 环境变量（支持 Go duration 与纯数字秒），非法或缺失时用 fallback。
func defaultDurationOr(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		return time.Duration(secs) * time.Second
	}
	return fallback
}

// envBoolOr 读取布尔环境变量（1/true/yes/on 为真），非法或缺失时返回 fallback。
func envBoolOr(key string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch raw {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
