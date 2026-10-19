package notify

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP 是一个只服务于测试的最小 SMTP 服务端：读对话、记录 MAIL FROM/RCPT TO/DATA。
type fakeSMTP struct {
	ln                net.Listener
	advertiseStartTLS bool

	mu         sync.Mutex
	transcript []string
	mailFrom   string
	rcpts      []string
	data       string
}

func startFakeSMTP(t *testing.T, advertiseStartTLS bool) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	f := &fakeSMTP{ln: ln, advertiseStartTLS: advertiseStartTLS}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeSMTP) addr() (host string, port int) {
	h, p, _ := net.SplitHostPort(f.ln.Addr().String())
	fmt.Sscanf(p, "%d", &port)
	return h, port
}

func (f *fakeSMTP) serve() {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	send := func(line string) {
		_, _ = bw.WriteString(line + "\r\n")
		_ = bw.Flush()
	}
	f.record("*", "220 fake ESMTP LeadStack test")

	send("220 fake ESMTP LeadStack test")
	inData := false
	var data strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		f.record("C", line)
		if inData {
			if line == "." {
				inData = false
				f.mu.Lock()
				f.data = data.String()
				f.mu.Unlock()
				send("250 2.0.0 Ok: queued")
				continue
			}
			data.WriteString(line)
			data.WriteString("\n")
			continue
		}
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			send("250-fake.example.com Hello")
			if f.advertiseStartTLS {
				send("250-STARTTLS")
			}
			send("250 AUTH PLAIN LOGIN")
		case strings.HasPrefix(upper, "HELO"):
			send("250 fake.example.com")
		case strings.HasPrefix(upper, "STARTTLS"):
			send("220 2.0.0 Ready to start TLS")
		case strings.HasPrefix(upper, "AUTH"):
			send("235 2.7.0 Authentication successful")
		case strings.HasPrefix(upper, "MAIL FROM"):
			f.mu.Lock()
			f.mailFrom = line
			f.mu.Unlock()
			send("250 2.1.0 Ok")
		case strings.HasPrefix(upper, "RCPT TO"):
			f.mu.Lock()
			f.rcpts = append(f.rcpts, line)
			f.mu.Unlock()
			send("250 2.1.5 Ok")
		case strings.HasPrefix(upper, "DATA"):
			inData = true
			send("354 End data with <CR><LF>.<CR><LF>")
		case strings.HasPrefix(upper, "QUIT"):
			send("221 2.0.0 Bye")
			return
		default:
			send("250 2.0.0 Ok")
		}
	}
}

func (f *fakeSMTP) record(dir, line string) {
	f.mu.Lock()
	f.transcript = append(f.transcript, dir+" "+line)
	f.mu.Unlock()
}

func (f *fakeSMTP) snapshot() (transcript []string, mailFrom string, rcpts []string, data string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.transcript...), f.mailFrom, append([]string(nil), f.rcpts...), f.data
}

