package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/maliang/leadstack/internal/store"
	"github.com/maliang/leadstack/pkg/protocol"
)

// createCheckVia 通过 REST 创建检测项并返回其载荷。
func (e *testEnv) createCheckVia(jwt string, body map[string]any) checkPayload {
	e.t.Helper()
	status, raw := e.request(http.MethodPost, "/api/v1/checks", jwt, body)
	if status != http.StatusCreated {
		e.t.Fatalf("创建检测项失败: status=%d body=%s", status, raw)
	}
	var out struct {
		Data checkPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("解析检测项响应失败: %v", err)
	}
	return out.Data
}

// TestChecksAuthAndCRUD 覆盖验收 9（401）与检测项 CRUD/目标覆盖/校验。
func TestChecksAuthAndCRUD(t *testing.T) {
	env := newTestEnv(t)

	// 未带 token → 401
	status, _ := env.request(http.MethodGet, "/api/v1/checks", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("未带 token 访问 /checks 期望 401，实际 %d", status)
	}

	jwt := env.login()
	srvA := env.createServerVia(jwt, map[string]any{"name": "ca", "host": "10.1.0.1", "mode": "agent"})
	srvB := env.createServerVia(jwt, map[string]any{"name": "cb", "host": "10.1.0.2", "mode": "ssh"})

	// 创建（带目标）
	created := env.createCheckVia(jwt, map[string]any{
		"name": "devssh 端口", "kind": "tcp", "target": "127.0.0.1:22",
		"interval_s": 10, "timeout_s": 5, "server_ids": []string{srvA.ID, srvB.ID},
		"params": map[string]any{"fail_threshold": 2},
	})
	if created.ID == "" || created.Kind != "tcp" || created.TargetCount != 2 || created.ServerCount != 2 {
		t.Fatalf("创建结果不正确: %+v", created)
	}
	if created.Status != store.CheckStatusUnknown || created.State != store.CheckStatusUnknown {
		t.Fatalf("无结果时状态应为 unknown，实际 status=%q state=%q", created.Status, created.State)
	}
	if len(created.ServerIDs) != 2 {
		t.Fatalf("server_ids 期望 2 个，实际 %v", created.ServerIDs)
	}
	if threshold, ok := created.Params["fail_threshold"].(float64); !ok || int(threshold) != 2 {
		t.Fatalf("params 未回显 fail_threshold: %+v", created.Params)
	}
	if env.runner.wakeCount() == 0 {
		t.Fatal("创建检测项后应调用 Wake()")
	}

	// 列表
	status, raw := env.request(http.MethodGet, "/api/v1/checks", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询检测项列表失败: %d", status)
	}
	var list struct {
		Data []checkPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("解析列表失败: %v", err)
	}
	if len(list.Data) != 1 || list.Data[0].ID != created.ID {
		t.Fatalf("列表内容不正确: %+v", list.Data)
	}
	if !strings.Contains(string(raw), `"server_count"`) || !strings.Contains(string(raw), `"state"`) {
		t.Fatalf("列表项应同时带 server_count 与 state（前端 检测 约定）: %s", raw)
	}

	// 详情 / 404
	status, raw = env.request(http.MethodGet, "/api/v1/checks/"+created.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询详情失败: %d（body=%s）", status, raw)
	}
	status, _ = env.request(http.MethodGet, "/api/v1/checks/not-exist", jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("不存在的检测项期望 404，实际 %d", status)
	}

	// 覆盖式设置目标
	status, raw = env.request(http.MethodPost, "/api/v1/checks/"+created.ID+"/targets", jwt,
		map[string]any{"server_ids": []string{srvB.ID}})
	if status != http.StatusOK {
		t.Fatalf("设置目标失败: %d（body=%s）", status, raw)
	}
	after := env.getCheck(jwt, created.ID)
	if after.TargetCount != 1 || len(after.ServerIDs) != 1 || after.ServerIDs[0] != srvB.ID {
		t.Fatalf("目标覆盖失败: %+v", after)
	}

	// 目标里的服务器不存在 → 404
	status, _ = env.request(http.MethodPost, "/api/v1/checks/"+created.ID+"/targets", jwt,
		map[string]any{"server_ids": []string{"ghost"}})
	if status != http.StatusNotFound {
		t.Fatalf("绑定不存在的服务器期望 404，实际 %d", status)
	}

	// PATCH：改间隔（低于下限 → 400）、改启用状态
	status, _ = env.request(http.MethodPatch, "/api/v1/checks/"+created.ID, jwt,
		map[string]any{"interval_s": 5})
	if status != http.StatusBadRequest {
		t.Fatalf("低于下限的 interval_s 期望 400，实际 %d", status)
	}
	status, raw = env.request(http.MethodPatch, "/api/v1/checks/"+created.ID, jwt,
		map[string]any{"interval_s": 30, "enabled": false, "name": "改过的名字"})
	if status != http.StatusOK {
		t.Fatalf("更新检测项失败: %d（body=%s）", status, raw)
	}
	patched := env.getCheck(jwt, created.ID)
	if patched.IntervalS != 30 || patched.Enabled || patched.Name != "改过的名字" {
		t.Fatalf("更新结果不正确: %+v", patched)
	}
	status, _ = env.request(http.MethodPatch, "/api/v1/checks/"+created.ID, jwt, map[string]any{})
	if status != http.StatusBadRequest {
		t.Fatalf("空 PATCH 期望 400，实际 %d", status)
	}

	// 参数/目标校验
	badCases := []map[string]any{
		{"name": "x", "kind": "bogus", "target": "127.0.0.1:80"},
		{"name": "x", "kind": "http", "target": "127.0.0.1/healthz"}, // 缺 scheme
		{"name": "x", "kind": "tcp", "target": "127.0.0.1"},          // 缺端口
		{"name": "x", "kind": "tcp", "target": "127.0.0.1:99999"},    // 端口越界
		{"name": "x", "kind": "tcp", "target": "127.0.0.1:80", "params": map[string]any{"fail_threshold": 0}},
		{"name": "", "kind": "tcp", "target": "127.0.0.1:80"},
	}
	for _, body := range badCases {
		status, raw := env.request(http.MethodPost, "/api/v1/checks", jwt, body)
		if status != http.StatusBadRequest {
			t.Fatalf("非法创建请求 %v 期望 400，实际 %d（body=%s）", body, status, raw)
		}
	}

	// 删除（连带结果与状态）
	status, raw = env.request(http.MethodDelete, "/api/v1/checks/"+created.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("删除检测项失败: %d（body=%s）", status, raw)
	}
	status, _ = env.request(http.MethodDelete, "/api/v1/checks/"+created.ID, jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("重复删除期望 404，实际 %d", status)
	}
}

