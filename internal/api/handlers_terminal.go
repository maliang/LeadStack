package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/maliang/leadstack/internal/term"
	"github.com/maliang/leadstack/pkg/protocol"
)

// 终端参数（契约第 40 节）。
const (
	terminalTicketTTL   = 60 * time.Second
	terminalDefaultCols = 120
	terminalDefaultRows = 32
	terminalMaxDim      = 1000
)

// 终端控制帧（首帧 JSON 元信息）。
type terminalControl struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// terminalTicket 是一次性终端凭证。
type terminalTicket struct {
	ServerID  string
	Actor     string
	Role      string
	ExpiresAt time.Time
	Cols      int
	Rows      int
}

// terminalTicketResponse 是票据响应。
type terminalTicketResponse struct {
	Ticket    string `json:"ticket"`
	ExpiresIn int    `json:"expires_in"`
}

// issueTerminalTicket 生成一次性 ticket。
func (a *API) issueTerminalTicket(r *http.Request, serverID string, cols, rows int) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成终端票据: %w", err)
	}
	ticket := base64.RawURLEncoding.EncodeToString(buf)
	a.termTickets.Store(ticket, &terminalTicket{
		ServerID: serverID, Actor: actorName(r), Role: actorRole(r),
		ExpiresAt: time.Now().Add(terminalTicketTTL), Cols: cols, Rows: rows,
	})
	// 清理过期票据（简单惰性清理，量小）。
	a.termTickets.Range(func(key, value any) bool {
		if t, ok := value.(*terminalTicket); ok && time.Now().After(t.ExpiresAt) {
			a.termTickets.Delete(key)
		}
		return true
	})
	return ticket, nil
}

// consumeTerminalTicket 取出并删除票据（一次性）。
func (a *API) consumeTerminalTicket(ticket, serverID string) (*terminalTicket, error) {
	value, ok := a.termTickets.LoadAndDelete(ticket)
	if !ok {
		return nil, errors.New("终端票据无效或已被使用")
	}
	t, ok := value.(*terminalTicket)
	if !ok {
		return nil, errors.New("终端票据无效")
	}
	if time.Now().After(t.ExpiresAt) {
		return nil, errors.New("终端票据已过期，请重新申请")
	}
	if t.ServerID != serverID {
		return nil, errors.New("终端票据与目标服务器不匹配")
	}
	return t, nil
}

// handleTerminalTicket 处理 POST /api/v1/servers/{id}/terminal-ticket。
func (a *API) handleTerminalTicket(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.TerminalEnabled {
		writeError(w, http.StatusForbidden, codeForbidden,
			"Web 终端未启用：请设置 LEADSTACK_TERMINAL_ENABLED=true 后重启控制端")
		return
	}
	serverID := chi.URLParam(r, "serverID")
	if _, err := a.st.GetServer(r.Context(), serverID); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	cols, rows := terminalDefaultCols, terminalDefaultRows
	if raw := r.URL.Query().Get("cols"); raw != "" {
		if v, err := strconvAtoi(raw); err == nil && v > 0 && v <= terminalMaxDim {
			cols = v
		}
	}
	if raw := r.URL.Query().Get("rows"); raw != "" {
		if v, err := strconvAtoi(raw); err == nil && v > 0 && v <= terminalMaxDim {
			rows = v
		}
	}
	ticket, err := a.issueTerminalTicket(r, serverID, cols, rows)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	a.logger.Info("发放终端票据", "server_id", serverID, "user", actorName(r))
	writeData(w, http.StatusOK, terminalTicketResponse{Ticket: ticket, ExpiresIn: int(terminalTicketTTL.Seconds())})
}

// terminalUpgrader 是终端 WebSocket 升级器。
var terminalUpgrader = websocket.Upgrader{
	ReadBufferSize:  8192,
	WriteBufferSize: 8192,
	CheckOrigin:     func(*http.Request) bool { return true },
}

// handleTerminalWS 处理 GET /api/v1/servers/{id}/terminal?ticket=。
//
// 帧约定：服务端→客户端用二进制帧传终端数据；客户端→服务端用文本帧传控制 JSON
// （`{"type":"input","data":"..."}` / `{"type":"resize","cols":..,"rows":..}`）。
func (a *API) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.TerminalEnabled {
		writeError(w, http.StatusForbidden, codeForbidden, "Web 终端未启用")
		return
	}
	serverID := chi.URLParam(r, "serverID")
	ticket := r.URL.Query().Get("ticket")
	claims, err := a.consumeTerminalTicket(ticket, serverID)
	if err != nil {
		writeError(w, http.StatusForbidden, codeForbidden, err.Error())
		return
	}
	conn, err := terminalUpgrader.Upgrade(w, r, nil)
	if err != nil {
		a.logger.Warn("终端 WebSocket 升级失败", "server_id", serverID, "err", err)
		return
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			a.logger.Debug("关闭终端 WebSocket 失败", "err", cerr)
		}
	}()

	sessionID := uuid.NewString()
	session, err := a.openTerminalSession(r.Context(), serverID, sessionID, claims.Cols, claims.Rows)
	if err != nil {
		msg := fmt.Sprintf("打开终端会话失败: %v", err)
		a.logger.Warn("打开终端会话失败", "server_id", serverID, "err", err)
		if encoded, merr := json.Marshal(map[string]any{"type": "error", "data": msg}); merr == nil {
			_ = conn.WriteMessage(websocket.TextMessage, encoded)
		}
		return
	}
	defer func() {
		if cerr := session.Close(); cerr != nil {
			a.logger.Debug("关闭终端会话失败", "err", cerr)
		}
	}()

	openedAt := a.nowMS()
	a.auditManual(r, "terminal.open", "server", serverID,
		fmt.Sprintf(`{"session_id":%q,"user":%q}`, sessionID, claims.Actor))
	defer func() {
		duration := a.nowMS() - openedAt
		a.auditManual(r, "terminal.close", "server", serverID,
			fmt.Sprintf(`{"session_id":%q,"user":%q,"duration_ms":%d}`, sessionID, claims.Actor, duration))
	}()

	// 会话 → 浏览器
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 8192)
		for {
			n, rerr := session.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	// 浏览器 → 会话
	for {
		msgType, data, rerr := conn.ReadMessage()
		if rerr != nil {
			break
		}
		if msgType == websocket.BinaryMessage {
			if _, werr := session.Write(data); werr != nil {
				break
			}
			continue
		}
		var control terminalControl
		if err := json.Unmarshal(data, &control); err != nil {
			continue
		}
		switch control.Type {
		case "input":
			if _, werr := session.Write([]byte(control.Data)); werr != nil {
				break
			}
		case "resize":
			if control.Cols > 0 && control.Rows > 0 {
				if rerr := session.Resize(control.Cols, control.Rows); rerr != nil {
					a.logger.Debug("调整终端大小失败", "err", rerr)
				}
			}
		case "ping":
			// 心跳：保持连接活跃。
		default:
			// 未知控制帧忽略（向前兼容）。
		}
	}
	<-done
}