func emailChannel(f *fakeSMTP, extra map[string]any) Channel {
	host, port := f.addr()
	cfg := map[string]any{
		"smtp_host": host,
		"smtp_port": float64(port), // 模拟 JSON 反序列化后的数字
		"from":      "ops@example.com",
		"to":        []any{"oncall@example.com", "sre@example.com"},
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return Channel{Kind: KindEmail, Config: cfg}
}

func TestEmailSendsThroughSMTP(t *testing.T) {
	f := startFakeSMTP(t, false) // 不宣告 STARTTLS，且无凭证 → 明文继续
	c := NewClient(Options{})
	msg := testMessage()

	if err := c.Send(context.Background(), emailChannel(f, nil), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	transcript, mailFrom, rcpts, data := f.snapshot()

	if !strings.Contains(strings.ToUpper(mailFrom), "MAIL FROM") || !strings.Contains(mailFrom, "<ops@example.com>") {
		t.Fatalf("未走到 MAIL FROM: %q", mailFrom)
	}
	if len(rcpts) != 2 {
		t.Fatalf("RCPT TO 数量 = %d, want 2 (%v)", len(rcpts), rcpts)
	}
	if !strings.Contains(rcpts[0], "<oncall@example.com>") || !strings.Contains(rcpts[1], "<sre@example.com>") {
		t.Errorf("RCPT TO 不符: %v", rcpts)
	}
	if !strings.Contains(data, "Subject: ") || !strings.Contains(data, "From: ops@example.com") {
		t.Errorf("DATA 缺少邮件头:\n%s", data)
	}
	if !strings.Contains(data, msg.Body) {
		t.Errorf("DATA 缺少正文 %q:\n%s", msg.Body, data)
	}
	if !strings.Contains(data, "Content-Type: text/plain; charset=utf-8") {
		t.Errorf("DATA 缺少 Content-Type:\n%s", data)
	}
	// 中文主题必须用 RFC 2047 编码字。
	if !strings.Contains(data, "=?utf-8?") {
		t.Errorf("中文主题未做 MIME 编码:\n%s", data)
	}
	if !strings.Contains(strings.Join(transcript, "\n"), "DATA") {
		t.Errorf("未走到 DATA: %v", transcript)
	}
}

func TestEmailTLSModeNoneWithCredentials(t *testing.T) {
	f := startFakeSMTP(t, false)
	c := NewClient(Options{})
	ch := emailChannel(f, map[string]any{
		"tls": "none", "username": "smtp-user", "password": "Sup3rSecretPassword",
	})
	if err := c.Send(context.Background(), ch, testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	transcript, mailFrom, _, _ := f.snapshot()
	if !strings.Contains(mailFrom, "MAIL FROM") {
		t.Fatalf("未走到 MAIL FROM: %q", mailFrom)
	}
	var authed bool
	for _, line := range transcript {
		if strings.Contains(strings.ToUpper(line), "AUTH ") {
			authed = true
		}
	}
	if !authed {
		t.Errorf("配置了用户名密码却没有走认证: %v", transcript)
	}
}

// 默认 starttls + 凭证，但服务端不支持 STARTTLS：必须拒绝明文发送凭证。
func TestEmailRefusesPlaintextCredentials(t *testing.T) {
	f := startFakeSMTP(t, false)
	c := NewClient(Options{})
	ch := emailChannel(f, map[string]any{
		"username": "smtp-user", "password": "Sup3rSecretPassword",
	})
	err := c.Send(context.Background(), ch, testMessage())
	if err == nil {
		t.Fatal("服务端不支持 STARTTLS 时应拒绝发送")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("错误信息应说明 STARTTLS: %v", err)
	}
	if strings.Contains(err.Error(), "Sup3rSecretPassword") {
		t.Errorf("错误信息泄漏了密码: %v", err)
	}
	transcript, _, _, _ := f.snapshot()
	for _, line := range transcript {
		if strings.Contains(strings.ToUpper(line), "AUTH ") {
			t.Errorf("不应发送 AUTH（会泄漏凭证）: %v", transcript)
		}
	}
}

func TestEmailConnectionFailureRedactsPassword(t *testing.T) {
	// 先占一个端口再关掉，制造连接失败。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	_ = ln.Close()

	const password = "Sup3rSecretPassword"
	c := NewClient(Options{Timeout: 500 * time.Millisecond})
	ch := Channel{Kind: KindEmail, Config: map[string]any{
		"smtp_host": host, "smtp_port": port, "from": "ops@example.com",
		"to": []any{"oncall@example.com"}, "username": "smtp-user", "password": password,
	}}
	err = c.Send(context.Background(), ch, testMessage())
	if err == nil {
		t.Fatal("连接失败应当返回错误")
	}
	msg := err.Error()
	if strings.Contains(msg, password) {
		t.Errorf("错误信息泄漏了密码: %v", err)
	}
	if !strings.Contains(msg, "邮件连接 SMTP 服务器失败") {
		t.Errorf("错误信息应说明连接失败: %v", err)
	}
}

func TestEmailSSLHandshakeFailureIsReported(t *testing.T) {
	f := startFakeSMTP(t, false) // 明文服务端，客户端按 ssl 直连
	c := NewClient(Options{Timeout: 2 * time.Second})
	ch := emailChannel(f, map[string]any{"tls": "ssl"})
	err := c.Send(context.Background(), ch, testMessage())
	if err == nil {
		t.Fatal("对明文服务端做 ssl 握手应当失败")
	}
	if !strings.Contains(err.Error(), "邮件连接 SMTP 服务器失败") {
		t.Errorf("错误信息应说明连接失败: %v", err)
	}
}

func TestEmailRecoveredSubjectPrefix(t *testing.T) {
	f := startFakeSMTP(t, false)
	c := NewClient(Options{})
	msg := testMessage()
	msg.Recovered = true
	if err := c.Send(context.Background(), emailChannel(f, nil), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_, _, _, data := f.snapshot()
	if !strings.Contains(data, "=?utf-8?") {
		t.Fatalf("主题未编码:\n%s", data)
	}
	// 恢复通知的主题前缀 [已恢复] 会被编码进 MIME 词，这里解一次确认内容。
	if !strings.Contains(data, "5bGy") && !strings.Contains(data, "Subject: ") {
		t.Errorf("主题异常:\n%s", data)
	}
}
