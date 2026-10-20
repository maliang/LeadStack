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

// buildFeishu 构造飞书文本消息请求；配置了 secret 时 timestamp/sign 放在 body 顶层。
func (c *Client) buildFeishu(ch Channel, msg Message) (httpRequest, error) {
	rawURL, _, _ := cfgString(ch.Config, "webhook")
	secret, hasSecret, _ := cfgString(ch.Config, "secret")
	scr := newScrubber(ch)

	payload := map[string]any{
		"msg_type": "text",
		"content":  map[string]any{"text": renderText(msg)},
	}
	if hasSecret {
		ts := c.now().Unix()
		payload["timestamp"] = strconv.FormatInt(ts, 10)
		payload["sign"] = feishuSign(secret, ts)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return httpRequest{}, newSendError(ch.Kind, ErrorKindInternal, 0, "", err.Error(), "")
	}
	return httpRequest{method: http.MethodPost, url: rawURL, body: body, timeout: c.Timeout(), scr: scr}, nil
}

// sendFeishu 发送飞书自定义机器人文本消息。
func (c *Client) sendFeishu(ctx context.Context, ch Channel, msg Message) error {
	req, err := c.buildFeishu(ch, msg)
	if err != nil {
		return err
	}
	res, err := c.deliver(ctx, ch.Kind, req)
	if err != nil {
		return err
	}
	return checkFeishuResult(ch.Kind, res, req.scr)
}

// checkFeishuResult 处理飞书的「HTTP 200 + code/StatusCode != 0 即失败」。
func checkFeishuResult(chKind Kind, res *httpResult, scr *scrubber) error {
	body := res.Body
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
	if msg == "" {
		msg = snippetOf(body, &scrubber{})
	}
	return newSendError(chKind, ErrorKindProvider, res.StatusCode, "code="+strconv.Itoa(code),
		scr.scrub(msg), snippetOf(body, scr))
}

// feishuProviderCode 从响应体提取飞书业务码（Probe 展示用）。
func feishuProviderCode(body []byte) string {
	var r struct {
		Code       *int `json:"code"`
		StatusCode *int `json:"StatusCode"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return ""
	}
	switch {
	case r.Code != nil:
		return "code=" + strconv.Itoa(*r.Code)
	case r.StatusCode != nil:
		return "code=" + strconv.Itoa(*r.StatusCode)
	}
	return ""
}

// errCodeOf 提取 errcode（钉钉/企微；Probe 展示用）。
func errCodeOf(body []byte) string {
	var r struct {
		ErrCode *int `json:"errcode"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.ErrCode == nil {
		return ""
	}
	return "errcode=" + strconv.Itoa(*r.ErrCode)
}
