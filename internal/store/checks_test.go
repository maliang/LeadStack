package store

import (
	"context"
	"errors"
	"testing"
)

// seedCheck 插入一个测试检测项。
func seedCheck(t *testing.T, st *Store, id, name, kind, target string, enabled bool) *Check {
	t.Helper()
	c := &Check{
		ID: id, Name: name, Kind: kind, Target: target,
		IntervalS: 10, TimeoutS: 10, ParamsJSON: `{"fail_threshold":3}`,
		Enabled: enabled, CreatedAt: NowMS(),
	}
	if err := st.CreateCheck(context.Background(), c); err != nil {
		t.Fatalf("创建检测项 %s 失败: %v", id, err)
	}
	return c
}

// TestCheckCRUD 校验检测项的增删改查与级联删除。
func TestCheckCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	c := seedCheck(t, st, "chk-1", "端口检测", "tcp", "127.0.0.1:22", true)
	got, err := st.GetCheck(ctx, c.ID)
	if err != nil {
		t.Fatalf("查询检测项失败: %v", err)
	}
	if got.Name != "端口检测" || got.Kind != "tcp" || !got.Enabled {
		t.Fatalf("检测项内容不正确: %+v", got)
	}

	name := "改名后"
	interval := 30
	enabled := false
	updated, err := st.UpdateCheck(ctx, c.ID, CheckUpdate{Name: &name, IntervalS: &interval, Enabled: &enabled})
	if err != nil {
		t.Fatalf("更新检测项失败: %v", err)
	}
	if updated.Name != "改名后" || updated.IntervalS != 30 || updated.Enabled {
		t.Fatalf("更新结果不正确: %+v", updated)
	}

	if _, err := st.UpdateCheck(ctx, "missing", CheckUpdate{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("更新不存在的检测项期望 ErrNotFound，实际 %v", err)
	}
	if _, err := st.GetCheck(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("查询不存在的检测项期望 ErrNotFound，实际 %v", err)
	}

	all, err := st.ListChecks(ctx)
	if err != nil {
		t.Fatalf("查询检测项列表失败: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("期望 1 个检测项，实际 %d", len(all))
	}

	// 级联删除：目标 / 结果 / 状态一并清理
	seedServer(t, st, "srv-1", "n1", ModeAgent, "", nil)
	if err := st.SetCheckTargets(ctx, c.ID, []string{"srv-1"}); err != nil {
		t.Fatalf("设置目标失败: %v", err)
	}
	if err := st.InsertCheckResult(ctx, &CheckResult{CheckID: c.ID, ServerID: "srv-1", TS: NowMS(), OK: true}); err != nil {
		t.Fatalf("写入结果失败: %v", err)
	}
	if err := st.UpsertCheckState(ctx, c.ID, "srv-1", true, NowMS()); err != nil {
		t.Fatalf("写入状态失败: %v", err)
	}

	if err := st.DeleteCheck(ctx, c.ID); err != nil {
		t.Fatalf("删除检测项失败: %v", err)
	}
	if targets, err := st.ListCheckTargets(ctx, c.ID); err != nil || len(targets) != 0 {
		t.Fatalf("删除后目标应清空，实际 %v / %v", targets, err)
	}
	if results, err := st.ListCheckResults(ctx, CheckResultFilter{CheckID: c.ID}); err != nil || len(results) != 0 {
		t.Fatalf("删除后结果应清空，实际 %v / %v", results, err)
	}
	if _, err := st.GetCheckState(ctx, c.ID, "srv-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后状态应清空，实际 %v", err)
	}
	if err := st.DeleteCheck(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除期望 ErrNotFound，实际 %v", err)
	}
}

