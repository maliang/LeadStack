package term

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maliang/leadstack/internal/devssh"
	"github.com/maliang/leadstack/internal/sshx"
)

// fakeSession 是可观测的 Session 测试替身。
type fakeSession struct {
	mu          sync.Mutex
	closed      bool
	closeCount  int
	resizeCalls int
	closedCh    chan struct{}
}

func newFakeSession() *fakeSession {
	return &fakeSession{closedCh: make(chan struct{})}
}

func (f *fakeSession) Read([]byte) (int, error) {
	<-f.closedCh
	return 0, io.EOF
}

func (f *fakeSession) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, errors.New("closed")
	}
	return len(p), nil
}

func (f *fakeSession) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizeCalls++
	return nil
}

func (f *fakeSession) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		f.closeCount++
		close(f.closedCh)
	}
	return nil
}

func (f *fakeSession) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func waitCond(t *testing.T, what string, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待 %s 超时", what)
}

func TestManagerLifecycle(t *testing.T) {
	m := NewManager(ManagerOptions{IdleTimeout: time.Minute})
	defer m.Close()

	a, b := newFakeSession(), newFakeSession()
	if err := m.Add("sess-a", "srv-1", a); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if err := m.Add("sess-b", "srv-2", b); err != nil {
		t.Fatalf("Add b: %v", err)
	}
	if err := m.Add("sess-a", "srv-3", newFakeSession()); err == nil {
		t.Fatal("重复 sessionID 应报错")
	}
	if err := m.Add("", "srv-3", newFakeSession()); err == nil {
		t.Fatal("空 sessionID 应报错")
	}
	if err := m.Add("sess-nil", "srv-3", nil); err == nil {
		t.Fatal("nil session 应报错")
	}
	if m.Count() != 2 {
		t.Fatalf("Count = %d, 期望 2", m.Count())
	}
	if got, ok := m.Get("sess-b"); !ok || got != b {
		t.Fatal("Get 未返回正确会话")
	}
	if _, ok := m.Get("missing"); ok {
		t.Fatal("Get 不存在的会话应返回 false")
	}
	if err := m.Remove("sess-a"); err != nil {
		t.Fatalf("Remove a: %v", err)
	}
	if !a.isClosed() {
		t.Fatal("Remove 应关闭会话")
	}
	if err := m.Remove("sess-a"); err == nil {
		t.Fatal("重复 Remove 应报错")
	}
	if m.Count() != 1 {
		t.Fatalf("Count = %d, 期望 1", m.Count())
	}
	if err := m.Remove("sess-b"); err != nil {
		t.Fatalf("Remove b: %v", err)
	}
	if m.Count() != 0 {
		t.Fatalf("Count = %d, 期望 0", m.Count())
	}
}

func TestManagerIdleReap(t *testing.T) {
	type closedEvent struct {
		sessionID string
		serverID  string
		idle      bool
	}
	var (
		mu     sync.Mutex
		events []closedEvent
	)
	m := NewManager(ManagerOptions{
		IdleTimeout: 60 * time.Millisecond,
		OnClose: func(sessionID, serverID string, idle bool) {
			mu.Lock()
			events = append(events, closedEvent{sessionID, serverID, idle})
			mu.Unlock()
		},
	})
	defer m.Close()

	s1 := newFakeSession()
	if err := m.Add("idle-1", "srv-9", s1); err != nil {
		t.Fatalf("Add: %v", err)
	}
	waitCond(t, "空闲会话被回收", func() bool { return s1.isClosed() }, 3*time.Second)
	if m.Count() != 0 {
		t.Fatalf("回收后 Count = %d, 期望 0", m.Count())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("OnClose 调用次数 = %d, 期望 1", len(events))
	}
	if events[0].sessionID != "idle-1" || events[0].serverID != "srv-9" || !events[0].idle {
		t.Fatalf("OnClose 事件异常: %+v", events[0])
	}
}

func TestManagerTouchKeepsAlive(t *testing.T) {
	m := NewManager(ManagerOptions{IdleTimeout: 100 * time.Millisecond})
	defer m.Close()

	s := newFakeSession()
	if err := m.Add("touch-1", "srv-1", s); err != nil {
		t.Fatalf("Add: %v", err)
	}
	for i := 0; i < 6; i++ {
		time.Sleep(40 * time.Millisecond)
		m.Touch("touch-1")
		if s.isClosed() {
			t.Fatal("持续 Touch 的会话不应被回收")
		}
	}
	waitCond(t, "停止 Touch 后被回收", func() bool { return s.isClosed() }, 3*time.Second)
}

