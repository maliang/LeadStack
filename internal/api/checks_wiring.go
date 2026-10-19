package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/maliang/leadstack/internal/checker"
	"github.com/maliang/leadstack/internal/store"
)

// CheckerStore 把 store 适配成 checker.Store（契约第 14.1 节）。
//
// 适配器里不做业务判断，只做类型转换与 Mode 带出；checker 因此不需要 import store。
type CheckerStore struct {
	st     *store.Store
	logger *slog.Logger
}

// 编译期确认接口实现。
var _ checker.Store = (*CheckerStore)(nil)

// NewCheckerStore 创建 checker.Store 适配器。
func NewCheckerStore(st *store.Store, logger *slog.Logger) *CheckerStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &CheckerStore{st: st, logger: logger}
}

// ListEnabledTargets 返回启用中的 (检测项, 服务器)，并带出服务器 mode 供检测层选执行通道。
func (s *CheckerStore) ListEnabledTargets(ctx context.Context) ([]checker.Target, error) {
	rows, err := s.st.ListEnabledCheckTargets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]checker.Target, 0, len(rows))
	for _, row := range rows {
		out = append(out, checker.Target{
			CheckID:  row.Check.ID,
			ServerID: row.ServerID,
			Mode:     row.Mode,
			Spec: checker.Spec{
				ID:        row.Check.ID,
				Name:      row.Check.Name,
				Kind:      checker.Kind(row.Check.Kind),
				Target:    row.Check.Target,
				IntervalS: row.Check.IntervalS,
				TimeoutS:  row.Check.TimeoutS,
				Params:    decodeParams(row.Check.ParamsJSON),
			},
		})
	}
	return out, nil
}

// SaveResult 写入一条检测结果。
func (s *CheckerStore) SaveResult(ctx context.Context, checkID, serverID string, r checker.Result) error {
	if checkID == "" || serverID == "" {
		return errors.New("保存检测结果: check_id/server_id 为空")
	}
	ts := r.TS
	if ts <= 0 {
		ts = store.NowMS()
	}
	res := &store.CheckResult{
		CheckID:   checkID,
		ServerID:  serverID,
		TS:        ts,
		OK:        r.OK,
		LatencyMS: r.LatencyMS,
	}
	if r.StatusCode != 0 {
		code := r.StatusCode
		res.StatusCode = &code
	}
	if r.Message != "" {
		msg := r.Message
		res.Message = &msg
	}
	return s.st.InsertCheckResult(ctx, res)
}

// UpdateState 推进状态机（连续失败计数、状态起点、最近时间与摘要）。
func (s *CheckerStore) UpdateState(ctx context.Context, checkID, serverID string, ok bool, ts int64) error {
	if ts <= 0 {
		ts = store.NowMS()
	}
	return s.st.UpsertCheckState(ctx, checkID, serverID, ok, ts)
}

// CheckRunnerAdapter 把 checker.Scheduler 适配成 api.CheckRunner。
type CheckRunnerAdapter struct {
	sched  *checker.Scheduler
	logger *slog.Logger
}

// 编译期确认接口实现。
var _ CheckRunner = (*CheckRunnerAdapter)(nil)

// NewCheckRunner 创建调度器适配器；sched 为 nil 时返回 nil（API 会走 503 分支）。
func NewCheckRunner(sched *checker.Scheduler, logger *slog.Logger) *CheckRunnerAdapter {
	if sched == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &CheckRunnerAdapter{sched: sched, logger: logger}
}

// RunOnce 立即执行一次检测。
func (a *CheckRunnerAdapter) RunOnce(ctx context.Context, checkID, serverID string) (CheckRunResult, error) {
	res := a.sched.RunOnce(ctx, checkID, serverID)
	return CheckRunResult{
		OK:         res.OK,
		LatencyMS:  res.LatencyMS,
		StatusCode: res.StatusCode,
		Message:    res.Message,
		TS:         res.TS,
	}, nil
}

// Wake 通知调度器重读检测目标。
func (a *CheckRunnerAdapter) Wake() { a.sched.Wake() }

// RoutingExecutor 按服务器 mode 选择执行通道：
// agent → jobs.Dispatcher（控制端下发 job.exec），ssh → SSHExecutor（SSH Run）。
type RoutingExecutor struct {
	st     *store.Store
	agent  checker.Executor
	ssh    checker.Executor
	logger *slog.Logger
}

// 编译期确认接口实现。
var _ checker.Executor = (*RoutingExecutor)(nil)

// NewRoutingExecutor 创建按模式路由的执行器。
func NewRoutingExecutor(st *store.Store, agentExec, sshExec checker.Executor, logger *slog.Logger) *RoutingExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	return &RoutingExecutor{st: st, agent: agentExec, ssh: sshExec, logger: logger}
}

// Exec 按目标服务器的接入模式选择通道执行命令。
func (e *RoutingExecutor) Exec(ctx context.Context, serverID, command string, timeoutS int) (stdout, stderr string, exitCode int, err error) {
	srv, err := e.st.GetServer(ctx, serverID)
	if err != nil {
		return "", "", -1, fmt.Errorf("查询服务器 %s 失败: %w", serverID, err)
	}
	if srv.Mode == store.ModeSSH {
		if e.ssh == nil {
			return "", "", -1, errors.New("SSH 执行通道未启用")
		}
		return e.ssh.Exec(ctx, serverID, command, timeoutS)
	}
	if e.agent == nil {
		return "", "", -1, errors.New("agent 任务下发通道未启用")
	}
	return e.agent.Exec(ctx, serverID, command, timeoutS)
}
