package notify

import (
	"context"
	"fmt"
)

// ProbeResult 是一次真实端点探测的结果。
// Reachable=true 表示收到了服务商层面的响应（HTTP 任意状态码 / SMTP 回复），
// 说明地址、协议与报文格式被对方解析；此时即使状态码是 401/404、业务码非 0，
// 也属于「发得出去、格式对」。
type ProbeResult struct {
	Kind            Kind      // 渠道类型
	Reachable       bool      // 是否收到了服务商层面的响应
	StatusCode      int       // HTTP 状态码；邮件渠道为 0
	ProviderCode    string    // 服务商业务码，如 "errcode=300005" / "SMTP 503"
	ResponseSnippet string    // 已脱敏、截断到 512 字节的响应片段
	ErrorKind       ErrorKind // 传输层失败时的分类；成功时为空
}

// probeMessage 是探测时发送的中性消息（不含真实告警内容）。
func probeMessage() Message {
	return Message{
		Title:    "LeadStack channel probe",
		Body:     "Connectivity probe from LeadStack. Fake credentials are expected to be rejected by the provider.",
		Severity: SeverityInfo,
		TS:       1,
	}
}

// Probe 用真实端点做一次「只验证连通性与报文格式」的探测，不会真正投递告警。
//
// 行为：
//   - 与 Send 走完全相同的请求构造（同 URL、方法、请求头、JSON body、加签），
//     因此能证明「发得出去、格式对」；
//   - 收到任意 HTTP 响应（含 4xx/5xx）或任意 SMTP 回复都算 Reachable=true，返回 nil error；
//   - 只有 DNS 失败 / 连接失败 / TLS 失败 / 超时 / 取消，或配置不合法时才返回 error，
//     并用 *SendError 给出分类；
//   - 探测不做重试（重试策略只作用于 Send）。
func (c *Client) Probe(ctx context.Context, ch Channel) (ProbeResult, error) {
	if c == nil {
		return ProbeResult{}, &SendError{Kind: ErrorKindInternal, Message: "nil client"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.Validate(ch); err != nil {
		return ProbeResult{Kind: ch.Kind, ErrorKind: ErrorKindConfig}, wrapConfigError(ch, err)
	}
	if ch.Kind == KindEmail {
		return c.probeEmail(ctx, ch)
	}

	msg := probeMessage()
	var (
		req httpRequest
		err error
	)
	switch ch.Kind {
	case KindWebhook:
		req, err = c.buildWebhook(ch, msg)
	case KindDingTalk:
		req, err = c.buildDingTalk(ch, msg)
	case KindWeCom:
		req, err = c.buildWeCom(ch, msg)
	case KindFeishu:
		req, err = c.buildFeishu(ch, msg)
	case KindSlack:
		req, err = c.buildSlack(ch, msg)
	case KindTelegram:
		req, err = c.buildTelegram(ch, msg)
	default:
		return ProbeResult{Kind: ch.Kind, ErrorKind: ErrorKindConfig},
			wrapConfigError(ch, fmt.Errorf("不支持的渠道类型 %q", string(ch.Kind)))
	}
	if err != nil {
		return ProbeResult{Kind: ch.Kind, ErrorKind: KindOf(err)}, err
	}

	res, err := c.do(ctx, req.method, req.url, req.headers, req.body, req.timeout, req.scr, ch.Kind)
	if err != nil {
		return ProbeResult{Kind: ch.Kind, ErrorKind: KindOf(err)}, err
	}
	return ProbeResult{
		Kind:            ch.Kind,
		Reachable:       true,
		StatusCode:      res.StatusCode,
		ProviderCode:    providerCodeOf(ch.Kind, res.Body),
		ResponseSnippet: snippetOf(res.Body, req.scr),
	}, nil
}

// providerCodeOf 从响应体里提取服务商业务码（用于探测报告）。
func providerCodeOf(k Kind, body []byte) string {
	switch k {
	case KindDingTalk, KindWeCom:
		return errCodeOf(body)
	case KindFeishu:
		return feishuProviderCode(body)
	case KindTelegram:
		return telegramProviderCode(body)
	default:
		return ""
	}
}
