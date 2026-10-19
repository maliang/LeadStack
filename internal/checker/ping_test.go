package checker

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// fakeICMPConn 是一个可控的 ICMP 连接：记录发出的报文，并按脚本产生应答/错误。
type fakeICMPConn struct {
	written   []byte
	replies   [][]byte
	err       error // ReadFrom 的错误（replies 用尽后）
	writeErr  error
	closeCnt  int
	readDelay time.Duration
}

func (c *fakeICMPConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	c.written = append([]byte(nil), b...)
	return len(b), nil
}

func (c *fakeICMPConn) ReadFrom(b []byte) (int, net.Addr, error) {
	if c.readDelay > 0 {
		time.Sleep(c.readDelay)
	}
	if len(c.replies) == 0 {
		if c.err != nil {
			return 0, nil, c.err
		}
		return 0, nil, os.ErrDeadlineExceeded
	}
	reply := c.replies[0]
	c.replies = c.replies[1:]
	n := copy(b, reply)
	addr := &net.IPAddr{IP: net.IPv4(127, 0, 0, 1)}
	return n, addr, nil
}

func (c *fakeICMPConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *fakeICMPConn) SetWriteDeadline(t time.Time) error { return nil }
func (c *fakeICMPConn) Close() error {
	c.closeCnt++
	return nil
}

// withResolveIP 替换主机解析入口，让 DNS 失败分支可确定性触发。
func withResolveIP(t *testing.T, fn func(host string) (*net.IPAddr, error)) {
	t.Helper()
	old := resolveIP
	resolveIP = fn
	t.Cleanup(func() { resolveIP = old })
}

