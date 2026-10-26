// Package jobs 实现控制端向 agent 下发命令任务并收集结果的链路
// （契约见 docs/ARCHITECTURE.md 第 14.2 节）。
//
// 它同时提供 checker.Executor 所需的 Exec 语义，因此 检测 的 process/script 检测
// 可以直接把 *Dispatcher 注入 checker.Deps.Exec。
//
// 设计要点：
//   - runID 隔离：每次 Exec 生成唯一 runID，等待表以 (serverID, runID) 为键，
//     多台机器或同机并发的任务各自拿到自己的结果；
//   - 同机并发上限：默认每台服务器 4 个在途任务，超出后在 ctx 允许范围内排队等待
//     （而不是快速失败），避免把单台机器的 agent 压垮；
//   - 超时 / ctx 取消 / 连接断开都会清理等待表条目，绝不泄漏；
//   - HandleAck / HandleLog / HandleDone 对未知 (serverID, runID) 静默忽略，
//     兼容 agent 重发与控制端重启后的迟到帧；
//   - 单次任务 stdout / stderr 各自最多保留 Options.MaxOutputBytes（默认 1MiB），
//     超出部分丢弃并追加「…（已截断）」标记。
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/maliang/leadstack/pkg/protocol"
)

// Sender 由 backend 的 WS Hub 实现：把一条 job.exec 发给指定服务器。
// SendJob 必须并发安全；envelope 是 protocol.Envelope 的 JSON 字节。
type Sender interface {
	SendJob(serverID string, envelope []byte) error
	IsOnline(serverID string) bool
}

// Result 是一次任务执行的聚合结果。
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// 默认参数。
const (
	// DefaultMaxConcurrentPerServer 是同一台服务器的默认在途任务上限。
	DefaultMaxConcurrentPerServer = 4
	// DefaultMaxOutputBytes 是 stdout / stderr 各自默认保留的最大字节数。
	DefaultMaxOutputBytes = 1 << 20 // 1 MiB
	// DefaultTimeout 是 timeoutS <= 0 时的默认等待超时。
	DefaultTimeout = 60 * time.Second
	// truncatedMarker 是输出被截断时追加的标记。
	truncatedMarker = "…（已截断）"
)

// 哨兵错误，调用方可用 errors.Is 区分失败类型。
var (
	// ErrServerOffline 表示目标服务器不在线（或 Sender 为 nil）。
	ErrServerOffline = errors.New("服务器离线")
	// ErrSendFailed 表示 job.exec 发送失败（Hub/连接层错误）。
	ErrSendFailed = errors.New("下发任务失败")
	// ErrTimeout 表示等待 job.done 超时。
	ErrTimeout = errors.New("任务执行超时")
	// ErrInvalidArgument 表示入参非法（如 serverID 为空）。
	ErrInvalidArgument = errors.New("参数非法")
)

// Options 是 Dispatcher 的可选参数，零值使用默认值。
type Options struct {
	// MaxConcurrentPerServer 是同一台服务器的在途任务上限（默认 4），超出排队。
	MaxConcurrentPerServer int
	// MaxOutputBytes 是 stdout / stderr 各自保留的最大字节数（默认 1MiB）。
	MaxOutputBytes int
	// Timeout 是 timeoutS <= 0 时使用的默认等待超时（默认 60s）。
	Timeout time.Duration
	// Logger 是可选的日志器，nil 时使用 slog.Default()。
	Logger *slog.Logger
}

// Dispatcher 下发 job.exec 并等待对应的 job.done。
// 所有方法均可并发调用。
type Dispatcher struct {
	sender Sender
	opts   Options
	log    *slog.Logger

	mu      sync.Mutex
	waiters map[waitKey]*pending
	// sem 是每台服务器的并发信号量（容量 = MaxConcurrentPerServer）。
	sem map[string]chan struct{}
}

// waitKey 是等待表的键：runID 只保证在单台服务器内唯一，因此必须带上 serverID。
type waitKey struct {
	serverID string
	runID    string
}

