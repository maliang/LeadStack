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

// ------------------------------------------------ M1 ------------------------------------------------

// TestMasterKeyFromEnv 校验 LEADSTACK_MASTER_KEY 解析与校验。
func TestMasterKeyFromEnv(t *testing.T) {
	t.Setenv(EnvListen, "")
	t.Setenv(EnvDataDir, "")
	t.Setenv(EnvAdminPassword, "")
	t.Setenv(EnvLogLevel, "")
	t.Setenv(EnvSSHInterval, "")

	hexKey := strings.Repeat("ab", 32) // 64 个 hex 字符 = 32 字节
	t.Setenv(EnvMasterKey, hexKey)

	cfg, err := Load("dev", nil)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if len(cfg.MasterKey) != MasterKeySize {
		t.Fatalf("主密钥期望 %d 字节，实际 %d", MasterKeySize, len(cfg.MasterKey))
	}
	if cfg.MasterKey[0] != 0xab || cfg.MasterKey[31] != 0xab {
		t.Fatalf("主密钥解码不正确: %x", cfg.MasterKey)
	}

	// 长度不对 / 非 hex 都要报错
	t.Setenv(EnvMasterKey, "abcd")
	if _, err := Load("dev", nil); err == nil {
		t.Fatal("长度不足的主密钥应报错")
	}
	t.Setenv(EnvMasterKey, strings.Repeat("zz", 32))
	if _, err := Load("dev", nil); err == nil {
		t.Fatal("非 hex 主密钥应报错")
	}
	// 未设置则不读取文件（由 cmd 决定）
	t.Setenv(EnvMasterKey, "")
	cfg, err = Load("dev", nil)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if len(cfg.MasterKey) != 0 {
		t.Fatalf("未设置主密钥时应为空，实际 %d 字节", len(cfg.MasterKey))
	}
}

// TestSSHIntervalParsing 校验 LEADSTACK_SSH_INTERVAL 支持 Go duration 字符串（验收脚本传 3s）。
func TestSSHIntervalParsing(t *testing.T) {
	t.Setenv(EnvListen, "")
	t.Setenv(EnvDataDir, "")
	t.Setenv(EnvLogLevel, "")
	t.Setenv(EnvAdminPassword, "")

	t.Setenv(EnvSSHInterval, "3s")
	cfg, err := Load("dev", nil)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.SSHInterval != 3*time.Second {
		t.Fatalf("ssh-interval 期望 3s，实际 %v", cfg.SSHInterval)
	}

	// 纯数字按秒解释
	t.Setenv(EnvSSHInterval, "45")
	cfg, err = Load("dev", nil)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.SSHInterval != 45*time.Second {
		t.Fatalf("ssh-interval 期望 45s，实际 %v", cfg.SSHInterval)
	}

	// 默认值
	t.Setenv(EnvSSHInterval, "")
	cfg, err = Load("dev", nil)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.SSHInterval != DefaultSSHInterval {
		t.Fatalf("默认 ssh-interval 期望 %v，实际 %v", DefaultSSHInterval, cfg.SSHInterval)
	}
	if cfg.SSHConcurrency != DefaultSSHConcurrency {
		t.Fatalf("默认 ssh-concurrency 期望 %d，实际 %d", DefaultSSHConcurrency, cfg.SSHConcurrency)
	}

	// 过小/过大都要报错
	if _, err := Load("dev", []string{"-ssh-interval", "10ms"}); err == nil {
		t.Fatal("过小的 ssh-interval 应报错")
	}
	if _, err := Load("dev", []string{"-ssh-concurrency", "0"}); err == nil {
		t.Fatal("非法 ssh-concurrency 应报错")
	}
}

