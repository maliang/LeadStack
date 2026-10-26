package checker

import (
	"context"
	"strings"
	"testing"
)

// fakeExec 只实现 Executor（不支持目标机代检），模拟 SSH 通道。
type fakeExec struct {
	stdout string
	calls  int
}

func (f *fakeExec) Exec(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
	f.calls++
	return f.stdout, "", 0, nil
}

// fakeProber 同时实现 Executor 与 AgentProber，模拟 agent 通道（RoutingExecutor）。
type fakeProber struct {
	fakeExec
	probed  int
	last    Spec
	lastSrv string
	result  Result
}

func (f *fakeProber) ProbeOnTarget(ctx context.Context, serverID string, spec Spec) Result {
	f.probed++
	f.last = spec
	f.lastSrv = serverID
	return f.result
}

// TestProbeViaAgentDispatches 验证 probe_via=agent 时探测被下发到目标机，
// 并且原始 Spec（含 params）被完整透传。
func TestProbeViaAgentDispatches(t *testing.T) {
	pr := &fakeProber{result: Result{OK: true, LatencyMS: 7, TS: 123}}
	spec := Spec{
		Kind:   KindTCP,
		Target: "10.0.0.9:3306",
		Params: map[string]any{"probe_via": "agent", "timeout_ms": 500},
	}

	res := Probe(context.Background(), spec, pr, "srv-1")
	if pr.probed != 1 {
		t.Fatalf("ProbeOnTarget 调用次数 = %d，期望 1", pr.probed)
	}
	if pr.lastSrv != "srv-1" {
		t.Errorf("serverID = %q，期望 srv-1", pr.lastSrv)
	}
	if pr.last.Target != "10.0.0.9:3306" || pr.last.Params["probe_via"] != "agent" {
		t.Errorf("Spec 未完整透传: %+v", pr.last)
	}
	if !res.OK || res.LatencyMS != 7 || res.TS != 123 {
		t.Errorf("结果未透传: %+v", res)
	}
}

// TestProbeViaAgentUnsupported 验证通道不支持时明确失败，而不是静默直连。
func TestProbeViaAgentUnsupported(t *testing.T) {
	ex := &fakeExec{}
	spec := Spec{Kind: KindHTTP, Target: "http://10.0.0.9/health", Params: map[string]any{"probe_via": "agent"}}

	res := Probe(context.Background(), spec, ex, "srv-1")
	if res.OK {
		t.Fatal("期望失败结果")
	}
	if !strings.Contains(res.Message, "agent 代检") {
		t.Errorf("错误信息应指明代检不支持，实际: %q", res.Message)
	}
	if ex.calls != 0 {
		t.Errorf("不应退化成目标机 shell 执行，实际执行 %d 次", ex.calls)
	}
}

// TestProbeViaAgentIgnoredForProcess 验证 process/script 本就运行在目标机，不受 probe_via 影响。
func TestProbeViaAgentIgnoredForProcess(t *testing.T) {
	pr := &fakeProber{fakeExec: fakeExec{stdout: "2"}}
	spec := Spec{
		Kind:   KindProcess,
		Target: "nginx",
		Params: map[string]any{"probe_via": "agent", "min_count": 1, "os": "linux"},
	}

	res := Probe(context.Background(), spec, pr, "srv-1")
	if pr.probed != 0 {
		t.Errorf("process 不应走代检，实际调用 %d 次", pr.probed)
	}
	if !res.OK {
		t.Errorf("进程数 2 >= 1 应判定成功，实际: %+v", res)
	}
}

// TestProbeViaControlDefault 验证缺省与非法值都走控制端直连（不触发代检）。
func TestProbeViaControlDefault(t *testing.T) {
	for _, via := range []any{nil, "", "weird", "CONTROL"} {
		pr := &fakeProber{result: Result{OK: true}}
		spec := Spec{Kind: KindTCP, Target: "127.0.0.1:1"}
		if via != nil {
			spec.Params = map[string]any{"probe_via": via}
		}
		_ = Probe(context.Background(), spec, pr, "srv-1")
		if pr.probed != 0 {
			t.Errorf("probe_via=%v 不应触发代检", via)
		}
	}
}

// TestProbeLocallyStripsFlag 验证 agent 本机执行时不会被 probe_via 再次路由（防自递归）。
func TestProbeLocallyStripsFlag(t *testing.T) {
	pr := &fakeProber{result: Result{OK: true}}
	spec := Spec{Kind: KindTCP, Target: "127.0.0.1:1", Params: map[string]any{"probe_via": "agent"}}

	_ = ProbeLocally(context.Background(), spec, pr, "")

	if pr.probed != 0 {
		t.Fatalf("ProbeLocally 不允许再次下发代检，实际调用 %d 次", pr.probed)
	}
	if _, still := spec.Params["probe_via"]; !still {
		t.Error("ProbeLocally 不应修改调用方传入的 Params")
	}
}
