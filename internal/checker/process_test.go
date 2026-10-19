package checker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// decodePrintfOctal 从生成的 POSIX 命令里取回 printf 的八进制字面量并解码，
// 用来证明 target 被完整、无损地还原。
func decodePrintfOctal(t *testing.T, command string) string {
	t.Helper()
	start := strings.Index(command, "printf '")
	if start < 0 {
		t.Fatalf("命令里没有 printf 字面量: %q", command)
	}
	rest := command[start+len("printf '"):]
	end := strings.Index(rest, "')")
	if end < 0 {
		t.Fatalf("printf 字面量未闭合: %q", command)
	}
	lit := rest[:end]
	var out []byte
	for i := 0; i < len(lit); {
		if lit[i] != '\\' || i+3 >= len(lit) {
			t.Fatalf("八进制字面量含非法片段: %q @%d", lit, i)
		}
		n, err := strconv.ParseUint(lit[i+1:i+4], 8, 32)
		if err != nil {
			t.Fatalf("八进制解析失败: %q: %v", lit[i:i+4], err)
		}
		out = append(out, byte(n))
		i += 4
	}
	return string(out)
}

// TestProcessCommandInjectionPayloads 是安全红线测试：
// 各种命令注入 payload 作为 target 时，生成的命令
//  1. 不含 target 原文；2) 经真实 sh 执行后不产生任何副作用；
//  3. 八进制字面量能无损还原出原 target。
func TestProcessCommandInjectionPayloads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX 命令生成在 macOS/Linux 上验证")
	}
	canary := filepath.Join(t.TempDir(), "pwned")
	payloads := []string{
		"foo; rm -rf /",
		"foo; touch " + canary,
		"foo`touch " + canary + "`",
		"foo$(touch " + canary + ")",
		"foo|touch " + canary,
		"foo&&touch " + canary,
		"foo||touch " + canary,
		"foo > " + canary,
		"foo; cat /etc/passwd",
		"foo' ; touch " + canary + " ; echo '",
		`foo" ; touch ` + canary + ` ; echo "`,
		"foo\n touch " + canary,
		"\n touch " + canary + " \n",
		"foo${IFS}touch${IFS}" + canary,
		"foo$(id)",
		"'; rm -rf / #",
		"--dangerous",
		"-u root",
	}
	exec := &realExecutor{}
	for i, payload := range payloads {
		t.Run(fmt.Sprintf("payload-%02d", i), func(t *testing.T) {
			command, err := processCommand("darwin", payload, "")
			if err != nil {
				t.Fatalf("processCommand 报错: %v", err)
			}
			if strings.Contains(command, payload) {
				t.Fatalf("生成的命令里出现了 target 原文，存在注入风险:\n%s", command)
			}
			if got := decodePrintfOctal(t, command); got != payload {
				t.Fatalf("八进制还原不一致: got %q want %q", got, payload)
			}
			stdout, stderr, exitCode, err := exec.Exec(context.Background(), "srv", command, 10)
			if err != nil {
				t.Fatalf("执行生成的命令失败: %v", err)
			}
			if exitCode != 0 {
				t.Fatalf("命令退出码 %d（stdout=%q stderr=%q）", exitCode, stdout, stderr)
			}
			if _, statErr := os.Stat(canary); statErr == nil {
				t.Fatalf("注入生效：canary 文件被创建！命令=%s", command)
			}
			count, perr := parseProcessCount("darwin", stdout)
			if perr != nil {
				t.Fatalf("解析计数失败: %v (stdout=%q)", perr, stdout)
			}
			if count != 0 {
				t.Fatalf("不存在的进程名应计数 0，得到 %d（命令=%s）", count, command)
			}
		})
	}
}

// TestProcessCommandWindowsAllowlist Windows 分支不做 cmd 转义，而是白名单拒绝。
func TestProcessCommandWindowsAllowlist(t *testing.T) {
	for _, ok := range []string{"nginx", "my-app.exe", "node", "postgres_1", "a.b.c"} {
		command, err := processCommand("windows", ok, "")
		if err != nil {
			t.Fatalf("windows 进程名 %q 应被接受: %v", ok, err)
		}
		if !strings.Contains(command, "tasklist /FI \"IMAGENAME eq ") {
			t.Fatalf("windows 命令形态不对: %q", command)
		}
		wantImage := ok
		if !strings.Contains(ok, ".") {
			wantImage = ok + ".exe"
		}
		if !strings.Contains(command, wantImage) {
			t.Fatalf("windows 命令缺少映像名 %q: %q", wantImage, command)
		}
	}
	rejected := []string{
		"foo & calc", "foo|calc", `foo"`, "foo'", "foo%PATH%", "foo^&calc", "foo*",
		"foo>out", "foo<in", "foo;calc", "foo`calc`", "foo$(calc)", "foo\ncalc", "", "   ",
	}
	for _, bad := range rejected {
		if _, err := processCommand("windows", bad, ""); err == nil {
			t.Errorf("windows 进程名 %q 应被拒绝", bad)
		}
	}
	if _, err := processCommand("windows", strings.Repeat("a", 300), ""); err == nil {
		t.Errorf("超长进程名应被拒绝")
	}
}

