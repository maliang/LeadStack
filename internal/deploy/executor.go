package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// 运行状态（与 §23 的 release_run 状态机一致）。
const (
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusFailed  = "failed"
)

// DefaultConcurrency 是执行器的全局并发上限默认值（避免同时压满所有机器）。
const DefaultConcurrency = 4

// rollbackTimeout 是自动回滚的总超时。
//
// 回滚用 context.WithoutCancel(ctx) 单独限时：用户取消发布时也要能把 current 切回去。
const rollbackTimeout = 5 * time.Minute

// Runner 在目标机上执行一条命令，由 backend 注入（agent → jobs.Dispatcher；ssh → sshx 包装）。
// 与 checker.Executor 同形，但**额外提供流式日志回调**。
//
// 约定：
//   - onLog 由 Runner 在执行过程中**实时**调用，不得缓冲到命令结束；
//   - 传输层失败返回 err；命令本身的非 0 退出用 exitCode 表达（err 为 nil）；
//   - timeoutS 由 Runner 负责执行；ctx 取消时必须终止命令。
type Runner interface {
	Run(ctx context.Context, serverID, command string, timeoutS int,
		onLog func(stream, chunk string)) (stdout, stderr string, exitCode int, err error)
}

// Executor 按计划在目标机上串行执行步骤，失败时逆序执行回滚步骤。
//
// 全局并发上限（ExecutorOptions.Concurrency，默认 4）按「计划」为单位占用：
// 同一台机器上多次 RunPlan 不会互相并发，多台机器最多同时跑 Concurrency 个计划。
type Executor struct {
	runner Runner
	sem    chan struct{}
	log    *slog.Logger
}

// ExecutorOptions 是执行器选项。
type ExecutorOptions struct {
	Concurrency int // 全局并发上限，默认 4（避免同时压满所有机器）
	Logger      *slog.Logger
}

// NewExecutor 构造执行器；Concurrency <= 0 时取 DefaultConcurrency，Logger 为 nil 时用 slog.Default()。
func NewExecutor(r Runner, opts ExecutorOptions) *Executor {
	conc := opts.Concurrency
	if conc <= 0 {
		conc = DefaultConcurrency
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Executor{
		runner: r,
		sem:    make(chan struct{}, conc),
		log:    logger,
	}
}

// Hooks 是执行过程中的回调；全部可为 nil。
//
// OnStep：正向步骤用其在 Plan.Steps 里的下标；回滚步骤用其在 Plan.RollbackSteps 里的下标，
// 且名字带 `rollback:` 前缀（便于 UI 区分）。
type Hooks struct {
	OnStep   func(stepIndex int, name string)
	OnLog    func(stream, chunk string)
	OnStatus func(status string, message string)
}

// RunOutcome 是一次 RunPlan 的结果。
type RunOutcome struct {
	Status     string // success | failed
	ExitCode   int
	Message    string
	FailedStep string
}

// RunPlan 在单台服务器上执行计划，逐步回调进度与日志。
//
// 执行语义（§25.1 硬要求）：
//  1. 步骤**严格串行**，任一步非 0 退出即失败（IgnoreFailure 的步骤除外）；
//  2. 失败（或 ctx 取消）时按 **RollbackSteps 的逆序** best-effort 回滚，回滚失败只记日志、不掩盖原始失败；
//  3. 日志经 Hooks.OnLog 实时流出（由 Runner 在运行中回调，本方法不做缓冲）；
//  4. ctx 取消时终止当前步骤并回滚（回滚用不带取消的 ctx 单独限时）。
//
// 返回值约定：
//   - 步骤失败 → 返回 (RunOutcome{Status:"failed"}, **nil error**)，细节在 Outcome 里；
//   - 根本无法开始执行（Runner 未注入、计划为空、等待并发额度时被取消）→ 返回非 nil error。
func (e *Executor) RunPlan(ctx context.Context, serverID string, p Plan, hooks Hooks) (RunOutcome, error) {
	if e == nil || e.runner == nil {
		return RunOutcome{Status: StatusFailed}, errors.New("deploy: Runner 未注入")
	}
	if len(p.Steps) == 0 {
		return RunOutcome{Status: StatusFailed}, errors.New("deploy: 计划没有可执行步骤")
	}
	if err := ctx.Err(); err != nil {
		return RunOutcome{Status: StatusFailed, Message: "执行前已被取消: " + err.Error()}, err
	}
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		return RunOutcome{Status: StatusFailed, Message: "等待并发额度时被取消: " + ctx.Err().Error()}, ctx.Err()
	}

	env := &runEnv{executor: e, serverID: serverID, plan: p, hooks: hooks}
	env.emitStatus(StatusRunning, fmt.Sprintf("开始执行 %d 个步骤", len(p.Steps)))

	ignored := 0
	for i, step := range p.Steps {
		if err := ctx.Err(); err != nil {
			return env.fail(ctx, step.Name, -1, "执行前已被取消: "+err.Error(), ""), nil
		}
		env.emitStep(i, step.Name)
		e.log.Info("deploy: 步骤开始", "server", serverID, "index", i, "step", step.Name, "timeout_s", step.TimeoutS)

		stdout, stderr, exitCode, err := e.runner.Run(ctx, serverID, step.Command, step.TimeoutS, env.emitLog)
		if err == nil && exitCode == 0 {
			e.log.Info("deploy: 步骤完成", "server", serverID, "index", i, "step", step.Name)
			continue
		}
		detail := failureDetail(ctx, err, exitCode)
		if step.IgnoreFailure {
			ignored++
			e.log.Warn("deploy: 步骤失败但已忽略（best-effort）", "server", serverID, "index", i, "step", step.Name, "exit_code", exitCode, "err", err, "stderr", snippetOutput(stderr))
			_ = stdout
			continue
		}
		return env.fail(ctx, step.Name, exitCode, detail, stderr), nil
	}

	msg := fmt.Sprintf("全部 %d 个步骤执行成功", len(p.Steps))
	if ignored > 0 {
		msg += fmt.Sprintf("（%d 个 best-effort 步骤失败已忽略）", ignored)
	}
	env.emitStatus(StatusSuccess, msg)
	e.log.Info("deploy: 发布成功", "server", serverID, "release", p.ReleaseID, "app", p.AppSlug)
	return RunOutcome{Status: StatusSuccess, Message: msg}, nil
}

