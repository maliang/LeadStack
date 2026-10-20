package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type runCall struct {
	serverID string
	command  string
	timeoutS int
	ctxErr   error // 执行瞬间的 ctx 状态（回滚时用来断言没被取消）
}

// fakeRunner 记录调用序列，可注入行为；**不真的执行任何命令**。
type fakeRunner struct {
	mu   sync.Mutex
	fn   func(ctx context.Context, call runCall, onLog func(stream, chunk string)) (string, string, int, error)
	call []runCall

	inflight, peak int
}

func (f *fakeRunner) Run(ctx context.Context, serverID, command string, timeoutS int, onLog func(string, string)) (string, string, int, error) {
	call := runCall{serverID: serverID, command: command, timeoutS: timeoutS, ctxErr: ctx.Err()}
	f.mu.Lock()
	f.call = append(f.call, call)
	f.inflight++
	if f.inflight > f.peak {
		f.peak = f.inflight
	}
	fn := f.fn
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inflight--
		f.mu.Unlock()
	}()
	if fn != nil {
		return fn(ctx, call, onLog)
	}
	if onLog != nil {
		onLog("stdout", "fake: "+command)
	}
	return "", "", 0, nil
}

func (f *fakeRunner) calls() []runCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runCall(nil), f.call...)
}

func (f *fakeRunner) commands() []string {
	out := []string{}
	for _, c := range f.calls() {
		out = append(out, c.command)
	}
	return out
}

func (f *fakeRunner) peakInflight() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peak
}

