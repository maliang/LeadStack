package api

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/maliang/leadstack/internal/checker"
	"github.com/maliang/leadstack/internal/store"
)

// TestDisabledCheckRunsOnceAndPersists 停用的检测项点「立即执行」必须真正跑一次并落库。
//
// 这条路径此前是坏的：调度器 lookupTarget 只查启用列表，停用项一律报「未找到检测目标」，
// 且该分支不调用 record ⇒ 零条结果、页面统计全是 0。停用只该停掉定时探测，手动执行必须有效。
func TestDisabledCheckRunsOnceAndPersists(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	jwt := env.login()

	srv := env.createServerVia(jwt, map[string]any{"name": "停用执行", "host": "10.10.0.9", "mode": "agent"})
	check := env.createCheckVia(jwt, map[string]any{
		"name": "停用检测", "kind": "tcp", "target": "127.0.0.1:1", "interval_s": 3600,
		"timeout_s": 2, "enabled": false, "server_ids": []string{srv.ID},
	})
	if check.Enabled {
		t.Fatalf("夹具要求停用状态，实际 enabled=true")
	}

	// 启用列表里没有它（定时循环据此跳过），但精确查得到。
	adapter := NewCheckerStore(env.st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	enabled, err := adapter.ListEnabledTargets(ctx)
	if err != nil {
		t.Fatalf("ListEnabledTargets 失败: %v", err)
	}
	for _, tg := range enabled {
		if tg.CheckID == check.ID {
			t.Fatalf("停用检测项不应出现在启用目标里: %+v", tg)
		}
	}
	target, found, err := adapter.GetCheckTarget(ctx, check.ID, srv.ID)
	if err != nil || !found {
		t.Fatalf("精确查停用目标失败: found=%v err=%v", found, err)
	}
	if target.CheckID != check.ID || target.ServerID != srv.ID || target.Mode != "agent" {
		t.Fatalf("目标内容不正确: %+v", target)
	}
	if _, found, err := adapter.GetCheckTarget(ctx, check.ID, "srv-missing"); err != nil || found {
		t.Fatalf("非目标服务器应返回 found=false、err=nil，实际 found=%v err=%v", found, err)
	}

	// 手动执行一次：必须真正探测（本用例目标端口必然拒绝），并把结果写进库。
	sched := checker.NewScheduler(checker.Deps{Store: adapter, Exec: &dialOnlyExec{t: t}})
	res := sched.RunOnce(ctx, check.ID, srv.ID)
	if strings.Contains(res.Message, "未找到检测目标") {
		t.Fatalf("停用检测项手动执行不应报「未找到检测目标」: %+v", res)
	}
	if res.TS <= 0 {
		t.Fatalf("手动执行应返回带时间戳的结果: %+v", res)
	}

	rows, err := env.st.ListCheckResults(ctx, store.CheckResultFilter{CheckID: check.ID, Limit: 10})
	if err != nil {
		t.Fatalf("查询检测结果失败: %v", err)
	}
	if len(rows) != 1 || rows[0].ServerID != srv.ID {
		t.Fatalf("停用检测项手动执行必须落库一条结果，实际 %+v", rows)
	}
	if rows[0].OK {
		t.Fatalf("127.0.0.1:1 必然拒绝连接，结果不应是成功: %+v", rows[0])
	}

	// 统计（results_24h）随之从 0 变 1。
	stats, err := env.st.ListAvailability(ctx, store.NowMS()-24*3600*1000, []string{srv.ID})
	if err != nil {
		t.Fatalf("查询可用率失败: %v", err)
	}
	if stat, ok := stats[check.ID]; !ok || stat.Total != 1 {
		t.Fatalf("范围内统计应为 1 条，实际 %+v", stats)
	}

	// 手动执行不得改变 enabled 字段——停用状态原样保留。
	reread, err := env.st.GetCheck(ctx, check.ID)
	if err != nil {
		t.Fatalf("复查检测项失败: %v", err)
	}
	if reread.Enabled {
		t.Fatalf("手动执行不得把检测项改成启用: %+v", reread)
	}
}

// dialOnlyExec 是占位执行器：kind=tcp 的探测由控制端直连，不需要它执行任何命令。
type dialOnlyExec struct{ t *testing.T }

func (e *dialOnlyExec) Exec(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
	e.t.Helper()
	return "", "", 0, nil
}