// NewDispatcher 创建 Dispatcher。sender 为 nil 时所有 Exec 都会快速失败为
// ErrServerOffline（便于上层在 Hub 尚未就绪时安全降级）。
func NewDispatcher(s Sender, opts Options) *Dispatcher {
	if opts.MaxConcurrentPerServer <= 0 {
		opts.MaxConcurrentPerServer = DefaultMaxConcurrentPerServer
	}
	if opts.MaxOutputBytes <= 0 {
		opts.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{
		sender:  s,
		opts:    opts,
		log:     log,
		waiters: make(map[waitKey]*pending),
		sem:     make(map[string]chan struct{}),
	}
}

// Exec 下发一条 command 任务并等待 job.done，返回聚合后的 stdout/stderr/exitCode。
//
// 语义说明：
//   - 服务器离线或 SendJob 失败：立即返回错误，不等待；
//   - ctx 取消或超时（timeoutS，<=0 时用 Options.Timeout）到点：返回错误并清理等待者；
//   - 命令以非 0 退出：err 为 nil，exitCode 为该退出码（正常结果）；
//   - agent 明确回报执行失败（job.done.error 非空）：err 非 nil，exitCode 仅供参考。
//
// 该签名与 checker.Executor 一致，可直接注入 checker.Deps.Exec。
func (d *Dispatcher) Exec(ctx context.Context, serverID, command string, timeoutS int) (stdout, stderr string, exitCode int, err error) {
	res, err := d.dispatch(ctx, serverID, timeoutS, func(runID string, timeout time.Duration) ([]byte, error) {
		return buildJobEnvelope(runID, protocol.JobKindCommand, command, timeout)
	})
	if res == nil {
		return "", "", -1, err
	}
	return res.Stdout, res.Stderr, res.ExitCode, err
}

// Probe 请目标机上的 agent **原生执行**一次探测（tcp/http/ping），返回探测结果。
//
// 为什么需要它：tcp/http/ping 默认由控制端直连目标机，目标机若没有入向端口
// （只允许出向访问控制端）就必然失败。走 agent 代检时，探测在目标机内部执行，
// 只依赖那条已经建立的长连接。
//
// 协议：Kind=probe 的 job.exec，Command 为 protocol.ProbeRequest 的 JSON；
// agent 把 protocol.ProbeResult 的 JSON 写到 stdout，exit code 恒为 0
// （探测失败属于结果，不属于任务失败）。
func (d *Dispatcher) Probe(ctx context.Context, serverID string, req protocol.ProbeRequest) (protocol.ProbeResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return protocol.ProbeResult{}, fmt.Errorf("编码探测请求失败: %w", err)
	}
	res, err := d.dispatch(ctx, serverID, req.TimeoutS, func(runID string, timeout time.Duration) ([]byte, error) {
		return buildJobEnvelope(runID, protocol.JobKindProbe, string(body), timeout)
	})
	if res == nil {
		return protocol.ProbeResult{}, err
	}
	if err != nil {
		return protocol.ProbeResult{}, err
	}
	raw := strings.TrimSpace(res.Stdout)
	if raw == "" {
		return protocol.ProbeResult{}, fmt.Errorf("agent 未返回探测结果（run 输出为空）")
	}
	var out protocol.ProbeResult
	if e := json.Unmarshal([]byte(raw), &out); e != nil {
		return protocol.ProbeResult{}, fmt.Errorf("解析探测结果失败: %w（原始输出：%s）", e, truncateForLog(raw))
	}
	return out, nil
}

