package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/checker"
	"github.com/maliang/leadstack/internal/store"
	"github.com/maliang/leadstack/pkg/protocol"
)

// fakeProbeDispatcher 模拟 jobs.Dispatcher.Probe。
type fakeProbeDispatcher struct {
	calls   int
	last    protocol.ProbeRequest
	lastSrv string
	result  protocol.ProbeResult
	err     error
}

// Exec 让 fakeProbeDispatcher 同时满足 checker.Executor（RoutingExecutor 需要它）。
func (f *fakeProbeDispatcher) Exec(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
	return "", "", 0, nil
}

func (f *fakeProbeDispatcher) Probe(ctx context.Context, serverID string, req protocol.ProbeRequest) (protocol.ProbeResult, error) {
	f.calls++
	f.lastSrv = serverID
	f.last = req
	return f.result, f.err
}

// newProbeTestStore 建一个带迁移的空库，并按需插入服务器。
func newProbeTestStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return st
}

// createProbeServer 建一台指定接入模式的服务器。
func createProbeServer(t *testing.T, st *store.Store, mode string) string {
	t.Helper()
	id := uuid.NewString()
	srv := &store.Server{
		ID: id, Name: "srv-" + id[:6], Host: "10.0.0.9", Port: 22,
		Mode: mode, TagsJSON: "[]", Status: "online", CreatedAt: store.NowMS(),
	}
	if err := st.CreateServer(context.Background(), srv); err != nil {
		t.Fatalf("创建服务器失败: %v", err)
	}
	return id
}

func probeTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestProbeOnTargetAgentMode 验证 agent 模式下探测被下发，结果字段原样映射。
func TestProbeOnTargetAgentMode(t *testing.T) {
	st := newProbeTestStore(t)
	serverID := createProbeServer(t, st, store.ModeAgent)
	agent := &fakeProbeDispatcher{result: protocol.ProbeResult{OK: true, LatencyMS: 21, StatusCode: 200, TS: 999}}
	ex := NewRoutingExecutor(st, agent, nil, probeTestLogger())

	spec := checker.Spec{Kind: checker.KindHTTP, Target: "http://10.0.0.9/health", TimeoutS: 4,
		Params: map[string]any{"probe_via": "agent", "expect_status": 200}}

	res := ex.ProbeOnTarget(context.Background(), serverID, spec)
	if agent.calls != 1 {
		t.Fatalf("下发次数 = %d，期望 1", agent.calls)
	}
	if agent.lastSrv != serverID {
		t.Errorf("serverID = %q，期望 %q", agent.lastSrv, serverID)
	}
	if agent.last.Kind != "http" || agent.last.Target != "http://10.0.0.9/health" || agent.last.TimeoutS != 4 {
		t.Errorf("探测请求未正确转换: %+v", agent.last)
	}
	if !res.OK || res.LatencyMS != 21 || res.StatusCode != 200 || res.TS != 999 {
		t.Errorf("结果映射不匹配: %+v", res)
	}
}

// TestProbeOnTargetSSHMode 验证 SSH 接入明确失败（不静默退化）。
func TestProbeOnTargetSSHMode(t *testing.T) {
	st := newProbeTestStore(t)
	serverID := createProbeServer(t, st, store.ModeSSH)
	agent := &fakeProbeDispatcher{}
	ex := NewRoutingExecutor(st, agent, nil, probeTestLogger())

	res := ex.ProbeOnTarget(context.Background(), serverID, checker.Spec{Kind: checker.KindTCP, Target: "10.0.0.9:3306", TimeoutS: 3})
	if res.OK {
		t.Fatal("SSH 接入不应返回成功")
	}
	if !strings.Contains(res.Message, "SSH") || !strings.Contains(res.Message, "agent 代检") {
		t.Errorf("错误信息应说明原因，实际: %q", res.Message)
	}
	if agent.calls != 0 {
		t.Errorf("SSH 接入不应下发任务，实际 %d 次", agent.calls)
	}
	if res.TS == 0 {
		t.Error("失败结果也要带时间戳")
	}
}

// TestProbeOnTargetDispatchError 验证下发失败被转成失败结果而不是丢失。
func TestProbeOnTargetDispatchError(t *testing.T) {
	st := newProbeTestStore(t)
	serverID := createProbeServer(t, st, store.ModeAgent)
	agent := &fakeProbeDispatcher{err: errors.New("服务器离线")}
	ex := NewRoutingExecutor(st, agent, nil, probeTestLogger())

	res := ex.ProbeOnTarget(context.Background(), serverID, checker.Spec{Kind: checker.KindPing, Target: "8.8.8.8", TimeoutS: 3})
	if res.OK || !strings.Contains(res.Message, "agent 代检失败") {
		t.Errorf("期望带原因的失败结果，实际: %+v", res)
	}
}

// TestProbeOnTargetMissingTS 验证 agent 没给时间戳时由控制端补齐。
func TestProbeOnTargetMissingTS(t *testing.T) {
	st := newProbeTestStore(t)
	serverID := createProbeServer(t, st, store.ModeAgent)
	agent := &fakeProbeDispatcher{result: protocol.ProbeResult{OK: false, Message: "连接被拒绝"}}
	ex := NewRoutingExecutor(st, agent, nil, probeTestLogger())

	before := time.Now().UnixMilli()
	res := ex.ProbeOnTarget(context.Background(), serverID, checker.Spec{Kind: checker.KindTCP, Target: "x:1", TimeoutS: 3})
	if res.TS < before {
		t.Errorf("时间戳未补齐: %d < %d", res.TS, before)
	}
	if res.OK || res.Message != "连接被拒绝" {
		t.Errorf("结果应保留 agent 的失败原因: %+v", res)
	}
}

// TestNormalizeCheckParamsProbeVia 覆盖 API 层对 probe_via 的校验与归一。
func TestNormalizeCheckParamsProbeVia(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		in      map[string]any
		want    string
		wantErr bool
	}{
		{name: "缺省不写入", kind: "tcp", in: map[string]any{}, want: ""},
		{name: "显式 agent", kind: "tcp", in: map[string]any{"probe_via": "agent"}, want: "agent"},
		{name: "大小写与空格归一", kind: "http", in: map[string]any{"probe_via": "  AGENT "}, want: "agent"},
		{name: "显式 control", kind: "ping", in: map[string]any{"probe_via": "control"}, want: "control"},
		{name: "非法值拒绝", kind: "tcp", in: map[string]any{"probe_via": "server"}, wantErr: true},
		{name: "非字符串拒绝", kind: "tcp", in: map[string]any{"probe_via": 1}, wantErr: true},
		{name: "process 不允许", kind: "process", in: map[string]any{"probe_via": "agent"}, wantErr: true},
		{name: "script 不允许", kind: "script", in: map[string]any{"probe_via": "agent"}, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := normalizeCheckParams(c.kind, c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际通过: %+v", out)
				}
				if !strings.Contains(err.Error(), "probe_via") {
					t.Errorf("错误信息应提到 probe_via: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
			got, _ := out["probe_via"].(string)
			if got != c.want {
				t.Errorf("probe_via = %q，期望 %q", got, c.want)
			}
			// 归一结果必须能重新编码进 params_json。
			if _, err := json.Marshal(out); err != nil {
				t.Errorf("params 无法序列化: %v", err)
			}
		})
	}
}
