package api

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync/atomic"
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
			"remote", clientIP(r),
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

// trustedProxies 是可信反向代理的网段，启动时由 SetTrustedProxies 写入。
// 为空表示不信任任何代理头（默认）。
var trustedProxies atomic.Pointer[[]*net.IPNet]

// SetTrustedProxies 设置可信反向代理网段；只应在进程启动时调用一次。
func SetTrustedProxies(nets []*net.IPNet) {
	trustedProxies.Store(&nets)
}

// peerHost 从 RemoteAddr 提取主机部分（TCP 对端，可能是反向代理）。
func peerHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// clientIP 返回真实客户端 IP：对端不在可信代理列表时直接用 TCP 对端地址，
// 否则沿 X-Forwarded-For 从右往左找到第一个不可信的地址。
//
// 为什么必须显式配置可信代理：X-Forwarded-For 是客户端可伪造的，
// 无条件采信等于让任何人自选审计日志与登录限流里的 IP。
func clientIP(r *http.Request) string {
	var trusted []*net.IPNet
	if p := trustedProxies.Load(); p != nil {
		trusted = *p
	}
	return clientIPWith(r, trusted)
}

// clientIPWith 是 clientIP 的纯函数版本，便于测试。
func clientIPWith(r *http.Request, trusted []*net.IPNet) string {
	cur := peerHost(r)
	if !ipTrusted(cur, trusted) {
		return cur
	}

	// XFF 允许重复头，按出现顺序拼成一条链（左=最早=最靠近真实客户端）。
	var parts []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, item := range strings.Split(v, ",") {
			if ip := normalizeIP(item); ip != "" {
				parts = append(parts, ip)
			}
		}
	}
	if len(parts) == 0 {
		if ip := normalizeIP(r.Header.Get("X-Real-IP")); ip != "" && !ipTrusted(ip, trusted) {
			return ip
		}
		return cur
	}

	// 从右往左：右侧是最近一跳（我们自己的代理），遇到不可信地址即为客户端。
	for i := len(parts) - 1; i >= 0; i-- {
		cur = parts[i]
		if !ipTrusted(parts[i], trusted) {
			return parts[i]
		}
	}
	return cur
}

// normalizeIP 把 XFF/X-Real-IP 里的条目归一成纯 IP 文本，非法条目返回空串。
func normalizeIP(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || strings.EqualFold(s, "unknown") {
		return ""
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	s = strings.Trim(strings.TrimSpace(s), "[]")
	ip := net.ParseIP(s)
	if ip == nil {
		return ""
	}
	return ip.String()
}

// ipTrusted 判断地址是否落在可信代理网段内。
func ipTrusted(host string, trusted []*net.IPNet) bool {
	if len(trusted) == 0 {
		return false
	}
	ip := net.ParseIP(strings.Trim(strings.TrimSpace(host), "[]"))
	if ip == nil {
		return false
	}
	for _, n := range trusted {
		if n != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}