// dispatch 是所有任务下发的公共路径：限流 → 登记等待者 → 发送 → 等待 job.done。
// 返回的 *Result 在「没有拿到任何结果」时为 nil（此时 err 一定非 nil）。
func (d *Dispatcher) dispatch(ctx context.Context, serverID string, timeoutS int,
	build func(runID string, timeout time.Duration) ([]byte, error)) (*Result, error) {
	if serverID == "" {
		return nil, fmt.Errorf("%w: serverID 为空", ErrInvalidArgument)
	}
	if !d.isOnline(serverID) {
		return nil, fmt.Errorf("%w: %s", ErrServerOffline, serverID)
	}

	// 同机并发限流：拿不到槽位就排队，直到 ctx 取消。
	release, err := d.acquire(ctx, serverID)
	if err != nil {
		return nil, err
	}
	defer release()

	// 排队期间连接可能已经断开，发送前再确认一次。
	if !d.isOnline(serverID) {
		return nil, fmt.Errorf("%w: %s", ErrServerOffline, serverID)
	}

	timeout := d.effectiveTimeout(timeoutS)
	runID := uuid.NewString()
	p := newPending(serverID, runID, d.opts.MaxOutputBytes)

	// 必须先登记等待者再发送，避免 job.done 先于登记到达而丢失结果。
	d.register(p)

	envelope, err := build(runID, agentTimeout(ctx, timeout))
	if err != nil {
		d.removeWaiter(serverID, runID)
		return nil, fmt.Errorf("构造 job.exec 失败: %w", err)
	}
	if err := d.sender.SendJob(serverID, envelope); err != nil {
		d.removeWaiter(serverID, runID)
		return nil, fmt.Errorf("%w: %v", ErrSendFailed, err)
	}
	d.log.Debug("已下发任务", "server_id", serverID, "run_id", runID, "timeout", timeout)

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case o := <-p.done:
		return &Result{ExitCode: o.exitCode, Stdout: o.stdout, Stderr: o.stderr}, o.err
	case <-ctx.Done():
		d.removeWaiter(serverID, runID)
		return nil, ctx.Err()
	case <-timer.C:
		d.removeWaiter(serverID, runID)
		// ctx 与 timer 可能几乎同时到点，此时以 ctx 的错误为准，避免取消被误报成超时。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: run_id=%s 在 %s 内未收到 job.done", ErrTimeout, runID, timeout)
	}
}

// truncateForLog 截断过长的原始输出，避免错误信息里塞进整段日志。
func truncateForLog(s string) string {
	const max = 512
	if len(s) <= max {
		return s
	}
	return s[:max] + truncatedMarker
}

// HandleAck 处理 agent 的 job.ack；未知 runID 静默忽略。
func (d *Dispatcher) HandleAck(serverID, runID string) {
	p := d.lookup(serverID, runID)
	if p == nil {
		return
	}
	p.markAck()
	d.log.Debug("任务已被 agent 接手", "server_id", serverID, "run_id", runID)
}

// HandleLog 处理 agent 的 job.log；按 seq 去重排序，未知 runID 静默忽略。
func (d *Dispatcher) HandleLog(serverID, runID string, seq int, stream, chunk string) {
	p := d.lookup(serverID, runID)
	if p == nil {
		return
	}
	p.addLog(seq, stream, chunk)
}

// HandleDone 处理 agent 的 job.done：组装并交付结果，同时清理等待表；未知 runID 静默忽略。
func (d *Dispatcher) HandleDone(serverID, runID string, exitCode int, errMsg string) {
	p := d.takeWaiter(serverID, runID)
	if p == nil {
		return
	}
	o, ok := p.buildOutcome(exitCode, errMsg)
	if !ok {
		return
	}
	p.send(o)
	d.log.Debug("任务完成",
		"server_id", serverID,
		"run_id", runID,
		"exit_code", exitCode,
		"stdout_bytes", len(o.stdout),
		"stderr_bytes", len(o.stderr))
}