// getCheck 读取检测项详情。
func (e *testEnv) getCheck(jwt, id string) checkPayload {
	e.t.Helper()
	status, raw := e.request(http.MethodGet, "/api/v1/checks/"+id, jwt, nil)
	if status != http.StatusOK {
		e.t.Fatalf("查询检测项失败: status=%d body=%s", status, raw)
	}
	var out struct {
		Data checkPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("解析检测项失败: %v", err)
	}
	return out.Data
}

// TestCheckStatusAndAvailability 覆盖状态口径（up/down/unknown）与可用率聚合。
func TestCheckStatusAndAvailability(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	srv := env.createServerVia(jwt, map[string]any{"name": "s1", "host": "10.2.0.1", "mode": "agent"})
	check := env.createCheckVia(jwt, map[string]any{
		"name": "阈值 2", "kind": "tcp", "target": "127.0.0.1:81", "interval_s": 10,
		"params": map[string]any{"fail_threshold": 2}, "server_ids": []string{srv.ID},
	})

	ctx := context.Background()
	now := store.NowMS()

	// 1 次成功 + 2 次失败 → 连续失败 2 ≥ 阈值 2 → down
	seedCheckResult(t, env, check.ID, srv.ID, now-3000, true, "ok")
	seedCheckResult(t, env, check.ID, srv.ID, now-2000, false, "端口不可达")
	seedCheckResult(t, env, check.ID, srv.ID, now-1000, false, "端口不可达")
	if err := env.st.UpsertCheckState(ctx, check.ID, srv.ID, true, now-3000); err != nil {
		t.Fatalf("写入状态失败: %v", err)
	}
	if err := env.st.UpsertCheckState(ctx, check.ID, srv.ID, false, now-2000); err != nil {
		t.Fatalf("写入状态失败: %v", err)
	}
	if err := env.st.UpsertCheckState(ctx, check.ID, srv.ID, false, now-1000); err != nil {
		t.Fatalf("写入状态失败: %v", err)
	}

	got := env.getCheck(jwt, check.ID)
	if got.Status != store.CheckStatusDown {
		t.Fatalf("连续失败达阈值后期望 down，实际 %q", got.Status)
	}
	// §15.1：列表项需带 consecutive_fail，供前端展示「2/3」进度
	if got.ConsecutiveFail != 2 {
		t.Fatalf("consecutive_fail 期望 2，实际 %d", got.ConsecutiveFail)
	}
	// 列表接口同样要带该字段
	checkListRaw := func() string {
		status, raw := env.request(http.MethodGet, "/api/v1/checks", jwt, nil)
		if status != http.StatusOK {
			t.Fatalf("查询检测项列表失败: %d", status)
		}
		return string(raw)
	}()
	if !strings.Contains(checkListRaw, `"consecutive_fail":2`) {
		t.Fatalf("列表项应带 consecutive_fail: %s", checkListRaw)
	}
	if got.LastResult == nil || got.LastResult.OK {
		t.Fatalf("最近结果应是最新那次失败: %+v", got.LastResult)
	}
	if got.Results24h != 3 {
		t.Fatalf("24h 结果数期望 3，实际 %d", got.Results24h)
	}
	if got.Availability24h < 33 || got.Availability24h > 34 {
		t.Fatalf("可用率期望 33.33%%，实际 %v", got.Availability24h)
	}

	// 恢复一次成功 → up
	if err := env.st.UpsertCheckState(ctx, check.ID, srv.ID, true, now); err != nil {
		t.Fatalf("写入状态失败: %v", err)
	}
	seedCheckResult(t, env, check.ID, srv.ID, now, true, "ok")
	recovered := env.getCheck(jwt, check.ID)
	if recovered.Status != store.CheckStatusUp {
		t.Fatalf("恢复后期望 up，实际 %q", recovered.Status)
	}
	if recovered.ConsecutiveFail != 0 {
		t.Fatalf("恢复后 consecutive_fail 应归零，实际 %d", recovered.ConsecutiveFail)
	}
	// 恢复成功后过期的失败文案不应再出现（§15.1：成功时 message 可为空）
	if recovered.LastResult == nil || !recovered.LastResult.OK {
		t.Fatalf("恢复后最近结果应为成功: %+v", recovered.LastResult)
	}
	if strings.Contains(recovered.LastResult.Message, "不可达") {
		t.Fatalf("恢复后仍带着过期的失败文案: %q", recovered.LastResult.Message)
	}

	// summary
	status, raw := env.request(http.MethodGet, "/api/v1/checks/summary", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询 summary 失败: %d", status)
	}
	var summary struct {
		Data struct {
			Total   int            `json:"total"`
			Up      int            `json:"up"`
			Down    int            `json:"down"`
			Unknown int            `json:"unknown"`
			Checks  []checkPayload `json:"checks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("解析 summary 失败: %v", err)
	}
	if summary.Data.Total != 1 || summary.Data.Up != 1 || len(summary.Data.Checks) != 1 {
		t.Fatalf("summary 内容不正确: %+v", summary.Data)
	}

	// 结果时间线（数组）
	status, raw = env.request(http.MethodGet, "/api/v1/checks/"+check.ID+"/results?limit=2", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询结果时间线失败: %d", status)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), `{"data":[`) {
		t.Fatalf("结果时间线应在 data 下返回数组: %s", raw)
	}
	var results struct {
		Data []checkResultPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatalf("解析结果时间线失败: %v（body=%s）", err, raw)
	}
	if len(results.Data) != 2 {
		t.Fatalf("limit=2 期望 2 条，实际 %d", len(results.Data))
	}
	if results.Data[0].TS < results.Data[1].TS {
		t.Fatalf("结果应按时间倒序: %+v", results.Data)
	}

	// 按服务器过滤 + 服务器维度检测项列表
	status, raw = env.request(http.MethodGet, "/api/v1/servers/"+srv.ID+"/checks", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询服务器检测项失败: %d", status)
	}
	var sc struct {
		Data []checkPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatalf("服务器检测项应为数组（元素同 GET /checks）: %v（body=%s）", err, raw)
	}
	if len(sc.Data) != 1 || sc.Data[0].ID != check.ID || sc.Data[0].Status != store.CheckStatusUp {
		t.Fatalf("服务器检测项内容不正确: %+v", sc.Data)
	}
	if sc.Data[0].LastResult == nil || !sc.Data[0].LastResult.OK {
		t.Fatalf("服务器视角最近结果应是最新结果: %+v", sc.Data[0].LastResult)
	}

	status, _ = env.request(http.MethodGet, "/api/v1/servers/ghost/checks", jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("不存在服务器的检测项期望 404，实际 %d", status)
	}
}

// seedCheckResult 直接写入一条检测结果。
func seedCheckResult(t *testing.T, env *testEnv, checkID, serverID string, ts int64, ok bool, msg string) {
	t.Helper()
	r := &store.CheckResult{CheckID: checkID, ServerID: serverID, TS: ts, OK: ok, LatencyMS: 4}
	if msg != "" {
		r.Message = &msg
	}
	if err := env.st.InsertCheckResult(context.Background(), r); err != nil {
		t.Fatalf("写入检测结果失败: %v", err)
	}
}

// TestRunCheckEndpoint 校验 POST /checks/{id}/run 按目标顺序返回逐台结果与汇总。
func TestRunCheckEndpoint(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	srvA := env.createServerVia(jwt, map[string]any{"name": "ra", "host": "10.3.0.1"})
	srvB := env.createServerVia(jwt, map[string]any{"name": "rb", "host": "10.3.0.2"})
	check := env.createCheckVia(jwt, map[string]any{
		"name": "run", "kind": "tcp", "target": "127.0.0.1:22", "interval_s": 10,
		"server_ids": []string{srvA.ID, srvB.ID},
	})

	status, raw := env.request(http.MethodPost, "/api/v1/checks/"+check.ID+"/run", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("立即执行失败: %d（body=%s）", status, raw)
	}
	var out struct {
		Data struct {
			Results []checkRunItem `json:"results"`
			Summary struct {
				OK     int `json:"ok"`
				Failed int `json:"failed"`
			} `json:"summary"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析执行结果失败: %v", err)
	}
	if len(out.Data.Results) != 2 {
		t.Fatalf("期望 2 条逐台结果，实际 %d", len(out.Data.Results))
	}
	// 结果顺序必须与 GET /checks 暴露的 server_ids 顺序一致（消费方可据此对齐）。
	checkAfter := env.getCheck(jwt, check.ID)
	if len(checkAfter.ServerIDs) != 2 {
		t.Fatalf("server_ids 期望 2 个，实际 %v", checkAfter.ServerIDs)
	}
	for i, serverID := range checkAfter.ServerIDs {
		if out.Data.Results[i].ServerID != serverID {
			t.Fatalf("第 %d 条结果应与 server_ids[%d]=%s 一致，实际 %+v",
				i, i, serverID, out.Data.Results)
		}
	}
	// 两台机器都在结果里（顺序按 server_id 升序，是稳定确定的）
	seen := map[string]bool{out.Data.Results[0].ServerID: true, out.Data.Results[1].ServerID: true}
	if !seen[srvA.ID] || !seen[srvB.ID] {
		t.Fatalf("两台目标都应出现在结果中: %+v", out.Data.Results)
	}
	if out.Data.Summary.OK != 2 || out.Data.Summary.Failed != 0 {
		t.Fatalf("汇总不正确: %+v", out.Data.Summary)
	}
	if out.Data.Results[0].LatencyMS == 0 || !out.Data.Results[0].OK {
		t.Fatalf("结果应带延迟且成功: %+v", out.Data.Results[0])
	}
	if !strings.Contains(string(raw), `"status_code"`) && !strings.Contains(string(raw), `"summary"`) {
		t.Fatalf("run 响应应含 §15.1 约定的字段: %s", raw)
	}

	// 无目标 → 空结果
	empty := env.createCheckVia(jwt, map[string]any{"name": "empty", "kind": "ping", "target": "127.0.0.1", "interval_s": 10})
	status, raw = env.request(http.MethodPost, "/api/v1/checks/"+empty.ID+"/run", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("空目标执行失败: %d", status)
	}
	if !strings.Contains(string(raw), `"results":[]`) {
		t.Fatalf("空目标应返回空数组: %s", raw)
	}

	// 调度器未装配 → 503
	env.api.checks = nil
	status, _ = env.request(http.MethodPost, "/api/v1/checks/"+check.ID+"/run", jwt, nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("调度器未装配期望 503，实际 %d", status)
	}
	env.api.checks = env.runner
}

// TestMetricsDegradeLargeRawRange 覆盖验收 8：跨度 > 6h 的 raw 自动降级为 1m。
func TestMetricsDegradeLargeRawRange(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	srv := env.createServerVia(jwt, map[string]any{"name": "m2", "host": "10.4.0.1"})

	now := store.NowMS()
	base := (now/60000)*60000 - 120000
	if _, err := env.st.InsertSamples(context.Background(), srv.ID, []store.MetricSample{
		{Metric: "cpu.pct", TS: base, Value: 10},
		{Metric: "cpu.pct", TS: base + 15000, Value: 20},
	}); err != nil {
		t.Fatalf("写入采样失败: %v", err)
	}
	if _, err := env.st.RollupOnce(context.Background(), store.DefaultRollupLimit); err != nil {
		t.Fatalf("降采样失败: %v", err)
	}

	// 1 小时窗口：raw 不降级
	status, raw := env.request(http.MethodGet,
		"/api/v1/servers/"+srv.ID+"/metrics?from="+itoa(now-3600*1000)+"&to="+itoa(now)+"&step=raw", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询 metrics 失败: %d", status)
	}
	if !strings.Contains(string(raw), `"step":"raw"`) {
		t.Fatalf("1 小时窗口不应降级: %s", raw)
	}

	// 8 小时窗口：raw 自动降级为 1m
	status, raw = env.request(http.MethodGet,
		"/api/v1/servers/"+srv.ID+"/metrics?from="+itoa(now-8*3600*1000)+"&to="+itoa(now)+"&step=raw", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询大范围 metrics 失败: %d", status)
	}
	var out struct {
		Data metricsResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 metrics 响应失败: %v", err)
	}
	if out.Data.Step != store.RollupBucketMinute {
		t.Fatalf("8 小时 raw 窗口应降级为 1m，实际 step=%q（body=%s）", out.Data.Step, raw)
	}
	if len(out.Data.Series) == 0 {
		t.Fatalf("降级后应返回 1m 汇总序列: %s", raw)
	}

	// 显式 step=1m 不受影响
	status, raw = env.request(http.MethodGet,
		"/api/v1/servers/"+srv.ID+"/metrics?from="+itoa(now-8*3600*1000)+"&to="+itoa(now)+"&step=1m", jwt, nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"step":"1m"`) {
		t.Fatalf("显式 1m 查询异常: status=%d body=%s", status, raw)
	}
}

// TestJobFrameRouting 覆盖 检测 关键链路：job.ack/job.log/job.done 真正转给下发器，断开时 FailServer。
func TestJobFrameRouting(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	token := env.createEnrollToken(jwt, "")
	conn, _, err := env.dialAgent(token)
	if err != nil {
		t.Fatalf("连接 agent WebSocket 失败: %v", err)
	}

	sendFrame(t, conn, protocol.TypeHello, protocol.Hello{Hostname: "job-agent", OS: "linux", Arch: "amd64"})
	ack := readFrame(t, conn, 5*time.Second)
	if ack.Type != protocol.TypeHelloAck {
		t.Fatalf("期望 hello_ack，实际 %q", ack.Type)
	}
	servers := env.listServers(jwt)
	if len(servers) != 1 {
		t.Fatalf("期望 1 台服务器，实际 %d", len(servers))
	}
	serverID := servers[0].ID

	runID := "run-123"
	sendFrame(t, conn, protocol.TypeJobAck, protocol.JobAck{RunID: runID})
	sendFrame(t, conn, protocol.TypeJobLog, protocol.JobLog{RunID: runID, Seq: 1, Stream: "stdout", Chunk: "hello\n"})
	sendFrame(t, conn, protocol.TypeJobLog, protocol.JobLog{RunID: runID, Seq: 2, Stream: "stderr", Chunk: "warn\n"})
	sendFrame(t, conn, protocol.TypeJobDone, protocol.JobDone{RunID: runID, ExitCode: 0})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		acks, logs, dones, _ := env.jobs.counts()
		if acks >= 1 && logs >= 2 && dones >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	acks, logs, dones, _ := env.jobs.counts()
	if acks != 1 {
		t.Fatalf("job.ack 应被路由 1 次，实际 %d", acks)
	}
	if logs != 2 {
		t.Fatalf("job.log 应被路由 2 次，实际 %d", logs)
	}
	if dones != 1 {
		t.Fatalf("job.done 应被路由 1 次，实际 %d", dones)
	}
	env.jobs.mu.Lock()
	if !strings.HasPrefix(env.jobs.acks[0], serverID+"/"+runID) {
		t.Fatalf("job.ack 的 serverID/runID 不正确: %v", env.jobs.acks)
	}
	if !strings.Contains(env.jobs.logs[0], "stdout/hello\n") {
		t.Fatalf("job.log 内容不正确: %v", env.jobs.logs)
	}
	if !strings.Contains(env.jobs.dones[0], "/0/") {
		t.Fatalf("job.done 退出码不正确: %v", env.jobs.dones)
	}
	env.jobs.mu.Unlock()

	// 未知类型仍不得断开连接：发一帧未知类型后连接应仍然可用
	sendFrame(t, conn, "future.ext", map[string]any{"x": 1})
	sendFrame(t, conn, protocol.TypeJobAck, protocol.JobAck{RunID: "run-456"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if acks, _, _, _ := env.jobs.counts(); acks >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if acks, _, _, _ := env.jobs.counts(); acks != 2 {
		t.Fatalf("未知类型不应断开连接，ack 次数期望 2，实际 %d", acks)
	}

	// 断开连接 → FailServer 唤醒等待者
	if err := conn.Close(); err != nil {
		t.Fatalf("关闭连接失败: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, _, failed := env.jobs.counts(); failed >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, _, _, failed := env.jobs.counts(); failed != 1 {
		t.Fatalf("连接断开应调用 FailServer 1 次，实际 %d", failed)
	}
}

// TestJobFramesIgnoredWithoutRouter 校验未装配下发器时保持 基础 行为（忽略但不断连）。
func TestJobFramesIgnoredWithoutRouter(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	env.api.jobs = nil

	token := env.createEnrollToken(jwt, "")
	conn, _, err := env.dialAgent(token)
	if err != nil {
		t.Fatalf("连接 agent WebSocket 失败: %v", err)
	}
	defer func() { _ = conn.Close() }()

	sendFrame(t, conn, protocol.TypeHello, protocol.Hello{Hostname: "no-router", OS: "linux", Arch: "amd64"})
	if env := readFrame(t, conn, 5*time.Second); env.Type != protocol.TypeHelloAck {
		t.Fatalf("期望 hello_ack，实际 %q", env.Type)
	}
	sendFrame(t, conn, protocol.TypeJobAck, protocol.JobAck{RunID: "r1"})
	sendFrame(t, conn, protocol.TypeHeartbeat, protocol.Heartbeat{CPUPct: 1})

	// 心跳仍被处理 → 连接未断开
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := env.listServers(jwt); len(s) == 1 && s[0].Status == protocol.StatusOnline {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("未装配下发器时连接应保持可用")
}

// checksSummaryPayload 是 GET /checks/summary 的响应体。
type checksSummaryPayload struct {
	Total   int            `json:"total"`
	Up      int            `json:"up"`
	Down    int            `json:"down"`
	Unknown int            `json:"unknown"`
	Checks  []checkPayload `json:"checks"`
}

// getChecksSummary 查询检测项概览。
func (e *testEnv) getChecksSummary(jwt string) checksSummaryPayload {
	e.t.Helper()
	status, raw := e.request(http.MethodGet, "/api/v1/checks/summary", jwt, nil)
	if status != http.StatusOK {
		e.t.Fatalf("查询 checks summary 失败: %d（body=%s）", status, raw)
	}
	var out struct {
		Data checksSummaryPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("解析 checks summary 失败: %v（body=%s）", err, raw)
	}
	return out.Data
}

// checksListIDs 返回 GET /checks 的 ID 列表。
func (e *testEnv) checksListIDs(jwt string) []string {
	e.t.Helper()
	status, raw := e.request(http.MethodGet, "/api/v1/checks", jwt, nil)
	if status != http.StatusOK {
		e.t.Fatalf("查询检测项列表失败: %d（body=%s）", status, raw)
	}
	var out struct {
		Data []checkPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("解析检测项列表失败: %v（body=%s）", err, raw)
	}
	ids := make([]string, 0, len(out.Data))
	for _, c := range out.Data {
		ids = append(ids, c.ID)
	}
	return ids
}

// TestChecksSummaryScopedToVisibleChecks 覆盖 A12/A14：summary 必须先按资源范围过滤再计数，
// 口径与 GET /checks 完全一致；全局角色返回与改动前逐字段一致。
func TestChecksSummaryScopedToVisibleChecks(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	admin := env.login()
	now := store.NowMS()

	srvIn := env.createServerVia(admin, map[string]any{"name": "sum-in", "host": "10.20.0.1"})
	srvOut := env.createServerVia(admin, map[string]any{"name": "sum-out", "host": "10.20.0.2"})
	groupID := env.createGroupVia(admin, "汇总范围组")
	env.setGroupMembers(admin, groupID, []string{srvIn.ID})

	roleKey := env.createRoleVia(admin, "汇总只读", []string{"server.read", "check.read"},
		map[string]any{"mode": "groups", "group_ids": []string{groupID}, "server_ids": []string{}})
	env.createUserVia(admin, "sumviewer", "SumPass-123", roleKey)
	scoped := env.loginAs(t, "sumviewer", "SumPass-123")

	inOnly := env.createCheckVia(admin, map[string]any{
		"name": "范围内", "kind": "tcp", "target": "127.0.0.1:2201", "interval_s": 30,
		"server_ids": []string{srvIn.ID},
	})
	both := env.createCheckVia(admin, map[string]any{
		"name": "跨范围", "kind": "tcp", "target": "127.0.0.1:2202", "interval_s": 30,
		"params": map[string]any{"fail_threshold": 2}, "server_ids": []string{srvIn.ID, srvOut.ID},
	})
	outOnly := env.createCheckVia(admin, map[string]any{
		"name": "范围外", "kind": "tcp", "target": "127.0.0.1:2203", "interval_s": 30,
		"server_ids": []string{srvOut.ID},
	})
	noTarget := env.createCheckVia(admin, map[string]any{
		"name": "无目标", "kind": "ping", "target": "127.0.0.1", "interval_s": 30,
		"server_ids": []string{},
	})

	// 状态：inOnly → up；both（范围内那台）→ down（连续失败达阈值 2）；其余 unknown。
	if err := env.st.UpsertCheckState(ctx, inOnly.ID, srvIn.ID, true, now); err != nil {
		t.Fatalf("写入状态失败: %v", err)
	}
	for i, ts := range []int64{now - 2000, now - 1000} {
		if err := env.st.UpsertCheckState(ctx, both.ID, srvIn.ID, false, ts); err != nil {
			t.Fatalf("写入状态失败(%d): %v", i, err)
		}
	}

	// 全局角色：全量，与改动前一致（A14）。
	adminSummary := env.getChecksSummary(admin)
	adminListIDs := env.checksListIDs(admin)
	if adminSummary.Total != 4 || len(adminSummary.Checks) != 4 || len(adminListIDs) != 4 {
		t.Fatalf("全局角色应看到全部 4 个检测项：summary=%d checks=%d list=%d",
			adminSummary.Total, len(adminSummary.Checks), len(adminListIDs))
	}
	if adminSummary.Up != 1 || adminSummary.Down != 1 || adminSummary.Unknown != 2 {
		t.Fatalf("全局角色计数不正确: %+v", adminSummary)
	}
	if adminSummary.Up+adminSummary.Down+adminSummary.Unknown != adminSummary.Total {
		t.Fatalf("up/down/unknown 之和必须等于 total: %+v", adminSummary)
	}
	if adminSummary.Total != len(adminListIDs) {
		t.Fatalf("total 必须与列表条数一致：total=%d list=%d", adminSummary.Total, len(adminListIDs))
	}

	// 受限角色：checks[] 只含范围内目标，计数与列表同口径（A12）。
	scopedSummary := env.getChecksSummary(scoped)
	scopedListIDs := env.checksListIDs(scoped)
	if scopedSummary.Total != 2 || len(scopedSummary.Checks) != 2 {
		t.Fatalf("受限角色应只看到 2 个检测项，实际 %+v", scopedSummary)
	}
	if scopedSummary.Total != len(scopedListIDs) {
		t.Fatalf("受限角色 total 必须与列表条数一致：total=%d list=%d",
			scopedSummary.Total, len(scopedListIDs))
	}
	scopedIDs := map[string]bool{}
	for _, c := range scopedSummary.Checks {
		scopedIDs[c.ID] = true
		if !containsString(c.ServerIDs, srvIn.ID) {
			t.Fatalf("受限角色的 checks[] 出现范围外检测项: %+v", c.ServerIDs)
		}
	}
	if !scopedIDs[inOnly.ID] || !scopedIDs[both.ID] {
		t.Fatalf("范围内检测项缺失: %+v", scopedIDs)
	}
	if scopedIDs[outOnly.ID] || scopedIDs[noTarget.ID] {
		t.Fatalf("范围外/无目标检测项不应出现: %+v", scopedIDs)
	}
	if scopedSummary.Up != 1 || scopedSummary.Down != 1 || scopedSummary.Unknown != 0 {
		t.Fatalf("受限角色计数不正确: %+v", scopedSummary)
	}
	if scopedSummary.Up+scopedSummary.Down+scopedSummary.Unknown != scopedSummary.Total {
		t.Fatalf("受限角色 up/down/unknown 之和必须等于 total: %+v", scopedSummary)
	}
	if len(scopedListIDs) != len(scopedSummary.Checks) {
		t.Fatalf("summary 与列表条数不一致：summary=%d list=%d",
			len(scopedSummary.Checks), len(scopedListIDs))
	}

	// 范围内一台服务器都没有 → 200 全 0，checks 必须是 []（不是 null）。
	emptyGroup := env.createGroupVia(admin, "空范围组")
	emptyRole := env.createRoleVia(admin, "空范围只读", []string{"server.read", "check.read"},
		map[string]any{"mode": "groups", "group_ids": []string{emptyGroup}, "server_ids": []string{}})
	env.createUserVia(admin, "emptyviewer", "EmptyPass-123", emptyRole)
	emptyJWT := env.loginAs(t, "emptyviewer", "EmptyPass-123")

	status, raw := env.request(http.MethodGet, "/api/v1/checks/summary", emptyJWT, nil)
	if status != http.StatusOK {
		t.Fatalf("空范围查询 summary 期望 200，实际 %d（body=%s）", status, raw)
	}
	if !strings.Contains(string(raw), `"checks":[]`) {
		t.Fatalf("空范围 checks 必须序列化为 []：%s", raw)
	}
	var zero struct {
		Data checksSummaryPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &zero); err != nil {
		t.Fatalf("解析空范围 summary 失败: %v（body=%s）", err, raw)
	}
	if zero.Data.Total != 0 || zero.Data.Up != 0 || zero.Data.Down != 0 || zero.Data.Unknown != 0 {
		t.Fatalf("空范围统计应全 0，实际 %+v", zero.Data)
	}
}