func TestProcessCommandPOSIXUserAndNameValidation(t *testing.T) {
	command, err := processCommand("linux", "nginx", "www-data")
	if err != nil {
		t.Fatalf("合法用户名应被接受: %v", err)
	}
	if !strings.Contains(command, `-v u="www-data"`) || !strings.Contains(command, "$3==u") {
		t.Fatalf("用户名过滤未生效: %q", command)
	}
	for _, bad := range []string{"root; id", "root`id`", "root$(id)", "root name", "root\"x"} {
		if _, err := processCommand("linux", "nginx", bad); err == nil {
			t.Errorf("非法用户名 %q 应被拒绝", bad)
		}
	}
	if _, err := processCommand("linux", "", ""); err == nil {
		t.Errorf("空进程名应被拒绝")
	}
	if _, err := processCommand("linux", strings.Repeat("a", 2000), ""); err == nil {
		t.Errorf("超长进程名应被拒绝")
	}
	if _, err := processCommand("linux", "a\x00b", ""); err == nil {
		t.Errorf("含 NUL 的进程名应被拒绝")
	}
}

// TestProcessCommandNoSelfMatch 验证「命令自身的进程树」不会被计入。
//
// 做法：用只可能出现在我们自己生成的命令文本里的字符串当进程名
// （target 本身是八进制转义，绝不会出现在命令行里；这些固定片段会）。
// 如果 PID/PPID 过滤失效，count 必然 ≥1。
//
// 环境干扰（例如外层 shell 的命令行恰好含同名片段）会让该断言失真，
// 因此先用 ps 自检：片段已在别处出现就跳过该用例。
func TestProcessCommandNoSelfMatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX 路径")
	}
	exec := &realExecutor{}
	helperOnlyTokens := []string{
		"ENVIRON",
		"ppid=,command=",
		"END{print n+0}",
		"--marker-printf-fragment--",
		"-v me=",
		"index(line,",
	}
	checked := 0
	for _, token := range helperOnlyTokens {
		if commandLineSeenInEnvironment(t, token) {
			t.Logf("跳过 %q：当前环境已有进程命令行包含该片段，无法隔离验证自匹配", token)
			continue
		}
		command, err := processCommand(runtime.GOOS, token, "")
		if err != nil {
			t.Fatalf("processCommand(%q): %v", token, err)
		}
		stdout, stderr, exitCode, err := exec.Exec(context.Background(), "srv", command, 10)
		if err != nil || exitCode != 0 {
			t.Fatalf("token=%q 执行失败 exit=%d err=%v stderr=%q", token, exitCode, err, stderr)
		}
		count, perr := parseProcessCount(runtime.GOOS, stdout)
		if perr != nil {
			t.Fatalf("token=%q 解析失败: %v stdout=%q", token, perr, stdout)
		}
		if count != 0 {
			t.Errorf("token=%q 出现自匹配假阳性：count=%d（命令=%s）", token, count, command)
		}
		checked++
	}
	if checked == 0 {
		t.Skip("所有片段都被环境占用，无法验证自匹配")
	}
}

// commandLineSeenInEnvironment 报告当前是否有进程的命令行包含 token。
func commandLineSeenInEnvironment(t *testing.T, token string) bool {
	t.Helper()
	out, err := exec.Command("ps", "-ww", "-eo", "command=").Output()
	if err != nil {
		t.Fatalf("ps 执行失败: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, token) {
			return true
		}
	}
	return false
}

func TestParseProcessCount(t *testing.T) {
	posixCases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{in: "3\n", want: 3},
		{in: "       3\n", want: 3},
		{in: "0\n", want: 0},
		{in: "not-a-number", wantErr: true},
		{in: "", wantErr: true},
		{in: "-2", wantErr: true},
	}
	for _, tc := range posixCases {
		got, err := parseProcessCount("linux", tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseProcessCount(%q) 期望报错，得到 %d", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parseProcessCount(%q) = %d, %v want %d", tc.in, got, err, tc.want)
		}
	}

	winCSV := "\"nginx.exe\",\"1234\",\"Console\",\"1\",\"10,000 K\"\n\"nginx.exe\",\"1235\",\"Console\",\"1\",\"10,000 K\"\n"
	if got, err := parseProcessCount("windows", winCSV); err != nil || got != 2 {
		t.Errorf("windows CSV: got %d err %v want 2", got, err)
	}
	if got, err := parseProcessCount("windows", "INFO: No tasks are running which match the specified criteria.\n"); err != nil || got != 0 {
		t.Errorf("windows 无匹配: got %d err %v want 0", got, err)
	}
	if got, err := parseProcessCount("windows", "\n"); err != nil || got != 0 {
		t.Errorf("windows 空输出: got %d err %v want 0", got, err)
	}
}