// runEnv 把一次 RunPlan 的回调与上下文收拢，便于失败/回滚路径复用。
type runEnv struct {
	executor *Executor
	serverID string
	plan     Plan
	hooks    Hooks
	logMu    sync.Mutex
}

// emitLog 把 Runner 的实时日志转给调用方；加锁保证多条流不会交错乱序。
func (env *runEnv) emitLog(stream, chunk string) {
	if env.hooks.OnLog == nil {
		return
	}
	env.logMu.Lock()
	defer env.logMu.Unlock()
	env.hooks.OnLog(stream, chunk)
}

func (env *runEnv) emitStep(index int, name string) {
	if env.hooks.OnStep != nil {
		env.hooks.OnStep(index, name)
	}
}

func (env *runEnv) emitStatus(status, message string) {
	if env.hooks.OnStatus != nil {
		env.hooks.OnStatus(status, message)
	}
}

// fail 组装失败结果、执行回滚（逆序、best-effort）并回调最终状态。
func (env *runEnv) fail(ctx context.Context, failedStep string, exitCode int, detail, stderr string) RunOutcome {
	out := RunOutcome{
		Status:     StatusFailed,
		ExitCode:   exitCode,
		FailedStep: failedStep,
		Message:    fmt.Sprintf("步骤 %s 失败: %s", failedStep, detail),
	}
	if s := snippetOutput(stderr); s != "" {
		out.Message += "；stderr: " + s
	}
	env.executor.log.Error("deploy: 执行失败", "server", env.serverID, "step", failedStep, "exit_code", exitCode, "detail", detail)

	if len(env.plan.RollbackSteps) == 0 {
		env.emitStatus(StatusRunning, fmt.Sprintf("步骤 %s 失败，计划没有回滚步骤", failedStep))
	} else {
		env.emitStatus(StatusRunning, fmt.Sprintf("步骤 %s 失败，开始回滚（%d 步）", failedStep, len(env.plan.RollbackSteps)))
		rbFailed := env.rollback(ctx)
		if rbFailed > 0 {
			out.Message += fmt.Sprintf("；回滚 %d 步中有 %d 步失败（详见日志）", len(env.plan.RollbackSteps), rbFailed)
		}
	}
	env.emitStatus(StatusFailed, out.Message)
	return out
}

// rollback 逆序执行 RollbackSteps，返回失败步数（best-effort，不掩盖原始失败）。
func (env *runEnv) rollback(parent context.Context) int {
	rbCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), rollbackTimeout)
	defer cancel()

	failed := 0
	for i := len(env.plan.RollbackSteps) - 1; i >= 0; i-- {
		step := env.plan.RollbackSteps[i]
		env.emitStep(i, "rollback:"+step.Name)
		env.executor.log.Info("deploy: 回滚步骤开始", "server", env.serverID, "index", i, "step", step.Name)
		_, stderr, exitCode, err := env.executor.runner.Run(rbCtx, env.serverID, step.Command, step.TimeoutS, env.emitLog)
		if err != nil || exitCode != 0 {
			failed++
			env.executor.log.Error("deploy: 回滚步骤失败（继续执行剩余回滚步骤）",
				"server", env.serverID, "index", i, "step", step.Name,
				"exit_code", exitCode, "err", err, "stderr", snippetOutput(stderr))
			continue
		}
		env.executor.log.Info("deploy: 回滚步骤完成", "server", env.serverID, "index", i, "step", step.Name)
	}
	return failed
}

// failureDetail 生成可读的失败原因。
func failureDetail(ctx context.Context, err error, exitCode int) string {
	if cerr := ctx.Err(); cerr != nil {
		if err != nil {
			return fmt.Sprintf("已取消（%s；底层错误 %s）", cerr.Error(), err.Error())
		}
		return "已取消: " + cerr.Error()
	}
	if err != nil {
		return "执行通道错误: " + err.Error()
	}
	return fmt.Sprintf("退出码 %d", exitCode)
}

// snippetOutput 把 stdout/stderr 压成一行并截断，控制日志与 message 体积。
func snippetOutput(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	const max = 300
	if len(s) > max {
		return s[:max] + "…（已截断）"
	}
	return s
}
