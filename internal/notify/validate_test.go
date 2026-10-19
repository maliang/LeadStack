package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func validChannel(kind Kind) Channel {
	switch kind {
	case KindWebhook:
		return Channel{Kind: kind, Config: map[string]any{"url": "https://example.com/hook"}}
	case KindDingTalk:
		return Channel{Kind: kind, Config: map[string]any{"webhook": "https://oapi.dingtalk.com/robot/send?access_token=abc", "secret": "SEC1234567890abcdef"}}
	case KindWeCom:
		return Channel{Kind: kind, Config: map[string]any{"webhook": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc"}}
	case KindFeishu:
		return Channel{Kind: kind, Config: map[string]any{"webhook": "https://open.feishu.cn/open-apis/bot/v2/hook/abc", "secret": "FS1234567890abcdef"}}
	case KindSlack:
		return Channel{Kind: kind, Config: map[string]any{"webhook": "https://hooks.slack.com/services/T/B/x", "channel": "#ops"}}
	case KindTelegram:
		return Channel{Kind: kind, Config: map[string]any{"bot_token": "123456:abcdefghijklmnop", "chat_id": "-1001234567890"}}
	case KindEmail:
		return Channel{Kind: kind, Config: map[string]any{
			"smtp_host": "smtp.example.com", "from": "ops@example.com", "to": []any{"oncall@example.com"},
		}}
	}
	return Channel{Kind: kind}
}

func TestKindsAndSupportedKind(t *testing.T) {
	kinds := Kinds()
	if len(kinds) != 7 {
		t.Fatalf("Kinds() 长度 = %d, want 7", len(kinds))
	}
	for _, k := range kinds {
		if !SupportedKind(k) {
			t.Errorf("%s 应被支持", k)
		}
		if err := NewClient(Options{}).Validate(validChannel(k)); err != nil {
			t.Errorf("%s 的合法配置被拒: %v", k, err)
		}
	}
	if SupportedKind("sms") {
		t.Error("sms 不应被支持")
	}
}

func TestValidateRejectsBadConfigs(t *testing.T) {
	cases := []struct {
		name    string
		ch      Channel
		wantSub string
	}{
		{"未知类型", Channel{Kind: "sms"}, "不支持的渠道类型"},
		{"webhook 缺 url", Channel{Kind: KindWebhook}, "缺少必填配置 url"},
		{"webhook url 非字符串", Channel{Kind: KindWebhook, Config: map[string]any{"url": 123}}, "url 必须是字符串"},
		{"webhook url 非法", Channel{Kind: KindWebhook, Config: map[string]any{"url": "http://"}}, "合法的 http/https 地址"},
		{"webhook 协议不支持", Channel{Kind: KindWebhook, Config: map[string]any{"url": "ftp://example.com/hook?access_token=leaktoken12345"}}, "只支持 http/https"},
		{"webhook method 不支持", Channel{Kind: KindWebhook, Config: map[string]any{"url": "https://e.com/h", "method": "DELETE"}}, "不支持 method"},
		{"webhook template 非法", Channel{Kind: KindWebhook, Config: map[string]any{"url": "https://e.com/h", "template": "yaml"}}, "template 只支持 json 或 text"},
		{"webhook headers 类型错", Channel{Kind: KindWebhook, Config: map[string]any{"url": "https://e.com/h", "headers": "nope"}}, "必须是键值对"},
		{"webhook timeout_s 非整数", Channel{Kind: KindWebhook, Config: map[string]any{"url": "https://e.com/h", "timeout_s": "abc"}}, "timeout_s 必须是整数"},
		{"钉钉缺 webhook", Channel{Kind: KindDingTalk, Config: map[string]any{"secret": "SEC1234567890"}}, "钉钉渠道缺少必填配置 webhook"},
		{"企微缺 webhook", Channel{Kind: KindWeCom}, "企业微信渠道缺少必填配置 webhook"},
		{"飞书缺 webhook", Channel{Kind: KindFeishu, Config: map[string]any{"secret": "FS1234567890"}}, "飞书渠道缺少必填配置 webhook"},
		{"slack 缺 webhook", Channel{Kind: KindSlack, Config: map[string]any{"channel": "#ops"}}, "Slack 渠道缺少必填配置 webhook"},
		{"telegram 缺 bot_token", Channel{Kind: KindTelegram, Config: map[string]any{"chat_id": "1"}}, "缺少必填配置 bot_token"},
		{"telegram 缺 chat_id", Channel{Kind: KindTelegram, Config: map[string]any{"bot_token": "123:abc"}}, "缺少必填配置 chat_id"},
		{"telegram api_base 非法", Channel{Kind: KindTelegram, Config: map[string]any{"bot_token": "123:abc", "chat_id": "1", "api_base": "not a url"}}, "api_base 必须是合法的 http/https 地址"},
		{"邮件缺 smtp_host", Channel{Kind: KindEmail, Config: map[string]any{"from": "a@b.c", "to": []any{"x@y.z"}}}, "缺少必填配置 smtp_host"},
		{"邮件缺 from", Channel{Kind: KindEmail, Config: map[string]any{"smtp_host": "smtp.e.com", "to": []any{"x@y.z"}}}, "缺少必填配置 from"},
		{"邮件缺 to", Channel{Kind: KindEmail, Config: map[string]any{"smtp_host": "smtp.e.com", "from": "a@b.c"}}, "缺少必填配置 to"},
		{"邮件 to 元素非字符串", Channel{Kind: KindEmail, Config: map[string]any{"smtp_host": "smtp.e.com", "from": "a@b.c", "to": []any{1}}}, "元素必须是字符串"},
		{"邮件端口非法", Channel{Kind: KindEmail, Config: map[string]any{"smtp_host": "smtp.e.com", "from": "a@b.c", "to": []any{"x@y.z"}, "smtp_port": 70000}}, "smtp_port 非法"},
		{"邮件 tls 非法", Channel{Kind: KindEmail, Config: map[string]any{"smtp_host": "smtp.e.com", "from": "a@b.c", "to": []any{"x@y.z"}, "tls": "tls1"}}, "tls 只支持 starttls/ssl/none"},
		{"邮件有密码无用户名", Channel{Kind: KindEmail, Config: map[string]any{"smtp_host": "smtp.e.com", "from": "a@b.c", "to": []any{"x@y.z"}, "password": "Sup3rSecretPassword"}}, "缺少 username"},
	}
	c := NewClient(Options{})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.Validate(tc.ch)
			if err == nil {
				t.Fatalf("应当返回错误")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("错误信息 %q 不包含 %q", err.Error(), tc.wantSub)
			}
			// 错误信息绝不包含配置里的密钥明文。
			for _, v := range tc.ch.Config {
				s, ok := v.(string)
				if ok && len(s) >= 10 && isSensitiveKey(keyOf(tc.ch.Config, s)) {
					if strings.Contains(err.Error(), s) {
						t.Errorf("校验错误泄漏了密钥: %v", err)
					}
				}
			}
		})
	}
}