func TestProbeProcessWithFakeExecutor(t *testing.T) {
	ctx := context.Background()

	t.Run("命中", func(t *testing.T) {
		exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
			return "3\n", "", 0, nil
		}}
		r := Probe(ctx, Spec{Kind: KindProcess, Target: "nginx", Params: map[string]any{"min_count": 2}}, exec, "srv-1")
		if !r.OK || !strings.Contains(r.Message, "匹配进程数 3") {
			t.Fatalf("期望命中: %+v", r)
		}
		call, _ := exec.lastCall()
		if call.serverID != "srv-1" {
			t.Fatalf("serverID 未透传: %q", call.serverID)
		}
		if strings.Contains(call.command, "nginx") {
			t.Fatalf("命令里不应出现 target 原文: %q", call.command)
		}
		want, err := processCommand(runtime.GOOS, "nginx", "")
		if err != nil || want != call.command {
			t.Fatalf("命令与 processCommand 不一致: got %q want %q err %v", call.command, want, err)
		}
	})

	t.Run("数量不足", func(t *testing.T) {
		exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
			return "1\n", "", 0, nil
		}}
		r := Probe(ctx, Spec{Kind: KindProcess, Target: "nginx", Params: map[string]any{"min_count": 5}}, exec, "srv-1")
		if r.OK || !strings.Contains(r.Message, "1 < 期望最少 5") {
			t.Fatalf("期望失败并说明数量: %+v", r)
		}
	})

	t.Run("默认min_count为1", func(t *testing.T) {
		exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
			return "1\n", "", 0, nil
		}}
		r := Probe(ctx, Spec{Kind: KindProcess, Target: "nginx"}, exec, "srv-1")
		if !r.OK {
			t.Fatalf("默认 min_count=1 时应命中: %+v", r)
		}
	})

	t.Run("退出码非零", func(t *testing.T) {
		exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
			return "", "tasklist: not found", 1, nil
		}}
		r := Probe(ctx, Spec{Kind: KindProcess, Target: "nginx"}, exec, "srv-1")
		if r.OK || !strings.Contains(r.Message, "退出码 1") || !strings.Contains(r.Message, "not found") {
			t.Fatalf("非零退出码应失败并带 stderr: %+v", r)
		}
	})

	t.Run("传输层错误", func(t *testing.T) {
		exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
			return "", "", -1, errors.New("目标机离线")
		}}
		r := Probe(ctx, Spec{Kind: KindProcess, Target: "nginx"}, exec, "srv-1")
		if r.OK || !strings.Contains(r.Message, "目标机执行失败") {
			t.Fatalf("传输层错误应失败: %+v", r)
		}
	})

	t.Run("ctx取消", func(t *testing.T) {
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
			return "", "", -1, ctx.Err()
		}}
		r := Probe(canceled, Spec{Kind: KindProcess, Target: "nginx"}, exec, "srv-1")
		if r.OK || !strings.Contains(r.Message, "被取消") {
			t.Fatalf("取消应给出明确原因: %+v", r)
		}
	})

	t.Run("输出不可解析", func(t *testing.T) {
		exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
			return "pgrep: command not found\n", "", 0, nil
		}}
		r := Probe(ctx, Spec{Kind: KindProcess, Target: "nginx"}, exec, "srv-1")
		if r.OK || !strings.Contains(r.Message, "解析进程数失败") {
			t.Fatalf("不可解析输出应失败: %+v", r)
		}
	})
}

// TestProbeProcessRealSystem 用真实进程验证「有进程 → up」「进程消失 → down」，
// 同时证明不存在自匹配假阳性。
func TestProbeProcessRealSystem(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX 路径")
	}
	marker := fmt.Sprintf("lsp_marker_%d", os.Getpid())
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, marker)
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nsleep 30\n:\n"), 0o755); err != nil {
		t.Fatalf("写临时脚本: %v", err)
	}
	runner := &realExecutor{}
	spec := Spec{Kind: KindProcess, Target: marker, TimeoutS: 5}

	// 进程不存在：必须 ok=false（这条最容易因为自匹配而假 up）
	r0 := Probe(context.Background(), spec, runner, "srv-1")
	if r0.OK {
		t.Fatalf("进程不存在时应 ok=false，得到 %+v（命令=%s）", r0, runner.lastCommand())
	}

	cmd := exec.Command(scriptPath)
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动标记进程: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	var r1 Result
	if !waitFor(5*time.Second, func() bool {
		r1 = Probe(context.Background(), spec, runner, "srv-1")
		return r1.OK
	}) {
		t.Fatalf("标记进程已启动但探测未命中: %+v", r1)
	}
	if !strings.Contains(r1.Message, "匹配进程数 1") {
		t.Logf("命中但数量不是 1（可能有同名进程）: %s", r1.Message)
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if !waitFor(5*time.Second, func() bool {
		r := Probe(context.Background(), spec, runner, "srv-1")
		return !r.OK
	}) {
		t.Fatalf("进程被杀掉后仍报 up，疑似自匹配假阳性")
	}
}