// openTerminalSession 按服务器模式打开终端会话。
func (a *API) openTerminalSession(ctx context.Context, serverID, sessionID string, cols, rows int) (term.Session, error) {
	srv, err := a.st.GetServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if srv.Mode == "ssh" {
		if a.termOpener == nil {
			return nil, errors.New("SSH 终端通道未启用")
		}
		return a.termOpener.Open(ctx, serverID)
	}
	// agent 通道：向 agent 下发 term.open，并通过 term.data 帧双向转发。
	conn, ok := a.hub.Get(serverID)
	if !ok {
		return nil, errors.New("该服务器的 agent 未在线")
	}
	session := newAgentTermSession(conn, sessionID)
	if err := session.open(cols, rows); err != nil {
		return nil, err
	}
	a.agentTerms.Store(sessionID, session)
	return session, nil
}

// routeAgentTermData 把 agent 上报的 term.data 转发给对应终端会话。
func (a *API) routeAgentTermData(sessionID, b64 string) {
	value, ok := a.agentTerms.Load(sessionID)
	if !ok {
		return
	}
	session, ok := value.(*agentTermSession)
	if !ok {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		a.logger.Debug("终端数据解码失败", "session_id", sessionID, "err", err)
		return
	}
	session.push(raw)
}

// agentTermSession 是 agent 模式的终端会话（经控制端 WS 转发）。
type agentTermSession struct {
	conn      *agentConn
	sessionID string

	mu     sync.Mutex
	chunks chan []byte
	closed bool
	done   chan struct{}
	once   sync.Once
}

// newAgentTermSession 创建 agent 终端会话。
func newAgentTermSession(conn *agentConn, sessionID string) *agentTermSession {
	return &agentTermSession{
		conn: conn, sessionID: sessionID,
		chunks: make(chan []byte, 64), done: make(chan struct{}),
	}
}

// open 发送 term.open。
func (s *agentTermSession) open(cols, rows int) error {
	payload, err := json.Marshal(protocol.TermOpen{SessionID: s.sessionID, Cols: cols, Rows: rows})
	if err != nil {
		return fmt.Errorf("序列化 term.open: %w", err)
	}
	return s.conn.writeEnvelope(protocol.Envelope{
		Type: protocol.TypeTermOpen, ID: uuid.NewString(), TS: time.Now().UnixMilli(), Payload: payload,
	})
}

// push 把 agent 输出塞进读缓冲（非阻塞，满了丢弃旧数据以免拖垮连接）。
func (s *agentTermSession) push(data []byte) {
	select {
	case s.chunks <- data:
	default:
		// 缓冲满：丢弃一块，保证控制端不被终端输出拖死。
	}
}

// Read 实现 term.Session。
func (s *agentTermSession) Read(p []byte) (int, error) {
	select {
	case data := <-s.chunks:
		return copy(p, data), nil
	case <-s.done:
		return 0, errors.New("终端会话已关闭")
	}
}

// Write 实现 term.Session：发送 term.data。
func (s *agentTermSession) Write(p []byte) (int, error) {
	payload, err := json.Marshal(protocol.TermData{
		SessionID: s.sessionID, B64: base64.StdEncoding.EncodeToString(p),
	})
	if err != nil {
		return 0, fmt.Errorf("序列化 term.data: %w", err)
	}
	if err := s.conn.writeEnvelope(protocol.Envelope{
		Type: protocol.TypeTermData, ID: uuid.NewString(), TS: time.Now().UnixMilli(), Payload: payload,
	}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Resize 实现 term.Session。
func (s *agentTermSession) Resize(cols, rows int) error {
	payload, err := json.Marshal(protocol.TermResize{SessionID: s.sessionID, Cols: cols, Rows: rows})
	if err != nil {
		return fmt.Errorf("序列化 term.resize: %w", err)
	}
	return s.conn.writeEnvelope(protocol.Envelope{
		Type: protocol.TypeTermResize, ID: uuid.NewString(), TS: time.Now().UnixMilli(), Payload: payload,
	})
}

// Close 实现 term.Session。
func (s *agentTermSession) Close() error {
	var err error
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.done)
		payload, merr := json.Marshal(protocol.TermClose{SessionID: s.sessionID})
		if merr != nil {
			err = merr
			return
		}
		err = s.conn.writeEnvelope(protocol.Envelope{
			Type: protocol.TypeTermClose, ID: uuid.NewString(), TS: time.Now().UnixMilli(), Payload: payload,
		})
	})
	return err
}
