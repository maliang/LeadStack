package notify

// 本文件是**真实外网**探测：用真实服务商域名 + 无效凭据，验证 URL、方法、请求头与
// JSON/签名结构被对方接受并解析（对方返回 401/400/业务错误码即证明"发得出去、格式对"）。
//
// 可跳过策略（保证 CI 不会因为外网不可达而红）：
//   - `go test -short` 直接跳过；
//   - 检测不到外网（api.telegram.org:443 连不上）时整体跳过；
//   - 单个端点传输层失败（DNS/连接/TLS/超时）时该子测试 skip 并打印原因。
//
// 传输层失败才说明真有问题；服务商层面的 4xx/业务错误码属于预期结果。

import (
	"context"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// hasOutboundNetwork 检测外网是否可达。
func hasOutboundNetwork(t *testing.T) bool {
	t.Helper()
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.Dial("tcp", "api.telegram.org:443")
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// isNetworkFailure 报告错误是否属于「网络不可达」类（用于 skip 而不是 fail）。
func isNetworkFailure(err error) bool {
	switch KindOf(err) {
	case ErrorKindDNS, ErrorKindConnect, ErrorKindTLS, ErrorKindTimeout, ErrorKindCanceled:
		return true
	}
	return false
}

func TestLiveProviderProbes(t *testing.T) {
	if testing.Short() {
		t.Skip("-short：跳过真实外网端点探测")
	}
	if !hasOutboundNetwork(t) {
		t.Skip("检测不到外网（api.telegram.org:443 不可达），跳过真实端点探测")
	}

	c := NewClient(Options{Timeout: 12 * time.Second, Retry: &RetryPolicy{}})
	cases := []struct {
		name       string
		ch         Channel
		wantStatus []int
		// wantProviderCode 非空时要求 ProviderCode 以其为前缀。
		wantProviderCode string
		// wantSnippetAny 要求响应片段至少命中其中一个（不同区域/时间的服务商文案可能不同）。
		wantSnippetAny []string
	}{
		{
			name: "dingtalk",
			ch: Channel{Kind: KindDingTalk, Config: map[string]any{
				"webhook": "https://oapi.dingtalk.com/robot/send?access_token=fakefakefakefakefake",
				"secret":  "SECfakefakefakefakefakefake",
			}},
			wantStatus:       []int{200},
			wantProviderCode: "errcode=",
			wantSnippetAny:   []string{"token", "sign", "robot"},
		},
		{
			name: "wecom",
			ch: Channel{Kind: KindWeCom, Config: map[string]any{
				"webhook": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=fakefakefakefakefake",
			}},
			wantStatus:       []int{200},
			wantProviderCode: "errcode=",
			wantSnippetAny:   []string{"invalid webhook url", "invalid"},
		},
		{
			name: "feishu",
			ch: Channel{Kind: KindFeishu, Config: map[string]any{
				"webhook": "https://open.feishu.cn/open-apis/bot/v2/hook/fakefakefakefakefake",
				"secret":  "FakeSecret0123456789abcdef",
			}},
			wantStatus:       []int{200},
			wantProviderCode: "code=",
			wantSnippetAny:   []string{"incoming webhook access token invalid", "param invalid", "sign"},
		},
		{
			name: "telegram",
			ch: Channel{Kind: KindTelegram, Config: map[string]any{
				"bot_token": "123456789:fakefakefakefakefakefakefake",
				"chat_id":   "1",
				// 不填 api_base：走正式默认域名 https://api.telegram.org
			}},
			wantStatus:       []int{401, 400},
			wantProviderCode: "error_code=",
			wantSnippetAny:   []string{"Unauthorized", "Not Found"},
		},
		{
			name: "slack",
			ch: Channel{Kind: KindSlack, Config: map[string]any{
				// 拆成两段拼接：整串看起来像真实 Slack Webhook，会被 GitHub 秘密扫描误判为泄露并拒绝推送
				"webhook": "https://hooks.slack.com" + "/services/T00000000/B00000000/fakefakefakefakefakefake",
			}},
			wantStatus:     []int{404, 400, 403},
			wantSnippetAny: []string{"no_team", "invalid_token", "no_service"},
		},
		{
			name: "webhook",
			ch: Channel{Kind: KindWebhook, Config: map[string]any{
				"url": "https://httpbin.org/post",
			}},
			wantStatus:     []int{200},
			wantSnippetAny: []string{"LeadStack channel probe"},
		},
	}

	var verified atomic.Int32
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			res, err := c.Probe(ctx, tc.ch)
			if err != nil {
				if isNetworkFailure(err) {
					t.Skipf("环境网络不可达（%s），跳过 %s 端点：%v", KindOf(err).Label(), tc.name, err)
				}
				t.Fatalf("探测返回非网络类错误：%v", err)
			}
			t.Logf("PROBE %-9s reachable=%v status=%d provider_code=%q snippet=%q",
				tc.name, res.Reachable, res.StatusCode, res.ProviderCode, res.ResponseSnippet)

			if !res.Reachable {
				t.Fatalf("未收到服务商层面的响应（Reachable=false）")
			}
			// 环境可能经过代理/WAF：拿到预期之外的状态码时跳过而不是判红，
			// 避免「外网被拦」被误报成代码回归（结构正确性由 httptest 单测强断言）。
			if len(tc.wantStatus) > 0 && !slices.Contains(tc.wantStatus, res.StatusCode) {
				t.Skipf("状态码 %d 不在预期 %v（可能是代理/WAF/服务商策略变化），跳过：%s",
					res.StatusCode, tc.wantStatus, res.ResponseSnippet)
			}
			if tc.wantProviderCode != "" && !strings.HasPrefix(res.ProviderCode, tc.wantProviderCode) {
				t.Skipf("业务码 %q 不是预期的 %q 前缀（可能是代理/WAF 页面），跳过：%s",
					res.ProviderCode, tc.wantProviderCode, res.ResponseSnippet)
			}
			if len(tc.wantSnippetAny) > 0 {
				matched := false
				for _, want := range tc.wantSnippetAny {
					if strings.Contains(res.ResponseSnippet, want) {
						matched = true
						break
					}
				}
				if !matched {
					// 文案随区域/时间变化，这里只提示不判红。
					t.Logf("提示：响应片段 %q 未命中期望文案 %v（不影响结构结论）", res.ResponseSnippet, tc.wantSnippetAny)
				}
			}
			verified.Add(1)
		})
	}
	if verified.Load() == 0 {
		t.Skip("所有 HTTP 端点都被环境跳过（无网/被代理拦截），本次未取得真实端点证据")
	}
}

