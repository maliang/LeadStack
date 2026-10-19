package checker

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
)

func TestProbeTCPOpenPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	r := Probe(context.Background(), Spec{Kind: KindTCP, Target: ln.Addr().String(), TimeoutS: 3}, nil, "srv-1")
	if !r.OK {
		t.Fatalf("期望探测成功，得到 OK=false message=%q", r.Message)
	}
	if r.TS == 0 {
		t.Fatalf("Result.TS 未设置")
	}
	if r.Message == "" {
		t.Fatalf("成功结果应带摘要 message")
	}
}

func TestProbeTCPClosedPortRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // 立刻关闭端口，连接应被拒绝

	r := Probe(context.Background(), Spec{Kind: KindTCP, Target: addr, TimeoutS: 3}, nil, "srv-1")
	if r.OK {
		t.Fatalf("关闭的端口不应探测成功: %+v", r)
	}
	if !strings.Contains(r.Message, "连接被拒绝") {
		t.Fatalf("期望「连接被拒绝」，得到 %q", r.Message)
	}
}

func TestProbeTCPDNSFailure(t *testing.T) {
	// 注意：本机 DNS 存在泛解析（不存在的域名也会被解析到本地代理），
	// 因此这里用超过 63 字符的非法 label，保证解析必然失败且与网络环境无关。
	badHost := strings.Repeat("a", 70) + ".invalid:80"
	r := Probe(context.Background(), Spec{Kind: KindTCP, Target: badHost, TimeoutS: 5}, nil, "srv-1")
	if r.OK {
		t.Fatalf("不存在的域名不应成功: %+v", r)
	}
	if !strings.Contains(r.Message, "DNS 解析失败") {
		t.Fatalf("期望「DNS 解析失败」，得到 %q", r.Message)
	}
}

// TestDialErrMessageClassification 直接对分类器做确定性断言：
// 本机存在透明代理，无法用真实网络稳定复现 timeout，因此用合成错误覆盖。
func TestDialErrMessageClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "DNS", err: &net.DNSError{Err: "no such host", Name: "x", IsNotFound: true}, want: "DNS 解析失败"},
		{name: "超时", err: &timeoutErr{}, want: "连接超时"},
		{name: "拒绝", err: &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrPermission}, want: "连接错误"},
		{name: "取消", err: context.Canceled, want: "探测被取消"},
		{name: "截止", err: context.DeadlineExceeded, want: "连接超时"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dialErrMessage(tc.err)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("dialErrMessage(%v)=%q 应包含 %q", tc.err, got, tc.want)
			}
		})
	}
	// refused 走文本/errno 双路径
	refused := errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
	if got := dialErrMessage(refused); !strings.Contains(got, "连接被拒绝") {
		t.Fatalf("refused 分类失败: %q", got)
	}
	if got := dialErrMessage(nil); got != "" {
		t.Fatalf("nil 错误应返回空串，得到 %q", got)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string { return "i/o timeout" }
func (timeoutErr) Timeout() bool { return true }

func TestProbeTCPContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := Probe(ctx, Spec{Kind: KindTCP, Target: "192.0.2.1:80", TimeoutS: 5}, nil, "srv-1")
	if r.OK {
		t.Fatalf("已取消的 ctx 不应成功: %+v", r)
	}
	if !strings.Contains(r.Message, "取消") {
		t.Fatalf("期望取消提示，得到 %q", r.Message)
	}
}

func TestProbeTCPEmptyTarget(t *testing.T) {
	r := Probe(context.Background(), Spec{Kind: KindTCP, Target: "   "}, nil, "srv-1")
	if r.OK || !strings.Contains(r.Message, "不能为空") {
		t.Fatalf("空目标应失败并说明原因，得到 %+v", r)
	}
}

func TestProbeTCPShortTimeoutStillReturnsResult(t *testing.T) {
	// 本机网络存在透明拦截，无法保证 192.0.2.1 一定超时；
	// 这里只要求「无论如何都返回一个带 message 的 Result，且不 panic」。
	r := Probe(context.Background(), Spec{Kind: KindTCP, Target: "192.0.2.1:80", TimeoutS: 1}, nil, "srv-1")
	if r.TS == 0 {
		t.Fatalf("必须返回 Result: %+v", r)
	}
	if !r.OK && r.Message == "" {
		t.Fatalf("失败必须有 message")
	}
}

func TestProbeUnknownKind(t *testing.T) {
	r := Probe(context.Background(), Spec{Kind: Kind("bogus"), Target: "x"}, nil, "srv-1")
	if r.OK || !strings.Contains(r.Message, "未知检测类型") {
		t.Fatalf("未知类型应失败，得到 %+v", r)
	}
}

func TestProbeProcessAndScriptNeedExecutor(t *testing.T) {
	for _, kind := range []Kind{KindProcess, KindScript} {
		r := Probe(context.Background(), Spec{Kind: kind, Target: "x"}, nil, "srv-1")
		if r.OK || !strings.Contains(r.Message, "需要目标机执行通道") {
			t.Fatalf("%s 在 Executor 为 nil 时应失败，得到 %+v", kind, r)
		}
	}
}

func TestProbeNeverPanicsOnOddParams(t *testing.T) {
	specs := []Spec{
		{Kind: KindHTTP, Target: "http://127.0.0.1:1/", Params: map[string]any{}},
		{Kind: KindHTTP, Target: "http://127.0.0.1:1/", Params: map[string]any{"expect_status": []any{200}}},
		{Kind: KindHTTP, Target: "not a url", Params: nil},
		{Kind: KindPing, Target: "", Params: nil},
		{Kind: KindPing, Target: "[::1]:80", Params: nil},
		{Kind: KindProcess, Target: "", Params: nil},
		{Kind: KindScript, Target: "", Params: nil},
		{Kind: KindTCP, Target: ":::", Params: nil},
	}
	for i, spec := range specs {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					t.Fatalf("Probe 第 %d 个用例 panic: %v", i, rec)
				}
			}()
			r := Probe(context.Background(), spec, &fakeExecutor{}, "srv-1")
			if r.TS == 0 {
				t.Fatalf("第 %d 个用例 Result.TS 未设置: %+v", i, r)
			}
			if !r.OK && r.Message == "" {
				t.Fatalf("第 %d 个用例失败但无 message: %+v", i, r)
			}
		}()
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"example.com":    "example.com",
		"example.com:80": "example.com",
		"[::1]:80":       "::1",
		" 1.2.3.4 ":      "1.2.3.4",
		"":               "",
		"[fe80::1]":      "fe80::1",
	}
	for in, want := range cases {
		if got := normalizeHost(in); got != want {
			t.Errorf("normalizeHost(%q)=%q want %q", in, got, want)
		}
	}
}

func TestResultLatencyNonNegative(t *testing.T) {
	r := Probe(context.Background(), Spec{Kind: KindTCP, Target: "127.0.0.1:1", TimeoutS: 1}, nil, "srv-1")
	if r.LatencyMS < 0 {
		t.Fatalf("LatencyMS 不能为负: %d", r.LatencyMS)
	}
}
