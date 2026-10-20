package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// validateSlack 校验 Slack 渠道配置（必填 webhook，可选 channel）。
func validateSlack(ch Channel) error {
	raw, ok, err := cfgString(ch.Config, "webhook")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("notify: Slack 渠道缺少必填配置 webhook")
	}
	if err := validateURL("webhook", raw); err != nil {
		return err
	}
	if _, _, err := cfgString(ch.Config, "channel"); err != nil {
		return err
	}
	return nil
}

// buildSlack 构造 Slack Incoming Webhook 请求（text 为渲染后的纯文本）。
func (c *Client) buildSlack(ch Channel, msg Message) (httpRequest, error) {
	rawURL, _, _ := cfgString(ch.Config, "webhook")
	channel, hasChannel, _ := cfgString(ch.Config, "channel")
	scr := newScrubber(ch)

	payload := map[string]any{"text": renderText(msg)}
	if hasChannel {
		payload["channel"] = channel
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return httpRequest{}, newSendError(ch.Kind, ErrorKindInternal, 0, "", err.Error(), "")
	}
	return httpRequest{method: http.MethodPost, url: rawURL, body: body, timeout: c.Timeout(), scr: scr}, nil
}

// sendSlack 发送 Slack 消息。
func (c *Client) sendSlack(ctx context.Context, ch Channel, msg Message) error {
	req, err := c.buildSlack(ch, msg)
	if err != nil {
		return err
	}
	_, err = c.deliver(ctx, ch.Kind, req)
	return err
}
