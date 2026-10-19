package checker

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestProbeScriptWithFakeExecutor(t *testing.T) {
	ctx := context.Background()

	t.Run("退出码0视为成功", func(t *testing.T) {
		exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
			if command != "echo hi" {
				t.Fatalf("脚本内容应原样下发，得到 %q", command)
			}
			return "hi\n", "", 0, nil
		}}
		r := Probe(ctx, Spec{Kind: KindScript, Target: "echo hi"}, exec, "srv-1")
		if !r.OK {
			t.Fatalf("期望成功: %+v", r)
		}
	})

	t.Run("默认exit_zero_ok为true", func(t *testing.T) {
		exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
			return "", "boom", 1, nil
		}}
		r := Probe(ctx, Spec{Kind: KindScript, Target: "exit 1"}, exec, "srv-1")
		if r.OK || !strings.Contains(r.Message, "退出码 1") || !strings.Contains(r.Message, "boom") {
			t.Fatalf("非零退出码应失败并带 stderr: %+v", r)
		}
	})

	t.Run("exit_zero_ok=false忽略退出码", func(t *testing.T) {
		exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
			return "partial", "", 7, nil
		}}
		r := Probe(ctx, Spec{Kind: KindScript, Target: "exit 7", Params: map[string]any{"exit_zero_ok": false}}, exec, "srv-1")
		if !r.OK {
			t.Fatalf("exit_zero_ok=false 时不应因退出码失败: %+v", r)
		}
	})

	t.Run("expect_stdout命中", func(t *testing.T) {
		exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
			return "service is healthy\n", "", 0, nil
		}}
		r := Probe(ctx, Spec{Kind: KindScript, Target: "check", Params: map[string]any{"expect_stdout": "healthy"}}, exec, "srv-1")
		if !r.OK || !strings.Contains(r.Message, "期望输出") {
			t.Fatalf("期望命中: %+v", r)
		}
	})

	t.Run("expect_stdout未命中", func(t *testing.T) {
		exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
			return "service is down\n", "", 0, nil
		}}
		r := Probe(ctx, Spec{Kind: KindScript, Target: "check", Params: map[string]any{"expect_stdout": "healthy"}}, exec, "srv-1")
		if r.OK || !strings.Contains(r.Message, "未包含期望内容") {
			t.Fatalf("期望失败: %+v", r)
		}
	})

	t.Run("传输层错误", func(t *testing.T) {
		exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
			return "", "", -1, errors.New("ssh: connection closed")
		}}
		r := Probe(ctx, Spec{Kind: KindScript, Target: "check"}, exec, "srv-1")
		if r.OK || !strings.Contains(r.Message, "目标机执行失败") {
			t.Fatalf("传输层错误应失败: %+v", r)
		}
	})

	t.Run("空脚本", func(t *testing.T) {
		r := Probe(ctx, Spec{Kind: KindScript, Target: "   "}, &fakeExecutor{}, "srv-1")
		if r.OK || !strings.Contains(r.Message, "不能为空") {
			t.Fatalf("空脚本应失败: %+v", r)
		}
	})

	t.Run("超时秒数透传", func(t *testing.T) {
		exec := &fakeExecutor{}
		_ = Probe(ctx, Spec{Kind: KindScript, Target: "x", TimeoutS: 7}, exec, "srv-1")
		call, _ := exec.lastCall()
		if call.timeoutS != 7 {
			t.Fatalf("timeoutS=%d want 7", call.timeoutS)
		}
		_ = Probe(ctx, Spec{Kind: KindScript, Target: "x"}, exec, "srv-1")
		call, _ = exec.lastCall()
		if call.timeoutS != DefaultTimeoutS {
			t.Fatalf("默认 timeoutS=%d want %d", call.timeoutS, DefaultTimeoutS)
		}
	})
}

// TestProbeScriptRealExecution 通过真实 sh 执行脚本，覆盖两种执行通道里
// script 检测的失败/成功语义（agent 侧 job.exec 与 ssh 侧 Run 都是 sh -c）。
func TestProbeScriptRealExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell 语义")
	}
	exec := &realExecutor{}
	ctx := context.Background()

	r := Probe(ctx, Spec{Kind: KindScript, Target: "echo hello-leadstack", Params: map[string]any{"expect_stdout": "hello-leadstack"}}, exec, "srv-1")
	if !r.OK {
		t.Fatalf("真实脚本应成功: %+v", r)
	}

	r2 := Probe(ctx, Spec{Kind: KindScript, Target: "echo oops >&2; exit 3"}, exec, "srv-1")
	if r2.OK || !strings.Contains(r2.Message, "退出码 3") || !strings.Contains(r2.Message, "oops") {
		t.Fatalf("非零退出码应失败: %+v", r2)
	}

	r3 := Probe(ctx, Spec{Kind: KindScript, Target: "echo oops >&2; exit 3", Params: map[string]any{"exit_zero_ok": false}}, exec, "srv-1")
	if !r3.OK {
		t.Fatalf("exit_zero_ok=false 时应视为成功: %+v", r3)
	}

	// 超时（ctx 生效）必须中断
	start := time.Now()
	tctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	r4 := Probe(tctx, Spec{Kind: KindScript, Target: "sleep 5", TimeoutS: 5}, exec, "srv-1")
	if r4.OK {
		t.Fatalf("长时间脚本在 ctx 超时后不应成功: %+v", r4)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("ctx 超时未中断执行，耗时 %s", elapsed)
	}
}
