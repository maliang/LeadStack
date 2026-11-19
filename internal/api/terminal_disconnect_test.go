package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/maliang/leadstack/internal/store"
	"github.com/maliang/leadstack/internal/term"
)

// blockingTermSession 模拟一个真实的交互式终端会话：
// Read 会一直阻塞，直到 Close 被调用才返回。浏览端断开却不 Close 时，
// 调用方的读协程就会永久卡住——这正是 P1 终端会话泄漏的形态。
type blockingTermSession struct {
	opened     chan struct{}
	readOnce   sync.Once
	closed     chan struct{}
	closeOnce  sync.Once
	closeCalls atomic.Int32
}

func newBlockingTermSession() *blockingTermSession {
	return &blockingTermSession{opened: make(chan struct{}), closed: make(chan struct{})}
}

// markOpened 只在第一次 Read 时通知测试：会话确实进入了阻塞读状态。
func (s *blockingTermSession) markOpened() {
	s.readOnce.Do(func() { close(s.opened) })
}

func (s *blockingTermSession) Read([]byte) (int, error) {
	s.markOpened()
	<-s.closed
	return 0, errors.New("终端会话已关闭")
}

func (s *blockingTermSession) Write(p []byte) (int, error) { return len(p), nil }

func (s *blockingTermSession) Resize(int, int) error { return nil }

func (s *blockingTermSession) Close() error {
	s.closeCalls.Add(1)
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

// blockingTermOpener 记录每次打开的会话，便于断言「每次断开都关闭了一次」。
type blockingTermOpener struct {
	mu       sync.Mutex
	sessions []*blockingTermSession
}

func (o *blockingTermOpener) Open(context.Context, string) (term.Session, error) {
	s := newBlockingTermSession()
	o.mu.Lock()
	o.sessions = append(o.sessions, s)
	o.mu.Unlock()
	return s, nil
}

func (o *blockingTermOpener) session(i int) *blockingTermSession {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		o.mu.Lock()
		if i < len(o.sessions) {
			s := o.sessions[i]
			o.mu.Unlock()
			return s
		}
		o.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

func (o *blockingTermOpener) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.sessions)
}

// dialTerminal 直接塞一张一次性票据并建立终端 WebSocket。
func dialTerminal(t *testing.T, env *testEnv, serverID string) *websocket.Conn {
	t.Helper()
	ticket := uuid.NewString()
	env.api.termTickets.Store(ticket, &terminalTicket{
		ServerID: serverID, Actor: "admin", Role: "admin",
		ExpiresAt: time.Now().Add(time.Minute), Cols: 80, Rows: 24,
	})
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, resp, err := dialer.Dial(env.wsURL("/api/v1/servers/"+serverID+"/terminal?ticket="+ticket), nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("连接终端 WebSocket 失败: status=%d err=%v", status, err)
	}
	return conn
}

// TestTerminalBrowserDisconnectClosesSession 覆盖 P1：浏览器断开后必须真正关闭会话，
// 并且连续打开-断开 5 次都不能因为上一轮会话残留而失败。
func TestTerminalBrowserDisconnectClosesSession(t *testing.T) {
	env := newTestEnv(t)
	env.cfg.TerminalEnabled = true
	opener := &blockingTermOpener{}
	env.api.termOpener = opener

	srv := &store.Server{
		ID: uuid.NewString(), Name: "term-disconnect", Host: "10.9.9.9",
		Port: 22, Mode: "ssh", Status: "offline", CreatedAt: store.NowMS(),
	}
	if err := env.st.CreateServer(context.Background(), srv); err != nil {
		t.Fatalf("创建测试服务器失败: %v", err)
	}

	for i := 0; i < 5; i++ {
		conn := dialTerminal(t, env, srv.ID)
		sess := opener.session(i)
		if sess == nil {
			t.Fatalf("第 %d 轮未打开终端会话", i+1)
		}
		// 等 Read 真正阻塞后再断开，确保覆盖「读阻塞中被浏览器断开」的时序。
		select {
		case <-sess.opened:
		case <-time.After(3 * time.Second):
			t.Fatalf("第 %d 轮会话未进入读阻塞状态", i+1)
		}
		if err := conn.Close(); err != nil {
			t.Fatalf("第 %d 轮关闭浏览器连接失败: %v", i+1, err)
		}

		select {
		case <-sess.closed:
		case <-time.After(3 * time.Second):
			t.Fatalf("第 %d 轮浏览器断开后会话未被关闭（Close 从未执行，会话泄漏）", i+1)
		}
		// 退出路径与 defer 只能 Close 一次，不能重复关闭。
		if got := sess.closeCalls.Load(); got != 1 {
			t.Fatalf("第 %d 轮 Close 调用 %d 次，期望 1 次", i+1, got)
		}
	}

	if got := opener.count(); got != 5 {
		t.Fatalf("连续 5 轮应打开 5 个会话，实际 %d 个（出现重复拒绝/未重新打开）", got)
	}
}

// TestAgentTermSessionCloseCleansIndex 验证 agent 会话关闭后 agentTerms 索引被清理，
// 且 Close 可重复调用而不报错、不重复发送 term.close。
func TestAgentTermSessionCloseCleansIndex(t *testing.T) {
	env := newTestEnv(t)
	serverConn := newTestWSPair(t)
	sessionID := uuid.NewString()

	session := newAgentTermSession(&agentConn{ws: serverConn}, sessionID)
	session.onClose = func() { env.api.agentTerms.Delete(sessionID) }
	env.api.agentTerms.Store(sessionID, session)

	if err := session.Close(); err != nil {
		t.Fatalf("第一次 Close 失败: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("重复 Close 不应报错: %v", err)
	}
	if _, ok := env.api.agentTerms.Load(sessionID); ok {
		t.Fatal("会话关闭后 agentTerms 仍残留索引")
	}
	if _, err := session.Read(make([]byte, 8)); err == nil {
		t.Fatal("会话已关闭，Read 应返回错误")
	}
}

// newTestWSPair 用 httptest 起一个最小 WebSocket 端点，返回服务端连接。
// 客户端连接由 Cleanup 负责关闭，避免测试结束后残留 goroutine。
func newTestWSPair(t *testing.T) *websocket.Conn {
	t.Helper()
	connCh := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connCh <- conn
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("建立测试 WebSocket 失败: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	select {
	case server := <-connCh:
		t.Cleanup(func() { _ = server.Close() })
		return server
	case <-time.After(3 * time.Second):
		t.Fatal("等待 WebSocket 升级超时")
		return nil
	}
}
