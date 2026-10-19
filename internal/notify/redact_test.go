package notify

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"a", "***"},
		{"abc", "***"},
		{"abcd", "***"},
		{"abcde", "abcd***"},
		{"SECtestsecret0123456789abcdef", "SECt***"},
	}
	for _, tc := range cases {
		if got := Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMaskConfig(t *testing.T) {
	ch := Channel{Kind: KindDingTalk, Config: map[string]any{
		"webhook":   "https://oapi.dingtalk.com/robot/send?access_token=dt-access-token-0987654321&monkey=1",
		"secret":    "SECtestsecret0123456789abcdef",
		"password":  "Sup3rSecretPassword",
		"bot_token": "123456789:AAEtestbotTokenValue_xyZ",
		"headers":   map[string]any{"Authorization": "Bearer super-secret-jwt", "X-Plain": "public", "X-Api-Key": "apikey1234567890"},
		"method":    "POST",
	}}
	masked := MaskConfig(ch)

	if got := masked["secret"]; got != "SECt***" {
		t.Errorf("secret = %v, want SECt***", got)
	}
	if got := masked["password"]; got != "Sup3***" {
		t.Errorf("password = %v", got)
	}
	if got := masked["bot_token"]; got != "1234***" {
		t.Errorf("bot_token = %v", got)
	}
	url, _ := masked["webhook"].(string)
	if strings.Contains(url, "dt-access-token-0987654321") {
		t.Errorf("URL 中的 access_token 未脱敏: %s", url)
	}
	if !strings.Contains(url, "dt-a***") {
		t.Errorf("URL 中的 access_token 应保留前 4 位: %s", url)
	}
	headers, _ := masked["headers"].(map[string]any)
	if headers["Authorization"] != "Bear***" {
		t.Errorf("Authorization 未脱敏: %v", headers["Authorization"])
	}
	if headers["X-Api-Key"] != "apik***" {
		t.Errorf("X-Api-Key 未脱敏: %v", headers["X-Api-Key"])
	}
	if headers["X-Plain"] != "public" {
		t.Errorf("非敏感头不应被改动: %v", headers["X-Plain"])
	}
	if masked["method"] != "POST" {
		t.Errorf("普通字段不应被改动: %v", masked["method"])
	}

	// 不得修改入参。
	if ch.Config["secret"] != "SECtestsecret0123456789abcdef" {
		t.Errorf("MaskConfig 修改了入参: %v", ch.Config["secret"])
	}
	if !strings.Contains(ch.Config["webhook"].(string), "dt-access-token-0987654321") {
		t.Errorf("MaskConfig 修改了入参 URL: %v", ch.Config["webhook"])
	}
}

func TestMaskURL(t *testing.T) {
	masked, toks := maskURL("https://api.telegram.org/bot123456789:AAEtestbotTokenValue_xyZ/sendMessage")
	if strings.Contains(masked, "AAEtestbotTokenValue_xyZ") {
		t.Errorf("telegram token 未脱敏: %s", masked)
	}
	if !strings.Contains(masked, "/bot1234***/sendMessage") {
		t.Errorf("telegram token 应保留前 4 位: %s", masked)
	}
	if len(toks) == 0 {
		t.Error("应收集到 URL 中的 token 明文")
	}

	masked, _ = maskURL("https://hooks.slack.com/services/T000/B000/XXXXXXXX")
	if masked != "https://hooks.slack.com/services/T000/B000/XXXXXXXX" {
		t.Errorf("不含 token 的 URL 不应被改动: %s", masked)
	}

	if got, toks := maskURL("not a url at all"); strings.Contains(got, "not a url") {
		t.Errorf("非法 URL 不应回显原文: %s", got)
	} else if len(toks) != 1 {
		t.Errorf("非法 URL 的原文应进入清洗清单: %v", toks)
	}
}

func TestScrubberRemovesSecretsButKeepsDiagnostics(t *testing.T) {
	ch := Channel{Kind: KindWebhook, Config: map[string]any{
		"url":     "https://hooks.example.com/hook?access_token=leaktoken12345&monkey=1",
		"headers": map[string]any{"Authorization": "Bearer supersecretvalue1234"},
		"secret":  "SECtestsecret0123456789abcdef",
	}}
	sc := newScrubber(ch)

	// 长明文全局替换。
	got := sc.scrub("认证失败 token=SECtestsecret0123456789abcdef 被拒绝")
	if strings.Contains(got, "SECtestsecret0123456789abcdef") {
		t.Errorf("长 secret 未脱敏: %s", got)
	}
	if !strings.Contains(got, "SECt***") {
		t.Errorf("长 secret 应保留前 4 位: %s", got)
	}

	// URL 中的 token（含短参数 monkey=1）必须精确脱敏，不能误伤正常文本。
	text := `Post "https://hooks.example.com/hook?access_token=leaktoken12345&monkey=1": dial tcp 127.0.0.1:8080: connect: connection refused`
	got = sc.scrub(text)
	if strings.Contains(got, "leaktoken12345") {
		t.Errorf("URL token 未脱敏: %s", got)
	}
	if !strings.Contains(got, "127.0.0.1:8080") {
		t.Errorf("脱敏误伤 IP/端口: %s", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Errorf("脱敏误伤错误原因: %s", got)
	}
	if strings.Contains(got, "***27.0.0.***") {
		t.Errorf("出现短值全局替换造成的破坏: %s", got)
	}

	// header 值（名字不像密钥也要清洗，因为可能出现在响应体里）。
	got = sc.scrub("Bearer supersecretvalue1234 无效")
	if strings.Contains(got, "supersecretvalue1234") {
		t.Errorf("header 值未脱敏: %s", got)
	}
}

func TestMaskURLForKindPathCredentials(t *testing.T) {
	cases := []struct {
		name    string
		kind    Kind
		raw     string
		mustNot string
		mustHas string
	}{
		{
			name: "slack 末段是凭证", kind: KindSlack,
			raw:     "https://hooks.slack.com/services/T000/B000/slackhooksecret123456",
			mustNot: "slackhooksecret123456", mustHas: "slac***",
		},
		{
			name: "feishu hook id 是凭证", kind: KindFeishu,
			raw:     "https://open.feishu.cn/open-apis/bot/v2/hook/feishuhookid1234567890",
			mustNot: "feishuhookid1234567890", mustHas: "feis***",
		},
		{
			name: "通用 webhook 长末段按凭证处理", kind: KindWebhook,
			raw:     "https://example.com/api/v1/incoming/9f8e7d6c5b4a3210",
			mustNot: "9f8e7d6c5b4a3210", mustHas: "9f8e***",
		},
		{
			name: "通用 webhook 短路径段保持可读", kind: KindWebhook,
			raw:     "https://example.com/notify/hook",
			mustHas: "/notify/hook",
		},
		{
			name: "钉钉查询串 token 脱敏", kind: KindDingTalk,
			raw:     "https://oapi.dingtalk.com/robot/send?access_token=dingtoken1234567890",
			mustNot: "dingtoken1234567890", mustHas: "ding***",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			masked, toks := maskURLForKind(tc.kind, tc.raw)
			if tc.mustNot != "" && strings.Contains(masked, tc.mustNot) {
				t.Errorf("未脱敏: %s", masked)
			}
			if tc.mustHas != "" && !strings.Contains(masked, tc.mustHas) {
				t.Errorf("脱敏结果 %q 不含预期片段 %q", masked, tc.mustHas)
			}
			if tc.mustNot != "" && len(toks) == 0 {
				t.Errorf("应收集到明文用于清洗错误文本: %s", masked)
			}
		})
	}
}

func TestScrubberNilSafe(t *testing.T) {
	var sc *scrubber
	if got := sc.scrub("原样返回"); got != "原样返回" {
		t.Errorf("nil scrubber 应原样返回: %q", got)
	}
}

func TestTruncateBytesRespectsUTF8(t *testing.T) {
	s := strings.Repeat("中", 300) // 900 字节
	cut, truncated := truncateBytes(s, errorBodyLimit)
	if !truncated {
		t.Fatal("应当发生截断")
	}
	if len(cut) > errorBodyLimit {
		t.Fatalf("截断后长度 = %d > %d", len(cut), errorBodyLimit)
	}
	if !strings.Contains(strings.ToValidUTF8(cut, ""), "中") {
		t.Errorf("截断结果不应是乱码: %q", cut)
	}
	if _, truncated := truncateBytes("短", errorBodyLimit); truncated {
		t.Error("未超长不应截断")
	}
}

func TestIsSensitiveKeyAndHeader(t *testing.T) {
	for _, k := range []string{"secret", "password", "bot_token", "access_token", "key", "passwd", "PASSWORD"} {
		if !isSensitiveKey(k) {
			t.Errorf("%s 应视为密钥字段", k)
		}
	}
	for _, k := range []string{"url", "method", "template", "chat_id", "smtp_host", "from", "to", "parse_mode", "api_base", "timeout_s"} {
		if isSensitiveKey(k) {
			t.Errorf("%s 不应视为密钥字段", k)
		}
	}
	for _, h := range []string{"Authorization", "X-Api-Key", "X-Token", "Cookie", "X-Secret"} {
		if !isSensitiveHeader(h) {
			t.Errorf("%s 应视为敏感头", h)
		}
	}
	for _, h := range []string{"Content-Type", "X-Plain", "Accept"} {
		if isSensitiveHeader(h) {
			t.Errorf("%s 不应视为敏感头", h)
		}
	}
}
