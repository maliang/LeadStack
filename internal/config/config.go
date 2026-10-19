// Package config 负责控制端的配置加载。
//
// 优先级：命令行 flag > 环境变量 > 内置默认值。
// 详见 docs/ARCHITECTURE.md 第 2 节：默认监听 127.0.0.1:8080，数据默认落在 ./data。
package config

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 环境变量名。命令行 flag 同名覆盖环境变量。
const (
	EnvListen        = "LEADSTACK_LISTEN"
	EnvDataDir       = "LEADSTACK_DATA_DIR"
	EnvAdminPassword = "LEADSTACK_ADMIN_PASSWORD"
	EnvLogLevel      = "LEADSTACK_LOG_LEVEL"
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
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("解析命令行参数: %w", err)
	}

	cfg := &Config{
		Listen:          strings.TrimSpace(*listen),
		DataDir:         strings.TrimSpace(*dataDir),
		AdminPassword:   *adminPassword,
		TokenTTL:        *tokenTTL,
		ShutdownTimeout: *shutdownTimeout,
		Version:         version,
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
