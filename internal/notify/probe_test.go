package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Probe 对「服务商已经应答」的情况必须算可达（即使 4xx/5xx/业务码非 0）。
func TestProbeHTTPProviderResponsesAreReachable(t *testing.T) {
	cases := []struct {
		name             string
		ch               Channel
		status           int
		body             string
		wantStatus       int
		wantProviderCode string
		wantSnippet      string
	}{
		{
			name:   "webhook 401",
			ch:     Channel{Kind: KindWebhook, Config: map[string]any{"url": "SRV/hook"}},
			status: http.StatusUnauthorized, body: `{"error":"invalid token"}`,
			wantStatus: 401, wantSnippet: "invalid token",
		},
		{
			name: "钉钉 errcode",
			ch: Channel{Kind: KindDingTalk, Config: map[string]any{
				"webhook": "SRV/robot/send?access_token=fakefakefake",
			}},
			status: http.StatusOK, body: `{"errcode":300005,"errmsg":"token is not exist"}`,
			wantStatus: 200, wantProviderCode: "errcode=300005", wantSnippet: "token is not exist",
		},
		{
			name: "企微 errcode",
			ch: Channel{Kind: KindWeCom, Config: map[string]any{
				"webhook": "SRV/cgi-bin/webhook/send?key=fakefakefake",
			}},
			status: http.StatusOK, body: `{"errcode":93000,"errmsg":"invalid webhook url"}`,
			wantStatus: 200, wantProviderCode: "errcode=93000", wantSnippet: "invalid webhook url",
		},
		{
			name: "飞书 code",
			ch: Channel{Kind: KindFeishu, Config: map[string]any{
				"webhook": "SRV/open-apis/bot/v2/hook/fakefakefake",
			}},
			status: http.StatusOK, body: `{"code":19001,"msg":"param invalid: incoming webhook access token invalid"}`,
			wantStatus: 200, wantProviderCode: "code=19001", wantSnippet: "incoming webhook access token invalid",
		},
		{
			name: "slack 404",
			ch: Channel{Kind: KindSlack, Config: map[string]any{
				"webhook": "SRV/services/T000/B000/fakefakefake",
			}},
			status: http.StatusNotFound, body: `no_team`,
			wantStatus: 404, wantSnippet: "no_team",
		},
		{
			name: "telegram 401",
			ch: Channel{Kind: KindTelegram, Config: map[string]any{
				"bot_token": "123456789:fakefakefakefakefake", "chat_id": "1", "api_base": "SRV",
			}},
			status: http.StatusUnauthorized, body: `{"ok":false,"error_code":401,"description":"Unauthorized"}`,
			wantStatus: 401, wantProviderCode: "error_code=401", wantSnippet: "Unauthorized",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := httptest.NewServer(rec.serve(tc.status, tc.body))
			defer srv.Close()
			ch := tc.ch
			ch.Config = rewriteSRV(ch.Config, srv.URL)

			res, err := NewClient(Options{Retry: &RetryPolicy{}}).Probe(context.Background(), ch)
			if err != nil {
				t.Fatalf("服务商已应答，Probe 不应返回错误: %v", err)
			}
			if !res.Reachable {
				t.Fatal("Reachable 应为 true")
			}
			if res.StatusCode != tc.wantStatus {
				t.Errorf("StatusCode = %d, want %d", res.StatusCode, tc.wantStatus)
			}
			if res.ProviderCode != tc.wantProviderCode {
				t.Errorf("ProviderCode = %q, want %q", res.ProviderCode, tc.wantProviderCode)
			}
			if !strings.Contains(res.ResponseSnippet, tc.wantSnippet) {
				t.Errorf("ResponseSnippet = %q, 应包含 %q", res.ResponseSnippet, tc.wantSnippet)
			}
			if rec.count() != 1 {
				t.Errorf("Probe 只应发出 1 次请求，实际 %d", rec.count())
			}
		})
	}
}

// rewriteSRV 把测试用例里占位的 "SRV" 替换成 httptest 的地址。
func rewriteSRV(cfg map[string]any, base string) map[string]any {
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if s, ok := v.(string); ok && strings.HasPrefix(s, "SRV") {
			out[k] = base + strings.TrimPrefix(s, "SRV")
			continue
		}
		out[k] = v
	}
	return out
}

