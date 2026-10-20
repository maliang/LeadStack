package checker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestProcessCommandPerOS 断言三种目标机 OS 生成的命令形态不同，
// 且都沿用同一套「target 原文不出现在命令行」的注入防线。
func TestProcessCommandPerOS(t *testing.T) {
	linux, err := processCommand("linux", "nginx", "")
	if err != nil {
		t.Fatalf("linux: %v", err)
	}
	if !strings.Contains(linux, "ps -ww -eo pid=,ppid=,command=") {
		t.Fatalf("linux 应使用 procps 的 -eo: %q", linux)
	}
	darwin, err := processCommand("darwin", "nginx", "")
	if err != nil {
		t.Fatalf("darwin: %v", err)
	}
	if !strings.Contains(darwin, "ps -ww -axo pid=,ppid=,command=") {
		t.Fatalf("darwin 应使用 BSD ps 的 -axo: %q", darwin)
	}
	if linux == darwin {
		t.Fatalf("linux 与 darwin 的命令必须不同")
	}
	windows, err := processCommand("windows", "nginx", "")
	if err != nil {
		t.Fatalf("windows: %v", err)
	}
	if !strings.Contains(windows, `tasklist /FI "IMAGENAME eq nginx.exe"`) {
		t.Fatalf("windows 应使用 tasklist: %q", windows)
	}
	// windows + user：tasklist 不支持按用户过滤，命令保持 tasklist（过滤逻辑不适用）
	if _, err := processCommand("windows", "nginx", "root"); err != nil {
		t.Fatalf("windows 忽略 user 即可: %v", err)
	}
	// 三种命令都不得包含 target 原文
	for name, cmd := range map[string]string{"linux": linux, "darwin": darwin, "windows": windows} {
		if strings.Contains(cmd, "nginx") && name != "windows" {
			t.Fatalf("%s 命令泄漏了 target 原文: %q", name, cmd)
		}
	}
	// windows 有自己更严格的白名单：注入载荷必须直接拒绝
	for _, bad := range []string{"foo & calc", "foo;calc", "foo`id`", "foo$(id)", "foo\"bar"} {
		if _, err := processCommand("windows", bad, ""); err == nil {
			t.Fatalf("windows 进程名 %q 应被拒绝", bad)
		}
	}
	// POSIX 的 user 过滤在 darwin/linux 都要生效
	for _, os := range []string{"linux", "darwin"} {
		cmd, err := processCommand(os, "nginx", "www-data")
		if err != nil {
			t.Fatalf("%s user: %v", os, err)
		}
		if !strings.Contains(cmd, `-v u="www-data"`) || !strings.Contains(cmd, "$3==u") {
			t.Fatalf("%s 的用户过滤未生效: %q", os, cmd)
		}
	}
}

