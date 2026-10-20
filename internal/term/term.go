// Package term 提供交互式终端会话的抽象与生命周期管理。
//
// 契约见 docs/ARCHITECTURE.md 第 40 节：SSH 通道由本包的 SSH 实现提供，
// agent 通道由 backend 自己实现（只要满足 Session 接口即可）。
package term

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Session 是一条可读写的交互式终端会话。
type Session interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(cols, rows int) error
	Close() error
}

// Opener 按服务器 ID 打开一条终端会话。
type Opener interface {
	Open(ctx context.Context, serverID string) (Session, error)
}

// DefaultIdleTimeout 是终端会话的空闲超时（30 分钟）。
const DefaultIdleTimeout = 30 * time.Minute

// ManagerOptions 是 Manager 的构造参数。
type ManagerOptions struct {
	// IdleTimeout 是空闲超时；<=0 时使用 DefaultIdleTimeout。
	IdleTimeout time.Duration
	// Now 返回当前时间；nil 时使用 time.Now（测试注入用）。
	Now func() time.Time
	// OnClose 在会话被关闭时回调（idle=true 表示因空闲超时被回收）。
	// 回调在管理循环/调用方 goroutine 中同步执行，请保持轻量（例如只写审计日志）。
	OnClose func(sessionID, serverID string, idle bool)
}

// Manager 按 sessionID 管理活跃终端会话，并回收空闲超时的会话。
type Manager struct {
	mu          sync.Mutex
	idleTimeout time.Duration
	now         func() time.Time
	onClose     func(sessionID, serverID string, idle bool)
	entries     map[string]*managedSession
	closed      bool

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

type managedSession struct {
	sessionID  string
	serverID   string
	session    Session
	lastActive time.Time
}

// NewManager 构造并启动 Manager（自动开始空闲回收）。
func NewManager(opts ManagerOptions) *Manager {
	idleTimeout := opts.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	m := &Manager{
		idleTimeout: idleTimeout,
		now:         now,
		onClose:     opts.OnClose,
		entries:     make(map[string]*managedSession),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	go m.reapLoop(reapInterval(idleTimeout))
	return m
}

// reapInterval 由空闲超时推导回收检查周期（测试里用小超时也能快速触发）。
func reapInterval(idleTimeout time.Duration) time.Duration {
	interval := idleTimeout / 10
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	if interval > time.Minute {
		interval = time.Minute
	}
	return interval
}

func (m *Manager) reapLoop(interval time.Duration) {
	defer close(m.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			m.reapIdle()
		}
	}
}

// reapIdle 关闭并移除所有空闲超时的会话。
func (m *Manager) reapIdle() {
	now := m.now()
	m.mu.Lock()
	var expired []*managedSession
	for id, e := range m.entries {
		if now.Sub(e.lastActive) >= m.idleTimeout {
			expired = append(expired, e)
			delete(m.entries, id)
		}
	}
	m.mu.Unlock()

	for _, e := range expired {
		_ = e.session.Close()
		m.notify(e, true)
	}
}

// Add 注册一个新会话；sessionID 重复时返回错误。
func (m *Manager) Add(sessionID, serverID string, s Session) error {
	if sessionID == "" {
		return errors.New("term: sessionID 不能为空")
	}
	if s == nil {
		return errors.New("term: session 不能为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("term: Manager 已关闭")
	}
	if _, exists := m.entries[sessionID]; exists {
		return fmt.Errorf("term: 会话 %s 已存在", sessionID)
	}
	m.entries[sessionID] = &managedSession{
		sessionID:  sessionID,
		serverID:   serverID,
		session:    s,
		lastActive: m.now(),
	}
	return nil
}

// Get 返回会话；不存在时 ok=false。有流量到达时调用即视为活跃（自动 Touch）。
func (m *Manager) Get(sessionID string) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[sessionID]
	if !ok {
		return nil, false
	}
	e.lastActive = m.now()
	return e.session, true
}

// Touch 刷新会话的空闲计时（收到用户输入或终端输出时调用）。
func (m *Manager) Touch(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[sessionID]; ok {
		e.lastActive = m.now()
	}
}

// Remove 关闭并移除一个会话；不存在时返回错误。
func (m *Manager) Remove(sessionID string) error {
	m.mu.Lock()
	e, ok := m.entries[sessionID]
	if ok {
		delete(m.entries, sessionID)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("term: 会话 %s 不存在", sessionID)
	}
	_ = e.session.Close()
	m.notify(e, false)
	return nil
}

// Count 返回活跃会话数。
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// CloseAll 关闭所有会话（不停止回收循环）。
func (m *Manager) CloseAll() {
	m.mu.Lock()
	entries := m.entries
	m.entries = make(map[string]*managedSession)
	m.mu.Unlock()
	for _, e := range entries {
		_ = e.session.Close()
		m.notify(e, false)
	}
}

// Close 停止回收循环并关闭所有会话；可重复调用。
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.stopOnce.Do(func() {
		close(m.stop)
	})
	<-m.done

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	entries := m.entries
	m.entries = make(map[string]*managedSession)
	m.mu.Unlock()

	for _, e := range entries {
		_ = e.session.Close()
		m.notify(e, false)
	}
	return nil
}

func (m *Manager) notify(e *managedSession, idle bool) {
	if m.onClose == nil || e == nil {
		return
	}
	m.onClose(e.sessionID, e.serverID, idle)
}