// Probe 使用与 Send 完全相同的报文构造（可用记录器逐字节对比）。
func TestProbeSendsSameShapeAsSend(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusUnauthorized, `{"error":"bad token"}`))
	defer srv.Close()

	ch := Channel{Kind: KindWebhook, Config: map[string]any{
		"url":     srv.URL + "/hook",
		"headers": map[string]any{"X-Probe": "yes"},
	}}
	c := NewClient(Options{Retry: &RetryPolicy{}})
	if _, err := c.Probe(context.Background(), ch); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if err := c.Send(context.Background(), ch, testMessage()); err == nil {
		t.Fatal("401 时 Send 应返回错误")
	}
	if rec.count() != 2 {
		t.Fatalf("请求数 = %d, want 2", rec.count())
	}
	probeReq := rec.reqs[0]
	sendReq := rec.reqs[1]
	if probeReq.method != sendReq.method {
		t.Errorf("method 不一致: %s vs %s", probeReq.method, sendReq.method)
	}
	if probeReq.url.Path != sendReq.url.Path {
		t.Errorf("path 不一致: %s vs %s", probeReq.url.Path, sendReq.url.Path)
	}
	if probeReq.header.Get("Content-Type") != "application/json" || probeReq.header.Get("X-Probe") != "yes" {
		t.Errorf("请求头不一致: %v", probeReq.header)
	}
	var probeBody, sendBody map[string]any
	if err := json.Unmarshal(probeReq.body, &probeBody); err != nil {
		t.Fatalf("probe body 不是 JSON: %v", err)
	}
	if err := json.Unmarshal(sendReq.body, &sendBody); err != nil {
		t.Fatalf("send body 不是 JSON: %v", err)
	}
	for _, k := range []string{"title", "body", "text", "severity", "status", "ts", "recovered"} {
		if _, ok := probeBody[k]; !ok {
			t.Errorf("probe body 缺少字段 %s: %s", k, probeReq.body)
		}
		if _, ok := sendBody[k]; !ok {
			t.Errorf("send body 缺少字段 %s: %s", k, sendReq.body)
		}
	}
	if probeBody["severity"] != string(SeverityInfo) {
		t.Errorf("probe severity = %v, want info", probeBody["severity"])
	}
}

// Probe 传输层失败必须返回分类错误（DNS / 连接 / 超时），且不重试。
func TestProbeTransportFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := srv.URL
	srv.Close()

	c := NewClient(Options{Retry: &RetryPolicy{}})
	res, err := c.Probe(context.Background(), webhookChannel(deadURL))
	if err == nil {
		t.Fatal("连不上时 Probe 应返回错误")
	}
	if KindOf(err) != ErrorKindConnect {
		t.Errorf("Kind = %s, want connect", KindOf(err))
	}
	if res.ErrorKind != ErrorKindConnect {
		t.Errorf("ProbeResult.ErrorKind = %s", res.ErrorKind)
	}
	if res.Reachable {
		t.Error("传输层失败时 Reachable 应为 false")
	}

	// 配置不合法 → config 分类，不发请求。
	if _, err := c.Probe(context.Background(), Channel{Kind: KindWebhook}); KindOf(err) != ErrorKindConfig {
		t.Errorf("缺配置应归为 config，实际 %s", KindOf(err))
	}

	// 超时。
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
	}))
	defer slow.Close()
	tc := NewClient(Options{Timeout: 30 * time.Millisecond, Retry: &RetryPolicy{}})
	if _, err := tc.Probe(context.Background(), webhookChannel(slow.URL)); KindOf(err) != ErrorKindTimeout {
		t.Errorf("超时应归为 timeout，实际 %s", KindOf(err))
	}
}

