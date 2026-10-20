package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/maliang/leadstack/internal/backup"
	"github.com/maliang/leadstack/internal/config"
	"github.com/maliang/leadstack/internal/store"
)

// prepareDataDir 造一个「真实可用」的数据目录：有效的 SQLite 库 + known_hosts + master.key。
func prepareDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	st, err := store.Open(context.Background(), filepath.Join(dir, "leadstack.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	// 写一条可识别的数据，用于验证恢复后内容回滚。
	srv := &store.Server{
		ID: "srv-backup-test", Name: "backup-marker", Host: "10.0.0.1",
		Port: 22, Mode: "agent", TagsJSON: "[]", Status: "unknown", CreatedAt: store.NowMS(),
	}
	if err := st.CreateServer(context.Background(), srv); err != nil {
		t.Fatalf("写入标记服务器失败: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("关闭测试数据库失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "known_hosts"), []byte("example.com ssh-ed25519 AAAA\n"), 0o600); err != nil {
		t.Fatalf("写入 known_hosts 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "master.key"), make([]byte, 32), 0o600); err != nil {
		t.Fatalf("写入 master.key 失败: %v", err)
	}
	return dir
}

// TestCLIBackupCreateAndVerify 覆盖 `backup --out` 与 `backup verify`。
func TestCLIBackupCreateAndVerify(t *testing.T) {
	dir := prepareDataDir(t)
	out := filepath.Join(t.TempDir(), "manual.lsbak")

	if err := runBackupCommand([]string{"--data-dir", dir, "--out", out}); err != nil {
		t.Fatalf("CLI 备份失败: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("读取备份文件失败: %v", err)
	}
	if !strings.HasPrefix(string(raw), "LSBAK1") {
		t.Fatalf("归档应以 LSBAK1 开头: %q", string(raw[:min(8, len(raw))]))
	}
	// 归档必须加密：明文数据库头/私钥头都不应出现
	if strings.Contains(string(raw), "SQLite format 3") {
		t.Fatal("归档中出现了明文 SQLite 头（未加密）")
	}
	// 口令文件应自动生成且不进归档
	if _, err := os.Stat(filepath.Join(dir, "backup.pass")); err != nil {
		t.Fatalf("应自动生成 backup.pass: %v", err)
	}

	// verify 子命令
	if err := runBackupCommand([]string{"verify", "--data-dir", dir, "--file", out}); err != nil {
		t.Fatalf("CLI 校验失败: %v", err)
	}
}

// TestCLIBackupWrongPassphrase 校验口令错误时提示可执行（输出/错误必须含「口令」）。
func TestCLIBackupWrongPassphrase(t *testing.T) {
	dir := prepareDataDir(t)
	out := filepath.Join(t.TempDir(), "pw.lsbak")

	// 用显式口令创建
	t.Setenv(config.EnvBackupPassphrase, "correct-passphrase-123")
	if err := runBackupCommand([]string{"--data-dir", dir, "--out", out}); err != nil {
		t.Fatalf("CLI 备份失败: %v", err)
	}

	// 用错误口令校验
	wrongFile := filepath.Join(t.TempDir(), "wrong.pass")
	if err := os.WriteFile(wrongFile, []byte("totally-wrong-passphrase"), 0o600); err != nil {
		t.Fatalf("写入口令文件失败: %v", err)
	}
	err := runBackupCommand([]string{"verify", "--data-dir", dir, "--file", out, "--passphrase-file", wrongFile})
	if err == nil {
		t.Fatal("错误口令应校验失败")
	}
	if !strings.Contains(err.Error(), "口令") {
		t.Fatalf("错误口令的提示必须含「口令」二字: %v", err)
	}
}

// TestRestoreCLI 覆盖 `restore` 子命令与数据目录占用检查。
func TestRestoreCLI(t *testing.T) {
	dir := prepareDataDir(t)
	out := filepath.Join(t.TempDir(), "restore-src.lsbak")

	// 先备份（含标记服务器）
	if err := runBackupCommand([]string{"--data-dir", dir, "--out", out}); err != nil {
		t.Fatalf("CLI 备份失败: %v", err)
	}

	// 占用检查：自己的 pid 一定存活 → 必须报错
	if err := writePIDFile(dir); err != nil {
		t.Fatalf("写入 pid 文件失败: %v", err)
	}
	if err := waitDataDirIdle(dir, 300*1e6); err == nil {
		t.Fatal("数据目录被占用时应报错")
	} else if !strings.Contains(err.Error(), "停止") {
		t.Fatalf("占用错误应提示先停止服务: %v", err)
	}
	if err := runRestoreCommand([]string{"--data-dir", dir, "--file", out}); err == nil {
		t.Fatal("服务运行中 restore 应被拒绝")
	}
	// 陈旧 pid（不存在的进程）→ 视为已停止
	if err := os.WriteFile(filepath.Join(dir, pidFileName), []byte("999999"), 0o600); err != nil {
		t.Fatalf("写入陈旧 pid 失败: %v", err)
	}
	if err := waitDataDirIdle(dir, 300*1e6); err != nil {
		t.Fatalf("陈旧 pid 应视为已停止: %v", err)
	}
	removePIDFile(dir)

	// 改动数据：删除标记服务器（模拟误删）
	st, err := store.Open(context.Background(), filepath.Join(dir, "leadstack.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := st.DeleteServer(context.Background(), "srv-backup-test"); err != nil {
		t.Fatalf("删除标记服务器失败: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	// 执行恢复
	if err := runRestoreCommand([]string{"--data-dir", dir, "--file", out}); err != nil {
		t.Fatalf("CLI 恢复失败: %v", err)
	}

	// 恢复后标记服务器应重新出现
	st2, err := store.Open(context.Background(), filepath.Join(dir, "leadstack.db"))
	if err != nil {
		t.Fatalf("恢复后打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	if _, err := st2.GetServer(context.Background(), "srv-backup-test"); err != nil {
		t.Fatalf("恢复后标记服务器应存在: %v", err)
	}
	// pre-restore 目录应保留
	entries, err := os.ReadDir(filepath.Dir(dir))
	if err != nil {
		t.Fatalf("读取父目录失败: %v", err)
	}
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), filepath.Base(dir)+".pre-restore-") {
			found = true
		}
	}
	if !found {
		t.Fatalf("应保留 pre-restore 目录: %v", entries)
	}
}

// TestApplyPendingRestoreOnStartup 覆盖「启动时应用暂存恢复」。
func TestApplyPendingRestoreOnStartup(t *testing.T) {
	dir := prepareDataDir(t)
	out := filepath.Join(t.TempDir(), "pending-src.lsbak")

	// 1) 造备份
	if err := runBackupCommand([]string{"--data-dir", dir, "--out", out}); err != nil {
		t.Fatalf("CLI 备份失败: %v", err)
	}
	// 2) 改动数据
	st, err := store.Open(context.Background(), filepath.Join(dir, "leadstack.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := st.DeleteServer(context.Background(), "srv-backup-test"); err != nil {
		t.Fatalf("删除标记服务器失败: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}
	// 3) 暂存恢复（模拟 API 上传）
	cfg := &config.Config{DataDir: dir, BackupDir: filepath.Join(dir, "backups"), Version: "test"}
	pass, err := backup.ResolvePassphrase(dir, "")
	if err != nil {
		t.Fatalf("解析口令失败: %v", err)
	}
	if _, err := backup.StageRestore(context.Background(), out, cfg.RestorePendingDir(), pass); err != nil {
		t.Fatalf("暂存恢复失败: %v", err)
	}
	// 4) 启动逻辑应用
	logger := newLogger(4) // error 级别，避免测试输出噪音
	if err := applyPendingRestoreIfAny(cfg, logger); err != nil {
		t.Fatalf("应用暂存恢复失败: %v", err)
	}
	// 暂存目录应被清理
	if _, err := os.Stat(cfg.RestorePendingDir()); !os.IsNotExist(err) {
		t.Fatalf("暂存目录应被清理: %v", err)
	}
	// 数据应恢复
	st2, err := store.Open(context.Background(), filepath.Join(dir, "leadstack.db"))
	if err != nil {
		t.Fatalf("恢复后打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	if _, err := st2.GetServer(context.Background(), "srv-backup-test"); err != nil {
		t.Fatalf("启动恢复后标记服务器应存在: %v", err)
	}

	// 5) 失败路径：暂存目录存在但内容损坏 → 必须拒绝启动且保留暂存目录
	if err := os.MkdirAll(cfg.RestorePendingDir(), 0o700); err != nil {
		t.Fatalf("创建暂存目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.RestorePendingDir(), "meta.json"),
		[]byte(`{"source":"api-upload","created_at":1}`), 0o600); err != nil {
		t.Fatalf("写入 meta.json 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.RestorePendingDir(), "leadstack.db"),
		[]byte("not-a-valid-db"), 0o600); err != nil {
		t.Fatalf("写入损坏数据库失败: %v", err)
	}
	if err := applyPendingRestoreIfAny(cfg, logger); err == nil {
		t.Fatal("损坏的暂存恢复必须拒绝启动")
	} else if !strings.Contains(err.Error(), "拒绝启动") {
		t.Fatalf("失败提示应说明拒绝启动与处理建议: %v", err)
	}
	if _, err := os.Stat(cfg.RestorePendingDir()); err != nil {
		t.Fatalf("失败后应保留暂存目录供人工处理: %v", err)
	}
}

// TestDispatchSubcommand 校验子命令分发不影响默认行为。
func TestDispatchSubcommand(t *testing.T) {
	cases := []struct {
		args    []string
		handled bool
	}{
		{[]string{}, false},
		{[]string{"-listen", "127.0.0.1:1"}, false},
		{[]string{"backup", "--out", "/tmp/x"}, true},
		{[]string{"backup", "verify", "--file", "/tmp/x"}, true},
		{[]string{"restore", "--file", "/tmp/x"}, true},
	}
	for _, tc := range cases {
		handled := isSubcommand(tc.args)
		if handled != tc.handled {
			t.Fatalf("args=%v 期望 handled=%t，实际 %t", tc.args, tc.handled, handled)
		}
	}
}

// min 返回较小值。
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ = strconv.Itoa
