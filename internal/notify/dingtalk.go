package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// validateDingTalk 校验钉钉渠道配置（必填 webhook，可选 secret）。
func validateDingTalk(ch Channel) error {
	raw, ok, err := cfgString(ch.Config, "webhook")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("notify: 钉钉渠道缺少必填配置 webhook")
	}
	if err := validateURL("webhook", raw); err != nil {
		return err
	}
	if _, _, err := cfgString(ch.Config, "secret"); err != nil {
		return err
	}
	return nil
}

// sendDingTalk 发送钉钉机器人 markdown 消息；配置了 secret 时按官方算法加签。
func (c *Client) sendDingTalk(ctx context.Context, ch Channel, msg Message) error {
	rawURL, _, _ := cfgString(ch.Config, "webhook")
	secret, hasSecret, _ := cfgString(ch.Config, "secret")
	scr := newScrubber(ch)

	target := rawURL
	if hasSecret {
		ts := c.now().UnixMilli()
		// 钉钉官方要求 sign 先 urlencode 再拼进 URL。这里手工拼接（而不是 url.Values.Encode），
		// 避免二次编码：直接 Encode 会把已编码的 %2B/%3D 再变成 %252B/%253D，服务端解码后
		// 得到错误的 base64，导致签名校验失败。
		t, err := appendRawQuery(rawURL, "timestamp", strconv.FormatInt(ts, 10), "sign", dingTalkSign(secret, ts))
		if err != nil {
			return err
		}
		target = t
	}

	payload := map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]any{
			"title": titleOr(msg.Title, "LeadStack 告警"),
			"text":  renderText(msg),
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	scr.addURL(target) // 加签后的完整 URL（含 access_token）精确脱敏
	respBody, err := c.do(ctx, http.MethodPost, target, nil, body, c.Timeout(), scr, "钉钉")
	if err != nil {
		return err
	}
	return checkErrCode("钉钉", respBody, scr)
}