// Probe 不做重试。
func TestProbeDoesNotRetry(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewClient(Options{}) // 默认策略（Send 会重试）
	res, err := c.Probe(context.Background(), webhookChannel(srv.URL))
	if err != nil {
		t.Fatalf("Probe 不应因 5xx 失败: %v", err)
	}
	if !res.Reachable || res.StatusCode != 500 {
		t.Errorf("res = %+v", res)
	}
	if got := n.Load(); got != 1 {
		t.Errorf("Probe 请求次数 = %d, want 1", got)
	}
}

// Probe 的响应片段必须脱敏。
func TestProbeSnippetRedacted(t *testing.T) {
	const secret = "Pj8sEcretValueNeverLeak"
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusUnauthorized, "token "+secret+" rejected"))
	defer srv.Close()
	ch := Channel{Kind: KindWebhook, Config: map[string]any{
		"url":     srv.URL + "/hook?access_token=" + secret,
		"headers": map[string]any{"Authorization": "Bearer " + secret},
	}}
	res, err := NewClient(Options{Retry: &RetryPolicy{}}).Probe(context.Background(), ch)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if strings.Contains(res.ResponseSnippet, secret) {
		t.Errorf("Probe 片段泄漏密钥: %q", res.ResponseSnippet)
	}
	if !strings.Contains(res.ResponseSnippet, "Pj8s***") {
		t.Errorf("Probe 片段应保留脱敏前缀: %q", res.ResponseSnippet)
	}
}

// 邮件探测：服务商在 MAIL FROM 处明确拒绝（5xx）也算「协议层可达」。
func TestProbeEmailProviderReplyIsReachable(t *testing.T) {
	f := startFakeSMTPWith(t, fakeSMTPConfig{mailFromReply: "550 5.1.0 sender rejected"})
	ch := emailChannel(f, nil)

	res, err := NewClient(Options{Retry: &RetryPolicy{}}).Probe(context.Background(), ch)
	if err != nil {
		t.Fatalf("SMTP 服务端已应答，Probe 不应返回错误: %v", err)
	}
	if !res.Reachable {
		t.Fatal("Reachable 应为 true")
	}
	if res.ProviderCode != "SMTP 550" {
		t.Errorf("ProviderCode = %q, want SMTP 550", res.ProviderCode)
	}
	if !strings.Contains(res.ResponseSnippet, "sender rejected") {
		t.Errorf("ResponseSnippet = %q", res.ResponseSnippet)
	}
}

// 邮件探测：连不上仍是连接错误。
func TestProbeEmailTransportFailure(t *testing.T) {
	ch := Channel{Kind: KindEmail, Config: map[string]any{
		"smtp_host": "127.0.0.1", "smtp_port": 1, "from": "ops@example.com",
		"to": []any{"oncall@example.com"}, "tls": "none",
	}}
	_, err := NewClient(Options{Timeout: 300 * time.Millisecond, Retry: &RetryPolicy{}}).Probe(context.Background(), ch)
	if KindOf(err) != ErrorKindConnect {
		t.Errorf("Kind = %s, want connect", KindOf(err))
	}
}

// 邮件 SMTP 5xx 必须映射为 provider（不重试），4xx 映射为可重试的 server。
func TestEmailSMTPErrorClassification(t *testing.T) {
	cases := []struct {
		reply string
		kind  ErrorKind
		retry bool
	}{
		{"550 5.1.0 sender rejected", ErrorKindProvider, false},
		{"451 4.3.0 try again later", ErrorKindServer, true},
		{"535 5.7.8 authentication failed", ErrorKindAuth, false},
	}
	for _, tc := range cases {
		t.Run(tc.reply[:3], func(t *testing.T) {
			f := startFakeSMTPWith(t, fakeSMTPConfig{mailFromReply: tc.reply})
			ch := emailChannel(f, nil)
			err := NewClient(Options{Retry: &RetryPolicy{}}).Send(context.Background(), ch, testMessage())
			if err == nil {
				t.Fatal("应返回错误")
			}
			if KindOf(err) != tc.kind {
				t.Errorf("Kind = %s, want %s（%v）", KindOf(err), tc.kind, err)
			}
			if KindOf(err).Retryable() != tc.retry {
				t.Errorf("Retryable = %v, want %v", KindOf(err).Retryable(), tc.retry)
			}
		})
	}
}
