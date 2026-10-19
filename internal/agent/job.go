package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maliang/leadstack/pkg/protocol"
)

const (
	// 任务未指定超时时的默认值。
	defaultJobTimeout = 5 * time.Minute
	// 任务超时上限，防止控制端下发过长超时。
	maxJobTimeout = 1 * time.Hour
	// 任务取消后等待子进程退出的宽限期。
	jobWaitDelay = 5 * time.Second
	// 单条 job.log 的日志块大小。
	jobChunkSize = 4096
)

// jobManager 跟踪正在执行的命令任务，保证进程退出时不残留 goroutine 与子进程。
type jobManager struct {
	log *slog.Logger

	mu      sync.Mutex
	running map[string]context.CancelFunc
	wg      sync.WaitGroup
}

// newJobManager 创建任务管理器。
func newJobManager(log *slog.Logger) *jobManager {
	if log == nil {
		log = slog.Default()
	}
	return &jobManager{log: log, running: make(map[string]context.CancelFunc)}
}

// start 异步执行一条 job.exec 任务；同一 run_id 不会重复执行。
func (m *jobManager) start(parent context.Context, w *wsConn, spec protocol.JobExec) {
	if spec.RunID == "" {
		m.log.Warn("收到缺少 run_id 的任务，已忽略")
		return
	}
	if spec.Kind != "" && spec.Kind != "command" {
		m.log.Warn("暂不支持的任务类型", "kind", spec.Kind, "run_id", spec.RunID)
		_ = w.send(parent, newEnvelope(protocol.TypeJobDone, protocol.JobDone{
			RunID:    spec.RunID,
			ExitCode: -1,
			Error:    fmt.Sprintf("不支持的任务类型 %q", spec.Kind),
		}))
		return
	}

	jobCtx, cancel := context.WithCancel(parent)

	m.mu.Lock()
	if _, exists := m.running[spec.RunID]; exists {
		m.mu.Unlock()
		cancel()
		m.log.Warn("重复的任务 run_id，已忽略", "run_id", spec.RunID)
		return
	}
	m.running[spec.RunID] = cancel
	m.wg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			delete(m.running, spec.RunID)
			m.mu.Unlock()
			cancel()
		}()
		// 单条任务内的 panic 不允许带崩整个 agent。
		defer func() {
			if r := recover(); r != nil {
				m.log.Error("执行任务时发生 panic", "run_id", spec.RunID, "panic", r)
			}
		}()
		m.run(jobCtx, w, spec)
	}()
}

// run 执行「回 ack → 流式回日志 → 回 done」的完整任务流程。
func (m *jobManager) run(ctx context.Context, w *wsConn, spec protocol.JobExec) {
	// 接手确认，便于控制端尽早展示运行状态。
	if err := w.send(ctx, newEnvelope(protocol.TypeJobAck, protocol.JobAck{RunID: spec.RunID})); err != nil {
		m.log.Warn("发送 job.ack 失败，放弃任务", "run_id", spec.RunID, "err", err)
		return
	}

	timeout := time.Duration(spec.TimeoutS) * time.Second
	if timeout <= 0 {
		timeout = defaultJobTimeout
	}
	if timeout > maxJobTimeout {
		timeout = maxJobTimeout
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	m.log.Info("开始执行任务", "run_id", spec.RunID, "timeout", timeout)

	cmd := shellCommand(cmdCtx, spec.Command)
	configureProc(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		m.finish(ctx, w, spec.RunID, -1, fmt.Errorf("创建 stdout 管道失败: %w", err).Error())
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		m.finish(ctx, w, spec.RunID, -1, fmt.Errorf("创建 stderr 管道失败: %w", err).Error())
		return
	}

	if err := cmd.Start(); err != nil {
		m.finish(ctx, w, spec.RunID, -1, fmt.Errorf("启动命令失败: %w", err).Error())
		return
	}

	var seq atomic.Int64
	var pumps sync.WaitGroup
	pumps.Add(2)
	go func() {
		defer pumps.Done()
		m.pump(ctx, w, spec.RunID, "stdout", stdout, &seq)
	}()
	go func() {
		defer pumps.Done()
		m.pump(ctx, w, spec.RunID, "stderr", stderr, &seq)
	}()

	// 必须先等输出读完后才能 Wait（Wait 会关闭管道）。
	pumps.Wait()
	waitErr := cmd.Wait()

	exitCode := 0
	errMsg := ""
	switch {
	case cmdCtx.Err() == context.DeadlineExceeded:
		exitCode = -1
		errMsg = fmt.Sprintf("命令执行超时（%s）", timeout)
	case waitErr != nil:
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
			errMsg = waitErr.Error()
		}
	}

	m.log.Info("任务执行结束", "run_id", spec.RunID, "exit_code", exitCode)
	m.finish(ctx, w, spec.RunID, exitCode, errMsg)
}

// pump 读取一路输出并分块回传 job.log；连接断开时丢弃剩余输出。
func (m *jobManager) pump(ctx context.Context, w *wsConn, runID, stream string, r io.Reader, seq *atomic.Int64) {
	buf := make([]byte, jobChunkSize)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			seqNo := int(seq.Add(1) - 1)
			env := newEnvelope(protocol.TypeJobLog, protocol.JobLog{
				RunID:  runID,
				Seq:    seqNo,
				Stream: stream,
				Chunk:  chunk,
			})
			if err := w.send(ctx, env); err != nil {
				// 连接已不可用，丢弃剩余输出（M0 不落盘），让进程按超时策略结束。
				_, _ = io.Copy(io.Discard, r)
				return
			}
		}
		if readErr != nil {
			return
		}
	}
}

// finish 回传 job.done。
func (m *jobManager) finish(ctx context.Context, w *wsConn, runID string, exitCode int, errMsg string) {
	env := newEnvelope(protocol.TypeJobDone, protocol.JobDone{
		RunID:    runID,
		ExitCode: exitCode,
		Error:    errMsg,
	})
	if err := w.send(ctx, env); err != nil {
		m.log.Warn("发送 job.done 失败", "run_id", runID, "err", err)
	}
}

// close 取消所有在跑任务并等待它们退出，供 agent 停止时调用。
func (m *jobManager) close() {
	m.mu.Lock()
	for _, cancel := range m.running {
		cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}