func withFakeICMP(t *testing.T, conn icmpConn, err error) {
	t.Helper()
	old := listenICMP
	listenICMP = func(network, address string) (icmpConn, error) {
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
	t.Cleanup(func() { listenICMP = old })
}

// TestProbePingUnsupportedEnvironment 覆盖红线：拿不到非特权 ICMP 权限时
// 必须 ok=false 且说明原因，绝不能静默成功。
func TestProbePingUnsupportedEnvironment(t *testing.T) {
	withFakeICMP(t, nil, errors.New("socket: permission denied"))
	r := Probe(context.Background(), Spec{Kind: KindPing, Target: "127.0.0.1"}, nil, "srv-1")
	if r.OK {
		t.Fatalf("无权限时绝不能返回成功: %+v", r)
	}
	if !strings.Contains(r.Message, "当前环境不支持非特权 ICMP") {
		t.Fatalf("message=%q 必须说明「当前环境不支持非特权 ICMP」", r.Message)
	}
	if !strings.Contains(r.Message, "permission denied") {
		t.Fatalf("应带上底层原因: %q", r.Message)
	}
}

// TestProbePingEchoReply 用假连接精确验证请求/应答匹配逻辑与 RTT 统计。
func TestProbePingEchoReply(t *testing.T) {
	// ReadFrom 第一次返回 ID 不匹配的应答，第二次按已写入的请求构造正确应答。
	lazy := &lazyReplyConn{fakeICMPConn: &fakeICMPConn{}}
	withFakeICMP(t, lazy, nil)

	r := Probe(context.Background(), Spec{Kind: KindPing, Target: "127.0.0.1", TimeoutS: 2}, nil, "srv-1")
	if !r.OK {
		t.Fatalf("期望成功: %+v", r)
	}
	if !strings.Contains(r.Message, "ICMP 回显应答来自") {
		t.Fatalf("message=%q", r.Message)
	}
	if r.LatencyMS < 20 {
		t.Fatalf("LatencyMS=%d 应包含读应答耗时", r.LatencyMS)
	}
	if lazy.otherReplies == 0 {
		t.Fatalf("应发送过无关应答以验证过滤逻辑")
	}
}

// lazyReplyConn 先返回一个 ID 不匹配的应答，再返回正确应答，验证探测会跳过无关包。
type lazyReplyConn struct {
	*fakeICMPConn
	otherReplies int
	step         int
}

func (c *lazyReplyConn) ReadFrom(b []byte) (int, net.Addr, error) {
	switch c.step {
	case 0:
		c.step++
		// 无关的 echo reply（ID/Seq 不匹配）
		msg := icmp.Message{Type: ipv4.ICMPTypeEchoReply, Code: 0, Body: &icmp.Echo{ID: 1, Seq: 1, Data: []byte("nope")}}
		raw, err := msg.Marshal(nil)
		if err != nil {
			return 0, nil, err
		}
		c.otherReplies++
		n := copy(b, raw)
		return n, &net.IPAddr{IP: net.IPv4(127, 0, 0, 1)}, nil
	default:
		// 按请求报文构造正确应答
		if len(c.written) == 0 {
			return 0, nil, os.ErrDeadlineExceeded
		}
		req, err := icmp.ParseMessage(protocolICMPv4, c.written)
		if err != nil {
			return 0, nil, err
		}
		echo, ok := req.Body.(*icmp.Echo)
		if !ok {
			return 0, nil, errors.New("请求不是 echo")
		}
		reply := icmp.Message{
			Type: ipv4.ICMPTypeEchoReply,
			Code: 0,
			Body: &icmp.Echo{ID: echo.ID, Seq: echo.Seq, Data: echo.Data},
		}
		raw, err := reply.Marshal(nil)
		if err != nil {
			return 0, nil, err
		}
		time.Sleep(25 * time.Millisecond)
		n := copy(b, raw)
		return n, &net.IPAddr{IP: net.IPv4(127, 0, 0, 1)}, nil
	}
}

func TestProbePingTimeout(t *testing.T) {
	conn := &fakeICMPConn{err: os.ErrDeadlineExceeded}
	withFakeICMP(t, conn, nil)

	r := Probe(context.Background(), Spec{Kind: KindPing, Target: "127.0.0.1", TimeoutS: 1}, nil, "srv-1")
	if r.OK {
		t.Fatalf("无应答不应成功: %+v", r)
	}
	if !strings.Contains(r.Message, "ICMP 无响应") {
		t.Fatalf("message=%q 应说明无响应", r.Message)
	}
	if conn.closeCnt == 0 {
		t.Fatalf("探测结束必须关闭套接字")
	}
}

func TestProbePingWriteFailure(t *testing.T) {
	withFakeICMP(t, &fakeICMPConn{writeErr: errors.New("network is unreachable")}, nil)
	r := Probe(context.Background(), Spec{Kind: KindPing, Target: "127.0.0.1", TimeoutS: 1}, nil, "srv-1")
	if r.OK || !strings.Contains(r.Message, "发送 ICMP 请求失败") {
		t.Fatalf("普通写入失败应报错: %+v", r)
	}
}

// TestProbePingUnsupportedWrite 覆盖 macOS 27 实测到的 sendto EINVAL：
// 必须转成对用户可解释的「环境不支持」文案，而不是把底层错误直接抛给用户。
func TestProbePingUnsupportedWrite(t *testing.T) {
	cause := &net.OpError{Op: "write", Net: "udp", Source: &net.UDPAddr{}, Addr: &net.IPAddr{},
		Err: &os.SyscallError{Syscall: "write", Err: syscall.EINVAL}}
	withFakeICMP(t, &fakeICMPConn{writeErr: cause}, nil)

	r := Probe(context.Background(), Spec{Kind: KindPing, Target: "127.0.0.1", TimeoutS: 1}, nil, "srv-1")
	if r.OK {
		t.Fatalf("环境不支持时必须 ok=false: %+v", r)
	}
	if !strings.Contains(r.Message, "当前环境不支持非特权 ICMP") {
		t.Fatalf("message=%q 应含「当前环境不支持非特权 ICMP」", r.Message)
	}
	if !strings.Contains(r.Message, "建议改用 tcp 检测") {
		t.Fatalf("message=%q 应给出替代建议", r.Message)
	}
	if !strings.Contains(r.Message, "原始错误") || !strings.Contains(r.Message, "invalid argument") {
		t.Fatalf("message=%q 应保留底层错误原文", r.Message)
	}
}

func TestIsICMPUnsupportedError(t *testing.T) {
	yes := []error{
		syscall.EINVAL,
		syscall.EPERM,
		syscall.EACCES,
		&net.OpError{Op: "write", Err: &os.SyscallError{Syscall: "write", Err: syscall.EINVAL}},
		errors.New("socket: permission denied"),
		errors.New("bind: operation not permitted"),
		errors.New("write udp 0.0.0.0:0->127.0.0.1: invalid argument"),
	}
	for _, err := range yes {
		if !isICMPUnsupportedError(err) {
			t.Errorf("%v 应被识别为「环境不支持非特权 ICMP」", err)
		}
	}
	no := []error{
		nil,
		errors.New("network is unreachable"),
		errors.New("no route to host"),
		os.ErrDeadlineExceeded,
	}
	for _, err := range no {
		if isICMPUnsupportedError(err) {
			t.Errorf("%v 不应被识别为环境不支持", err)
		}
	}
}

func TestProbePingBadHost(t *testing.T) {
	withFakeICMP(t, &fakeICMPConn{}, nil)

	// DNS 解析失败（用注入的解析器确定性构造，避免依赖本机 DNS 行为）
	withResolveIP(t, func(host string) (*net.IPAddr, error) {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	})
	r := Probe(context.Background(), Spec{Kind: KindPing, Target: "leadstack-no-such-host.invalid"}, nil, "srv-1")
	if r.OK || !strings.Contains(r.Message, "DNS 解析失败") {
		t.Fatalf("域名解析失败应报错: %+v", r)
	}

	// 解析成功但地址族不支持（IPv6 字面量走 ip4 路径）
	withResolveIP(t, func(host string) (*net.IPAddr, error) {
		return nil, &net.AddrError{Err: "no suitable address found", Addr: host}
	})
	r2 := Probe(context.Background(), Spec{Kind: KindPing, Target: "::1"}, nil, "srv-1")
	if r2.OK || !strings.Contains(r2.Message, "目标地址非法或不支持 IPv4") {
		t.Fatalf("IPv6 目标在 udp4 路径下应明确失败: %+v", r2)
	}
}

func TestProbePingContextCanceled(t *testing.T) {
	// ctx 取消时 goroutine 会 Close 连接，ReadFrom 立即返回错误并带「被取消」。
	conn := &blockingICMPConn{closed: make(chan struct{})}
	withFakeICMP(t, conn, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	r := Probe(ctx, Spec{Kind: KindPing, Target: "127.0.0.1", TimeoutS: 5}, nil, "srv-1")
	if r.OK {
		t.Fatalf("取消后不应成功: %+v", r)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("取消未及时中断探测，耗时 %s", time.Since(start))
	}
	if !strings.Contains(r.Message, "取消") {
		t.Fatalf("message=%q 应说明被取消", r.Message)
	}
}

// blockingICMPConn 的 ReadFrom 一直阻塞到 Close 被调用。
type blockingICMPConn struct {
	closed chan struct{}
	once   bool
}

func (c *blockingICMPConn) WriteTo(b []byte, addr net.Addr) (int, error) { return len(b), nil }
func (c *blockingICMPConn) ReadFrom(b []byte) (int, net.Addr, error) {
	<-c.closed
	return 0, nil, errors.New("use of closed network connection")
}
func (c *blockingICMPConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *blockingICMPConn) SetWriteDeadline(t time.Time) error { return nil }
func (c *blockingICMPConn) Close() error {
	if !c.once {
		c.once = true
		close(c.closed)
	}
	return nil
}

// TestProbePingRealLocalhost 走真实非特权 ICMP：
// 要么成功，要么必须给出「当前环境不支持非特权 ICMP」+ 替代建议——
// 不允许静默成功，也不允许把底层错误原文直接抛给用户。
func TestProbePingRealLocalhost(t *testing.T) {
	r := Probe(context.Background(), Spec{Kind: KindPing, Target: "127.0.0.1", TimeoutS: 2}, nil, "srv-1")
	if r.OK {
		if r.Message == "" {
			t.Fatalf("成功必须带摘要")
		}
		t.Logf("真实非特权 ICMP 可用: %s (latency=%dms)", r.Message, r.LatencyMS)
		return
	}
	if !strings.Contains(r.Message, "当前环境不支持非特权 ICMP") {
		t.Fatalf("真实 ICMP 失败时必须是权限/环境原因，实际 message=%q", r.Message)
	}
	if !strings.Contains(r.Message, "建议改用 tcp 检测") {
		t.Fatalf("message=%q 应给出替代建议", r.Message)
	}
	t.Logf("本机不允许非特权 ICMP（预期分支）: %s", r.Message)
}
