// 交互式终端（agent 模式），契约见 docs/ARCHITECTURE.md 第 40 节。
//
// 本文件负责会话管理与协议帧收发；平台相关的 PTY 启动与进程组终止在
// term_pty_unix.go / term_windows.go 中实现。
//
// 实现约定（与协议方向保持一致）：
//   - 控制端 → agent：term.open / term.data（用户输入）/ term.resize / term.close
//   - agent → 控制端：term.data（PTY 输出，base64）；会话结束/错误提示也通过
//     term.data 发送一段人类可读文本，不额外发明反向的 term.close 帧。
package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maliang/leadstack/pkg/protocol"
)

const (
	defaultTermCols = 80
	defaultTermRows = 24
	// 窗口尺寸上限，防止异常参数导致 uint16 溢出或 shell 行为异常。
	maxTermCols = 500
	maxTermRows = 300
	// 单条 term.data 输入上限，避免控制端发送超大帧打爆内存。
	maxTerminalInputBytes = 1 << 20
	// 输入队列长度：WS 读循环只入队，由独立写协程写 PTY，避免阻塞心跳。
	terminalInputQueue = 32
	// 单次从 PTY 读取的最大字节数。
	terminalReadChunk = 4096
)

var (
	// terminalIdleTimeout 是终端会话的空闲自动关闭时间（定义为变量便于测试覆盖）。
	terminalIdleTimeout = 30 * time.Minute
	// errTerminalUnsupported 表示当前平台不支持交互式终端（Windows）。
	errTerminalUnsupported = errors.New("当前平台不支持交互式终端")
	// errTerminalBusy 表示已有终端会话在运行。
	errTerminalBusy = errors.New("已有终端会话在运行")
)

// terminalProcess 抽象一个平台上的 PTY 进程（Unix 下由 creack/pty 实现）。
type terminalProcess interface {
	// Read 从 PTY 主端读取 shell 输出。
	Read(p []byte) (int, error)
	// Write 向 PTY 主端写入用户输入。
	Write(p []byte) (int, error)
	// Resize 调整 PTY 窗口大小。
	Resize(cols, rows int) error
	// Kill 结束整个进程组（shell 及其 fork 的子进程）并释放 PTY。
	Kill() error
	// Pid 返回 shell 进程 PID。
	Pid() int
}

// terminalSession 表示一个活跃的终端会话。
type terminalSession struct {
	id   string
	proc terminalProcess
	// cancel 取消会话 ctx，用于结束读/写/空闲协程。
	cancel context.CancelFunc
	// activity 用于重置空闲计时器（容量 1，重复活动会合并）。
	activity chan struct{}
	// input 是用户输入队列，由独立写协程消费。
	input chan []byte
	// done 在会话结束时关闭。
	done chan struct{}
	// closing 保证清理逻辑只执行一次。
	closing atomic.Bool
}

// isClosed 返回会话是否已经结束。
func (s *terminalSession) isClosed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// touch 标记会话有活动，重置空闲计时。
func (s *terminalSession) touch() {
	select {
	case s.activity <- struct{}{}:
	default:
	}
}

// terminalManager 管理单条连接上的终端会话；同一时刻最多一个活跃会话。
// 生命周期与连接绑定：serve 结束时调用 CloseAll，保证不留下孤儿 shell。
type terminalManager struct {
	log *slog.Logger

	mu     sync.Mutex
	active *terminalSession
	wg     sync.WaitGroup
}

// newTerminalManager 创建终端会话管理器。
func newTerminalManager(log *slog.Logger) *terminalManager {
	if log == nil {
		log = slog.Default()
	}
	return &terminalManager{log: log}
}

