package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/maliang/leadstack/pkg/protocol"
)

const (
	writeTimeout  = 10 * time.Second
	sendQueueSize = 64
)

// errConnClosed 表示连接已关闭，发送方应停止工作。
var errConnClosed = errors.New("连接已关闭")

// wsConn 包装 gorilla/websocket 连接。
//
// gorilla 的写操作不是并发安全的，因此所有发送都经过 out 队列，由唯一的写协程串行写出；
// 读协程负责解析信封并分发。任一侧出错都会关闭连接并唤醒所有等待者。
type wsConn struct {
	conn *websocket.Conn
	out  chan protocol.Envelope
	log  *slog.Logger

	closeOnce sync.Once
	closed    chan struct{}

	errMu sync.Mutex
	err   error
}

// newWSConn 创建连接包装器。
func newWSConn(conn *websocket.Conn, log *slog.Logger) *wsConn {
	if log == nil {
		log = slog.Default()
	}
	return &wsConn{
		conn:   conn,
		out:    make(chan protocol.Envelope, sendQueueSize),
		log:    log,
		closed: make(chan struct{}),
	}
}

// send 把信封放入发送队列；ctx 取消或连接关闭时返回错误。
func (w *wsConn) send(ctx context.Context, env protocol.Envelope) error {
	// 先做一次快速失败检查，避免连接已关闭时还往队列里塞消息。
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.closed:
		return errConnClosed
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.closed:
		return errConnClosed
	case w.out <- env:
		return nil
	}
}

// writeDirect 直接在调用协程写出一帧，仅用于没有写协程的 --once 自测路径。
func (w *wsConn) writeDirect(env protocol.Envelope) error {
	_ = w.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return w.conn.WriteJSON(env)
}

// writeLoop 是唯一写协程：串行写出 out 队列中的信封。
func (w *wsConn) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.closed:
			return
		case env := <-w.out:
			_ = w.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := w.conn.WriteJSON(env); err != nil {
				w.fail(err)
				return
			}
		}
	}
}

// readLoop 持续读取控制端帧并交给 handle 处理；解析失败的帧直接忽略。
func (w *wsConn) readLoop(ctx context.Context, handle func(protocol.Envelope)) {
	for {
		_, data, err := w.conn.ReadMessage()
		if err != nil {
			if ctx.Err() == nil {
				w.fail(err)
			} else {
				w.close()
			}
			return
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			// 坏帧不应导致断线。
			w.log.Debug("忽略无法解析的帧", "err", err)
			continue
		}
		if env.Type == "" {
			continue
		}
		handle(env)
	}
}

// fail 记录首个错误并关闭连接。
func (w *wsConn) fail(err error) {
	w.errMu.Lock()
	if w.err == nil {
		w.err = err
	}
	w.errMu.Unlock()
	w.close()
}

// close 关闭底层 socket 与 closed 信号，可重复调用。
func (w *wsConn) close() {
	w.closeOnce.Do(func() {
		_ = w.conn.Close()
		close(w.closed)
	})
}

// closeGracefully 先发送 WebSocket close 控制帧再关闭底层连接，
// 让控制端记录为正常关闭而不是 1006 异常断开。
// WriteControl 与写协程并发是 gorilla 明确允许的。
func (w *wsConn) closeGracefully() {
	_ = w.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second),
	)
	w.close()
}

// Err 返回连接的首个错误。
func (w *wsConn) Err() error {
	w.errMu.Lock()
	defer w.errMu.Unlock()
	return w.err
}
