package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// defaultTelegramAPIBase 是 telegram 的正式默认 API 根地址；api_base 可覆盖（测试与代理用）。
const defaultTelegramAPIBase = "https://api.telegram.org"

// telegramEndpoint 组装 sendMessage 地址；base 为空时使用默认 api_base。
func telegramEndpoint(base, token string) string {
	if strings.TrimSpace(base) == "" {
		base = defaultTelegramAPIBase
	}
	return strings.TrimRight(base, "/") + "/bot" + token + "/sendMessage"
}

// validateTelegram 校验 telegram 渠道配置（必填 bot_token、chat_id）。
func validateTelegram(ch Channel) error {
	if _, ok, err := cfgString(ch.Config, "bot_token"); err != nil {
		return err
	} else if !ok {
		return errors.New("notify: telegram 渠道缺少必填配置 bot_token")
	}
	if _, ok, err := cfgText(ch.Config, "chat_id"); err != nil {
		return err
	} else if !ok {
		return errors.New("notify: telegram 渠道缺少必填配置 chat_id")
	}
	if _, _, err := cfgString(ch.Config, "parse_mode"); err != nil {
		return err
	}
	if base, ok, err := cfgString(ch.Config, "api_base"); err != nil {
		return err
	} else if ok {
		if err := validateURL("api_base", base); err != nil {
			return err
		}
	}
	return nil
}

// buildTelegram 构造 sendMessage 请求。
func (c *Client) buildTelegram(ch Channel, msg Message) (httpRequest, error) {
	token, _, _ := cfgString(ch.Config, "bot_token")
	chatID, _, _ := cfgText(ch.Config, "chat_id")
	parseMode, hasParseMode, _ := cfgString(ch.Config, "parse_mode")
	base, hasBase, _ := cfgString(ch.Config, "api_base")
	if !hasBase {
		base = defaultTelegramAPIBase
	}
	scr := newScrubber(ch)

	payload := map[string]any{
		"chat_id": chatID,
		"text":    renderText(msg),
	}
	if hasParseMode {
		payload["parse_mode"] = parseMode
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return httpRequest{}, newSendError(ch.Kind, ErrorKindInternal, 0, "", err.Error(), "")
	}
	endpoint := telegramEndpoint(base, token)
	scr.addURL(endpoint) // token 在 URL 路径里，必须整条替换才能安全脱敏
	return httpRequest{method: http.MethodPost, url: endpoint, body: body, timeout: c.Timeout(), scr: scr}, nil
}

// sendTelegram 调用 sendMessage API 发送文本消息。
func (c *Client) sendTelegram(ctx context.Context, ch Channel, msg Message) error {
	req, err := c.buildTelegram(ch, msg)
	if err != nil {
		return err
	}
	res, err := c.deliver(ctx, ch.Kind, req)
	if err != nil {
		return err
	}
	return checkTelegramResult(ch.Kind, res, req.scr)
}

// checkTelegramResult 处理 telegram 的 {"ok":false,...}。
func checkTelegramResult(chKind Kind, res *httpResult, scr *scrubber) error {
	body := res.Body
	var r struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil
	}
	if r.OK {
		return nil
	}
	desc := strings.TrimSpace(r.Description)
	if desc == "" {
		desc = "未知错误"
	}
	return newSendError(chKind, ErrorKindProvider, res.StatusCode,
		fmt.Sprintf("error_code=%d", r.ErrorCode), scr.scrub(desc), snippetOf(body, scr))
}

// telegramProviderCode 提取 telegram 的 error_code（Probe 展示用）。
func telegramProviderCode(body []byte) string {
	var r struct {
		ErrorCode int `json:"error_code"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.ErrorCode == 0 {
		return ""
	}
	return "error_code=" + strconv.Itoa(r.ErrorCode)
}
