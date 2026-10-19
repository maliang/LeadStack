package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// errorBodyLimit 是非 2xx 响应体进入错误信息前的截断长度（契约：512 字节）。
	errorBodyLimit = 512
	// successBodyReadLimit 是成功响应体的最大读取量（防止对端返回超大 body 拉爆内存）。
	successBodyReadLimit = 64 << 10
)

// truncateBytes 按字节截断（不切开 UTF-8 字符），返回截断结果与是否发生截断。
func truncateBytes(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	cut := s[:n]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut, true
}

// do 发送一次 HTTP 请求并返回响应体。
//   - method 为空时用 POST；
//   - headers 覆盖默认头；body 非 nil 时默认 Content-Type: application/json；
//   - 非 2xx 返回错误，响应体截断到 512 字节；
//   - 所有错误文本都经过 scr 清洗，绝不泄漏密钥。
func (c *Client) do(ctx context.Context, method, rawURL string, headers map[string]string, body []byte, timeout time.Duration, scr *scrubber, label string) ([]byte, error) {
	if method == "" {
		method = http.MethodPost
	}
	ctx, cancel := c.withTimeout(ctx, timeout)
	defer cancel()

	var rdr io.Reader
	if len(body) > 0 {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		// newRequest 的错误里可能带完整 URL，必须清洗。
		return nil, fmt.Errorf("notify: %s 构造请求失败: %s", label, scr.scrub(err.Error()))
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if len(body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "LeadStack-Notify/1.0")

	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error 会带上完整 URL（telegram token 在路径里），必须清洗。
		return nil, fmt.Errorf("notify: %s 请求失败: %s", label, scr.scrub(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, successBodyReadLimit))
	if readErr != nil {
		return nil, fmt.Errorf("notify: %s 读取响应失败: %s", label, scr.scrub(readErr.Error()))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		detail, truncated := truncateBytes(string(raw), errorBodyLimit)
		detail = strings.TrimSpace(detail)
		if truncated {
			detail += "…（已截断）"
		}
		if detail == "" {
			return nil, fmt.Errorf("notify: %s 返回 HTTP %d", label, resp.StatusCode)
		}
		return nil, fmt.Errorf("notify: %s 返回 HTTP %d: %s", label, resp.StatusCode, scr.scrub(detail))
	}
	return raw, nil
}

// checkErrCode 处理「HTTP 200 + errcode != 0 即业务失败」的响应（钉钉/企微）。
func checkErrCode(label string, body []byte, scr *scrubber) error {
	var r struct {
		ErrCode *int   `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.ErrCode == nil {
		return nil // 无法解析时不阻断（对方可能返回纯文本）
	}
	if *r.ErrCode == 0 {
		return nil
	}
	detail, _ := truncateBytes(strings.TrimSpace(r.ErrMsg), errorBodyLimit)
	if detail == "" {
		b, _ := truncateBytes(strings.TrimSpace(string(body)), errorBodyLimit)
		detail = b
	}
	return fmt.Errorf("notify: %s 返回业务错误 errcode=%d: %s", label, *r.ErrCode, scr.scrub(detail))
}

// withQuery 在 rawURL 上叠加查询参数（用于 webhook 的 GET 模板），值会被标准 urlencode。
func withQuery(rawURL string, params map[string]string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("notify: 地址无法解析")
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// appendRawQuery 在原查询串之后追加键值对，只对键做 urlencode、值必须由调用方保证已编码。
// 用于钉钉加签：官方示例把 URLEncoder.encode 后的 sign 直接拼进 URL，不能二次编码。
func appendRawQuery(rawURL string, kv ...string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("notify: 地址无法解析")
	}
	if len(kv)%2 != 0 {
		return "", fmt.Errorf("notify: 内部错误：查询参数不成对")
	}
	var b strings.Builder
	b.WriteString(u.RawQuery)
	for i := 0; i+1 < len(kv); i += 2 {
		if b.Len() > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(kv[i]))
		b.WriteByte('=')
		b.WriteString(kv[i+1])
	}
	u.RawQuery = b.String()
	return u.String(), nil
}