func silentExecutor(t *testing.T, r Runner, conc int) *Executor {
	t.Helper()
	return NewExecutor(r, ExecutorOptions{Concurrency: conc, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

func simplePlan(steps ...Step) Plan {
	return Plan{AppSlug: "my-app", ReleaseID: "r1", Version: "1.0", Root: "/opt/apps/my-app", Steps: steps}
}

func step(name string) Step { return Step{Name: name, Command: "cmd-" + name, TimeoutS: 30} }

// eventRecorder 记录回调顺序，用来断言「严格串行」与「日志实时不缓冲」。
type eventRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *eventRecorder) add(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *eventRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func TestRunPlanSerialOrderAndTimeoutPassthrough(t *testing.T) {
	rec := &eventRecorder{}
	runner := &fakeRunner{fn: func(ctx context.Context, call runCall, onLog func(string, string)) (string, string, int, error) {
		rec.add("run:" + call.command)
		if onLog != nil {
			onLog("stdout", "chunk-"+call.command)
		}
		return "", "", 0, nil
	}}
	e := silentExecutor(t, runner, 4)

	plan := simplePlan(step("a"), step("b"), step("c"))
	plan.Steps[1].TimeoutS = 42

	out, err := e.RunPlan(context.Background(), "srv-1", plan, Hooks{
		OnStep: func(i int, name string) { rec.add(fmt.Sprintf("step:%d:%s", i, name)) },
		OnLog:  func(stream, chunk string) { rec.add("log:" + chunk) },
		OnStatus: func(status, msg string) {
			rec.add("status:" + status)
		},
	})
	if err != nil {
		t.Fatalf("RunPlan 返回错误: %v", err)
	}
	if out.Status != StatusSuccess {
		t.Fatalf("期望 success，得到 %+v", out)
	}
	// 串行 + 实时：每一步的日志必须出现在下一步开始之前
	want := []string{
		"status:running",
		"step:0:a", "run:cmd-a", "log:chunk-cmd-a",
		"step:1:b", "run:cmd-b", "log:chunk-cmd-b",
		"step:2:c", "run:cmd-c", "log:chunk-cmd-c",
		"status:success",
	}
	if got := rec.snapshot(); !equalStrings(got, want) {
		t.Fatalf("事件顺序不符:\n got=%v\nwant=%v", got, want)
	}
	calls := runner.calls()
	if len(calls) != 3 || calls[0].command != "cmd-a" || calls[2].command != "cmd-c" {
		t.Fatalf("执行顺序不符: %+v", calls)
	}
	if calls[1].timeoutS != 42 {
		t.Fatalf("TimeoutS 未透传: %d", calls[1].timeoutS)
	}
	for _, c := range calls {
		if c.serverID != "srv-1" {
			t.Fatalf("serverID 未透传: %q", c.serverID)
		}
	}
}

func TestRunPlanFailureRollsBackInReverseOrder(t *testing.T) {
	runner := &fakeRunner{fn: func(ctx context.Context, call runCall, onLog func(string, string)) (string, string, int, error) {
		if call.command == "cmd-b" {
			return "", "boom: bad sha", 7, nil
		}
		return "", "", 0, nil
	}}
	e := silentExecutor(t, runner, 4)
	plan := simplePlan(step("a"), step("b"), step("c"))
	plan.RollbackSteps = []Step{
		{Name: "undo-2", Command: "rb-2", TimeoutS: 10}, // 切片逆序：先执行它
		{Name: "undo-1", Command: "rb-1", TimeoutS: 10},
	}

	out, err := e.RunPlan(context.Background(), "srv-1", plan, Hooks{})
	if err != nil {
		t.Fatalf("步骤失败应通过 RunOutcome 表达（error 为 nil），得到 %v", err)
	}
	if out.Status != StatusFailed || out.FailedStep != "b" || out.ExitCode != 7 {
		t.Fatalf("失败结果不符: %+v", out)
	}
	if !strings.Contains(out.Message, "boom: bad sha") {
		t.Fatalf("message 应包含 stderr 线索: %q", out.Message)
	}
	got := runner.commands()
	want := []string{"cmd-a", "cmd-b", "rb-1", "rb-2"} // c 不执行；回滚按逆序 = rb-1 → rb-2
	if !equalStrings(got, want) {
		t.Fatalf("执行序列不符:\n got=%v\nwant=%v", got, want)
	}
}

func TestRunPlanRollbackFailureDoesNotHideOriginalError(t *testing.T) {
	runner := &fakeRunner{fn: func(ctx context.Context, call runCall, onLog func(string, string)) (string, string, int, error) {
		switch call.command {
		case "cmd-a":
			return "", "disk full", 1, nil
		default:
			return "", "rollback boom", 9, nil
		}
	}}
	e := silentExecutor(t, runner, 4)
	plan := simplePlan(step("a"))
	plan.RollbackSteps = []Step{{Name: "undo", Command: "rb-1", TimeoutS: 5}}

	out, err := e.RunPlan(context.Background(), "srv-1", plan, Hooks{})
	if err != nil {
		t.Fatalf("err 应为 nil: %v", err)
	}
	if out.Status != StatusFailed || out.FailedStep != "a" {
		t.Fatalf("原始失败必须保留: %+v", out)
	}
	if !strings.Contains(out.Message, "disk full") {
		t.Fatalf("message 应保留原始 stderr: %q", out.Message)
	}
	if !strings.Contains(out.Message, "回滚") || !strings.Contains(out.Message, "1 步失败") {
		t.Fatalf("message 应说明回滚有失败: %q", out.Message)
	}
}

func TestRunPlanIgnoreFailure(t *testing.T) {
	runner := &fakeRunner{fn: func(ctx context.Context, call runCall, onLog func(string, string)) (string, string, int, error) {
		if call.command == "cmd-cleanup" {
			return "", "cleanup failed", 3, nil
		}
		return "", "", 0, nil
	}}
	e := silentExecutor(t, runner, 4)
	plan := simplePlan(step("a"), Step{Name: "cleanup", Command: "cmd-cleanup", TimeoutS: 5, IgnoreFailure: true}, step("z"))

	out, err := e.RunPlan(context.Background(), "srv-1", plan, Hooks{})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if out.Status != StatusSuccess {
		t.Fatalf("best-effort 步骤失败不应中断: %+v", out)
	}
	if !strings.Contains(out.Message, "已忽略") {
		t.Fatalf("message 应提示忽略了失败步骤: %q", out.Message)
	}
	if got := runner.commands(); !equalStrings(got, []string{"cmd-a", "cmd-cleanup", "cmd-z"}) {
		t.Fatalf("忽略后必须继续后续步骤: %v", got)
	}
}

func TestRunPlanTransportErrorFails(t *testing.T) {
	runner := &fakeRunner{fn: func(ctx context.Context, call runCall, onLog func(string, string)) (string, string, int, error) {
		return "", "", -1, errors.New("ssh: connection closed")
	}}
	e := silentExecutor(t, runner, 4)
	out, err := e.RunPlan(context.Background(), "srv-1", simplePlan(step("a")), Hooks{})
	if err != nil {
		t.Fatalf("err 应为 nil（通过 outcome 表达）: %v", err)
	}
	if out.Status != StatusFailed || !strings.Contains(out.Message, "ssh: connection closed") {
		t.Fatalf("传输层错误应判失败: %+v", out)
	}
}

// TestRunPlanContextCancelStillRollsBack 是硬要求：
// ctx 取消要终止当前步骤并回滚，且回滚本身不能被取消的 ctx 打断。
func TestRunPlanContextCancelStillRollsBack(t *testing.T) {
	var rollbackCtxErr error
	rollbackSawCanceled := false
	runner := &fakeRunner{fn: func(ctx context.Context, call runCall, onLog func(string, string)) (string, string, int, error) {
		if call.command == "cmd-a" {
			<-ctx.Done() // 模拟长时间命令，等 ctx 取消
			return "", "", -1, ctx.Err()
		}
		rollbackCtxErr = ctx.Err()
		if rollbackCtxErr != nil {
			rollbackSawCanceled = true
		}
		return "", "", 0, nil
	}}
	e := silentExecutor(t, runner, 4)
	plan := simplePlan(step("a"))
	plan.RollbackSteps = []Step{{Name: "undo", Command: "rb-1", TimeoutS: 5}}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	rec := &eventRecorder{}
	out, err := e.RunPlan(ctx, "srv-1", plan, Hooks{
		OnStep:   func(i int, name string) { rec.add(fmt.Sprintf("%d:%s", i, name)) },
		OnStatus: func(s, m string) { rec.add("status:" + s + ":" + m) },
	})
	if err != nil {
		t.Fatalf("取消应通过 outcome 表达: %v", err)
	}
	if out.Status != StatusFailed || out.FailedStep != "a" {
		t.Fatalf("取消应判失败: %+v", out)
	}
	if !strings.Contains(out.Message, "取消") {
		t.Fatalf("message 应说明被取消: %q", out.Message)
	}
	if rollbackCtxErr != nil {
		t.Fatalf("回滚必须使用未被取消的 ctx，实际 err=%v", rollbackCtxErr)
	}
	if rollbackSawCanceled {
		t.Fatalf("回滚 ctx 不应处于取消状态")
	}
	got := runner.commands()
	if !equalStrings(got, []string{"cmd-a", "rb-1"}) {
		t.Fatalf("取消后必须回滚: %v", got)
	}
	if !containsStr(rec.snapshot(), "0:rollback:undo") {
		t.Fatalf("回滚步骤应以 rollback: 前缀回报: %v", rec.snapshot())
	}
}

func TestRunPlanContextAlreadyCanceled(t *testing.T) {
	runner := &fakeRunner{}
	e := silentExecutor(t, runner, 4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := e.RunPlan(ctx, "srv-1", simplePlan(step("a")), Hooks{})
	if err == nil {
		t.Fatalf("开始前已取消应返回 error")
	}
	if out.Status != StatusFailed {
		t.Fatalf("outcome 应为 failed: %+v", out)
	}
	if len(runner.calls()) != 0 {
		t.Fatalf("不应执行任何步骤: %v", runner.commands())
	}
}

func TestRunPlanGuards(t *testing.T) {
	e := silentExecutor(t, &fakeRunner{}, 4)
	if _, err := e.RunPlan(context.Background(), "s", Plan{}, Hooks{}); err == nil {
		t.Fatalf("空计划应返回 error")
	}
	ne := NewExecutor(nil, ExecutorOptions{})
	if _, err := ne.RunPlan(context.Background(), "s", simplePlan(step("a")), Hooks{}); err == nil {
		t.Fatalf("Runner 为 nil 应返回 error")
	}
	// hooks 全 nil 不能 panic
	e2 := silentExecutor(t, &fakeRunner{}, 4)
	if _, err := e2.RunPlan(context.Background(), "s", simplePlan(step("a")), Hooks{}); err != nil {
		t.Fatalf("nil hooks 应可用: %v", err)
	}
}

func TestExecutorConcurrencyLimit(t *testing.T) {
	runner := &fakeRunner{fn: func(ctx context.Context, call runCall, onLog func(string, string)) (string, string, int, error) {
		time.Sleep(40 * time.Millisecond)
		return "", "", 0, nil
	}}
	e := silentExecutor(t, runner, 2)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			plan := simplePlan(step(fmt.Sprintf("s%d", i)))
			plan.ReleaseID = fmt.Sprintf("r%d", i)
			if _, err := e.RunPlan(context.Background(), fmt.Sprintf("srv-%d", i), plan, Hooks{}); err != nil {
				t.Errorf("RunPlan(%d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if peak := runner.peakInflight(); peak > 2 {
		t.Fatalf("全局并发上限失效：峰值=%d > 2", peak)
	}
	if peak := runner.peakInflight(); peak < 2 {
		t.Fatalf("并发未被利用（峰值=%d），上限可能写死为 1", peak)
	}
	if len(runner.calls()) != 8 {
		t.Fatalf("8 个计划都应执行，实际 %d", len(runner.calls()))
	}
}

func TestNewExecutorDefaults(t *testing.T) {
	e := NewExecutor(&fakeRunner{}, ExecutorOptions{})
	if cap(e.sem) != DefaultConcurrency {
		t.Fatalf("默认并发=%d want %d", cap(e.sem), DefaultConcurrency)
	}
	if e.log == nil {
		t.Fatalf("Logger 不应为 nil")
	}
	e2 := NewExecutor(&fakeRunner{}, ExecutorOptions{Concurrency: -3})
	if cap(e2.sem) != DefaultConcurrency {
		t.Fatalf("非法并发应回落默认值")
	}
}

// TestRunPlanExecutesRollbackPlanFromRollbackFunc 验证手动回滚路径：
// Rollback(p) 产出的计划按顺序执行（先切回 current，再重启），且不再下载。
func TestRunPlanExecutesRollbackPlanFromRollbackFunc(t *testing.T) {
	build, err := Build(Template{Kind: KindBinary, Content: "#!/bin/sh\ntrue\n"}, urlArtifact(), baseParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rb, err := Rollback(build)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	runner := &fakeRunner{}
	e := silentExecutor(t, runner, 4)
	out, err := e.RunPlan(context.Background(), "srv-1", rb, Hooks{})
	if err != nil || out.Status != StatusSuccess {
		t.Fatalf("手动回滚应成功: %+v err=%v", out, err)
	}
	cmds := runner.commands()
	if len(cmds) != 2 {
		t.Fatalf("回滚应只有 2 步（切回 + 重启），得到 %v", cmds)
	}
	if !strings.Contains(cmds[0], "current") || !strings.Contains(cmds[0], "20261019-160000-d4e5f6") {
		t.Fatalf("第一步应切回上一版本: %q", cmds[0])
	}
	if !strings.Contains(cmds[1], "leadstack-start.sh") {
		t.Fatalf("第二步应重启: %q", cmds[1])
	}
	for _, c := range cmds {
		if strings.Contains(c, "curl") {
			t.Fatalf("回滚不得下载: %q", c)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}
