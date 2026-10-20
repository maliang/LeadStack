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
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SMTP 加密方式（Config.tls）。
const (
	tlsStartTLS = "starttls"
	tlsSSL      = "ssl"
	tlsNone     = "none"
)

// smtpSession 是一次 SMTP 连接及其清理逻辑。
type smtpSession struct {
	conn   net.Conn
	client *smtp.Client
	host   string
	scr    *scrubber
	done   chan struct{}
	once   sync.Once
}

// Close 关闭会话（幂等）。
func (s *smtpSession) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		close(s.done)
		if s.client != nil {
			_ = s.client.Close()
		}
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})
}

// tlsConfigFor 返回邮件渠道使用的 TLS 配置。
func tlsConfigFor(host string) *tls.Config {
	return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
}

// looksLikeEmail 做最基本的邮箱格式检查（只用于 reply_to 之类可选字段）。
func looksLikeEmail(s string) bool {
	if strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	at := strings.LastIndex(s, "@")
	return at > 0 && at < len(s)-1 && !strings.Contains(s[at+1:], "@")
}

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
	if rt, hasReplyTo, err := cfgString(ch.Config, "reply_to"); err != nil {
		return err
	} else if hasReplyTo && !looksLikeEmail(rt) {
		return errors.New("notify: 邮件渠道配置项 reply_to 不是合法的邮箱地址")
	}
	if _, err := cfgBool(ch.Config, "html", false); err != nil {
		return err
	}
	return nil
}

// dialSMTP 建立 SMTP 会话：按 ssl/starttls/none 选择加密方式，并设置整体超时与 ctx 取消。
func (c *Client) dialSMTP(ctx context.Context, ch Channel) (*smtpSession, error) {
	host, _, _ := cfgString(ch.Config, "smtp_host")
	port, _ := cfgInt(ch.Config, "smtp_port", 587)
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
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConfigFor(host))
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		kind := classifyTransport(ctx, err)
		if kind == "" {
			kind = ErrorKindConnect
		}
		return nil, newSendError(ch.Kind, kind, 0, "",
			"邮件连接 SMTP 服务器失败: "+scr.scrub(err.Error()), "")
	}

	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	sess := &smtpSession{conn: conn, host: host, scr: scr, done: make(chan struct{})}
	// ctx 取消时强制关闭连接，保证超时/取消能真正中断 SMTP 对话。
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-sess.done:
		}
	}()

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		sess.Close()
		return nil, smtpFailure(ctx, ch, scr, "邮件 SMTP 握手失败", err)
	}
	sess.client = client
	return sess, nil
}

// smtpFailure 把 SMTP 阶段错误分类：
//   - 5xx / 认证失败 → 服务商拒绝（凭据或配置问题，不重试）；
//   - 4xx → 服务商临时失败（SMTP 协议里 4xx 明确是可重试的临时错误）；
//   - 传输层错误 → DNS/连接/TLS/超时。
func smtpFailure(ctx context.Context, ch Channel, scr *scrubber, step string, err error) error {
	kind := ErrorKindProvider
	code := 0
	var tpErr *textproto.Error
	if errors.As(err, &tpErr) {
		code = tpErr.Code
		switch {
		case code == 530 || code == 534 || code == 535 || code == 538:
			kind = ErrorKindAuth
		case code >= 500:
			kind = ErrorKindProvider
		case code >= 400:
			kind = ErrorKindServer
		}
	} else if strings.Contains(strings.ToLower(err.Error()), "unencrypted connection") {
		// smtp.PlainAuth 拒绝在明文连接上传凭证。
		kind = ErrorKindConfig
	} else if k := classifyTransport(ctx, err); k != "" {
		kind = k
	}

	providerCode := ""
	if code > 0 {
		providerCode = fmt.Sprintf("SMTP %d", code)
	}
	return newSendError(ch.Kind, kind, 0, providerCode,
		step+": "+scr.scrub(err.Error()), scr.scrub(err.Error()))
}

