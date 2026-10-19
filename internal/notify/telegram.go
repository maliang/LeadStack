package notify

import (
	"context"
	"encoding/json"
	"errors"
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

// sendTelegram 调用 sendMessage API 发送文本消息。
func (c *Client) sendTelegram(ctx context.Context, ch Channel, msg Message) error {
	token, _, _ := cfgString(ch.Config, "bot_token")
	chatID, _, _ := cfgText(ch.Config, "chat_id")
	parseMode, hasParseMode, _ := cfgString(ch.Config, "parse_mode")
	base, hasBase, _ := cfgString(ch.Config, "api_base")
	if !hasBase {
		base = defaultTelegramAPIBase
	}
	base = strings.TrimRight(base, "/")
	scr := newScrubber(ch)

	endpoint := telegramEndpoint(base, token)
	payload := map[string]any{
		"chat_id": chatID,
		"text":    renderText(msg),
	}
	if hasParseMode {
		payload["parse_mode"] = parseMode
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	scr.addURL(endpoint) // token 在 URL 路径里，必须整条替换才能安全脱敏
	respBody, err := c.do(ctx, http.MethodPost, endpoint, nil, body, c.Timeout(), scr, "telegram")
	if err != nil {
		return err
	}
	return checkTelegramResult(respBody, scr)
}

// checkTelegramResult 处理 telegram 的 {"ok":false,...}。
func checkTelegramResult(body []byte, scr *scrubber) error {
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
	desc, _ := truncateBytes(strings.TrimSpace(r.Description), errorBodyLimit)
	if desc == "" {
		desc = "未知错误"
	}
	return errors.New("notify: telegram 返回失败 error_code=" + strconv.Itoa(r.ErrorCode) + ": " + scr.scrub(desc))
}
