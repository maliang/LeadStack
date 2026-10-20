package notify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// validateURL 校验 http/https 地址；错误信息只出现配置项名，不回显地址（可能含 token）。
func validateURL(key, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("notify: 配置项 %s 必须是合法的 http/https 地址", key)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("notify: 配置项 %s 只支持 http/https", key)
	}
	return nil
}

// validateWebhook 校验 webhook 渠道配置（必填 url）。
func validateWebhook(ch Channel) error {
	raw, ok, err := cfgString(ch.Config, "url")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("notify: webhook 渠道缺少必填配置 url")
	}
	if err := validateURL("url", raw); err != nil {
		return err
	}
	if method, ok, err := cfgString(ch.Config, "method"); err != nil {
		return err
	} else if ok {
		switch strings.ToUpper(method) {
		case http.MethodPost, http.MethodGet, http.MethodPut, http.MethodPatch:
		default:
			return fmt.Errorf("notify: webhook 不支持 method=%s（可用 POST/GET/PUT/PATCH）", strings.ToUpper(method))
		}
	}
	if tpl, ok, err := cfgString(ch.Config, "template"); err != nil {
		return err
	} else if ok && tpl != "json" && tpl != "text" {
		return fmt.Errorf("notify: webhook template 只支持 json 或 text，收到 %s", tpl)
	}
	if _, err := cfgStringMap(ch.Config, "headers"); err != nil {
		return err
	}
	if _, err := cfgInt(ch.Config, "timeout_s", 0); err != nil {
		return err
	}
	return nil
}

// buildWebhook 构造通用 webhook 请求：默认 POST，支持自定义 method/headers 与 json/text 模板。
func (c *Client) buildWebhook(ch Channel, msg Message) (httpRequest, error) {
	rawURL, _, _ := cfgString(ch.Config, "url")
	method, ok, _ := cfgString(ch.Config, "method")
	if !ok {
		method = http.MethodPost
	} else {
		method = strings.ToUpper(method)
	}
	tpl, ok, _ := cfgString(ch.Config, "template")
	if !ok {
		tpl = "json"
	}
	headers, _ := cfgStringMap(ch.Config, "headers")
	if headers == nil {
		headers = map[string]string{}
	}
	timeout, _ := cfgSeconds(ch.Config, "timeout_s", c.Timeout())
	scr := newScrubber(ch)

	target := rawURL
	var body []byte
	var err error
	if tpl == "text" {
		body = []byte(renderText(msg))
		if !hasHeader(headers, "Content-Type") {
			headers["Content-Type"] = "text/plain; charset=utf-8"
		}
	} else {
		body, err = renderJSON(msg)
		if err != nil {
			return httpRequest{}, newSendError(ch.Kind, ErrorKindInternal, 0, "", err.Error(), "")
		}
	}
	if method == http.MethodGet {
		// GET 不带 body，把渲染结果放到查询参数里。
		target, err = withQuery(rawURL, map[string]string{
			"title":    titleOr(msg.Title, "LeadStack 告警"),
			"text":     renderText(msg),
			"severity": string(msg.Severity),
			"status":   statusValue(msg.Recovered),
		})
		if err != nil {
			return httpRequest{}, newSendError(ch.Kind, ErrorKindConfig, 0, "", err.Error(), "")
		}
		body = nil
	}
	scr.addURL(target)
	return httpRequest{method: method, url: target, headers: headers, body: body, timeout: timeout, scr: scr}, nil
}

// sendWebhook 发送通用 webhook。
func (c *Client) sendWebhook(ctx context.Context, ch Channel, msg Message) error {
	req, err := c.buildWebhook(ch, msg)
	if err != nil {
		return err
	}
	_, err = c.deliver(ctx, ch.Kind, req)
	return err
}

// hasHeader 判断用户是否显式设置了某个头（不区分大小写）。
func hasHeader(headers map[string]string, key string) bool {
	for k := range headers {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}