// TestMasterKeyPath 校验主密钥文件路径。
func TestMasterKeyPath(t *testing.T) {
	t.Setenv(EnvListen, "")
	t.Setenv(EnvDataDir, "")
	t.Setenv(EnvLogLevel, "")
	t.Setenv(EnvSSHInterval, "")

	dir := t.TempDir()
	cfg, err := Load("dev", []string{"-data-dir", dir})
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	want := filepath.Join(dir, MasterKeyFileName)
	if cfg.MasterKeyPath() != want {
		t.Fatalf("主密钥路径期望 %s，实际 %s", want, cfg.MasterKeyPath())
	}
}

// TestDecodeMasterKey 覆盖主密钥解码的容错形式。
func TestDecodeMasterKey(t *testing.T) {
	raw := strings.Repeat("cd", 32)
	key, err := DecodeMasterKey("0x" + raw)
	if err != nil {
		t.Fatalf("带前缀的密钥解码失败: %v", err)
	}
	if len(key) != MasterKeySize {
		t.Fatalf("期望 %d 字节，实际 %d", MasterKeySize, len(key))
	}
	if _, err := DecodeMasterKey(""); err == nil {
		t.Fatal("空密钥应报错")
	}
}

// ------------------------------------------------ M2 ------------------------------------------------

// TestCheckConfigDefaults 校验 M2 检测相关配置的默认值与解析。
func TestCheckConfigDefaults(t *testing.T) {
	t.Setenv(EnvListen, "")
	t.Setenv(EnvDataDir, "")
	t.Setenv(EnvAdminPassword, "")
	t.Setenv(EnvLogLevel, "")
	t.Setenv(EnvSSHInterval, "")
	t.Setenv(EnvMasterKey, "")
	t.Setenv(EnvCheckConcurrency, "")
	t.Setenv(EnvCheckIntervalMin, "")

	cfg, err := Load("dev", nil)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.CheckConcurrency != DefaultCheckConcurrency {
		t.Fatalf("默认 check-concurrency 期望 %d，实际 %d", DefaultCheckConcurrency, cfg.CheckConcurrency)
	}
	if cfg.CheckIntervalMin != DefaultCheckIntervalMin {
		t.Fatalf("默认 check-interval-min 期望 %v，实际 %v", DefaultCheckIntervalMin, cfg.CheckIntervalMin)
	}
}

// TestCheckConfigEnv 校验 LEADSTACK_CHECK_* 环境变量（验收脚本传 10s）。
func TestCheckConfigEnv(t *testing.T) {
	t.Setenv(EnvListen, "")
	t.Setenv(EnvDataDir, "")
	t.Setenv(EnvLogLevel, "")
	t.Setenv(EnvSSHInterval, "")
	t.Setenv(EnvMasterKey, "")

	t.Setenv(EnvCheckConcurrency, "3")
	t.Setenv(EnvCheckIntervalMin, "10s")
	cfg, err := Load("dev", nil)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.CheckConcurrency != 3 {
		t.Fatalf("check-concurrency 期望 3，实际 %d", cfg.CheckConcurrency)
	}
	if cfg.CheckIntervalMin != 10*time.Second {
		t.Fatalf("check-interval-min 期望 10s，实际 %v", cfg.CheckIntervalMin)
	}

	// 纯数字按秒解释
	t.Setenv(EnvCheckIntervalMin, "15")
	cfg, err = Load("dev", nil)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.CheckIntervalMin != 15*time.Second {
		t.Fatalf("check-interval-min 期望 15s，实际 %v", cfg.CheckIntervalMin)
	}

	// flag 覆盖环境变量
	cfg, err = Load("dev", []string{"-check-interval-min", "20s", "-check-concurrency", "5"})
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.CheckIntervalMin != 20*time.Second || cfg.CheckConcurrency != 5 {
		t.Fatalf("flag 未覆盖环境变量: %v / %d", cfg.CheckIntervalMin, cfg.CheckConcurrency)
	}

	// 非法值
	if _, err := Load("dev", []string{"-check-concurrency", "0"}); err == nil {
		t.Fatal("check-concurrency=0 应报错")
	}
	if _, err := Load("dev", []string{"-check-interval-min", "100ms"}); err == nil {
		t.Fatal("过小的 check-interval-min 应报错")
	}
}
