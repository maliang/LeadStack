package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// 钉钉：固定时钟 + 固定 secret，断言 URL 里的 timestamp/sign 与独立算出的已知答案一致。
func TestDingTalkSignedRequestKnownAnswer(t *testing.T) {
	const (
		secret      = "SECtestsecret0123456789abcdef"
		wantB64     = "oWnZboeOYjlS1NUgJCZh8t1VJ8C+IwsRPuEItFP4A0s="
		wantEncoded = "oWnZboeOYjlS1NUgJCZh8t1VJ8C%2BIwsRPuEItFP4A0s%3D"
	)
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, `{"errcode":0,"errmsg":"ok"}`))
	defer srv.Close()

	c := NewClient(Options{Now: fixedClock(1700000000000)})
	ch := Channel{Kind: KindDingTalk, Config: map[string]any{
		"webhook": srv.URL + "/robot/send?access_token=ding-access-token-0123456789",
		"secret":  secret,
	}}
	if err := c.Send(context.Background(), ch, testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	req := rec.last(t)
	if req.method != http.MethodPost {
		t.Errorf("method = %s, want POST", req.method)
	}
	if req.url.Path != "/robot/send" {
		t.Errorf("path = %s, want /robot/send", req.url.Path)
	}
	// access_token 必须原样保留。
	if got := req.url.Query().Get("access_token"); got != "ding-access-token-0123456789" {
		t.Errorf("access_token = %q", got)
	}
	if got := req.url.Query().Get("timestamp"); got != "1700000000000" {
		t.Errorf("timestamp = %q, want 1700000000000", got)
	}
	// 关键：URL 里必须是「一次 urlencode」的结果，不能出现二次编码的 %25。
	if strings.Contains(req.url.RawQuery, "%25") {
		t.Fatalf("sign 被二次编码（出现 %%25）：%s", req.url.RawQuery)
	}
	if !strings.Contains(req.url.RawQuery, "sign="+wantEncoded) {
		t.Fatalf("RawQuery 中的 sign 不是已知答案\n got: %s\nwant: sign=%s", req.url.RawQuery, wantEncoded)
	}
	// 服务端解码一次后必须等于独立算出的 base64。
	if got := req.url.Query().Get("sign"); got != wantB64 {
		t.Fatalf("解码后的 sign = %q, want %q", got, wantB64)
	}

	var payload struct {
		MsgType  string `json:"msgtype"`
		Markdown struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"markdown"`
	}
	if err := json.Unmarshal(req.body, &payload); err != nil {
		t.Fatalf("body 不是合法 JSON: %v (%s)", err, req.body)
	}
	if payload.MsgType != "markdown" {
		t.Errorf("msgtype = %q, want markdown", payload.MsgType)
	}
	if payload.Markdown.Title != "CPU 使用率过高" {
		t.Errorf("markdown.title = %q", payload.Markdown.Title)
	}
	if !strings.Contains(payload.Markdown.Text, "【触发】CPU 使用率过高") || !strings.Contains(payload.Markdown.Text, "严重") {
		t.Errorf("markdown.text 渲染不符: %q", payload.Markdown.Text)
	}
	if got := req.header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
}

// 未配置 secret 时不得加签。
func TestDingTalkUnsigned(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, `{"errcode":0,"errmsg":"ok"}`))
	defer srv.Close()

	c := NewClient(Options{Now: fixedClock(1700000000000)})
	ch := Channel{Kind: KindDingTalk, Config: map[string]any{"webhook": srv.URL + "/robot/send?access_token=t"}}
	if err := c.Send(context.Background(), ch, testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	q := rec.last(t).url.Query()
	if q.Has("sign") || q.Has("timestamp") {
		t.Errorf("未配置 secret 时不应加签: %v", q)
	}
}

// 钉钉「HTTP 200 + errcode != 0」必须视为失败（这正是签名错误时的真实表现）。
func TestDingTalkBusinessErrorSurfaces(t *testing.T) {
	const secret = "SECtestsecret0123456789abcdef"
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, `{"errcode":310000,"errmsg":"sign not match"}`))
	defer srv.Close()

	c := NewClient(Options{Now: fixedClock(1700000000000)})
	ch := Channel{Kind: KindDingTalk, Config: map[string]any{
		"webhook": srv.URL + "/robot/send?access_token=abcdefghijklmnop", "secret": secret,
	}}
	err := c.Send(context.Background(), ch, testMessage())
	if err == nil {
		t.Fatal("errcode != 0 应当返回错误")
	}
	if !strings.Contains(err.Error(), "310000") || !strings.Contains(err.Error(), "sign not match") {
		t.Errorf("错误信息应包含 errcode/errmsg: %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("错误信息泄漏了 secret: %v", err)
	}
}

// 钉钉连接失败时，错误信息不得泄漏 secret 与 URL 中的 access_token。
func TestDingTalkErrorRedactsSecrets(t *testing.T) {
	const (
		secret = "SECtestsecret0123456789abcdef"
		token  = "ding-access-token-0123456789"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	target := srv.URL + "/robot/send?access_token=" + token + "&monkey=1"
	srv.Close()

	c := NewClient(Options{Now: fixedClock(1700000000000)})
	ch := Channel{Kind: KindDingTalk, Config: map[string]any{"webhook": target, "secret": secret}}
	err := c.Send(context.Background(), ch, testMessage())
	if err == nil {
		t.Fatal("连接失败应当返回错误")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) || strings.Contains(msg, token) {
		t.Errorf("错误信息泄漏了密钥: %s", msg)
	}
	if !strings.Contains(msg, "请求失败") {
		t.Errorf("错误信息缺少失败原因: %s", msg)
	}
	// 脱敏不能把诊断信息一起吃掉：主机与端口必须仍然可读。
	if !strings.Contains(msg, "127.0.0.1") {
		t.Errorf("脱敏过度，破坏了可读性: %s", msg)
	}
	if strings.Contains(msg, "***27.0.0.***") {
		t.Errorf("脱敏误伤 IP: %s", msg)
	}
}

func TestWeComRequest(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, `{"errcode":0,"errmsg":"ok"}`))
	defer srv.Close()

	c := NewClient(Options{})
	ch := Channel{Kind: KindWeCom, Config: map[string]any{
		"webhook": srv.URL + "/cgi-bin/webhook/send?key=wecom-key-0123456789",
	}}
	if err := c.Send(context.Background(), ch, testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	req := rec.last(t)
	if req.method != http.MethodPost || req.url.Path != "/cgi-bin/webhook/send" {
		t.Errorf("method/path = %s %s", req.method, req.url.Path)
	}
	if got := req.url.Query().Get("key"); got != "wecom-key-0123456789" {
		t.Errorf("key = %q", got)
	}
	var payload struct {
		MsgType  string `json:"msgtype"`
		Markdown struct {
			Content string `json:"content"`
		} `json:"markdown"`
	}
	if err := json.Unmarshal(req.body, &payload); err != nil {
		t.Fatalf("body: %v (%s)", err, req.body)
	}
	if payload.MsgType != "markdown" || !strings.Contains(payload.Markdown.Content, "CPU 使用率过高") {
		t.Errorf("body 不符: %+v", payload)
	}
	// 企业微信也要识别业务错误。
	srv.Close()
}

func TestWeComBusinessErrorSurfaces(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, `{"errcode":93000,"errmsg":"invalid webhook url"}`))
	defer srv.Close()
	c := NewClient(Options{})
	ch := Channel{Kind: KindWeCom, Config: map[string]any{"webhook": srv.URL + "/cgi-bin/webhook/send?key=abc"}}
	err := c.Send(context.Background(), ch, testMessage())
	if err == nil || !strings.Contains(err.Error(), "93000") {
		t.Fatalf("应返回业务错误: %v", err)
	}
}

// 飞书：sign/timestamp 在 body 顶层，timestamp 为秒级字符串，sign 为独立算出的已知答案。
func TestFeishuSignedRequestKnownAnswer(t *testing.T) {
	const (
		secret = "FeishuSecretTest0123456789abcd"
		want   = "uXURCgzFKclAtkaTVdpqFT3aFyTx/H5cko44KZPFLew="
	)
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, `{"code":0,"msg":"success"}`))
	defer srv.Close()

	c := NewClient(Options{Now: fixedClock(1700000000000)})
	ch := Channel{Kind: KindFeishu, Config: map[string]any{
		"webhook": srv.URL + "/open-apis/bot/v2/hook/feishu-hook-id",
		"secret":  secret,
	}}
	if err := c.Send(context.Background(), ch, testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	req := rec.last(t)
	if req.method != http.MethodPost || req.url.Path != "/open-apis/bot/v2/hook/feishu-hook-id" {
		t.Errorf("method/path = %s %s", req.method, req.url.Path)
	}
	var payload map[string]any
	if err := json.Unmarshal(req.body, &payload); err != nil {
		t.Fatalf("body: %v (%s)", err, req.body)
	}
	if payload["msg_type"] != "text" {
		t.Errorf("msg_type = %v", payload["msg_type"])
	}
	content, _ := payload["content"].(map[string]any)
	if content == nil || !strings.Contains(content["text"].(string), "CPU 使用率过高") {
		t.Errorf("content.text 不符: %v", payload["content"])
	}
	if got := payload["timestamp"]; got != "1700000000" {
		t.Errorf("timestamp = %v, want \"1700000000\"（秒级字符串）", got)
	}
	if got := payload["sign"]; got != want {
		t.Errorf("sign = %v, want %s", got, want)
	}
	// 加签后的请求体不得把 secret 也带出去。
	if strings.Contains(string(req.body), secret) {
		t.Errorf("请求体泄漏了 secret: %s", req.body)
	}
}

func TestFeishuUnsignedHasNoSignFields(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, `{"code":0}`))
	defer srv.Close()

	c := NewClient(Options{Now: fixedClock(1700000000000)})
	ch := Channel{Kind: KindFeishu, Config: map[string]any{"webhook": srv.URL + "/hook"}}
	if err := c.Send(context.Background(), ch, testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.last(t).body, &payload); err != nil {
		t.Fatalf("body: %v", err)
	}
	if _, ok := payload["sign"]; ok {
		t.Error("未配置 secret 时不应有 sign 字段")
	}
	if _, ok := payload["timestamp"]; ok {
		t.Error("未配置 secret 时不应有 timestamp 字段")
	}
}

func TestFeishuBusinessErrorSurfaces(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, `{"code":19021,"msg":"sign match fail"}`))
	defer srv.Close()
	c := NewClient(Options{Now: fixedClock(1700000000000)})
	ch := Channel{Kind: KindFeishu, Config: map[string]any{"webhook": srv.URL + "/hook", "secret": "s3cr3t-value-xyz"}}
	err := c.Send(context.Background(), ch, testMessage())
	if err == nil {
		t.Fatal("code != 0 应当返回错误")
	}
	if !strings.Contains(err.Error(), "19021") || !strings.Contains(err.Error(), "sign match fail") {
		t.Errorf("错误信息应包含 code/msg: %v", err)
	}
	if strings.Contains(err.Error(), "s3cr3t-value-xyz") {
		t.Errorf("错误信息泄漏了 secret: %v", err)
	}
}

func TestSlackRequest(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, "ok"))
	defer srv.Close()

	c := NewClient(Options{})
	msg := testMessage()
	msg.Severity = SeverityWarning

	withChannel := Channel{Kind: KindSlack, Config: map[string]any{
		"webhook": srv.URL + "/services/T00/B00/xxxx", "channel": "#ops-alerts",
	}}
	if err := c.Send(context.Background(), withChannel, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	req := rec.last(t)
	if req.method != http.MethodPost || req.url.Path != "/services/T00/B00/xxxx" {
		t.Errorf("method/path = %s %s", req.method, req.url.Path)
	}
	var payload map[string]any
	if err := json.Unmarshal(req.body, &payload); err != nil {
		t.Fatalf("body: %v (%s)", err, req.body)
	}
	if payload["channel"] != "#ops-alerts" {
		t.Errorf("channel = %v", payload["channel"])
	}
	text, _ := payload["text"].(string)
	if !strings.Contains(text, "【触发】CPU 使用率过高") || !strings.Contains(text, "警告") {
		t.Errorf("text 不符: %q", text)
	}

	// 未配置 channel 时不应带该字段。
	noChannel := Channel{Kind: KindSlack, Config: map[string]any{"webhook": srv.URL + "/services/x"}}
	if err := c.Send(context.Background(), noChannel, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	payload = nil
	if err := json.Unmarshal(rec.last(t).body, &payload); err != nil {
		t.Fatalf("body: %v", err)
	}
	if _, ok := payload["channel"]; ok {
		t.Error("未配置 channel 时不应出现 channel 字段")
	}
}

func TestTelegramRequest(t *testing.T) {
	const token = "123456789:AAEtestbotTokenValue_xyZ"
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, `{"ok":true,"result":{"message_id":1}}`))
	defer srv.Close()

	c := NewClient(Options{})
	ch := Channel{Kind: KindTelegram, Config: map[string]any{
		"bot_token": token, "chat_id": "-1001234567890", "api_base": srv.URL, "parse_mode": "Markdown",
	}}
	if err := c.Send(context.Background(), ch, testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	req := rec.last(t)
	if req.method != http.MethodPost {
		t.Errorf("method = %s, want POST", req.method)
	}
	if want := "/bot" + token + "/sendMessage"; req.url.Path != want {
		t.Errorf("path = %s, want %s", req.url.Path, want)
	}
	var payload map[string]any
	if err := json.Unmarshal(req.body, &payload); err != nil {
		t.Fatalf("body: %v (%s)", err, req.body)
	}
	if payload["chat_id"] != "-1001234567890" {
		t.Errorf("chat_id = %v", payload["chat_id"])
	}
	if payload["parse_mode"] != "Markdown" {
		t.Errorf("parse_mode = %v", payload["parse_mode"])
	}
	if text, _ := payload["text"].(string); !strings.Contains(text, "CPU 使用率过高") {
		t.Errorf("text 不符: %v", payload["text"])
	}
}

// api_base 可覆盖是给测试与代理用的正式设计；默认值必须是官网地址。
func TestTelegramAPIBaseDefaultAndOverride(t *testing.T) {
	const token = "123:abc"
	if got, want := telegramEndpoint("", token), "https://api.telegram.org/bot123:abc/sendMessage"; got != want {
		t.Errorf("默认 api_base 组装 = %q, want %q", got, want)
	}
	if got, want := telegramEndpoint("http://127.0.0.1:8081/", token), "http://127.0.0.1:8081/bot123:abc/sendMessage"; got != want {
		t.Errorf("覆盖 api_base 组装 = %q, want %q", got, want)
	}
}

// telegram 的 token 在 URL 路径里：失败时错误信息必须把它脱敏掉。
func TestTelegramErrorRedactsBotToken(t *testing.T) {
	const token = "123456789:AAEtestbotTokenValue_xyZ"
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
	defer srv.Close()

	c := NewClient(Options{})
	ch := Channel{Kind: KindTelegram, Config: map[string]any{
		"bot_token": token, "chat_id": "1", "api_base": srv.URL,
	}}
	err := c.Send(context.Background(), ch, testMessage())
	if err == nil {
		t.Fatal("HTTP 400 应当返回错误")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("错误信息泄漏了 bot_token: %v", err)
	}

	// 连接失败路径（URL 也会出现在 net/http 的错误里）。
	dead := Channel{Kind: KindTelegram, Config: map[string]any{
		"bot_token": token, "chat_id": "1", "api_base": srv.URL,
	}}
	srv.Close()
	err = c.Send(context.Background(), dead, testMessage())
	if err == nil {
		t.Fatal("连接失败应当返回错误")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("连接失败错误泄漏了 bot_token: %v", err)
	}
}

// telegram 返回 HTTP 200 + ok=false 也要视为失败。
func TestTelegramOKFalseSurfaces(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, `{"ok":false,"error_code":403,"description":"bot was blocked by the user"}`))
	defer srv.Close()
	c := NewClient(Options{})
	ch := Channel{Kind: KindTelegram, Config: map[string]any{
		"bot_token": "123:abc", "chat_id": "1", "api_base": srv.URL,
	}}
	err := c.Send(context.Background(), ch, testMessage())
	if err == nil {
		t.Fatal("ok=false 应当返回错误")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("错误信息应包含 error_code/description: %v", err)
	}
}

// 用 URL 解析器确认钉钉的查询串只编码一次（服务端视角）。
func TestDingTalkRawQuerySingleEncoding(t *testing.T) {
	const secret = "SECtestsecret0123456789abcdef"
	got := dingTalkSign(secret, 1700000000000)
	if strings.Contains(got, "%25") {
		t.Fatalf("sign 自身已包含二次编码: %q", got)
	}
	u, err := url.Parse("https://oapi.dingtalk.com/robot/send?access_token=t&timestamp=1700000000000&sign=" + got)
	if err != nil {
		t.Fatalf("URL 解析失败: %v", err)
	}
	if v := u.Query().Get("sign"); v != "oWnZboeOYjlS1NUgJCZh8t1VJ8C+IwsRPuEItFP4A0s=" {
		t.Fatalf("服务端解码后的 sign = %q", v)
	}
}
