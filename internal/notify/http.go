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

// httpRequest 描述一次待发送的 HTTP 请求（渠道实现与 Probe 共用）。
type httpRequest struct {
	method  string
	url     string
	headers map[string]string
	body    []byte
	timeout time.Duration
	scr     *scrubber
}

// httpResult 是一次 HTTP 调用的原始结果（不判断业务语义）。
type httpResult struct {
	StatusCode int
	Body       []byte
}

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

// snippetOf 返回已脱敏并截断到 512 字节的响应片段，可安全写入错误/日志。
func snippetOf(body []byte, scr *scrubber) string {
	cut, truncated := truncateBytes(string(body), errorBodyLimit)
	cut = strings.TrimSpace(cut)
	if truncated {
		cut += "…（已截断）"
	}
	return scr.scrub(cut)
}

// do 执行一次 HTTP 请求，只负责传输层：
//   - 返回 *httpResult（含状态码与响应体），非 2xx 不在此处判错；
//   - 传输层失败返回带分类的 *SendError（DNS/连接/TLS/超时/取消）；
//   - 所有错误文本与响应片段都经过 scr 清洗，绝不泄漏密钥。
func (c *Client) do(ctx context.Context, method, rawURL string, headers map[string]string, body []byte, timeout time.Duration, scr *scrubber, chKind Kind) (*httpResult, error) {
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
		return nil, newSendError(chKind, ErrorKindConfig, 0, "",
			"构造请求失败: "+scr.scrub(err.Error()), "")
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
		kind := classifyTransport(ctx, err)
		return nil, newSendError(chKind, kind, 0, "",
			"请求失败: "+scr.scrub(err.Error()), "")
	}
	defer func() { _ = resp.Body.Close() }()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, successBodyReadLimit))
	if readErr != nil {
		kind := classifyTransport(ctx, readErr)
		if kind == "" {
			kind = ErrorKindConnect
		}
		return nil, newSendError(chKind, kind, 0, "",
			"读取响应失败: "+scr.scrub(readErr.Error()), "")
	}
	return &httpResult{StatusCode: resp.StatusCode, Body: raw}, nil
}

// statusError 把非 2xx 状态码转成分类错误（401/403 → 凭据被拒绝，5xx → 可重试）。
func statusError(chKind Kind, res *httpResult, scr *scrubber) error {
	if res == nil || (res.StatusCode >= 200 && res.StatusCode <= 299) {
		return nil
	}
	kind := ErrorKindHTTP
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		kind = ErrorKindAuth
	case res.StatusCode >= 500:
		kind = ErrorKindServer
	}
	snippet := snippetOf(res.Body, scr)
	// 状态码由 SendError.Error() 统一渲染（"HTTP 401"），这里只保留响应片段，避免重复。
	return newSendError(chKind, kind, res.StatusCode, "", snippet, snippet)
}

// deliver 发送一次 HTTP 请求并做状态码判断，返回原始结果供业务错误检查。
func (c *Client) deliver(ctx context.Context, chKind Kind, req httpRequest) (*httpResult, error) {
	res, err := c.do(ctx, req.method, req.url, req.headers, req.body, req.timeout, req.scr, chKind)
	if err != nil {
		return nil, err
	}
	if err := statusError(chKind, res, req.scr); err != nil {
		return nil, err
	}
	return res, nil
}

// checkErrCode 处理「HTTP 200 + errcode != 0 即业务失败」的响应（钉钉/企微）。
func checkErrCode(chKind Kind, res *httpResult, scr *scrubber) error {
	body := res.Body
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
	msg := strings.TrimSpace(r.ErrMsg)
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	snippet := snippetOf(body, scr)
	return newSendError(chKind, ErrorKindProvider, res.StatusCode,
		fmt.Sprintf("errcode=%d", *r.ErrCode), scr.scrub(msg), snippet)
}

// withQuery 在 rawURL 上叠加查询参数（用于 webhook 的 GET 模板），值会被标准 urlencode。
func withQuery(rawURL string, params map[string]string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("地址无法解析")
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
		return "", fmt.Errorf("地址无法解析")
	}
	if len(kv)%2 != 0 {
		return "", fmt.Errorf("内部错误：查询参数不成对")
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