// HandleOpen 处理 term.open：开启一个新会话；已有会话时回复 busy 提示并忽略该请求。
func (m *terminalManager) HandleOpen(ctx context.Context, w *wsConn, spec protocol.TermOpen) {
	if strings.TrimSpace(spec.SessionID) == "" {
		m.log.Warn("term.open 缺少 session_id，已忽略")
		return
	}

	m.mu.Lock()
	active := m.active
	if active != nil && !active.isClosed() {
		m.mu.Unlock()
		m.log.Warn("已有终端会话，拒绝新的 term.open",
			"session_id", spec.SessionID, "active_session_id", active.id)
		notifyTerminalNotice(w, spec.SessionID, "错误：已有终端会话在运行，请先关闭当前会话。")
		return
	}
	m.mu.Unlock()

	shell := resolveShell(spec.Shell)
	cols, rows := normalizeTermSize(spec.Cols, spec.Rows)
	proc, err := startTerminalProcess(shell, cols, rows)
	if err != nil {
		m.log.Warn("启动终端 shell 失败", "shell", shell, "err", err)
		if errors.Is(err, errTerminalUnsupported) {
			notifyTerminalNotice(w, spec.SessionID, "错误：当前平台不支持交互式终端。")
		} else {
			notifyTerminalNotice(w, spec.SessionID, fmt.Sprintf("错误：启动 shell 失败：%v", err))
		}
		return
	}

	sessCtx, cancel := context.WithCancel(ctx)
	s := &terminalSession{
		id:       spec.SessionID,
		proc:     proc,
		cancel:   cancel,
		activity: make(chan struct{}, 1),
		input:    make(chan []byte, terminalInputQueue),
		done:     make(chan struct{}),
	}

	m.mu.Lock()
	m.active = s
	m.wg.Add(3)
	m.mu.Unlock()

	m.log.Info("终端会话已开启",
		"session_id", s.id, "shell", shell, "pid", proc.Pid(), "cols", cols, "rows", rows)

	go func() {
		defer m.wg.Done()
		m.readLoop(sessCtx, w, s)
	}()
	go func() {
		defer m.wg.Done()
		m.writeLoop(sessCtx, s)
	}()
	go func() {
		defer m.wg.Done()
		m.idleLoop(sessCtx, w, s)
	}()
}

// HandleData 处理控制端下发的 term.data：base64 解码后写入 PTY 输入队列。
func (m *terminalManager) HandleData(ctx context.Context, w *wsConn, data protocol.TermData) {
	s := m.sessionFor(data.SessionID)
	if s == nil {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(data.B64)
	if err != nil {
		m.log.Debug("term.data base64 解码失败，已忽略", "session_id", s.id, "err", err)
		return
	}
	if len(raw) == 0 {
		return
	}
	if len(raw) > maxTerminalInputBytes {
		m.log.Warn("终端输入超过上限，已丢弃", "session_id", s.id, "bytes", len(raw))
		return
	}
	// 非阻塞入队：输入洪峰时宁可丢输入，也不能阻塞 WS 读循环（否则心跳会断）。
	select {
	case s.input <- raw:
		s.touch()
	default:
		m.log.Warn("终端输入队列已满，丢弃一条输入", "session_id", s.id, "bytes", len(raw))
	}
}

// HandleResize 处理 term.resize：映射到 pty.Setsize。
func (m *terminalManager) HandleResize(ctx context.Context, w *wsConn, spec protocol.TermResize) {
	s := m.sessionFor(spec.SessionID)
	if s == nil {
		return
	}
	cols, rows := normalizeTermSize(spec.Cols, spec.Rows)
	if err := s.proc.Resize(cols, rows); err != nil {
		m.log.Debug("调整终端窗口大小失败", "session_id", s.id, "err", err)
		return
	}
	s.touch()
}

// HandleClose 处理 term.close：结束会话并回收整个进程组。
func (m *terminalManager) HandleClose(ctx context.Context, w *wsConn, spec protocol.TermClose) {
	s := m.sessionFor(spec.SessionID)
	if s == nil {
		m.log.Debug("term.close 收到未知或已结束的会话，已忽略", "session_id", spec.SessionID)
		return
	}
	m.closeSession(w, s, "")
}

// CloseAll 结束所有会话（连接断开或 agent 退出时调用）并等待相关协程退出。
func (m *terminalManager) CloseAll() {
	m.mu.Lock()
	s := m.active
	m.mu.Unlock()
	if s != nil {
		m.closeSession(nil, s, "")
	}
	m.wg.Wait()
}

// sessionFor 返回指定 id 的活跃会话；不存在或已结束返回 nil。
func (m *terminalManager) sessionFor(id string) *terminalSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.active; s != nil && s.id == id && !s.isClosed() {
		return s
	}
	return nil
}

