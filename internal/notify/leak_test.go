package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSendFailureNeverLeaksSecrets 是对安全红线的系统性覆盖：
// 每个渠道都构造一个「含密钥 + 必然失败」的请求，断言 error 里既没有密钥明文，
// 也没有 URL 中的 token，同时保留了可读的失败原因。
func TestSendFailureNeverLeaksSecrets(t *testing.T) {
	// 起一个服务端再关掉，得到「一定连不上」的地址，且能带上真实路径与查询串。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead := srv.URL
	srv.Close()

	cases := []struct {
		name    string
		ch      Channel
		secrets []string
	}{
		{
			name: "webhook",
			ch: Channel{Kind: KindWebhook, Config: map[string]any{
				"url":     dead + "/hook?access_token=hooktoken1234567890",
				"headers": map[string]any{"Authorization": "Bearer webhookbearersecret42"},
			}},
			secrets: []string{"hooktoken1234567890", "webhookbearersecret42"},
		},
		{
			name: "dingtalk",
			ch: Channel{
				Kind: KindDingTalk,
				Config: map[string]any{
					"webhook": dead + "/robot/send?access_token=dingtoken1234567890",
					"secret":  "SECtestsecret0123456789abcdef",
				},
			},
			secrets: []string{"dingtoken1234567890", "SECtestsecret0123456789abcdef"},
		},
		{
			name: "wecom",
			ch: Channel{Kind: KindWeCom, Config: map[string]any{
				"webhook": dead + "/cgi-bin/webhook/send?key=wecomkey1234567890",
			}},
			secrets: []string{"wecomkey1234567890"},
		},
		{
			name: "feishu",
			ch: Channel{Kind: KindFeishu, Config: map[string]any{
				"webhook": dead + "/open-apis/bot/v2/hook/feishuhookid1234567890",
				"secret":  "FeishuSecretTest0123456789abcd",
			}},
			secrets: []string{"feishuhookid1234567890", "FeishuSecretTest0123456789abcd"},
		},
		{
			name: "slack",
			ch: Channel{Kind: KindSlack, Config: map[string]any{
				"webhook": dead + "/services/T000/B000/slackhooksecret123456",
			}},
			secrets: []string{"slackhooksecret123456"},
		},
		{
			name: "telegram",
			ch: Channel{Kind: KindTelegram, Config: map[string]any{
				"bot_token": "123456789:AAEtestbotTokenValue_xyZ", "chat_id": "1",
			}},
			secrets: []string{"123456789:AAEtestbotTokenValue_xyZ"},
		},
		{
			name: "email",
			ch: Channel{Kind: KindEmail, Config: map[string]any{
				"smtp_host": "127.0.0.1", "smtp_port": 1, "from": "ops@example.com",
				"to": []any{"oncall@example.com"}, "username": "u", "password": "Sup3rSecretPassword",
			}},
			secrets: []string{"Sup3rSecretPassword"},
		},
	}

	c := NewClient(Options{})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.Send(context.Background(), tc.ch, testMessage())
			if err == nil {
				t.Fatalf("失败场景未返回错误")
			}
			msg := err.Error()
			for _, s := range tc.secrets {
				if strings.Contains(msg, s) {
					t.Errorf("错误信息泄漏了密钥 %q: %s", Redact(s), msg)
				}
			}
			// 错误必须是可读的中文诊断，而不是空壳。
			if !strings.Contains(msg, "notify:") {
				t.Errorf("错误信息缺少前缀: %s", msg)
			}
			if len(msg) > 4096 {
				t.Errorf("错误信息过长（%d 字节），疑似把响应体/配置整体打进错误", len(msg))
			}
		})
	}
}

// MaskConfig 的输出可安全用于日志：逐渠道确认脱敏后再查一遍。
func TestMaskConfigIsSafeForLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	channels := []Channel{
		{Kind: KindWebhook, Config: map[string]any{"url": srv.URL + "/h?token=webhooktoken1234567890", "headers": map[string]any{"Authorization": "Bearer abcdefghijklmnop"}}},
		{Kind: KindDingTalk, Config: map[string]any{"webhook": srv.URL + "/r?access_token=dingtoken1234567890", "secret": "SECtestsecret0123456789abcdef"}},
		{Kind: KindWeCom, Config: map[string]any{"webhook": srv.URL + "/w?key=wecomkey1234567890"}},
		{Kind: KindFeishu, Config: map[string]any{"webhook": srv.URL + "/f", "secret": "FeishuSecretTest0123456789abcd"}},
		{Kind: KindSlack, Config: map[string]any{"webhook": srv.URL + "/s"}},
		{Kind: KindTelegram, Config: map[string]any{"bot_token": "123456789:AAEtestbotTokenValue_xyZ", "chat_id": "1"}},
		{Kind: KindEmail, Config: map[string]any{"smtp_host": "smtp.example.com", "from": "a@b.c", "to": []any{"x@y.z"}, "password": "Sup3rSecretPassword"}},
	}
	for _, ch := range channels {
		masked := MaskConfig(ch)
		blob := ""
		for k, v := range masked {
			blob += k + "=" + toString(v) + ";"
		}
		for k, v := range ch.Config {
			s, ok := v.(string)
			if !ok || len(s) < 8 {
				continue
			}
			if !isSensitiveKey(k) && !isSensitiveHeader(k) {
				continue
			}
			_ = masked
			if strings.Contains(blob, s) {
				t.Errorf("%s 的 %s 未脱敏: %s", ch.Kind, k, blob)
			}
		}
	}
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return ""
	}
}
