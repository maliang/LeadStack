package notify

import (
	"context"
	"strings"
	"testing"
)

func TestEmailReplyToHeader(t *testing.T) {
	f := startFakeSMTP(t, false)
	c := NewClient(Options{})
	ch := emailChannel(f, map[string]any{"reply_to": "alerts@example.com"})
	if err := c.Send(context.Background(), ch, testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_, _, _, data := f.snapshot()
	if !strings.Contains(data, "Reply-To: alerts@example.com") {
		t.Errorf("缺少 Reply-To 头:\n%s", data)
	}

	// 未配置时不应出现该头。
	f2 := startFakeSMTP(t, false)
	if err := c.Send(context.Background(), emailChannel(f2, nil), testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, _, _, data := f2.snapshot(); strings.Contains(data, "Reply-To:") {
		t.Errorf("未配置 reply_to 时不应有 Reply-To 头:\n%s", data)
	}
}

func TestEmailHTMLMultipart(t *testing.T) {
	f := startFakeSMTP(t, false)
	c := NewClient(Options{})
	msg := testMessage()
	msg.Body = "磁盘 <b>已满</b> & 需要处理"
	ch := emailChannel(f, map[string]any{"html": true})
	if err := c.Send(context.Background(), ch, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_, _, _, data := f.snapshot()

	if !strings.Contains(data, "multipart/alternative") {
		t.Fatalf("html=true 时应为 multipart/alternative:\n%s", data)
	}
	if !strings.Contains(data, "Content-Type: text/plain; charset=utf-8") {
		t.Errorf("缺少纯文本部分:\n%s", data)
	}
	if !strings.Contains(data, "Content-Type: text/html; charset=utf-8") {
		t.Errorf("缺少 HTML 部分:\n%s", data)
	}
	// HTML 部分必须转义，不能把 <b> 当标签透传。
	if !strings.Contains(data, "&lt;b&gt;已满&lt;/b&gt;") {
		t.Errorf("HTML 部分未转义:\n%s", data)
	}
	if strings.Contains(data, "<b>已满</b>") && strings.Count(data, "<b>") > 1 {
		t.Errorf("HTML 部分疑似未转义:\n%s", data)
	}
	// 纯文本部分保持原文。
	if !strings.Contains(data, "磁盘 <b>已满</b> & 需要处理") {
		t.Errorf("纯文本部分应保留原文:\n%s", data)
	}
}

func TestEmailPlainTextByDefault(t *testing.T) {
	f := startFakeSMTP(t, false)
	if err := NewClient(Options{}).Send(context.Background(), emailChannel(f, nil), testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_, _, _, data := f.snapshot()
	if strings.Contains(data, "multipart/alternative") {
		t.Errorf("默认应为单部分纯文本:\n%s", data)
	}
}

func TestValidateEmailReplyToAndHTML(t *testing.T) {
	c := NewClient(Options{})
	base := func(extra map[string]any) Channel {
		cfg := map[string]any{
			"smtp_host": "smtp.example.com", "from": "ops@example.com", "to": []any{"a@b.c"},
		}
		for k, v := range extra {
			cfg[k] = v
		}
		return Channel{Kind: KindEmail, Config: cfg}
	}
	if err := c.Validate(base(map[string]any{"reply_to": "alerts@example.com"})); err != nil {
		t.Errorf("合法 reply_to 被拒: %v", err)
	}
	for _, bad := range []string{"not-an-email", "a b@example.com", "@example.com", "a@"} {
		err := c.Validate(base(map[string]any{"reply_to": bad}))
		if err == nil || !strings.Contains(err.Error(), "reply_to") {
			t.Errorf("非法 reply_to %q 应被拒绝: %v", bad, err)
		}
	}
	if err := c.Validate(base(map[string]any{"html": true})); err != nil {
		t.Errorf("html=true 应被接受: %v", err)
	}
	if err := c.Validate(base(map[string]any{"html": "maybe"})); err == nil {
		t.Error("html=maybe 应被拒绝")
	}
}
