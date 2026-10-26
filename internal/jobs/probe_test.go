package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/maliang/leadstack/pkg/protocol"
)

// TestProbeRoundTrip 验证 Kind=probe 的下发与结果解析：
// 探测结果走 job.log(stdout) 回传，Dispatcher 负责拼装并解析成 ProbeResult。
func TestProbeRoundTrip(t *testing.T) {
	f := newFakeSender("srv-1")
	d := setupDispatcher(f, Options{}, func(d *Dispatcher, serverID string, job protocol.JobExec) {
		d.HandleAck(serverID, job.RunID)
		out, _ := json.Marshal(protocol.ProbeResult{OK: true, LatencyMS: 12, StatusCode: 204})
		d.HandleLog(serverID, job.RunID, 0, "stdout", string(out))
		d.HandleDone(serverID, job.RunID, 0, "")
	})

	res, err := d.Probe(context.Background(), "srv-1", protocol.ProbeRequest{
		Kind: "http", Target: "https://example.com/health", TimeoutS: 5,
		Params: map[string]any{"expect_status": 204},
	})
	if err != nil {
		t.Fatalf("Probe 返回错误: %v", err)
	}
	if !res.OK || res.LatencyMS != 12 || res.StatusCode != 204 {
		t.Errorf("结果不匹配: %+v", res)
	}

	job, ok := f.firstJob()
	if !ok {
		t.Fatal("未下发任何任务")
	}
	if job.job.Kind != protocol.JobKindProbe {
		t.Errorf("Kind = %q，期望 %q", job.job.Kind, protocol.JobKindProbe)
	}
	var req protocol.ProbeRequest
	if err := json.Unmarshal([]byte(job.job.Command), &req); err != nil {
		t.Fatalf("Command 不是合法 ProbeRequest: %v", err)
	}
	if req.Kind != "http" || req.Target != "https://example.com/health" || req.TimeoutS != 5 {
		t.Errorf("探测请求不匹配: %+v", req)
	}
	if req.Params["expect_status"] != float64(204) {
		t.Errorf("params 未透传: %+v", req.Params)
	}
	if got := d.pendingCount(); got != 0 {
		t.Errorf("完成后等待表未清空：%d", got)
	}
}

// TestProbeEmptyOutput 验证 agent 没回结果时的错误信息可直接用于排障。
func TestProbeEmptyOutput(t *testing.T) {
	f := newFakeSender("srv-1")
	d := setupDispatcher(f, Options{}, func(d *Dispatcher, serverID string, job protocol.JobExec) {
		d.HandleDone(serverID, job.RunID, 0, "")
	})

	_, err := d.Probe(context.Background(), "srv-1", protocol.ProbeRequest{Kind: "tcp", Target: "127.0.0.1:22"})
	if err == nil {
		t.Fatal("期望返回错误")
	}
	if !strings.Contains(err.Error(), "探测结果") {
		t.Errorf("错误信息应指出缺少探测结果，实际: %v", err)
	}
}

// TestProbeInvalidJSON 验证回传内容不是 ProbeResult 时的报错。
func TestProbeInvalidJSON(t *testing.T) {
	f := newFakeSender("srv-1")
	d := setupDispatcher(f, Options{}, func(d *Dispatcher, serverID string, job protocol.JobExec) {
		d.HandleLog(serverID, job.RunID, 0, "stdout", "not-json")
		d.HandleDone(serverID, job.RunID, 0, "")
	})

	if _, err := d.Probe(context.Background(), "srv-1", protocol.ProbeRequest{Kind: "ping", Target: "8.8.8.8"}); err == nil {
		t.Fatal("期望解析失败返回错误")
	}
}

// TestProbeOffline 验证离线服务器直接失败，不进入等待。
func TestProbeOffline(t *testing.T) {
	f := newFakeSender("srv-1")
	f.setOnline("srv-1", false)
	d := setupDispatcher(f, Options{}, nil)

	if _, err := d.Probe(context.Background(), "srv-1", protocol.ProbeRequest{Kind: "tcp", Target: "x:1"}); !errors.Is(err, ErrServerOffline) {
		t.Errorf("期望 ErrServerOffline，实际: %v", err)
	}
}
