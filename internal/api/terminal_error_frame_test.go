package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/maliang/leadstack/internal/term"
)

// newWSPipe 建一对互联的 WebSocket 连接：服务端侧交给被测代码，客户端侧用于断言。
func newWSPipe(t *testing.T) (client *websocket.Conn, server *websocket.Conn) {
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
	case server = <-connCh:
	case <-time.After(3 * time.Second):
		t.Fatal("等待 WebSocket 升级超时")
	}
	t.Cleanup(func() { _ = server.Close() })
	return client, server
}

// startBrowserPump 复现 handler 里的「会话 → 浏览器」读协程。
func startBrowserPump(t *testing.T, session term.Session, browser *websocket.Conn) <-chan error {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- pumpSessionToBrowser(session, browser, make([]byte, 8192)) }()
	return errCh
}

// newAgentSessionForTest 建一个已登记进 agentTerms 的 agent 终端会话，
// 并返回其 agent 侧连接（Close 时用）。
func newAgentSessionForTest(t *testing.T, env *testEnv) (*agentTermSession, string) {
	t.Helper()
	_, agentPeer := newWSPipe(t)
	sessionID := uuid.NewString()
	session := newAgentTermSession(&agentConn{ws: agentPeer}, sessionID)
	session.onClose = func() { env.api.agentTerms.Delete(sessionID) }
	env.api.agentTerms.Store(sessionID, session)
	t.Cleanup(func() { _ = session.Close() })
	return session, sessionID
}

// TestAgentTerminalErrorFrameReachesBrowserAsText 覆盖本次修复：
// agent 发 error 帧后，浏览器收到的必须是 TextMessage、JSON 可解析，且 message/code 与发出的一致；
// 不能像旧实现那样降级成二进制纯文本（前端 ArrayBuffer 分支识别不了拒绝）。
func TestAgentTerminalErrorFrameReachesBrowserAsText(t *testing.T) {
	env := newTestEnv(t)
	browser, browserPeer := newWSPipe(t)
	session, sessionID := newAgentSessionForTest(t, env)
	pumpErr := startBrowserPump(t, session, browserPeer)

	env.api.routeAgentTermError(sessionID, "term_conflict", "已有终端会话在运行")

	_ = browser.SetReadDeadline(time.Now().Add(3 * time.Second))
	msgType, data, err := browser.ReadMessage()
	if err != nil {
		t.Fatalf("浏览器读取错误帧失败: %v", err)
	}
	if msgType != websocket.TextMessage {
		t.Fatalf("浏览器收到消息类型 %d，期望 TextMessage(%d)：错误帧仍被降级成二进制",
			msgType, websocket.TextMessage)
	}
	var frame struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("TextMessage 不是合法 JSON: %v，原文=%q", err, data)
	}
	if frame.Type != "error" {
		t.Fatalf("error 帧 type=%q，期望 %q", frame.Type, "error")
	}
	if frame.Message != "已有终端会话在运行" {
		t.Fatalf("error 帧 message=%q，与 agent 发出的不一致", frame.Message)
	}
	if frame.Code != "term_conflict" {
		t.Fatalf("error 帧 code=%q，与 agent 发出的不一致", frame.Code)
	}

	select {
	case perr := <-pumpErr:
		if perr == nil {
			t.Fatal("会话关闭后读协程应返回错误")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("会话关闭后读协程未退出")
	}
}

// TestAgentTerminalDataFrameStaysBinary 确认普通终端输出仍走 BinaryMessage（数据通路不变）。
func TestAgentTerminalDataFrameStaysBinary(t *testing.T) {
	env := newTestEnv(t)
	browser, browserPeer := newWSPipe(t)
	session, sessionID := newAgentSessionForTest(t, env)
	startBrowserPump(t, session, browserPeer)

	want := []byte("hello\r\n")
	env.api.routeAgentTermData(sessionID, base64.StdEncoding.EncodeToString(want))

	_ = browser.SetReadDeadline(time.Now().Add(3 * time.Second))
	msgType, data, err := browser.ReadMessage()
	if err != nil {
		t.Fatalf("浏览器读取数据帧失败: %v", err)
	}
	if msgType != websocket.BinaryMessage {
		t.Fatalf("数据帧消息类型 %d，期望 BinaryMessage(%d)", msgType, websocket.BinaryMessage)
	}
	if string(data) != string(want) {
		t.Fatalf("数据帧内容=%q，期望 %q", data, want)
	}
}

// TestAgentTerminalErrorFrameFallbacks 覆盖空 message/code 的兜底与未知会话不 panic。
func TestAgentTerminalErrorFrameFallbacks(t *testing.T) {
	env := newTestEnv(t)
	// 未知 session_id：只做忽略，不能 panic。
	env.api.routeAgentTermError("missing-session", "", "")

	browser, browserPeer := newWSPipe(t)
	session, sessionID := newAgentSessionForTest(t, env)
	startBrowserPump(t, session, browserPeer)

	env.api.routeAgentTermError(sessionID, "", "   ")

	_ = browser.SetReadDeadline(time.Now().Add(3 * time.Second))
	msgType, data, err := browser.ReadMessage()
	if err != nil {
		t.Fatalf("浏览器读取错误帧失败: %v", err)
	}
	if msgType != websocket.TextMessage {
		t.Fatalf("空文案错误帧消息类型 %d，期望 TextMessage", msgType)
	}
	var frame struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("TextMessage 不是合法 JSON: %v，原文=%q", err, data)
	}
	if frame.Message != "终端会话出错" {
		t.Fatalf("空 message 兜底文案=%q，期望 %q", frame.Message, "终端会话出错")
	}
}

// legacyPlainTermSession 模拟「旧控制端只发纯文本」：不区分帧类型，Read 只返回字节。
type legacyPlainTermSession struct {
	chunks chan []byte
	closed chan struct{}
	once   sync.Once
}

func newLegacyPlainTermSession() *legacyPlainTermSession {
	return &legacyPlainTermSession{chunks: make(chan []byte, 4), closed: make(chan struct{})}
}

func (s *legacyPlainTermSession) Read(p []byte) (int, error) {
	select {
	case data := <-s.chunks:
		return copy(p, data), nil
	case <-s.closed:
		return 0, errors.New("终端会话已关闭")
	}
}

func (s *legacyPlainTermSession) Write(p []byte) (int, error) { return len(p), nil }
func (s *legacyPlainTermSession) Resize(int, int) error       { return nil }
func (s *legacyPlainTermSession) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

// TestTerminalLegacyPlainTextStaysBinary 是兼容用例：旧控制端只发纯文本时，
// 读协程仍按 BinaryMessage 写回，前端 ArrayBuffer 兜底分支可照旧解码。
func TestTerminalLegacyPlainTextStaysBinary(t *testing.T) {
	browser, browserPeer := newWSPipe(t)
	session := newLegacyPlainTermSession()
	t.Cleanup(func() { _ = session.Close() })
	startBrowserPump(t, session, browserPeer)

	want := "已有终端会话在运行"
	session.chunks <- []byte(want)

	_ = browser.SetReadDeadline(time.Now().Add(3 * time.Second))
	msgType, data, err := browser.ReadMessage()
	if err != nil {
		t.Fatalf("浏览器读取旧控制端纯文本失败: %v", err)
	}
	if msgType != websocket.BinaryMessage {
		t.Fatalf("旧控制端纯文本消息类型 %d，期望 BinaryMessage(%d)", msgType, websocket.BinaryMessage)
	}
	if string(data) != want {
		t.Fatalf("旧控制端纯文本=%q，期望 %q", data, want)
	}
}