func TestManagerGetRefreshesActivity(t *testing.T) {
	m := NewManager(ManagerOptions{IdleTimeout: 120 * time.Millisecond})
	defer m.Close()

	s := newFakeSession()
	if err := m.Add("get-1", "srv-1", s); err != nil {
		t.Fatalf("Add: %v", err)
	}
	for i := 0; i < 5; i++ {
		time.Sleep(50 * time.Millisecond)
		if _, ok := m.Get("get-1"); !ok {
			t.Fatal("Get 不应让会话消失")
		}
	}
	if s.isClosed() {
		t.Fatal("频繁 Get 的会话不应被回收")
	}
}

func TestManagerCloseAllAndClose(t *testing.T) {
	m := NewManager(ManagerOptions{IdleTimeout: time.Minute})
	a, b := newFakeSession(), newFakeSession()
	if err := m.Add("a", "srv-1", a); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := m.Add("b", "srv-2", b); err != nil {
		t.Fatalf("Add: %v", err)
	}

	m.CloseAll()
	if !a.isClosed() || !b.isClosed() {
		t.Fatal("CloseAll 应关闭所有会话")
	}
	if m.Count() != 0 {
		t.Fatalf("CloseAll 后 Count = %d", m.Count())
	}
	// CloseAll 之后 Manager 仍可用。
	c := newFakeSession()
	if err := m.Add("c", "srv-3", c); err != nil {
		t.Fatalf("CloseAll 后 Add: %v", err)
	}

	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !c.isClosed() {
		t.Fatal("Close 应关闭会话")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("重复 Close 应返回 nil: %v", err)
	}
	if err := m.Add("d", "srv-4", newFakeSession()); err == nil {
		t.Fatal("Close 后 Add 应报错")
	}
}

func startDevsshForTerm(t *testing.T) *devssh.Server {
	t.Helper()
	srv, err := devssh.Start(devssh.Config{})
	if err != nil {
		t.Fatalf("启动 devssh: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func TestSSHOpenerIntegration(t *testing.T) {
	srv := startDevsshForTerm(t)
	host, portStr, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("拆分地址: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("解析端口: %v", err)
	}

	opener, err := NewSSHOpener(SSHOpenerOptions{
		Resolve: func(_ context.Context, serverID string) (sshx.PTYOptions, error) {
			if serverID != "srv-term" {
				return sshx.PTYOptions{}, errors.New("unknown server")
			}
			return sshx.PTYOptions{
				DialOptions: sshx.DialOptions{
					Host:          host,
					Port:          port,
					Auth:          sshx.Auth{Username: "devuser", Password: "devpass"},
					HostKeyPolicy: sshx.PolicyInsecure,
					Timeout:       5 * time.Second,
				},
				Cols: 100,
				Rows: 30,
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewSSHOpener: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess, err := opener.Open(ctx, "srv-term")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close()

	var (
		mu  sync.Mutex
		buf bytes.Buffer
	)
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := sess.Read(b)
			if n > 0 {
				mu.Lock()
				_, _ = buf.Write(b[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	if _, err := sess.Write([]byte("echo term-opener-ok\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitCond(t, "终端输出", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(buf.String(), "term-opener-ok")
	}, 10*time.Second)

	if err := sess.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
}

func TestSSHOpenerValidation(t *testing.T) {
	if _, err := NewSSHOpener(SSHOpenerOptions{}); err == nil {
		t.Fatal("缺少 Resolve 应报错")
	}

	sentinel := errors.New("resolve failed")
	opener, err := NewSSHOpener(SSHOpenerOptions{
		Resolve: func(context.Context, string) (sshx.PTYOptions, error) {
			return sshx.PTYOptions{}, sentinel
		},
	})
	if err != nil {
		t.Fatalf("NewSSHOpener: %v", err)
	}
	if _, err := opener.Open(context.Background(), "srv"); !errors.Is(err, sentinel) {
		t.Fatalf("Resolve 错误应透传，实际: %v", err)
	}
	if _, err := opener.Open(context.Background(), ""); err == nil {
		t.Fatal("空 serverID 应报错")
	}

	// Resolve 返回不完整的连接参数时，Open 应返回错误而不是 panic。
	badOpener, err := NewSSHOpener(SSHOpenerOptions{
		Resolve: func(context.Context, string) (sshx.PTYOptions, error) {
			return sshx.PTYOptions{}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewSSHOpener: %v", err)
	}
	if _, err := badOpener.Open(context.Background(), "srv"); err == nil {
		t.Fatal("无效连接参数应报错")
	}
}
