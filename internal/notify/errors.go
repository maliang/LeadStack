package notify

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
)

// ErrorKind 是发送失败的错误分类。backend 把它的中文 Label 写进
// alert_channels.last_error，用户就能一眼看出是「DNS 错了 / 连不上 / TLS 问题 /
// 超时 / 服务商说 token 无效」中的哪一类。
type ErrorKind string

const (
	// ErrorKindConfig 渠道配置不合法（缺字段、类型错、URL 非法）。用户改配置即可。
	ErrorKindConfig ErrorKind = "config"
	// ErrorKindDNS DNS 解析失败（域名拼错、DNS 不可用）。
	ErrorKindDNS ErrorKind = "dns"
	// ErrorKindConnect 连接失败（拒绝连接、网络不可达、连接被重置）。
	ErrorKindConnect ErrorKind = "connect"
	// ErrorKindTLS TLS 握手/证书校验失败（自签证书、证书过期、SNI 不匹配）。
	ErrorKindTLS ErrorKind = "tls"
	// ErrorKindTimeout 请求超时（可重试）。
	ErrorKindTimeout ErrorKind = "timeout"
	// ErrorKindCanceled 调用方取消或整体超时，不是渠道问题。
	ErrorKindCanceled ErrorKind = "canceled"
	// ErrorKindAuth 服务商返回 401/403 或认证失败：凭据/token 无效或权限不足。
	ErrorKindAuth ErrorKind = "auth"
	// ErrorKindHTTP 其它非 2xx（4xx 居多）：服务商拒绝了请求，通常改配置。
	ErrorKindHTTP ErrorKind = "http"
	// ErrorKindServer 服务商 5xx（或 SMTP 4xx 临时失败）：可重试。
	ErrorKindServer ErrorKind = "server"
	// ErrorKindProvider 服务商以 HTTP 200 + 业务错误码拒绝（errcode/code != 0）。
	ErrorKindProvider ErrorKind = "provider"
	// ErrorKindInternal 本地错误（序列化、nil client 等），基本不是用户能修的。
	ErrorKindInternal ErrorKind = "internal"
)

// Label 返回中文短标签，可直接拼进 last_error。
func (k ErrorKind) Label() string {
	switch k {
	case ErrorKindConfig:
		return "配置无效"
	case ErrorKindDNS:
		return "DNS 解析失败"
	case ErrorKindConnect:
		return "连接失败"
	case ErrorKindTLS:
		return "TLS 握手失败"
	case ErrorKindTimeout:
		return "请求超时"
	case ErrorKindCanceled:
		return "已取消"
	case ErrorKindAuth:
		return "凭据被拒绝"
	case ErrorKindHTTP:
		return "服务商返回错误"
	case ErrorKindServer:
		return "服务商暂时不可用"
	case ErrorKindProvider:
		return "服务商拒绝"
	case ErrorKindInternal:
		return "内部错误"
	}
	return string(k)
}

// Category 把错误进一步归类，便于 backend 区分「配置问题 / 网络问题 / 服务商问题」：
// config | network | provider | canceled | internal。
func (k ErrorKind) Category() string {
	switch k {
	case ErrorKindConfig:
		return "config"
	case ErrorKindDNS, ErrorKindConnect, ErrorKindTLS, ErrorKindTimeout:
		return "network"
	case ErrorKindAuth, ErrorKindHTTP, ErrorKindServer, ErrorKindProvider:
		return "provider"
	case ErrorKindCanceled:
		return "canceled"
	default:
		return "internal"
	}
}

// Retryable 报告该错误是否值得重试（契约：仅 5xx 与超时）。
func (k ErrorKind) Retryable() bool {
	return k == ErrorKindTimeout || k == ErrorKindServer
}

// SendError 是 Send/Probe 返回的带分类错误。
// 其中的 Message / ResponseSnippet 都已脱敏，可安全写入 last_error 或日志。
type SendError struct {
	Kind            ErrorKind // 错误分类
	Channel         Kind      // 渠道类型
	ChannelID       string    // 渠道 ID（可能为空）
	StatusCode      int       // HTTP 状态码；SMTP 为 0
	ProviderCode    string    // 服务商业务码，如 "errcode=300005" / "SMTP 503"
	Message         string    // 已脱敏的简短说明
	ResponseSnippet string    // 已脱敏、截断到 512 字节的响应片段
}