// TestSetCheckTargetsOverwrite 校验目标覆盖式设置与失效状态清理。
func TestSetCheckTargetsOverwrite(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seedServer(t, st, "srv-a", "a", ModeAgent, "", nil)
	seedServer(t, st, "srv-b", "b", ModeSSH, "", nil)
	seedServer(t, st, "srv-c", "c", ModeAgent, "", nil)
	seedCheck(t, st, "chk-1", "c1", "tcp", "127.0.0.1:80", true)

	if err := st.SetCheckTargets(ctx, "chk-1", []string{"srv-a", "srv-b", "srv-a"}); err != nil {
		t.Fatalf("设置目标失败: %v", err)
	}
	targets, err := st.ListCheckTargets(ctx, "chk-1")
	if err != nil {
		t.Fatalf("查询目标失败: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("目标应去重为 2 个，实际 %v", targets)
	}

	// 给 srv-a 写状态，再覆盖目标把它移除，状态应被清理
	if err := st.UpsertCheckState(ctx, "chk-1", "srv-a", true, NowMS()); err != nil {
		t.Fatalf("写入状态失败: %v", err)
	}
	if err := st.SetCheckTargets(ctx, "chk-1", []string{"srv-b", "srv-c"}); err != nil {
		t.Fatalf("覆盖目标失败: %v", err)
	}
	targets, _ = st.ListCheckTargets(ctx, "chk-1")
	if len(targets) != 2 || targets[0] != "srv-b" || targets[1] != "srv-c" {
		t.Fatalf("覆盖后目标不正确: %v", targets)
	}
	if _, err := st.GetCheckState(ctx, "chk-1", "srv-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("被移除目标的旧状态应清理，实际 %v", err)
	}

	if err := st.SetCheckTargets(ctx, "missing", []string{"srv-a"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("给不存在的检测项设置目标期望 ErrNotFound，实际 %v", err)
	}

	counts, err := st.ListCheckTargetCounts(ctx)
	if err != nil {
		t.Fatalf("统计目标数失败: %v", err)
	}
	if counts["chk-1"] != 2 {
		t.Fatalf("目标数期望 2，实际 %d", counts["chk-1"])
	}
}

// TestCheckStateMachine 校验连续失败阈值、恢复与状态起点。
func TestCheckStateMachine(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seedServer(t, st, "srv-1", "n1", ModeAgent, "", nil)
	seedCheck(t, st, "chk-1", "c1", "tcp", "127.0.0.1:80", true)

	base := NowMS()
	// 首次成功：fail=0，since=ts
	if err := st.UpsertCheckState(ctx, "chk-1", "srv-1", true, base); err != nil {
		t.Fatalf("写入状态失败: %v", err)
	}
	stt, err := st.GetCheckState(ctx, "chk-1", "srv-1")
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if !stt.OK || stt.ConsecutiveFail != 0 || stt.Since != base {
		t.Fatalf("首次成功状态不正确: %+v", stt)
	}

	// 连续 3 次失败：计数递增，since 停在第一次失败
	for i := 1; i <= 3; i++ {
		ts := base + int64(i)*10000
		if err := st.UpsertCheckState(ctx, "chk-1", "srv-1", false, ts); err != nil {
			t.Fatalf("第 %d 次失败写入状态出错: %v", i, err)
		}
		stt, err = st.GetCheckState(ctx, "chk-1", "srv-1")
		if err != nil {
			t.Fatalf("读取状态失败: %v", err)
		}
		if stt.ConsecutiveFail != i {
			t.Fatalf("第 %d 次失败后期望计数 %d，实际 %d", i, i, stt.ConsecutiveFail)
		}
		if stt.OK {
			t.Fatalf("第 %d 次失败后 ok 应为 false", i)
		}
	}
	if stt.Since != base+10000 {
		t.Fatalf("状态起点应停在首次失败时刻 %d，实际 %d", base+10000, stt.Since)
	}

	// 恢复成功：计数清零，since 刷新为恢复时刻
	recoverTS := base + 100000
	if err := st.UpsertCheckState(ctx, "chk-1", "srv-1", true, recoverTS); err != nil {
		t.Fatalf("恢复写入状态失败: %v", err)
	}
	stt, _ = st.GetCheckState(ctx, "chk-1", "srv-1")
	if !stt.OK || stt.ConsecutiveFail != 0 || stt.Since != recoverTS {
		t.Fatalf("恢复后状态不正确: %+v", stt)
	}

	// last_message 取最新结果摘要，与调用顺序无关
	if err := st.InsertCheckResult(ctx, &CheckResult{
		CheckID: "chk-1", ServerID: "srv-1", TS: recoverTS, OK: true,
		Message: ptrString("端口不可达"),
	}); err != nil {
		t.Fatalf("写入结果失败: %v", err)
	}
	if err := st.UpsertCheckState(ctx, "chk-1", "srv-1", false, recoverTS+1); err != nil {
		t.Fatalf("写入状态失败: %v", err)
	}
	stt, _ = st.GetCheckState(ctx, "chk-1", "srv-1")
	if stt.LastMessage == nil || *stt.LastMessage != "端口不可达" {
		t.Fatalf("last_message 应为最新结果摘要，实际 %v", stt.LastMessage)
	}
}

// TestCheckResultsAndAvailability 校验结果时间线与可用率聚合。
func TestCheckResultsAndAvailability(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seedServer(t, st, "srv-1", "n1", ModeAgent, "", nil)
	seedServer(t, st, "srv-2", "n2", ModeAgent, "", nil)
	seedCheck(t, st, "chk-1", "c1", "tcp", "127.0.0.1:80", true)

	now := NowMS()
	rows := []*CheckResult{
		{CheckID: "chk-1", ServerID: "srv-1", TS: now - 3000, OK: true, LatencyMS: 5},
		{CheckID: "chk-1", ServerID: "srv-1", TS: now - 2000, OK: false, LatencyMS: 0, Message: ptrString("超时")},
		{CheckID: "chk-1", ServerID: "srv-1", TS: now - 1000, OK: true, LatencyMS: 7},
		{CheckID: "chk-1", ServerID: "srv-2", TS: now - 1000, OK: false, LatencyMS: 0},
	}
	for _, r := range rows {
		if err := st.InsertCheckResult(ctx, r); err != nil {
			t.Fatalf("写入结果失败: %v", err)
		}
	}

	// 时间线：默认倒序，可按服务器过滤
	all, err := st.ListCheckResults(ctx, CheckResultFilter{CheckID: "chk-1", Limit: 100})
	if err != nil {
		t.Fatalf("查询结果失败: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("期望 4 条结果，实际 %d", len(all))
	}
	if all[0].TS < all[len(all)-1].TS {
		t.Fatalf("结果应按时间倒序，实际首 %d 末 %d", all[0].TS, all[len(all)-1].TS)
	}

	onlySrv1, err := st.ListCheckResults(ctx, CheckResultFilter{CheckID: "chk-1", ServerID: "srv-1", Limit: 2})
	if err != nil {
		t.Fatalf("按服务器查询失败: %v", err)
	}
	if len(onlySrv1) != 2 {
		t.Fatalf("limit=2 期望 2 条，实际 %d", len(onlySrv1))
	}

	windowed, err := st.ListCheckResults(ctx, CheckResultFilter{CheckID: "chk-1", From: now - 2500, Limit: 100})
	if err != nil {
		t.Fatalf("按时间窗查询失败: %v", err)
	}
	if len(windowed) != 3 {
		t.Fatalf("From 窗口期望 3 条，实际 %d", len(windowed))
	}
	bounded, err := st.ListCheckResults(ctx, CheckResultFilter{
		CheckID: "chk-1", From: now - 2500, To: now - 1500, Limit: 100,
	})
	if err != nil {
		t.Fatalf("按双端时间窗查询失败: %v", err)
	}
	if len(bounded) != 1 || bounded[0].TS != now-2000 {
		t.Fatalf("双端窗口期望仅 now-2000 一条，实际 %+v", bounded)
	}

	// 可用率：3 成功 / 4 总
	avail, err := st.Availability(ctx, "chk-1", "", now-10000)
	if err != nil {
		t.Fatalf("统计可用率失败: %v", err)
	}
	if avail.Total != 4 || avail.Up != 2 {
		t.Fatalf("可用率期望 2/4，实际 %d/%d", avail.Up, avail.Total)
	}
	if ratio := avail.Ratio(); ratio < 0.49 || ratio > 0.51 {
		t.Fatalf("可用率比值期望 0.5，实际 %v", ratio)
	}

	srv1Avail, err := st.Availability(ctx, "chk-1", "srv-1", now-10000)
	if err != nil {
		t.Fatalf("按服务器统计可用率失败: %v", err)
	}
	if srv1Avail.Total != 3 || srv1Avail.Up != 2 {
		t.Fatalf("srv-1 可用率期望 2/3，实际 %d/%d", srv1Avail.Up, srv1Avail.Total)
	}

	// 窗口外为 0 样本
	outside, err := st.Availability(ctx, "chk-1", "", now+1000)
	if err != nil {
		t.Fatalf("窗口外统计失败: %v", err)
	}
	if outside.Total != 0 || outside.Ratio() != 0 {
		t.Fatalf("窗口外应无样本，实际 %+v", outside)
	}

	batch, err := st.ListAvailability(ctx, now-10000)
	if err != nil {
		t.Fatalf("批量统计可用率失败: %v", err)
	}
	if batch["chk-1"].Total != 4 {
		t.Fatalf("批量统计期望 4，实际 %+v", batch)
	}

	// 最近结果：每个检测项一条
	latest, err := st.ListLatestCheckResults(ctx, []string{"chk-1"})
	if err != nil {
		t.Fatalf("查询最近结果失败: %v", err)
	}
	if latest["chk-1"].TS != now-1000 {
		t.Fatalf("最近结果时间期望 %d，实际 %d", now-1000, latest["chk-1"].TS)
	}

	// 保留策略
	if n, err := st.DeleteCheckResultsBefore(ctx, now-1500); err != nil {
		t.Fatalf("清理历史结果失败: %v", err)
	} else if n != 2 {
		t.Fatalf("期望清理 2 条，实际 %d", n)
	}
}

// TestListEnabledCheckTargets 校验只返回启用项，并带出服务器 mode。
func TestListEnabledCheckTargets(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seedServer(t, st, "srv-a", "a", ModeAgent, "", nil)
	seedServer(t, st, "srv-b", "b", ModeSSH, "", nil)
	seedCheck(t, st, "chk-on", "on", "process", "nginx", true)
	seedCheck(t, st, "chk-off", "off", "tcp", "127.0.0.1:80", false)

	if err := st.SetCheckTargets(ctx, "chk-on", []string{"srv-a", "srv-b"}); err != nil {
		t.Fatalf("设置目标失败: %v", err)
	}
	if err := st.SetCheckTargets(ctx, "chk-off", []string{"srv-a"}); err != nil {
		t.Fatalf("设置目标失败: %v", err)
	}

	targets, err := st.ListEnabledCheckTargets(ctx)
	if err != nil {
		t.Fatalf("查询启用目标失败: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("期望 2 个目标（禁用项不计），实际 %d", len(targets))
	}
	modes := map[string]string{}
	for _, tg := range targets {
		if tg.Check.ID != "chk-on" {
			t.Fatalf("不应带出禁用检测项: %+v", tg)
		}
		modes[tg.ServerID] = tg.Mode
	}
	if modes["srv-a"] != ModeAgent || modes["srv-b"] != ModeSSH {
		t.Fatalf("mode 未正确带出: %+v", modes)
	}

	// 删除服务器后其目标自动消失（JOIN 语义）
	if err := st.DeleteServer(ctx, "srv-b"); err != nil {
		t.Fatalf("删除服务器失败: %v", err)
	}
	targets, err = st.ListEnabledCheckTargets(ctx)
	if err != nil {
		t.Fatalf("查询启用目标失败: %v", err)
	}
	if len(targets) != 1 || targets[0].ServerID != "srv-a" {
		t.Fatalf("服务器删除后应只剩 1 个目标，实际 %+v", targets)
	}
}

// TestRecoveryClearsLastError 覆盖 task-16：采集/检测恢复成功后必须清空过期的失败文案。
func TestRecoveryClearsLastError(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// ---------- 1) agentless（SSH）通道：失败写 last_error，成功清空 ----------
	seedServer(t, st, "srv-ssh", "ssh-node", ModeSSH, "", nil)

	msg := "SSH 连接失败: connection refused"
	status := "online"
	if err := st.UpdateServerCollectionState(ctx, "srv-ssh", nil, nil, &msg); err != nil {
		t.Fatalf("写入失败原因出错: %v", err)
	}
	srv, err := st.GetServer(ctx, "srv-ssh")
	if err != nil {
		t.Fatalf("读取服务器失败: %v", err)
	}
	if srv.LastError == nil || *srv.LastError != msg {
		t.Fatalf("失败后应写入 last_error，实际 %v", srv.LastError)
	}

	// 连续失败达阈值 → offline 且保留 last_error
	offline := "offline"
	if err := st.UpdateServerCollectionState(ctx, "srv-ssh", &offline, nil, &msg); err != nil {
		t.Fatalf("标记离线出错: %v", err)
	}
	srv, _ = st.GetServer(ctx, "srv-ssh")
	if srv.Status != "offline" || srv.LastError == nil {
		t.Fatalf("离线后应保留 last_error: %+v", srv)
	}

	// 恢复成功：状态 online、last_error 清空
	now := NowMS()
	clear := ""
	if err := st.UpdateServerCollectionState(ctx, "srv-ssh", &status, &now, &clear); err != nil {
		t.Fatalf("写入恢复状态出错: %v", err)
	}
	srv, _ = st.GetServer(ctx, "srv-ssh")
	if srv.Status != "online" {
		t.Fatalf("恢复后状态期望 online，实际 %q", srv.Status)
	}
	if srv.LastError != nil {
		t.Fatalf("恢复成功后 last_error 应被清空（NULL），实际 %q", *srv.LastError)
	}

	// 再次失败 → 重新写入；再次成功 → 再清空（可重复）
	if err := st.UpdateServerCollectionState(ctx, "srv-ssh", nil, nil, &msg); err != nil {
		t.Fatalf("二次失败写入出错: %v", err)
	}
	if srv, _ = st.GetServer(ctx, "srv-ssh"); srv.LastError == nil {
		t.Fatal("二次失败应重新写入 last_error")
	}
	if err := st.UpdateServerCollectionState(ctx, "srv-ssh", &status, &now, &clear); err != nil {
		t.Fatalf("二次恢复写入出错: %v", err)
	}
	if srv, _ = st.GetServer(ctx, "srv-ssh"); srv.LastError != nil {
		t.Fatalf("二次恢复后 last_error 应被清空，实际 %q", *srv.LastError)
	}

	// ---------- 2) agent 通道：心跳是成功信号，应清空 last_error ----------
	seedServer(t, st, "srv-agent", "agent-node", ModeAgent, "", nil)
	if err := st.UpdateServerCollectionState(ctx, "srv-agent", nil, nil, &msg); err != nil {
		t.Fatalf("写入失败原因出错: %v", err)
	}
	if srv, _ = st.GetServer(ctx, "srv-agent"); srv.LastError == nil {
		t.Fatal("agent 模式失败后应写入 last_error")
	}
	if _, err := st.TouchServerHeartbeat(ctx, "srv-agent", now); err != nil {
		t.Fatalf("写入心跳出错: %v", err)
	}
	srv, _ = st.GetServer(ctx, "srv-agent")
	if srv.LastError != nil {
		t.Fatalf("心跳成功后 last_error 应被清空，实际 %q", *srv.LastError)
	}

	// ---------- 3) 检测维度：恢复为 up 时清空 last_message ----------
	seedCheck(t, st, "chk-1", "c1", "tcp", "127.0.0.1:80", true)
	if err := st.InsertCheckResult(ctx, &CheckResult{
		CheckID: "chk-1", ServerID: "srv-agent", TS: now - 1000, OK: false,
		Message: ptrString("TCP 连接失败: connection refused"),
	}); err != nil {
		t.Fatalf("写入检测结果出错: %v", err)
	}
	if err := st.UpsertCheckState(ctx, "chk-1", "srv-agent", false, now-1000); err != nil {
		t.Fatalf("写入检测状态出错: %v", err)
	}
	cst, err := st.GetCheckState(ctx, "chk-1", "srv-agent")
	if err != nil {
		t.Fatalf("读取检测状态出错: %v", err)
	}
	if cst.LastMessage == nil || *cst.LastMessage == "" {
		t.Fatalf("失败后应记录 last_message，实际 %v", cst.LastMessage)
	}
	if cst.ConsecutiveFail != 1 {
		t.Fatalf("连续失败次数期望 1，实际 %d", cst.ConsecutiveFail)
	}

	// 恢复成功：last_message 清空、计数归零
	if err := st.InsertCheckResult(ctx, &CheckResult{
		CheckID: "chk-1", ServerID: "srv-agent", TS: now, OK: true,
	}); err != nil {
		t.Fatalf("写入恢复结果出错: %v", err)
	}
	if err := st.UpsertCheckState(ctx, "chk-1", "srv-agent", true, now); err != nil {
		t.Fatalf("写入恢复状态出错: %v", err)
	}
	cst, _ = st.GetCheckState(ctx, "chk-1", "srv-agent")
	if cst.LastMessage != nil {
		t.Fatalf("恢复为 up 后 last_message 应被清空，实际 %q", *cst.LastMessage)
	}
	if cst.ConsecutiveFail != 0 || !cst.OK {
		t.Fatalf("恢复后应计数归零且 ok=true，实际 %+v", cst)
	}
}