// sendEmail 通过 SMTP 发送邮件：支持 starttls / ssl / none 三种加密方式，可选用户名密码。
func (c *Client) sendEmail(ctx context.Context, ch Channel, msg Message) error {
	sess, err := c.dialSMTP(ctx, ch)
	if err != nil {
		return err
	}
	defer sess.Close()

	mode, ok, _ := cfgString(ch.Config, "tls")
	if !ok {
		mode = tlsStartTLS
	}
	user, hasUser, _ := cfgString(ch.Config, "username")
	pass, _, _ := cfgString(ch.Config, "password")

	if mode == tlsStartTLS {
		if supported, _ := sess.client.Extension("STARTTLS"); supported {
			if err := sess.client.StartTLS(tlsConfigFor(sess.host)); err != nil {
				return smtpFailure(ctx, ch, sess.scr, "邮件 STARTTLS 失败", err)
			}
		} else if hasUser {
			return newSendError(ch.Kind, ErrorKindConfig, 0, "",
				"SMTP 服务器不支持 STARTTLS，已拒绝以明文发送凭证", "")
		}
	}
	if hasUser {
		if err := sess.client.Auth(smtp.PlainAuth("", user, pass, sess.host)); err != nil {
			return smtpFailure(ctx, ch, sess.scr, "邮件认证失败", err)
		}
	}

	from, _, _ := cfgString(ch.Config, "from")
	to, _ := cfgStringSlice(ch.Config, "to")

	if err := sess.client.Mail(from); err != nil {
		return smtpFailure(ctx, ch, sess.scr, "邮件 MAIL FROM 被拒绝", err)
	}
	for _, rcpt := range to {
		if err := sess.client.Rcpt(rcpt); err != nil {
			return smtpFailure(ctx, ch, sess.scr, "邮件 RCPT TO 被拒绝", err)
		}
	}
	w, err := sess.client.Data()
	if err != nil {
		return smtpFailure(ctx, ch, sess.scr, "邮件 DATA 被拒绝", err)
	}
	if _, err := w.Write(c.buildEmail(ch, from, to, msg)); err != nil {
		return smtpFailure(ctx, ch, sess.scr, "邮件正文写入失败", err)
	}
	if err := w.Close(); err != nil {
		return smtpFailure(ctx, ch, sess.scr, "邮件发送失败", err)
	}
	if err := sess.client.Quit(); err != nil {
		return smtpFailure(ctx, ch, sess.scr, "邮件退出 SMTP 会话失败", err)
	}
	return nil
}

// buildEmail 组装 RFC 5322 邮件：主题取 Message.Title，正文取 Message.Body（附渲染后的完整文本）；
// 可选 Reply-To 与 multipart/alternative（纯文本 + HTML）。
func (c *Client) buildEmail(ch Channel, from string, to []string, msg Message) []byte {
	subject := titleOr(msg.Title, "LeadStack 告警")
	if msg.Recovered && !strings.Contains(subject, "恢复") {
		subject = "[已恢复] " + subject
	}
	body := renderText(msg)
	if msg.Body != "" && !strings.Contains(body, msg.Body) {
		body += "\n\n" + msg.Body
	}
	replyTo, hasReplyTo, _ := cfgString(ch.Config, "reply_to")
	withHTML, _ := cfgBool(ch.Config, "html", false)

	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	if hasReplyTo {
		fmt.Fprintf(&b, "Reply-To: %s\r\n", replyTo)
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", c.now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")

	if withHTML {
		boundary := "leadstack-" + strconv.FormatInt(c.now().UnixNano(), 36)
		fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n", boundary)
		b.WriteString("\r\n")
		fmt.Fprintf(&b, "--%s\r\n", boundary)
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
		b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		b.WriteString(body)
		b.WriteString("\r\n")
		fmt.Fprintf(&b, "--%s\r\n", boundary)
		b.WriteString("Content-Type: text/html; charset=utf-8\r\n")
		b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		b.WriteString(renderHTML(msg))
		b.WriteString("\r\n")
		fmt.Fprintf(&b, "--%s--\r\n", boundary)
	} else {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
		b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
		b.WriteString("\r\n")
		b.WriteString(body)
		b.WriteString("\r\n")
	}
	return b.Bytes()
}

// probeEmail 用真实 SMTP 端点探测：TLS/STARTTLS 与 EHLO 成功即说明协议层可用；
// 只要能收到服务商的 SMTP 回复（哪怕 MAIL FROM 被拒）就算「可达」。
func (c *Client) probeEmail(ctx context.Context, ch Channel) (ProbeResult, error) {
	sess, err := c.dialSMTP(ctx, ch)
	if err != nil {
		return ProbeResult{Kind: KindEmail, ErrorKind: KindOf(err)}, err
	}
	defer sess.Close()

	mode, ok, _ := cfgString(ch.Config, "tls")
	if !ok {
		mode = tlsStartTLS
	}
	if mode == tlsStartTLS {
		if supported, _ := sess.client.Extension("STARTTLS"); supported {
			if err := sess.client.StartTLS(tlsConfigFor(sess.host)); err != nil {
				se := smtpFailure(ctx, ch, sess.scr, "邮件 STARTTLS 失败", err)
				return ProbeResult{Kind: KindEmail, ErrorKind: KindOf(se)}, se
			}
		}
	}

	from, _, _ := cfgString(ch.Config, "from")
	err = sess.client.Mail(from)
	if err == nil {
		_ = sess.client.Quit()
		return ProbeResult{Kind: KindEmail, Reachable: true, ResponseSnippet: "MAIL FROM accepted"}, nil
	}
	var tpErr *textproto.Error
	if errors.As(err, &tpErr) {
		// 服务商明确回复了错误码：说明地址、TLS、协议对话都被正确解析。
		_ = sess.client.Quit()
		return ProbeResult{
			Kind:            KindEmail,
			Reachable:       true,
			ProviderCode:    fmt.Sprintf("SMTP %d", tpErr.Code),
			ResponseSnippet: sess.scr.scrub(strings.TrimSpace(tpErr.Msg)),
		}, nil
	}
	se := smtpFailure(ctx, ch, sess.scr, "邮件 MAIL FROM 被拒绝", err)
	return ProbeResult{Kind: KindEmail, ErrorKind: KindOf(se)}, se
}
