package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// validateFeishu 校验飞书自定义机器人渠道配置（必填 webhook，可选 secret）。
func validateFeishu(ch Channel) error {
	raw, ok, err := cfgString(ch.Config, "webhook")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("notify: 飞书渠道缺少必填配置 webhook")
	}
	if err := validateURL("webhook", raw); err != nil {
		return err
	}
	if _, _, err := cfgString(ch.Config, "secret"); err != nil {
		return err
	}
	return nil
}

// sendFeishu 发送飞书自定义机器人文本消息；配置了 secret 时按官方算法加签。
// 加签字段 timestamp/sign 放在请求体顶层（timestamp 为秒级字符串）。
func (c *Client) sendFeishu(ctx context.Context, ch Channel, msg Message) error {
	rawURL, _, _ := cfgString(ch.Config, "webhook")
	secret, hasSecret, _ := cfgString(ch.Config, "secret")
	scr := newScrubber(ch)

	payload := map[string]any{
		"msg_type": "text",
		"content": map[string]any{
			"text": renderText(msg),
		},
	}
	if hasSecret {
		ts := c.now().Unix()
		payload["timestamp"] = strconv.FormatInt(ts, 10)
		payload["sign"] = feishuSign(secret, ts)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	respBody, err := c.do(ctx, http.MethodPost, rawURL, nil, body, c.Timeout(), scr, "飞书")
	if err != nil {
		return err
	}
	return checkFeishuResult(respBody, scr)
}

// checkFeishuResult 处理飞书的「HTTP 200 + code/StatusCode != 0 即失败」。
func checkFeishuResult(body []byte, scr *scrubber) error {
	var r struct {
		Code          *int   `json:"code"`
		Msg           string `json:"msg"`
		StatusCode    *int   `json:"StatusCode"`
		StatusMessage string `json:"StatusMessage"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil
	}
	code, msg := 0, ""
	switch {
	case r.Code != nil:
		code, msg = *r.Code, r.Msg
	case r.StatusCode != nil:
		code, msg = *r.StatusCode, r.StatusMessage
	}
	if code == 0 {
		return nil
	}
	detail, _ := truncateBytes(msg, errorBodyLimit)
	if detail == "" {
		detail, _ = truncateBytes(string(body), errorBodyLimit)
	}
	return errors.New("notify: 飞书返回业务错误 code=" + strconv.Itoa(code) + ": " + scr.scrub(detail))
}
