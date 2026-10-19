package checker

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// maxBodyBytes 是 http 检测读取响应体的上限（1MB）：
// 既要能查 keyword，又不能被超大响应打爆内存。
const maxBodyBytes = 1 << 20

// methodRe 只允许 token 形式的方法名，避免把任意字符串塞进请求行。
var methodRe = regexp.MustCompile(`^[A-Za-z]{3,16}$`)

// probeHTTP 发起 HTTP(S) 检测。
//
// 口径（docs/ARCHITECTURE.md 第 13 节）：
//   - expect_status：默认 2xx，支持单值 / 区间 / Nxx；
//   - keyword：在响应体中查找（区分大小写）；
//   - insecure：跳过证书校验（用户显式选择）；
//   - 延迟统计到「响应体读完」为止，否则 keyword 检测的延迟没有意义；
//   - 跟随重定向（net/http 默认策略，最多 10 跳）。
func probeHTTP(ctx context.Context, spec Spec, timeout time.Duration) Result {
	target := strings.TrimSpace(spec.Target)
	if target == "" {
		return failResult(0, "HTTP 目标 URL 不能为空")
	}
	method := strings.ToUpper(strings.TrimSpace(paramString(spec.Params, "method")))
	if method == "" {
		method = http.MethodGet
	}
	if !methodRe.MatchString(method) {
		return failResult(0, "method 参数非法: %q", method)
	}
	minStatus, maxStatus := http.StatusOK, 299
	if raw, ok := spec.Params["expect_status"]; ok && raw != nil {
		lo, hi, err := parseExpectStatus(raw)
		if err != nil {
			return failResult(0, "expect_status 参数非法: %s", err.Error())
		}
		minStatus, maxStatus = lo, hi
	}
	keyword := paramString(spec.Params, "keyword")
	insecure := paramBool(spec.Params, "insecure")

	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return failResult(0, "URL 非法: %s", err.Error())
	}
	req.Header.Set("User-Agent", "LeadStack-Checker/1.0")

	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	if insecure {
		// 用户显式 skip 证书校验：仅用于自签证书的内网服务。
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	client := &http.Client{Timeout: timeout, Transport: transport}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return failResult(elapsedMS(start), "HTTP 请求失败: %s", dialErrMessage(err))
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	_ = resp.Body.Close()
	latency := elapsedMS(start) // 读完后才计时
	status := resp.StatusCode
	truncated := len(body) > maxBodyBytes
	if truncated {
		body = body[:maxBodyBytes]
	}
	if readErr != nil {
		if ctx.Err() != nil {
			return failResultStatus(latency, status, "读取响应体被取消: %s", ctx.Err().Error())
		}
		return failResultStatus(latency, status, "读取响应体失败: %s", readErr.Error())
	}

	if status < minStatus || status > maxStatus {
		return failResultStatus(latency, status, "HTTP 状态码 %d 不在期望范围 %s", status, statusRangeText(minStatus, maxStatus))
	}
	if keyword != "" && !strings.Contains(string(body), keyword) {
		return failResultStatus(latency, status, "响应体未包含关键字 %q%s", keyword, bodyReadNote(len(body), truncated))
	}
	note := ""
	if keyword != "" {
		note += fmt.Sprintf("，已匹配关键字 %q", keyword)
	}
	if final := resp.Request.URL.String(); final != target {
		note += fmt.Sprintf("（重定向至 %s）", final)
	}
	if truncated {
		note += "（响应体超过 1MB，仅检查前 1MB）"
	}
	return okResult(latency, status, "HTTP %d%s", status, note)
}

func statusRangeText(lo, hi int) string {
	if lo == hi {
		return fmt.Sprintf("%d", lo)
	}
	if lo%100 == 0 && hi == lo+99 {
		return fmt.Sprintf("%dxx", lo/100)
	}
	return fmt.Sprintf("%d-%d", lo, hi)
}

// bodyReadNote 说明 keyword 未匹配时实际检查了多少字节，便于判断是不是被上限截断。
func bodyReadNote(readBytes int, truncated bool) string {
	if truncated {
		return fmt.Sprintf("（已读取 %d 字节，超过 1MB 上限已截断）", readBytes)
	}
	return fmt.Sprintf("（已读取 %d 字节）", readBytes)
}
