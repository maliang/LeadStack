package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/maliang/leadstack/internal/checker"
	"github.com/maliang/leadstack/internal/store"
	"github.com/maliang/leadstack/pkg/protocol"
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

// targetFromRow 把 store 的一行目标映射成 checker.Target（含服务器 mode 与规格）。
func targetFromRow(row store.EnabledCheckTarget) checker.Target {
	return checker.Target{
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
	}
}

// ListEnabledTargets 返回启用中的 (检测项, 服务器)，并带出服务器 mode 供检测层选执行通道。
func (s *CheckerStore) ListEnabledTargets(ctx context.Context) ([]checker.Target, error) {
	rows, err := s.st.ListEnabledCheckTargets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]checker.Target, 0, len(rows))
	for _, row := range rows {
		out = append(out, targetFromRow(row))
	}
	return out, nil
}

// GetCheckTarget 按 (检测项, 服务器) 精确查一个目标，忽略检测项的 enabled 标志。
//
// 手动「立即执行」走这条路：停用只停掉定时探测，用户点了执行仍要真正跑一次并落库。
func (s *CheckerStore) GetCheckTarget(ctx context.Context, checkID, serverID string) (checker.Target, bool, error) {
	row, err := s.st.GetCheckTarget(ctx, checkID, serverID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return checker.Target{}, false, nil
		}
		return checker.Target{}, false, err
	}
	return targetFromRow(*row), true, nil
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

// 编译期确认接口实现（Executor = 执行命令，AgentProber = 目标机代检）。
var (
	_ checker.Executor    = (*RoutingExecutor)(nil)
	_ checker.AgentProber = (*RoutingExecutor)(nil)
)

// dispatcherProber 是 jobs.Dispatcher 的探测能力（避免 api 直接依赖具体类型）。
type dispatcherProber interface {
	Probe(ctx context.Context, serverID string, req protocol.ProbeRequest) (protocol.ProbeResult, error)
}

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

// ProbeOnTarget 把一次探测下发到目标机上的 agent 原生执行（checker.AgentProber）。
//
// 用途：目标机没有入向端口（控制端连不进去）时，tcp/http/ping 仍要能检测。
// SSH 接入的服务器没有 agent 通道，这里明确失败而不是静默改走控制端直连
// ——后者在受限网络下必然失败，但错误信息会指向「目标不可达」，很容易误导排查。
func (e *RoutingExecutor) ProbeOnTarget(ctx context.Context, serverID string, spec checker.Spec) checker.Result {
	now := time.Now().UnixMilli()

	srv, err := e.st.GetServer(ctx, serverID)
	if err != nil {
		return checker.Result{Message: fmt.Sprintf("查询服务器 %s 失败: %v", serverID, err), TS: now}
	}
	if srv.Mode == store.ModeSSH {
		return checker.Result{TS: now, Message: "该服务器以 SSH 方式接入，无法由 agent 代检：请改用控制端直连，或把该服务器改为 agent 接入"}
	}
	if e.agent == nil {
		return checker.Result{TS: now, Message: "agent 任务下发通道未启用，无法代检"}
	}
	prober, ok := e.agent.(dispatcherProber)
	if !ok {
		return checker.Result{TS: now, Message: "当前执行通道不支持目标机代检"}
	}

	res, err := prober.Probe(ctx, serverID, protocol.ProbeRequest{
		Kind:     string(spec.Kind),
		Target:   spec.Target,
		TimeoutS: spec.TimeoutS,
		Params:   spec.Params,
	})
	if err != nil {
		return checker.Result{TS: now, Message: fmt.Sprintf("agent 代检失败: %v", err)}
	}

	out := checker.Result{
		OK:         res.OK,
		LatencyMS:  res.LatencyMS,
		StatusCode: res.StatusCode,
		Message:    res.Message,
		TS:         res.TS,
	}
	if out.TS == 0 {
		out.TS = now
	}
	e.logger.Debug("目标机代检完成", "server_id", serverID, "kind", spec.Kind, "ok", out.OK, "latency_ms", out.LatencyMS)
	return out
}
