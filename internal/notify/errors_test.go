package notify

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// roundTripFunc 让测试可以注入任意传输层错误（离线、确定性）。
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func webhookChannel(url string) Channel {
	return Channel{Kind: KindWebhook, Config: map[string]any{"url": url}}
}

func TestErrorKindLabelsCategoriesAndRetryable(t *testing.T) {
	cases := []struct {
		kind     ErrorKind
		label    string
		category string
		retry    bool
	}{
		{ErrorKindConfig, "配置无效", "config", false},
		{ErrorKindDNS, "DNS 解析失败", "network", false},
		{ErrorKindConnect, "连接失败", "network", false},
		{ErrorKindTLS, "TLS 握手失败", "network", false},
		{ErrorKindTimeout, "请求超时", "network", true},
		{ErrorKindCanceled, "已取消", "canceled", false},
		{ErrorKindAuth, "凭据被拒绝", "provider", false},
		{ErrorKindHTTP, "服务商返回错误", "provider", false},
		{ErrorKindServer, "服务商暂时不可用", "provider", true},
		{ErrorKindProvider, "服务商拒绝", "provider", false},
		{ErrorKindInternal, "内部错误", "internal", false},
	}
	for _, tc := range cases {
		if got := tc.kind.Label(); got != tc.label {
			t.Errorf("%s.Label() = %q, want %q", tc.kind, got, tc.label)
		}
		if got := tc.kind.Category(); got != tc.category {
			t.Errorf("%s.Category() = %q, want %q", tc.kind, got, tc.category)
		}
		if got := tc.kind.Retryable(); got != tc.retry {
			t.Errorf("%s.Retryable() = %v, want %v", tc.kind, got, tc.retry)
		}
	}
}

func TestClassificationDNS(t *testing.T) {
	c := NewClient(Options{HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &net.DNSError{Err: "no such host", Name: "notify.example.test", IsNotFound: true}
	})}})
	err := c.Send(context.Background(), webhookChannel("https://notify.example.test/hook"), testMessage())
	se, ok := AsSendError(err)
	if !ok {
		t.Fatalf("错误不是 *SendError: %T %v", err, err)
	}
	if se.Kind != ErrorKindDNS {
		t.Fatalf("Kind = %s, want dns（错误原文：%v）", se.Kind, err)
	}
	if !strings.Contains(err.Error(), "DNS 解析失败") {
		t.Errorf("错误信息应包含中文分类: %v", err)
	}
	if !strings.Contains(err.Error(), "请求失败") {
		t.Errorf("错误信息应保留原始诊断: %v", err)
	}
	if KindOf(err) != ErrorKindDNS {
		t.Errorf("KindOf = %s", KindOf(err))
	}
}

func TestClassificationConnectRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	// 连接失败不重试，所以不需要缩短退避。
	c := NewClient(Options{})
	err := c.Send(context.Background(), webhookChannel(url), testMessage())
	se, ok := AsSendError(err)
	if !ok {
		t.Fatalf("错误不是 *SendError: %T %v", err, err)
	}
	if se.Kind != ErrorKindConnect {
		t.Fatalf("Kind = %s, want connect（错误：%v）", se.Kind, err)
	}
	if !strings.Contains(err.Error(), "连接失败") {
		t.Errorf("分类标签缺失: %v", err)
	}
}

func TestClassificationTLSCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	// 默认客户端不信任 httptest 的自签证书 → 证书校验失败。
	c := NewClient(Options{HTTPClient: &http.Client{}})
	err := c.Send(context.Background(), webhookChannel(srv.URL), testMessage())
	se, ok := AsSendError(err)
	if !ok {
		t.Fatalf("错误不是 *SendError: %T %v", err, err)
	}
	if se.Kind != ErrorKindTLS {
		t.Fatalf("Kind = %s, want tls（错误：%v）", se.Kind, err)
	}
	if !strings.Contains(err.Error(), "TLS 握手失败") {
		t.Errorf("分类标签缺失: %v", err)
	}
}

func TestClassificationTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer slow.Close()

	c := NewClient(Options{Timeout: 40 * time.Millisecond, Retry: &RetryPolicy{}})
	err := c.Send(context.Background(), webhookChannel(slow.URL), testMessage())
	se, ok := AsSendError(err)
	if !ok {
		t.Fatalf("错误不是 *SendError: %T %v", err, err)
	}
	if se.Kind != ErrorKindTimeout {
		t.Fatalf("Kind = %s, want timeout（错误：%v）", se.Kind, err)
	}
	if !se.Kind.Retryable() {
		t.Error("超时应当标记为可重试")
	}
}

func TestClassificationCanceled(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := NewClient(Options{Retry: &RetryPolicy{}}).Send(ctx, webhookChannel(srv.URL), testMessage())
	if se, ok := AsSendError(err); !ok || se.Kind != ErrorKindCanceled {
		t.Fatalf("Kind = %v, want canceled（错误：%v）", KindOf(err), err)
	}
}

