// Package notify 实现 LeadStack 告警 的通知渠道。
//
// 契约见 docs/ARCHITECTURE.md 第 19.2 节：
//   - Kind / Channel / Severity / Message / Client 与各 Kind 的 Config 字段已冻结；
//   - 本包只依赖标准库，不得 import internal/store、internal/api、internal/alerting；
//   - 密钥类字段（secret / password / bot_token 以及 webhook URL 里的 token 段）在错误信息
//     与日志中一律脱敏（保留前 4 位 + "***"）；发送失败时不再泄漏完整 config；
//   - HTTP 非 2xx 视为失败，响应体截断到前 512 字节后进入错误信息。
package notify

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Kind 是通知渠道类型（冻结）。
type Kind string

const (
	KindWebhook  Kind = "webhook"
	KindEmail    Kind = "email"
	KindDingTalk Kind = "dingtalk"
	KindWeCom    Kind = "wecom"
	KindFeishu   Kind = "feishu"
	KindTelegram Kind = "telegram"
	KindSlack    Kind = "slack"
)

// allKinds 是受支持渠道的固定顺序，供校验与文档复用。
var allKinds = []Kind{
	KindWebhook, KindEmail, KindDingTalk, KindWeCom, KindFeishu, KindTelegram, KindSlack,
}

// Kinds 返回全部受支持的渠道类型。
func Kinds() []Kind {
	out := make([]Kind, len(allKinds))
	copy(out, allKinds)
	return out
}

// SupportedKind 报告 k 是否为受支持的渠道类型。
func SupportedKind(k Kind) bool {
	for _, v := range allKinds {
		if v == k {
			return true
		}
	}
	return false
}

// Severity 是告警级别（与 alerting.Severity 取值一致）。
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// Channel 是一次通知的渠道配置。Config 的字段随 Kind 不同，见契约表格。
type Channel struct {
	ID      string
	Name    string
	Kind    Kind
	Enabled bool
	Config  map[string]any
}

// Message 是一条待发送的通知内容。
type Message struct {
	Title      string
	Body       string
	Severity   Severity
	ServerName string
	CheckName  string
	Value      float64
	TS         int64
	Recovered  bool
	AlertID    string
	ConsoleURL string
}

// Options 是 NewClient 的依赖注入点，零值可用。
type Options struct {
	// HTTPClient 用于全部 HTTP 渠道；为 nil 时使用内部默认客户端。
	HTTPClient *http.Client
	// Timeout 是单渠道默认超时；<=0 时使用 DefaultTimeout（10s）。
	Timeout time.Duration
	// Now 返回当前时间，仅用于签名时间戳（测试可注入固定时钟）；为 nil 时使用 time.Now。
	Now func() time.Time
	// Retry 控制 5xx 与超时的重试；nil 时使用 DefaultRetryPolicy（2 次、200ms/400ms 退避）。
	// 显式传 &RetryPolicy{} 表示不重试。
	Retry *RetryPolicy
}

// DefaultTimeout 是契约约定的单渠道默认超时。
const DefaultTimeout = 10 * time.Second

// Client 负责校验并发送通知。可并发使用。
type Client struct {
	http    *http.Client
	timeout time.Duration
	now     func() time.Time
	retry   RetryPolicy
}

// NewClient 构造通知客户端。所有渠道共用同一个 http.Client。
func NewClient(opts Options) *Client {
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: DefaultTimeout}
	}
	to := opts.Timeout
	if to <= 0 {
		to = DefaultTimeout
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	policy := DefaultRetryPolicy()
	if opts.Retry != nil {
		policy = *opts.Retry
	}
	return &Client{
		http:    hc,
		timeout: to,
		now:     nowFn,
		retry:   policy.normalized(),
	}
}

// RetryPolicy 返回当前生效的重试策略（含默认值补齐）。
func (c *Client) RetryPolicy() RetryPolicy {
	if c == nil {
		return DefaultRetryPolicy()
	}
	return c.retry
}

// Timeout 返回单渠道默认超时。
func (c *Client) Timeout() time.Duration {
	if c == nil || c.timeout <= 0 {
		return DefaultTimeout
	}
	return c.timeout
}

// Validate 校验渠道配置，提前拦住缺字段/非法取值的配置，错误信息为中文可读文本。
// 错误信息中不出现任何密钥明文。
func (c *Client) Validate(ch Channel) error {
	switch ch.Kind {
	case KindWebhook:
		return validateWebhook(ch)
	case KindDingTalk:
		return validateDingTalk(ch)
	case KindWeCom:
		return validateWeCom(ch)
	case KindFeishu:
		return validateFeishu(ch)
	case KindSlack:
		return validateSlack(ch)
	case KindTelegram:
		return validateTelegram(ch)
	case KindEmail:
		return validateEmail(ch)
	default:
		return fmt.Errorf("notify: 不支持的渠道类型 %q", string(ch.Kind))
	}
}

// Send 发送一条通知。会先校验配置；Enabled 字段由调用方决定（本方法不因 Enabled=false 拒绝）。
//
// 重试契约：只有 5xx 与超时（ErrorKind.Retryable）会按 RetryPolicy 指数退避重试，
// 最多 2 次；4xx、鉴权失败与业务错误（HTTP 200 + errcode≠0）立即返回，不重试。
// 返回的错误一定是 *SendError（或包装），用 AsSendError/KindOf 取分类。
func (c *Client) Send(ctx context.Context, ch Channel, msg Message) error {
	if c == nil {
		return &SendError{Kind: ErrorKindInternal, Message: "nil client"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.Validate(ch); err != nil {
		return wrapConfigError(ch, err)
	}

	policy := c.retry
	for attempt := 0; ; attempt++ {
		err := c.dispatch(ctx, ch, msg)
		if err == nil {
			return nil
		}
		// 调用方整体取消/超时后不再重试，直接返回最后一次错误。
		if ctx.Err() != nil {
			return err
		}
		if attempt >= policy.MaxRetries || !KindOf(err).Retryable() {
			return err
		}
		if serr := sleepCtx(ctx, policy.backoffDelay(attempt)); serr != nil {
			return err
		}
	}
}

// dispatch 按渠道类型分发单次发送（不含重试与配置校验）。
func (c *Client) dispatch(ctx context.Context, ch Channel, msg Message) error {
	switch ch.Kind {
	case KindWebhook:
		return c.sendWebhook(ctx, ch, msg)
	case KindDingTalk:
		return c.sendDingTalk(ctx, ch, msg)
	case KindWeCom:
		return c.sendWeCom(ctx, ch, msg)
	case KindFeishu:
		return c.sendFeishu(ctx, ch, msg)
	case KindSlack:
		return c.sendSlack(ctx, ch, msg)
	case KindTelegram:
		return c.sendTelegram(ctx, ch, msg)
	case KindEmail:
		return c.sendEmail(ctx, ch, msg)
	default:
		return wrapConfigError(ch, fmt.Errorf("不支持的渠道类型 %q", string(ch.Kind)))
	}
}

// withTimeout 在调用方 ctx 之上叠加渠道超时。
func (c *Client) withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		d = c.Timeout()
	}
	return context.WithTimeout(ctx, d)
}
