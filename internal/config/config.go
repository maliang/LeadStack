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
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("解析命令行参数: %w", err)
	}

	cfg := &Config{
		Listen:           strings.TrimSpace(*listen),
		DataDir:          strings.TrimSpace(*dataDir),
		AdminPassword:    *adminPassword,
		TokenTTL:         *tokenTTL,
		ShutdownTimeout:  *shutdownTimeout,
		Version:          version,
		SSHInterval:      *sshInterval,
		SSHConcurrency:   *sshConcurrency,
		CheckConcurrency: *checkConcurrency,
		CheckIntervalMin: *checkIntervalMin,
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
