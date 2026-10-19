package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebhookJSONPost(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, `{"ok":true}`))
	defer srv.Close()

	c := NewClient(Options{})
	ch := Channel{
		ID: "ch1", Name: "钩子", Kind: KindWebhook, Enabled: true,
		Config: map[string]any{
			"url":     srv.URL + "/notify/hook",
			"headers": map[string]any{"X-Auth": "hdr-value"},
		},
	}
	if err := c.Send(context.Background(), ch, testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	req := rec.last(t)
	if req.method != http.MethodPost {
		t.Errorf("method = %s, want POST", req.method)
	}
	if req.url.Path != "/notify/hook" {
		t.Errorf("path = %s, want /notify/hook", req.url.Path)
	}
	if got := req.header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := req.header.Get("X-Auth"); got != "hdr-value" {
		t.Errorf("X-Auth = %q, want hdr-value", got)
	}
	if got := req.header.Get("User-Agent"); got != "LeadStack-Notify/1.0" {
		t.Errorf("User-Agent = %q", got)
	}

	var p webhookPayload
	if err := json.Unmarshal(req.body, &p); err != nil {
		t.Fatalf("body 不是合法 JSON: %v (%s)", err, req.body)
	}
	if p.Title != "CPU 使用率过高" || p.Body != "web-01 的 CPU 使用率持续高于 90%" {
		t.Errorf("title/body 不符: %+v", p)
	}
	if p.Severity != string(SeverityCritical) || p.Status != "firing" || p.Recovered {
		t.Errorf("severity/status/recovered 不符: %+v", p)
	}
	if p.ServerName != "web-01" || p.CheckName != "cpu-high" || p.Value != 95.5 {
		t.Errorf("server/check/value 不符: %+v", p)
	}
	if p.TS != 1700000000000 || p.AlertID != "alert-1" || p.ConsoleURL == "" {
		t.Errorf("ts/alert/console 不符: %+v", p)
	}
	if !strings.Contains(p.Text, "【触发】CPU 使用率过高") || !strings.Contains(p.Text, "严重") {
		t.Errorf("text 渲染不符: %q", p.Text)
	}
}

func TestWebhookTextTemplate(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, "ok"))
	defer srv.Close()

	c := NewClient(Options{})
	ch := Channel{Kind: KindWebhook, Config: map[string]any{
		"url": srv.URL + "/text", "template": "text",
	}}
	msg := testMessage()
	msg.Recovered = true
	if err := c.Send(context.Background(), ch, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	req := rec.last(t)
	if got := req.header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", got)
	}
	body := string(req.body)
	if strings.HasPrefix(strings.TrimSpace(body), "{") {
		t.Errorf("text 模板不应发送 JSON: %s", body)
	}
	if !strings.Contains(body, "【已恢复】CPU 使用率过高") {
		t.Errorf("text 模板内容不符: %s", body)
	}
	if !strings.Contains(body, "详情：http://console.example.com/alerts/alert-1") {
		t.Errorf("text 模板缺少详情链接: %s", body)
	}
}

func TestWebhookCustomMethodAndGETQuery(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, ""))
	defer srv.Close()

	c := NewClient(Options{})
	ch := Channel{Kind: KindWebhook, Config: map[string]any{
		"url": srv.URL + "/get", "method": "get",
	}}
	if err := c.Send(context.Background(), ch, testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	req := rec.last(t)
	if req.method != http.MethodGet {
		t.Errorf("method = %s, want GET", req.method)
	}
	q := req.url.Query()
	if q.Get("title") != "CPU 使用率过高" {
		t.Errorf("title 查询参数 = %q", q.Get("title"))
	}
	if q.Get("status") != "firing" || q.Get("severity") != "critical" {
		t.Errorf("status/severity 查询参数不符: %v", q)
	}
	if !strings.Contains(q.Get("text"), "严重") {
		t.Errorf("text 查询参数不符: %q", q.Get("text"))
	}
	if len(req.body) != 0 {
		t.Errorf("GET 不应带 body，实际 %q", req.body)
	}
}

func TestWebhookTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer slow.Close()

	c := NewClient(Options{Timeout: 120 * time.Millisecond})
	ch := Channel{Kind: KindWebhook, Config: map[string]any{"url": slow.URL}}

	start := time.Now()
	err := c.Send(context.Background(), ch, testMessage())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("超时应当返回错误")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("超时未生效，耗时 %v", elapsed)
	}
	if !strings.Contains(err.Error(), "请求失败") {
		t.Errorf("错误信息应说明请求失败: %v", err)
	}
}

func TestWebhookNon2xxBodyTruncatedTo512(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusInternalServerError, strings.Repeat("A", 1000)))
	defer srv.Close()

	c := NewClient(Options{})
	ch := Channel{Kind: KindWebhook, Config: map[string]any{"url": srv.URL}}
	err := c.Send(context.Background(), ch, testMessage())
	if err == nil {
		t.Fatal("非 2xx 应当返回错误")
	}
	msg := err.Error()
	if !strings.Contains(msg, "HTTP 500") {
		t.Errorf("错误信息应包含状态码: %v", err)
	}
	if !strings.Contains(msg, strings.Repeat("A", 512)) {
		t.Errorf("错误信息应保留前 512 字节响应体")
	}
	if strings.Contains(msg, strings.Repeat("A", 513)) {
		t.Errorf("响应体未被截断到 512 字节，错误信息长度 %d", len(msg))
	}
	if len(msg) > 512+256 {
		t.Errorf("错误信息过长（%d 字节），疑似未截断", len(msg))
	}
}

func TestWebhookConnectionFailureRedactsURLToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	target := srv.URL + "/hook?access_token=access-token-abcdef1234567890&sign=deadbeef"
	srv.Close() // 关掉服务端，制造连接失败

	const secret = "Pj8sEcretValueNeverLeak"
	c := NewClient(Options{})
	ch := Channel{Kind: KindWebhook, Config: map[string]any{"url": target, "headers": map[string]any{"X-Secret": secret}}}
	err := c.Send(context.Background(), ch, testMessage())
	if err == nil {
		t.Fatal("连接失败应当返回错误")
	}
	msg := err.Error()
	if strings.Contains(msg, "access-token-abcdef1234567890") {
		t.Errorf("错误信息泄漏了 webhook token: %s", msg)
	}
	if strings.Contains(msg, secret) {
		t.Errorf("错误信息泄漏了 header 中的 secret: %s", msg)
	}
	if !strings.Contains(msg, "请求失败") {
		t.Errorf("错误信息应说明请求失败: %v", err)
	}
}

func TestWebhookNon2xxBodyRedactsSecret(t *testing.T) {
	const secret = "Pj8sEcretValueNeverLeak"
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusUnauthorized, "invalid secret "+secret+" rejected"))
	defer srv.Close()

	c := NewClient(Options{})
	ch := Channel{Kind: KindWebhook, Config: map[string]any{"url": srv.URL, "headers": map[string]any{"Authorization": secret}}}
	err := c.Send(context.Background(), ch, testMessage())
	if err == nil {
		t.Fatal("非 2xx 应当返回错误")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("错误信息泄漏了 secret: %v", err)
	}
	if !strings.Contains(err.Error(), "Pj8s***") {
		t.Errorf("错误信息应包含脱敏后的 secret 前缀: %v", err)
	}
}

// TestWebhookDisabledChannelStillSends 记录设计决策：Enabled 由调用方判断，Send 不拦截。
func TestWebhookDisabledChannelStillSends(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, ""))
	defer srv.Close()

	c := NewClient(Options{})
	ch := Channel{Kind: KindWebhook, Enabled: false, Config: map[string]any{"url": srv.URL}}
	if err := c.Send(context.Background(), ch, testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if rec.count() != 1 {
		t.Errorf("请求数 = %d, want 1", rec.count())
	}
}
