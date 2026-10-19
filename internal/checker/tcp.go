package checker

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
	"time"
)

// probeTCP 直连 host:port，按连接是否建立判定；失败要区分
// DNS 解析失败 / 连接被拒绝 / 超时，便于运维定位。
func probeTCP(ctx context.Context, spec Spec, timeout time.Duration) Result {
	start := time.Now()
	target := strings.TrimSpace(spec.Target)
	if target == "" {
		return failResult(0, "TCP 目标（host:port）不能为空")
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", target)
	lat := elapsedMS(start)
	if err != nil {
		return failResult(lat, "TCP 连接失败: %s", dialErrMessage(err))
	}
	_ = conn.Close()
	return okResult(lat, 0, "TCP 连接成功")
}

// dialErrMessage 把底层连接错误翻译成可读的中文原因，保留原始错误便于排查。
func dialErrMessage(err error) string {
	if err == nil {
		return ""
	}
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr):
		return "DNS 解析失败: " + err.Error()
	case errors.Is(err, context.Canceled):
		return "探测被取消: " + err.Error()
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		return "连接超时: " + err.Error()
	case isConnectionRefused(err):
		return "连接被拒绝: " + err.Error()
	default:
		return "连接错误: " + err.Error()
	}
}

// isConnectionRefused 同时覆盖 errors.Is(ECONNREFUSED) 与错误文本，
// 因为 Windows/Linux/macOS 对 refused 的包装方式不同（Go 的错误字符串是英文常量，可安全匹配）。
func isConnectionRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection refused") || strings.Contains(msg, "actively refused")
}