func TestTargetOS(t *testing.T) {
	old := processGOOS
	processGOOS = "linux"
	defer func() { processGOOS = old }()

	cases := []struct {
		params map[string]any
		want   string
		bad    bool
	}{
		{params: nil, want: "linux"},
		{params: map[string]any{}, want: "linux"},
		{params: map[string]any{"os": ""}, want: "linux"},
		{params: map[string]any{"os": "windows"}, want: "windows"},
		{params: map[string]any{"os": "WINDOWS"}, want: "windows"},
		{params: map[string]any{"os": " darwin "}, want: "darwin"},
		{params: map[string]any{"os": "macOS"}, want: "darwin"},
		{params: map[string]any{"os": "Linux"}, want: "linux"},
		{params: map[string]any{"os": "plan9"}, bad: true},
		{params: map[string]any{"os": 42}, bad: true},
	}
	for _, tc := range cases {
		got, err := targetOS(tc.params)
		if tc.bad {
			if err == nil {
				t.Errorf("params=%v 应报错，得到 %q", tc.params, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("params=%v = %q, %v want %q", tc.params, got, err, tc.want)
		}
	}
}

// TestProbeProcessOSParam 验证 params["os"] 真正决定执行到目标机的命令，
// 并且不需要改动 Executor 接口。
func TestProbeProcessOSParam(t *testing.T) {
	ctx := context.Background()

	t.Run("windows 走 tasklist", func(t *testing.T) {
		exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
			if !strings.Contains(command, "tasklist /FI") {
				t.Errorf("应下发 tasklist 命令，实际 %q", command)
			}
			return "\"nginx.exe\",\"1234\",\"Console\",\"1\",\"10,000 K\"\n", "", 0, nil
		}}
		r := Probe(ctx, Spec{Kind: KindProcess, Target: "nginx", Params: map[string]any{"os": "windows"}}, exec, "srv-1")
		if !r.OK {
			t.Fatalf("windows 分支应命中: %+v", r)
		}
		if !strings.Contains(r.Message, "匹配进程数 1") {
			t.Fatalf("message=%q", r.Message)
		}
	})

	t.Run("linux 走 -eo", func(t *testing.T) {
		exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
			if !strings.Contains(command, "ps -ww -eo") || strings.Contains(command, "-axo") {
				t.Errorf("linux 分支命令不符: %q", command)
			}
			return "3\n", "", 0, nil
		}}
		r := Probe(ctx, Spec{Kind: KindProcess, Target: "nginx", Params: map[string]any{"os": "linux", "min_count": 2}}, exec, "srv-1")
		if !r.OK {
			t.Fatalf("linux 分支应命中: %+v", r)
		}
	})

	t.Run("darwin 走 -axo", func(t *testing.T) {
		exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
			if !strings.Contains(command, "ps -ww -axo") {
				t.Errorf("darwin 分支命令不符: %q", command)
			}
			return "2\n", "", 0, nil
		}}
		r := Probe(ctx, Spec{Kind: KindProcess, Target: "nginx", Params: map[string]any{"os": "darwin"}}, exec, "srv-1")
		if !r.OK {
			t.Fatalf("darwin 分支应命中: %+v", r)
		}
	})

	t.Run("非法 os 直接失败且不下发命令", func(t *testing.T) {
		exec := &fakeExecutor{}
		r := Probe(ctx, Spec{Kind: KindProcess, Target: "nginx", Params: map[string]any{"os": "plan9"}}, exec, "srv-1")
		if r.OK || !strings.Contains(r.Message, "os 参数非法") {
			t.Fatalf("非法 os 应失败: %+v", r)
		}
		if exec.callCount() != 0 {
			t.Fatalf("非法 os 不应下发任何命令")
		}
	})

	t.Run("缺省沿用控制端 GOOS", func(t *testing.T) {
		exec := &fakeExecutor{}
		_ = Probe(ctx, Spec{Kind: KindProcess, Target: "nginx"}, exec, "srv-1")
		call, ok := exec.lastCall()
		if !ok {
			t.Fatalf("应下发命令")
		}
		want, err := processCommand(processGOOS, "nginx", "")
		if err != nil || call.command != want {
			t.Fatalf("缺省 OS 命令不符: got %q want %q err=%v", call.command, want, err)
		}
	})

	t.Run("windows 下注入载荷被拒且不下发命令", func(t *testing.T) {
		exec := &fakeExecutor{}
		r := Probe(ctx, Spec{Kind: KindProcess, Target: "foo & calc", Params: map[string]any{"os": "windows"}}, exec, "srv-1")
		if r.OK || !strings.Contains(r.Message, "生成进程查询命令失败") {
			t.Fatalf("注入载荷应失败: %+v", r)
		}
		if exec.callCount() != 0 {
			t.Fatalf("非法进程名不应下发命令")
		}
	})
}

// TestProbeProcessDarwinParamRealShell 用真实 shell 验证「params.os=darwin」这条路径
// 生成的命令在 macOS 上确实可执行，并能在进程消失后正确判 down。
func TestProbeProcessDarwinParamRealShell(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("仅在 macOS 上验证 darwin 命令")
	}
	marker := "lsp_osmarker_" + itoa(os.Getpid())
	dir := t.TempDir()
	script := filepath.Join(dir, marker)
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n:\n"), 0o755); err != nil {
		t.Fatalf("写脚本: %v", err)
	}
	runner := &realExecutor{}
	spec := Spec{Kind: KindProcess, Target: marker, TimeoutS: 5, Params: map[string]any{"os": "darwin"}}

	if r := Probe(context.Background(), spec, runner, "srv-1"); r.OK {
		t.Fatalf("进程不存在时应 ok=false: %+v", r)
	}

	cmd := exec.Command(script)
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动标记进程: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	var hit Result
	if !waitFor(5*time.Second, func() bool {
		hit = Probe(context.Background(), spec, runner, "srv-1")
		return hit.OK
	}) {
		t.Fatalf("params.os=darwin 时未命中标记进程: %+v (命令=%s)", hit, runner.lastCommand())
	}
	if !strings.Contains(runner.lastCommand(), "ps -ww -axo") {
		t.Fatalf("darwin 分支命令不符: %q", runner.lastCommand())
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