func TestClassificationHTTPStatus(t *testing.T) {
	cases := []struct {
		status int
		kind   ErrorKind
	}{
		{http.StatusUnauthorized, ErrorKindAuth},
		{http.StatusForbidden, ErrorKindAuth},
		{http.StatusBadRequest, ErrorKindHTTP},
		{http.StatusNotFound, ErrorKindHTTP},
		{http.StatusTooManyRequests, ErrorKindHTTP}, // 429 属于 4xx，不重试
		{http.StatusInternalServerError, ErrorKindServer},
		{http.StatusBadGateway, ErrorKindServer},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			rec := &recorder{}
			srv := httptest.NewServer(rec.serve(tc.status, "provider says no"))
			defer srv.Close()

			// 5xx 可重试，用零退避避免测试变慢。
			c := NewClient(Options{Retry: &RetryPolicy{MaxRetries: 1, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}})
			err := c.Send(context.Background(), webhookChannel(srv.URL), testMessage())
			se, ok := AsSendError(err)
			if !ok {
				t.Fatalf("错误不是 *SendError: %T %v", err, err)
			}
			if se.Kind != tc.kind {
				t.Fatalf("Kind = %s, want %s（错误：%v）", se.Kind, tc.kind, err)
			}
			if se.StatusCode != tc.status {
				t.Errorf("StatusCode = %d, want %d", se.StatusCode, tc.status)
			}
			if !strings.Contains(err.Error(), "provider says no") {
				t.Errorf("错误应包含响应片段: %v", err)
			}
			if se.ResponseSnippet != "provider says no" {
				t.Errorf("ResponseSnippet = %q", se.ResponseSnippet)
			}
		})
	}
}

func TestClassificationProviderBusinessError(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, `{"errcode":300001,"errmsg":"invalid token"}`))
	defer srv.Close()

	c := NewClient(Options{})
	ch := Channel{Kind: KindDingTalk, Config: map[string]any{
		"webhook": srv.URL + "/robot/send?access_token=fake-token-123456",
	}}
	err := c.Send(context.Background(), ch, testMessage())
	se, ok := AsSendError(err)
	if !ok {
		t.Fatalf("错误不是 *SendError: %T %v", err, err)
	}
	if se.Kind != ErrorKindProvider {
		t.Fatalf("Kind = %s, want provider（错误：%v）", se.Kind, err)
	}
	if se.ProviderCode != "errcode=300001" {
		t.Errorf("ProviderCode = %q", se.ProviderCode)
	}
	if se.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", se.StatusCode)
	}
	msg := err.Error()
	for _, want := range []string{"服务商拒绝", "errcode=300001", "invalid token"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息 %q 缺少 %q", msg, want)
		}
	}
	if !strings.Contains(se.ResponseSnippet, "invalid token") {
		t.Errorf("ResponseSnippet 缺失: %q", se.ResponseSnippet)
	}
}

func TestClassificationConfigAndInternal(t *testing.T) {
	c := NewClient(Options{})
	if se, ok := AsSendError(c.Send(context.Background(), Channel{Kind: KindWebhook}, testMessage())); !ok || se.Kind != ErrorKindConfig {
		t.Errorf("缺 url 应归为 config，实际 %v", KindOf(c.Send(context.Background(), Channel{Kind: KindWebhook}, testMessage())))
	}
	if se, ok := AsSendError((*Client)(nil).Send(context.Background(), validChannel(KindWebhook), testMessage())); !ok || se.Kind != ErrorKindInternal {
		t.Errorf("nil client 应归为 internal，实际 %v", KindOf((*Client)(nil).Send(context.Background(), validChannel(KindWebhook), testMessage())))
	}
	if KindOf(nil) != "" {
		t.Errorf("KindOf(nil) 应为空")
	}
	if _, ok := AsSendError(errors.New("plain")); ok {
		t.Error("普通错误不应被识别为 SendError")
	}
	if KindOf(errors.New("plain")) != ErrorKindInternal {
		t.Error("未知错误应归为 internal")
	}
}

// 分类错误里的 Message / ResponseSnippet 也必须脱敏。
func TestSendErrorSnippetIsRedacted(t *testing.T) {
	const secret = "Pj8sEcretValueNeverLeak"
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusInternalServerError, "echo token "+secret+" rejected"))
	defer srv.Close()

	c := NewClient(Options{Retry: &RetryPolicy{}})
	ch := Channel{Kind: KindWebhook, Config: map[string]any{
		"url":     srv.URL + "/hook?access_token=" + secret,
		"headers": map[string]any{"Authorization": "Bearer " + secret},
	}}
	err := c.Send(context.Background(), ch, testMessage())
	se, ok := AsSendError(err)
	if !ok {
		t.Fatalf("错误不是 *SendError: %T", err)
	}
	if strings.Contains(se.Message, secret) || strings.Contains(se.ResponseSnippet, secret) || strings.Contains(err.Error(), secret) {
		t.Errorf("SendError 字段泄漏了密钥: %+v", se)
	}
	if !strings.Contains(se.ResponseSnippet, "Pj8s***") {
		t.Errorf("ResponseSnippet 应保留脱敏前缀: %q", se.ResponseSnippet)
	}
}

func TestSendErrorErrorRendering(t *testing.T) {
	var nilErr *SendError
	if got := nilErr.Error(); got != "notify: <nil>" {
		t.Errorf("nil SendError.Error() = %q", got)
	}
	e := &SendError{
		Kind:         ErrorKindServer,
		Channel:      KindEmail,
		StatusCode:   0,
		ProviderCode: "SMTP 451",
		Message:      "临时失败",
	}
	got := e.Error()
	for _, want := range []string{"notify:", "邮件", "服务商暂时不可用", "SMTP 451", "临时失败"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q 缺少 %q", got, want)
		}
	}
}