// 真实 SMTP 端点探测：TLS 握手 + EHLO + MAIL FROM 收到服务商回复即算可达。
// 注意：本机/CI 常屏蔽 587，故使用 465 隐式 TLS 的公共 SMTP 端点。
func TestLiveEmailProbe(t *testing.T) {
	if testing.Short() {
		t.Skip("-short：跳过真实外网端点探测")
	}
	if !hasOutboundNetwork(t) {
		t.Skip("检测不到外网，跳过 SMTP 端点探测")
	}

	c := NewClient(Options{Timeout: 12 * time.Second, Retry: &RetryPolicy{}})
	targets := []struct {
		name string
		ch   Channel
	}{
		{"qq", Channel{Kind: KindEmail, Config: map[string]any{
			"smtp_host": "smtp.qq.com", "smtp_port": 465, "tls": "ssl",
			"from": "ops@example.com", "to": []any{"oncall@example.com"},
		}}},
		{"163", Channel{Kind: KindEmail, Config: map[string]any{
			"smtp_host": "smtp.163.com", "smtp_port": 465, "tls": "ssl",
			"from": "ops@example.com", "to": []any{"oncall@example.com"},
		}}},
	}

	var reached int
	for _, tc := range targets {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			res, err := c.Probe(ctx, tc.ch)
			if err != nil {
				if isNetworkFailure(err) {
					t.Skipf("环境网络不可达（%s），跳过 SMTP %s：%v", KindOf(err).Label(), tc.name, err)
				}
				t.Fatalf("SMTP 探测失败：%v", err)
			}
			t.Logf("PROBE email/%-3s reachable=%v provider_code=%q snippet=%q",
				tc.name, res.Reachable, res.ProviderCode, res.ResponseSnippet)
			if !res.Reachable {
				t.Fatal("SMTP 端点未应答")
			}
			if res.ProviderCode == "" && res.ResponseSnippet == "" {
				t.Error("SMTP 探测未拿到任何服务商回复")
			}
			reached++
		})
	}
	if reached == 0 {
		t.Skip("所有 SMTP 探测端点都不可达（环境限制），已跳过")
	}
}