// closeSession 幂等地结束一个会话：取消协程、结束进程组、清理活动标记、可选提示对端。
func (m *terminalManager) closeSession(w *wsConn, s *terminalSession, notice string) {
	if s == nil {
		return
	}
	// CAS 保证多个来源（term.close / 空闲超时 / shell 退出 / 连接断开）只清理一次。
	if !s.closing.CompareAndSwap(false, true) {
		return
	}
	s.cancel()
	if err := s.proc.Kill(); err != nil {
		m.log.Debug("结束终端进程组时出错", "session_id", s.id, "err", err)
	}
	close(s.done)

	m.mu.Lock()
	if m.active == s {
		m.active = nil
	}
	m.mu.Unlock()

	m.log.Info("终端会话已关闭", "session_id", s.id, "pid", s.proc.Pid())
	if notice != "" {
		notifyTerminalNotice(w, s.id, notice)
	}
}

// readLoop 持续读取 PTY 输出并通过 term.data 回传控制端。
func (m *terminalManager) readLoop(ctx context.Context, w *wsConn, s *terminalSession) {
	buf := make([]byte, terminalReadChunk)
	for {
		n, err := s.proc.Read(buf)
		if n > 0 {
			s.touch()
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			payload := protocol.TermData{
				SessionID: s.id,
				B64:       base64.StdEncoding.EncodeToString(chunk),
			}
			if sendErr := w.send(ctx, newEnvelope(protocol.TypeTermData, payload)); sendErr != nil {
				// 连接已不可用，直接清理（对端收不到提示，无需 notice）。
				m.closeSession(nil, s, "")
				return
			}
		}
		if err != nil {
			m.closeSession(w, s, "终端会话已结束（shell 已退出）。")
			return
		}
	}
}

// writeLoop 消费输入队列，把用户输入写入 PTY。
func (m *terminalManager) writeLoop(ctx context.Context, s *terminalSession) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case data := <-s.input:
			if len(data) == 0 {
				continue
			}
			if _, err := s.proc.Write(data); err != nil {
				m.closeSession(nil, s, "")
				return
			}
			s.touch()
		}
	}
}

// idleLoop 在会话空闲超过 terminalIdleTimeout 时自动关闭会话。
func (m *terminalManager) idleLoop(ctx context.Context, w *wsConn, s *terminalSession) {
	timer := time.NewTimer(terminalIdleTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-s.activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(terminalIdleTimeout)
		case <-timer.C:
			m.log.Info("终端会话空闲超时，自动关闭", "session_id", s.id, "idle", terminalIdleTimeout)
			m.closeSession(w, s, fmt.Sprintf("终端会话空闲超过 %s，已自动关闭。", terminalIdleTimeout))
			return
		}
	}
}

// notifyTerminalNotice 通过 term.data 发送一段人类可读提示文本。
// 使用独立的短超时 ctx：会话 ctx 可能已被取消，但仍希望提示尽量送达。
func notifyTerminalNotice(w *wsConn, sessionID, text string) {
	if w == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	payload := protocol.TermData{
		SessionID: sessionID,
		B64:       base64.StdEncoding.EncodeToString([]byte(text + "\r\n")),
	}
	_ = w.send(ctx, newEnvelope(protocol.TypeTermData, payload))
}

// resolveShell 决定要启动的 shell：显式指定 > $SHELL > sh。
func resolveShell(requested string) string {
	if v := strings.TrimSpace(requested); v != "" {
		return v
	}
	if v := os.Getenv("SHELL"); v != "" {
		return v
	}
	return "sh"
}

// normalizeTermSize 归一化窗口大小并做上限保护。
func normalizeTermSize(cols, rows int) (int, int) {
	if cols <= 0 {
		cols = defaultTermCols
	}
	if rows <= 0 {
		rows = defaultTermRows
	}
	if cols > maxTermCols {
		cols = maxTermCols
	}
	if rows > maxTermRows {
		rows = maxTermRows
	}
	return cols, rows
}
