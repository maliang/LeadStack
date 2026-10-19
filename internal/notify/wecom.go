package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// validateWeCom 校验企业微信机器人渠道配置（必填 webhook）。
func validateWeCom(ch Channel) error {
	raw, ok, err := cfgString(ch.Config, "webhook")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("notify: 企业微信渠道缺少必填配置 webhook")
	}
	return validateURL("webhook", raw)
}

// sendWeCom 发送企业微信机器人 markdown 消息。
func (c *Client) sendWeCom(ctx context.Context, ch Channel, msg Message) error {
	rawURL, _, _ := cfgString(ch.Config, "webhook")
	scr := newScrubber(ch)

	payload := map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]any{
			"content": renderText(msg),
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	respBody, err := c.do(ctx, http.MethodPost, rawURL, nil, body, c.Timeout(), scr, "企业微信")
	if err != nil {
		return err
	}
	return checkErrCode("企业微信", respBody, scr)
}