// FailServer 在服务器连接断开时唤醒该服务器所有等待者，返回包含 cause 的错误。
// 其它服务器的等待者不受影响。
func (d *Dispatcher) FailServer(serverID string, cause error) {
	if cause == nil {
		cause = ErrServerOffline
	}
	d.mu.Lock()
	woken := make([]*pending, 0)
	for key, p := range d.waiters {
		if key.serverID == serverID {
			woken = append(woken, p)
			delete(d.waiters, key)
		}
	}
	d.mu.Unlock()

	for _, p := range woken {
		o, ok := p.buildOutcome(-1, "")
		if !ok {
			continue
		}
		o.err = fmt.Errorf("%w: 服务器 %s 连接断开: %w", ErrServerOffline, serverID, cause)
		p.send(o)
	}
	if len(woken) > 0 {
		d.log.Warn("服务器断开，已唤醒等待中的任务", "server_id", serverID, "count", len(woken), "cause", cause)
	}
}

// effectiveTimeout 计算 Dispatcher 的等待超时：
// timeoutS > 0 时以它为准，否则用 Options.Timeout。
func (d *Dispatcher) effectiveTimeout(timeoutS int) time.Duration {
	if timeoutS > 0 {
		return time.Duration(timeoutS) * time.Second
	}
	return d.opts.Timeout
}

// agentTimeout 计算下发给 agent 的 timeout_s：若 ctx 自带更早的 deadline，
// 则取 ctx 剩余时间（至少 1s），让 agent 侧的超时更贴近实际等待窗口。
// 注意 Dispatcher 自身的等待超时不受影响，ctx 取消仍以 ctx.Err() 返回。
func agentTimeout(ctx context.Context, wait time.Duration) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < wait {
			return remaining
		}
	}
	return wait
}

// isOnline 在 sender 为 nil 时安全返回 false。
func (d *Dispatcher) isOnline(serverID string) bool {
	if d.sender == nil {
		return false
	}
	return d.sender.IsOnline(serverID)
}

// acquire 获取指定服务器的一个并发槽位；ctx 取消时返回 ctx 的错误。
func (d *Dispatcher) acquire(ctx context.Context, serverID string) (func(), error) {
	d.mu.Lock()
	sem, ok := d.sem[serverID]
	if !ok {
		sem = make(chan struct{}, d.opts.MaxConcurrentPerServer)
		d.sem[serverID] = sem
	}
	d.mu.Unlock()

	select {
	case sem <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-sem }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// register 把等待者登记进等待表。
func (d *Dispatcher) register(p *pending) {
	d.mu.Lock()
	d.waiters[waitKey{serverID: p.serverID, runID: p.runID}] = p
	d.mu.Unlock()
}

// lookup 返回等待者但不移除；未找到返回 nil。
func (d *Dispatcher) lookup(serverID, runID string) *pending {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.waiters[waitKey{serverID: serverID, runID: runID}]
}

// takeWaiter 移除并返回等待者；未找到返回 nil。
func (d *Dispatcher) takeWaiter(serverID, runID string) *pending {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := waitKey{serverID: serverID, runID: runID}
	p, ok := d.waiters[key]
	if !ok {
		return nil
	}
	delete(d.waiters, key)
	return p
}

// removeWaiter 删除等待表条目（超时/取消清理路径）。
func (d *Dispatcher) removeWaiter(serverID, runID string) {
	d.mu.Lock()
	delete(d.waiters, waitKey{serverID: serverID, runID: runID})
	d.mu.Unlock()
}

// pendingCount 返回当前等待者数量，供测试与内部诊断使用。
func (d *Dispatcher) pendingCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.waiters)
}

// buildJobEnvelope 构造 job.exec 信封：type=job.exec，payload=protocol.JobExec。
// kind 为空时按 command 处理，保证与旧版 agent 的行为一致。
func buildJobEnvelope(runID, kind, command string, timeout time.Duration) ([]byte, error) {
	if kind == "" {
		kind = protocol.JobKindCommand
	}
	timeoutS := int(timeout / time.Second)
	if timeoutS < 1 {
		timeoutS = 1
	}
	payload, err := json.Marshal(protocol.JobExec{
		RunID:    runID,
		Kind:     kind,
		Command:  command,
		TimeoutS: timeoutS,
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(protocol.Envelope{
		Type:    protocol.TypeJobExec,
		ID:      runID,
		TS:      time.Now().UnixMilli(),
		Payload: payload,
	})
}
