package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoadDefaults 校验默认配置。
func TestLoadDefaults(t *testing.T) {
	t.Setenv(EnvListen, "")
	t.Setenv(EnvDataDir, "")
	t.Setenv(EnvAdminPassword, "")
	t.Setenv(EnvLogLevel, "")

	cfg, err := Load("1.2.3", nil)
	if err != nil {
		t.Fatalf("加载默认配置失败: %v", err)
	}
	if cfg.Listen != DefaultListen {
		t.Fatalf("默认监听期望 %s，实际 %s", DefaultListen, cfg.Listen)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		t.Fatalf("数据目录应被解析为绝对路径，实际 %s", cfg.DataDir)
	}
	if filepath.Base(cfg.DBPath) != DefaultDBFileName {
		t.Fatalf("默认数据库文件名期望 %s，实际 %s", DefaultDBFileName, cfg.DBPath)
	}
	if cfg.Version != "1.2.3" {
		t.Fatalf("版本期望 1.2.3，实际 %s", cfg.Version)
	}
	if cfg.LogLevel != slog.LevelInfo || cfg.TokenTTL != DefaultTokenTTL {
		t.Fatalf("默认日志级别/令牌有效期不符: level=%v ttl=%v", cfg.LogLevel, cfg.TokenTTL)
	}
	if cfg.ShutdownTimeout != DefaultShutdownTimeout {
		t.Fatalf("默认优雅关闭超时不符: %v", cfg.ShutdownTimeout)
	}
}

// TestLoadEnvAndFlagPrecedence 校验 环境变量 > 默认值，flag > 环境变量。
func TestLoadEnvAndFlagPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvListen, "0.0.0.0:9999")
	t.Setenv(EnvDataDir, dir)
	t.Setenv(EnvAdminPassword, "from-env")
	t.Setenv(EnvLogLevel, "debug")

	cfg, err := Load("dev", []string{"-listen", "127.0.0.1:1234", "-token-ttl", "30m"})
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.Listen != "127.0.0.1:1234" {
		t.Fatalf("flag 应覆盖环境变量，实际 %s", cfg.Listen)
	}
	if cfg.DataDir != dir {
		t.Fatalf("数据目录期望 %s，实际 %s", dir, cfg.DataDir)
	}
	if cfg.AdminPassword != "from-env" {
		t.Fatalf("管理员密码应来自环境变量，实际 %q", cfg.AdminPassword)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("日志级别期望 debug，实际 %v", cfg.LogLevel)
	}
	if cfg.TokenTTL != 30*time.Minute {
		t.Fatalf("令牌有效期期望 30m，实际 %v", cfg.TokenTTL)
	}
}

// TestLoadRejectsInvalid 校验非法配置被拒绝。
func TestLoadRejectsInvalid(t *testing.T) {
	t.Setenv(EnvListen, "")
	t.Setenv(EnvDataDir, "")
	t.Setenv(EnvLogLevel, "verbose")
	if _, err := Load("dev", nil); err == nil {
		t.Fatal("非法日志级别应报错")
	}
	t.Setenv(EnvLogLevel, "")
	if _, err := Load("dev", []string{"-listen", ""}); err == nil {
		t.Fatal("空监听地址应报错")
	}
	if _, err := Load("dev", []string{"-unknown-flag"}); err == nil {
		t.Fatal("未知 flag 应报错")
	}
	if _, err := Load("dev", []string{"-token-ttl", "0s"}); err == nil {
		t.Fatal("非正数 token-ttl 应报错")
	}
}

// TestEnsureDataDir 校验数据目录会被创建。
func TestEnsureDataDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv(EnvListen, "")
	t.Setenv(EnvDataDir, "")
	cfg, err := Load("dev", []string{"-data-dir", filepath.Join(base, "nested", "data")})
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if err := cfg.EnsureDataDir(); err != nil {
		t.Fatalf("创建数据目录失败: %v", err)
	}
	info, err := os.Stat(cfg.DataDir)
	if err != nil {
		t.Fatalf("数据目录不存在: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("数据目录不是目录")
	}
	if !strings.HasPrefix(cfg.DBPath, cfg.DataDir) {
		t.Fatalf("数据库路径应在数据目录内: %s", cfg.DBPath)
	}
}
