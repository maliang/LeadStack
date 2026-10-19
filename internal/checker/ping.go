package checker

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// protocolICMPv4 是 ICMPv4 的 IANA 协议号，icmp.ParseMessage 需要它。
const protocolICMPv4 = 1

// icmpConn 抽出探测需要的最小能力，便于测试注入假连接。
type icmpConn interface {
	WriteTo(b []byte, addr net.Addr) (int, error)
	ReadFrom(b []byte) (int, net.Addr, error)
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
	Close() error
}

// listenICMP 是非特权 ICMP 套接字的创建入口（测试可替换）。
var listenICMP = func(network, address string) (icmpConn, error) {
	return icmp.ListenPacket(network, address)
}

// resolveIP 是 ping 目标的解析入口（测试可替换）。
var resolveIP = func(host string) (*net.IPAddr, error) {
	return net.ResolveIPAddr("ip4", host)
}

// icmpUnsupportedMarkers 是各平台「本机不允许非特权 ICMP」的错误特征。
//
// 实测（macOS 27/Darwin 27）：udp4 ICMP socket 能创建，但任何 sendto 都返回
// `write udp 0.0.0.0:0->127.0.0.1: invalid argument`；Linux 上常见
// `socket: permission denied` / `operation not permitted`（net.ipv4.ping_group_range 未放开）。
var icmpUnsupportedMarkers = []string{
	"permission denied",
	"operation not permitted",
	"not permitted",
	"invalid argument",
	"access denied",
}

// icmpUnsupportedMessage 生成对用户可解释的文案：
// 保留底层错误原文（排查线索），并给出可执行的替代建议。
func icmpUnsupportedMessage(cause string) string {
	return "当前环境不支持非特权 ICMP（需要权限或系统未开放 ping socket），建议改用 tcp 检测（原始错误：" + cause + "）"
}

// isICMPUnsupportedError 判断错误是否属于「本机不支持非特权 ICMP」这一类。
func isICMPUnsupportedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EINVAL) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range icmpUnsupportedMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// probePing 用非特权 ICMP（udp4）探测主机存活。
//
// 安全/正确性红线：拿不到非特权 ICMP 能力时（套接字创建失败、无权限、
// 平台不允许发送）必须返回 ok=false 并给出可解释的原因，绝不能静默返回成功
// （否则会产生大量假 up）。
func probePing(ctx context.Context, spec Spec, timeout time.Duration) Result {
	start := time.Now()
	host := normalizeHost(spec.Target)
	if host == "" {
		return failResult(0, "ping 目标 host 不能为空")
	}
	dst, err := resolveIP(host)
	if err != nil {
		var addrErr *net.AddrError
		if errors.As(err, &addrErr) {
			return failResult(elapsedMS(start), "目标地址非法或不支持 IPv4: %s", err.Error())
		}
		return failResult(elapsedMS(start), "DNS 解析失败: %s", err.Error())
	}
	conn, err := listenICMP("udp4", "0.0.0.0")
	if err != nil {
		return failResult(elapsedMS(start), "%s", icmpUnsupportedMessage(err.Error()))
	}
	defer func() { _ = conn.Close() }()

	// ctx 取消时关闭套接字以中断阻塞读。
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopped:
		}
	}()
	defer close(stopped)

	id := os.Getpid() & 0xffff
	seq := int(time.Now().UnixNano() & 0x7fff)
	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("leadstack")},
	}
	wb, err := msg.Marshal(nil)
	if err != nil {
		return failResult(elapsedMS(start), "构造 ICMP 报文失败: %s", err.Error())
	}
	deadline := time.Now().Add(timeout)
	_ = conn.SetWriteDeadline(deadline)
	if _, err := conn.WriteTo(wb, dst); err != nil {
		if ctx.Err() != nil {
			return failResult(elapsedMS(start), "探测被取消: %s", ctx.Err().Error())
		}
		if isICMPUnsupportedError(err) {
			return failResult(elapsedMS(start), "%s", icmpUnsupportedMessage(err.Error()))
		}
		return failResult(elapsedMS(start), "发送 ICMP 请求失败: %s", err.Error())
	}

	_ = conn.SetReadDeadline(deadline)
	buf := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			lat := elapsedMS(start)
			if ctx.Err() != nil {
				return failResult(lat, "探测被取消: %s", ctx.Err().Error())
			}
			if isTimeout(err) {
				return failResult(lat, "ICMP 无响应（%s 内未收到回显应答）", timeout)
			}
			if isICMPUnsupportedError(err) {
				return failResult(lat, "%s", icmpUnsupportedMessage(err.Error()))
			}
			return failResult(lat, "接收 ICMP 应答失败: %s", err.Error())
		}
		reply, perr := icmp.ParseMessage(protocolICMPv4, buf[:n])
		if perr != nil {
			continue
		}
		if reply.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		echo, ok := reply.Body.(*icmp.Echo)
		if !ok || echo.ID != id || echo.Seq != seq {
			continue // 不是本次请求的应答（可能是并发探测或迟到的包）
		}
		peerText := dst.String()
		if peer != nil {
			peerText = peer.String()
		}
		return okResult(elapsedMS(start), 0, "ICMP 回显应答来自 %s", peerText)
	}
}

// normalizeHost 接受 host、host:port、[v6]:port 形式，返回裸 host。
func normalizeHost(target string) string {
	h := strings.TrimSpace(target)
	if h == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return strings.Trim(h, "[]")
}
