package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/maliang/leadstack/internal/api"
	"github.com/maliang/leadstack/internal/store"
)

// TestNetworkCLIReadWrite 覆盖离线 CLI 的参数解析与写库（不需要服务运行）。
func TestNetworkCLIReadWrite(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "leadstack.db")
	ctx := context.Background()

	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	// --listen 写入（服务未运行，应只写库）
	if err := runNetworkCommand([]string{"--data-dir", dir, "--listen", "127.0.0.1:19090"}); err != nil {
		t.Fatalf("写入监听地址失败: %v", err)
	}
	// --default-ssh-port 写入
	if err := runNetworkCommand([]string{"--data-dir", dir, "--default-ssh-port", "2222"}); err != nil {
		t.Fatalf("写入默认端口失败: %v", err)
	}
	// --show 不报错
	if err := runNetworkCommand([]string{"--data-dir", dir, "--show"}); err != nil {
		t.Fatalf("--show 失败: %v", err)
	}

	// 校验确实落库
	st2, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("重新打开数据库失败: %v", err)
	}
	defer func() { _ = st2.Close() }()
	snap, err := api.ReadNetworkSnapshot(ctx, st2)
	if err != nil {
		t.Fatalf("读取快照失败: %v", err)
	}
	if snap.SettingsListen != "127.0.0.1:19090" || snap.SettingsSSHPort != "2222" {
		t.Fatalf("设置未落库: %+v", snap)
	}

	// --reset 清空
	if err := runNetworkCommand([]string{"--data-dir", dir, "--reset"}); err != nil {
		t.Fatalf("--reset 失败: %v", err)
	}
	snap, _ = api.ReadNetworkSnapshot(ctx, st2)
	if snap.SettingsListen != "" || snap.SettingsSSHPort != "" {
		t.Fatalf("重置后设置应为空: %+v", snap)
	}

	// 非法值应报错
	if err := runNetworkCommand([]string{"--data-dir", dir, "--listen", "bad"}); err == nil {
		t.Fatal("非法监听地址应报错")
	}
	if err := runNetworkCommand([]string{"--data-dir", dir, "--default-ssh-port", "70000"}); err == nil {
		t.Fatal("非法端口应报错")
	}
	if err := runNetworkCommand([]string{"--data-dir", dir, "--public-url", "ftp://x"}); err == nil {
		t.Fatal("非法对外地址应报错")
	}
	// 数据库不存在时应给出可执行提示
	if err := runNetworkCommand([]string{"--data-dir", filepath.Join(dir, "nope"), "--show"}); err == nil {
		t.Fatal("数据库不存在时应报错")
	}
}

// TestNetworkSubcommandDispatch 确认 network 子命令被识别且不影响默认启动行为。
func TestNetworkSubcommandDispatch(t *testing.T) {
	cases := []struct {
		args    []string
		handled bool
	}{
		{[]string{"network", "--show"}, true},
		{[]string{"backup", "--out", "/tmp/x"}, true},
		{[]string{"restore", "--file", "/tmp/x"}, true},
		{[]string{"--listen", "127.0.0.1:1"}, false},
		{[]string{}, false},
	}
	for _, tc := range cases {
		if got := isSubcommand(tc.args); got != tc.handled {
			t.Fatalf("args=%v 期望 %t，实际 %t", tc.args, tc.handled, got)
		}
	}
}