// Error 渲染成一句可读的中文错误：notify: 钉钉发送失败（服务商拒绝） errcode=300005: token is not exist。
func (e *SendError) Error() string {
	if e == nil {
		return "notify: <nil>"
	}
	name := channelLabel(e.Channel)
	if name == "" {
		name = "通知"
	}
	var b strings.Builder
	b.WriteString("notify: ")
	b.WriteString(name)
	b.WriteString("发送失败（")
	b.WriteString(e.Kind.Label())
	b.WriteString("）")
	if e.StatusCode > 0 {
		fmt.Fprintf(&b, " HTTP %d", e.StatusCode)
	}
	if e.ProviderCode != "" {
		b.WriteString(" ")
		b.WriteString(e.ProviderCode)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

// AsSendError 提取 *SendError（支持包装链）。
func AsSendError(err error) (*SendError, bool) {
	var se *SendError
	if errors.As(err, &se) {
		return se, true
	}
	return nil, false
}

// KindOf 返回错误分类；非 *SendError 的非空错误归为 ErrorKindInternal。
func KindOf(err error) ErrorKind {
	if err == nil {
		return ""
	}
	if se, ok := AsSendError(err); ok {
		return se.Kind
	}
	return ErrorKindInternal
}

// channelLabel 返回渠道的中文名，用于错误信息。
func channelLabel(k Kind) string {
	switch k {
	case KindWebhook:
		return "webhook"
	case KindEmail:
		return "邮件"
	case KindDingTalk:
		return "钉钉"
	case KindWeCom:
		return "企业微信"
	case KindFeishu:
		return "飞书"
	case KindTelegram:
		return "telegram"
	case KindSlack:
		return "Slack"
	}
	return string(k)
}

// newSendError 构造分类错误。
func newSendError(chKind Kind, kind ErrorKind, status int, providerCode, message, snippet string) *SendError {
	return &SendError{
		Kind:            kind,
		Channel:         chKind,
		StatusCode:      status,
		ProviderCode:    providerCode,
		Message:         message,
		ResponseSnippet: snippet,
	}
}

// wrapConfigError 把 Validate/构建阶段的普通错误包装成 ErrorKindConfig。
func wrapConfigError(ch Channel, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := AsSendError(err); ok {
		return err
	}
	return &SendError{
		Kind:      ErrorKindConfig,
		Channel:   ch.Kind,
		ChannelID: ch.ID,
		Message:   stripNotifyPrefix(err.Error()),
	}
}

// stripNotifyPrefix 去掉内部错误信息的 "notify: " 前缀，避免拼接后重复。
func stripNotifyPrefix(s string) string {
	return strings.TrimPrefix(s, "notify: ")
}

// classifyTransport 把传输层错误归类为 DNS / 连接 / TLS / 超时 / 取消。
func classifyTransport(ctx context.Context, err error) ErrorKind {
	if err == nil {
		return ""
	}
	if ctx != nil && ctx.Err() != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return ErrorKindTimeout
		case errors.Is(ctx.Err(), context.Canceled):
			return ErrorKindCanceled
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return ErrorKindTimeout
	}
	if errors.Is(err, context.Canceled) {
		return ErrorKindCanceled
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ErrorKindDNS
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return ErrorKindTLS
	}
	var recErr tls.RecordHeaderError
	if errors.As(err, &recErr) {
		return ErrorKindTLS
	}
	var unknownCA x509.UnknownAuthorityError
	if errors.As(err, &unknownCA) {
		return ErrorKindTLS
	}
	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) {
		return ErrorKindTLS
	}
	var certInvalid x509.CertificateInvalidError
	if errors.As(err, &certInvalid) {
		return ErrorKindTLS
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Timeout() {
			return ErrorKindTimeout
		}
		if opErr.Op == "dial" {
			return ErrorKindConnect
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ErrorKindTimeout
	}

	// 跨平台兜底：某些包装较深的错误没有保留类型，只能看文案。
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "no such host"),
		strings.Contains(lower, "server misbehaving"),
		strings.Contains(lower, "name resolution"):
		return ErrorKindDNS
	case strings.Contains(lower, "tls:"),
		strings.Contains(lower, "x509:"),
		strings.Contains(lower, "certificate"):
		return ErrorKindTLS
	case strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "no route to host"),
		strings.Contains(lower, "network is unreachable"),
		strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "connection timed out"):
		return ErrorKindConnect
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "deadline exceeded"):
		return ErrorKindTimeout
	}
	return ErrorKindConnect
}
