package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTP 加密方式（Config.tls）。
const (
	tlsStartTLS = "starttls"
	tlsSSL      = "ssl"
	tlsNone     = "none"
)

// validateEmail 校验邮件渠道配置（必填 smtp_host、from、to）。
func validateEmail(ch Channel) error {
	if _, ok, err := cfgString(ch.Config, "smtp_host"); err != nil {
		return err
	} else if !ok {
		return errors.New("notify: 邮件渠道缺少必填配置 smtp_host")
	}
	if _, ok, err := cfgString(ch.Config, "from"); err != nil {
		return err
	} else if !ok {
		return errors.New("notify: 邮件渠道缺少必填配置 from")
	}
	to, err := cfgStringSlice(ch.Config, "to")
	if err != nil {
		return err
	}
	if len(to) == 0 {
		return errors.New("notify: 邮件渠道缺少必填配置 to（收件人数组）")
	}
	port, err := cfgInt(ch.Config, "smtp_port", 587)
	if err != nil {
		return err
	}
	if port <= 0 || port > 65535 {
		return fmt.Errorf("notify: 邮件 smtp_port 非法（%d，应在 1-65535）", port)
	}
	mode, ok, err := cfgString(ch.Config, "tls")
	if err != nil {
		return err
	}
	if ok {
		switch mode {
		case tlsStartTLS, tlsSSL, tlsNone:
		default:
			return fmt.Errorf("notify: 邮件 tls 只支持 starttls/ssl/none，收到 %s", mode)
		}
	}
	if _, hasUser, err := cfgString(ch.Config, "username"); err != nil {
		return err
	} else if !hasUser {
		if _, hasPass, _ := cfgString(ch.Config, "password"); hasPass {
			return errors.New("notify: 邮件渠道配置了 password 但缺少 username")
		}
	}
	return nil
}

// sendEmail 通过 SMTP 发送邮件：支持 starttls / ssl / none 三种加密方式，可选用户名密码。
func (c *Client) sendEmail(ctx context.Context, ch Channel, msg Message) error {
	host, _, _ := cfgString(ch.Config, "smtp_host")
	port, _ := cfgInt(ch.Config, "smtp_port", 587)
	from, _, _ := cfgString(ch.Config, "from")
	to, _ := cfgStringSlice(ch.Config, "to")
	user, hasUser, _ := cfgString(ch.Config, "username")
	pass, _, _ := cfgString(ch.Config, "password")
	mode, ok, _ := cfgString(ch.Config, "tls")
	if !ok {
		mode = tlsStartTLS
	}
	scr := newScrubber(ch)

	timeout := c.Timeout()
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: timeout}

	var conn net.Conn
	var err error
	if mode == tlsSSL {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("notify: 邮件连接 SMTP 服务器失败: %s", scr.scrub(err.Error()))
	}
	defer func() { _ = conn.Close() }()

	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	// ctx 取消时强制关闭连接，保证超时/取消能真正中断 SMTP 对话。
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("notify: 邮件 SMTP 握手失败: %s", scr.scrub(err.Error()))
	}
	defer func() { _ = client.Close() }()

	if mode == tlsStartTLS {
		if supported, _ := client.Extension("STARTTLS"); supported {
			if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("notify: 邮件 STARTTLS 失败: %s", scr.scrub(err.Error()))
			}
		} else if hasUser {
			return errors.New("notify: SMTP 服务器不支持 STARTTLS，已拒绝以明文发送凭证")
		}
	}
	if hasUser {
		auth := smtp.PlainAuth("", user, pass, host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("notify: 邮件认证失败: %s", scr.scrub(err.Error()))
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("notify: 邮件 MAIL FROM 被拒绝: %s", scr.scrub(err.Error()))
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("notify: 邮件 RCPT TO 被拒绝: %s", scr.scrub(err.Error()))
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("notify: 邮件 DATA 被拒绝: %s", scr.scrub(err.Error()))
	}
	if _, err := w.Write(c.buildEmail(from, to, msg)); err != nil {
		return fmt.Errorf("notify: 邮件正文写入失败: %s", scr.scrub(err.Error()))
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("notify: 邮件发送失败: %s", scr.scrub(err.Error()))
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("notify: 邮件退出 SMTP 会话失败: %s", scr.scrub(err.Error()))
	}
	return nil
}

// buildEmail 组装 RFC 5322 文本邮件：主题取 Message.Title，正文取 Message.Body（并附渲染后的完整文本）。
func (c *Client) buildEmail(from string, to []string, msg Message) []byte {
	subject := titleOr(msg.Title, "LeadStack 告警")
	if msg.Recovered && !strings.Contains(subject, "恢复") {
		subject = "[已恢复] " + subject
	}
	body := renderText(msg)
	if msg.Body != "" && !strings.Contains(body, msg.Body) {
		body += "\n\n" + msg.Body
	}

	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", c.now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	b.WriteString("\r\n")
	return b.Bytes()
}