// keyOf 找出某个取值对应的配置键名（测试辅助）。
func keyOf(cfg map[string]any, value string) string {
	for k, v := range cfg {
		if s, ok := v.(string); ok && s == value {
			return k
		}
	}
	return ""
}

func TestSendRejectsInvalidConfigBeforeSending(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, ""))
	defer srv.Close()

	c := NewClient(Options{})
	err := c.Send(context.Background(), Channel{Kind: KindWebhook}, testMessage())
	if err == nil {
		t.Fatal("缺 url 应当返回错误")
	}
	if rec.count() != 0 {
		t.Errorf("校验失败时不应发出请求，实际 %d 次", rec.count())
	}

	err = c.Send(context.Background(), Channel{Kind: "sms", Config: map[string]any{"url": srv.URL}}, testMessage())
	if err == nil || !strings.Contains(err.Error(), "不支持的渠道类型") {
		t.Errorf("未知类型应被拒绝: %v", err)
	}
}

func TestClientDefaults(t *testing.T) {
	if DefaultTimeout != 10*time.Second {
		t.Fatalf("DefaultTimeout = %v, want 10s", DefaultTimeout)
	}
	if got := NewClient(Options{}).Timeout(); got != DefaultTimeout {
		t.Errorf("默认超时 = %v, want %v", got, DefaultTimeout)
	}
	if got := NewClient(Options{Timeout: 3 * time.Second}).Timeout(); got != 3*time.Second {
		t.Errorf("自定义超时 = %v", got)
	}
	// 零值客户端也必须可用（内部自建 http.Client / time.Now）。
	var nilClient *Client
	if err := nilClient.Send(context.Background(), validChannel(KindWebhook), testMessage()); err == nil {
		t.Error("nil client 应返回错误而不是 panic")
	}
}

// 并发发送必须各自独立、无数据竞争（配合 -race 运行）。
func TestConcurrentSend(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.serve(http.StatusOK, `{"ok":true}`))
	defer srv.Close()

	c := NewClient(Options{})
	const n = 24
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ch := Channel{Kind: KindWebhook, Config: map[string]any{"url": srv.URL + "/hook"}}
			msg := testMessage()
			msg.Title = "并发-" + string(rune('A'+i%26))
			errs[i] = c.Send(context.Background(), ch, msg)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 次发送失败: %v", i, err)
		}
	}
	if rec.count() != n {
		t.Errorf("请求数 = %d, want %d", rec.count(), n)
	}
}

// 手动取消 context 必须真正中断发送。
func TestSendHonorsContextCancel(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	c := NewClient(Options{Timeout: 5 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := c.Send(ctx, Channel{Kind: KindWebhook, Config: map[string]any{"url": srv.URL}}, testMessage())
	if err == nil {
		t.Fatal("context 取消应当返回错误")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("context 取消未生效，耗时 %v", elapsed)
	}
}
