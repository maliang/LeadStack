package api

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ctxKeyRequestID 是请求 ID 在上下文中的私有键。
type ctxKeyRequestID struct{}

// withRequestID 把请求 ID 写入上下文。
func withRequestID(r *http.Request, id string) context.Context {
	return context.WithValue(r.Context(), ctxKeyRequestID{}, id)
}

// requestIDFromContext 取出请求 ID（不存在时返回空字符串）。
func requestIDFromContext(r *http.Request) string {
	if r == nil {
		return ""
	}
	if v, ok := r.Context().Value(ctxKeyRequestID{}).(string); ok {
		return v
	}
	return ""
}

// middlewareRequestID 为每个请求生成/透传 X-Request-Id。
func (a *API) middlewareRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-Id"))
		if id == "" || len(id) > 128 {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(withRequestID(r, id)))
	})
}

// middlewareRecover 捕获 handler panic，转成 500 JSON 响应，保证进程不被打挂。
func (a *API) middlewareRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rv := recover()
			if rv == nil {
				return
			}
			// http.ErrAbortHandler 是 net/http 约定的“主动放弃连接”信号，继续向上抛。
			if rv == http.ErrAbortHandler {
				panic(rv)
			}
			a.logger.Error("处理请求时发生 panic",
				"method", r.Method,
				"path", r.URL.Path,
				"request_id", requestIDFromContext(r),
				"err", rv,
				"stack", string(debug.Stack()),
			)
			writeError(w, http.StatusInternalServerError, codeInternal, "服务器内部错误")
		}()
		next.ServeHTTP(w, r)
	})
}

// middlewareLog 用 slog 记录访问日志。
func (a *API) middlewareLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		status := rec.status
		if status == 0 {
			if isWebSocketUpgrade(r) {
				status = http.StatusSwitchingProtocols
			} else {
				status = http.StatusOK
			}
		}
		a.logger.Info("http 请求",
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"bytes", rec.bytes,
			"remote", remoteHost(r),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", requestIDFromContext(r),
		)
	})
}

// statusRecorder 记录状态码与响应体大小。
//
// 必须透传 Hijacker/Flusher，否则 /agent/ws 的协议升级会失败。
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Hijack 透传底层连接的接管能力。
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("底层 ResponseWriter 不支持 Hijack")
	}
	return hijacker.Hijack()
}

// Flush 透传刷新能力。
func (s *statusRecorder) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap 兼容 http.ResponseController。
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// isWebSocketUpgrade 判断请求是否为 WebSocket 升级请求。
func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// remoteHost 从 RemoteAddr 提取主机部分（不信任代理头，避免伪造）。
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
