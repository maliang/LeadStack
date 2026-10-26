package checker

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// 本文件集中处理 Spec.Params（来自 JSON 反序列化的 map[string]any）的取值与校验。
// 参数可能来自 HTTP 请求，类型不可信，一律做宽松解析 + 严格校验。

// paramString 取字符串参数；数字/布尔统一转成文本，便于 expect_status、min_count 之类
// 参数既能收到 200 也能收到 "200"。
func paramString(p map[string]any, key string) string {
	if p == nil {
		return ""
	}
	v, ok := p[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case []byte:
		return string(t)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// probeViaOf 返回 params.probe_via：只有显式 "agent" 才走目标机代检，
// 缺省与非法值都按控制端直连处理（非法值由 API 层拒绝，这里只做兜底）。
func probeViaOf(p map[string]any) string {
	if strings.EqualFold(strings.TrimSpace(paramString(p, "probe_via")), ProbeViaAgent) {
		return ProbeViaAgent
	}
	return ProbeViaControl
}

// paramBool 取布尔参数；缺省或类型非法时返回 false。
func paramBool(p map[string]any, key string) bool {
	if p == nil {
		return false
	}
	v, ok := p[key]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(t))
		return err == nil && b
	case float64:
		return t != 0
	case float32:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	case json.Number:
		f, err := t.Float64()
		return err == nil && f != 0
	default:
		return false
	}
}

// paramInt 取整数参数，缺失或非法时返回 def。
func paramInt(p map[string]any, key string, def int) int {
	if p == nil {
		return def
	}
	v, ok := p[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case float32:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return def
		}
		return int(n)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return def
		}
		return n
	default:
		return def
	}
}

// parseExpectStatus 解析 http 的 params.expect_status，支持三种写法：
//
//	200        —— 单值
//	200-299    —— 闭区间
//	2xx        —— 整段，等价于 200-299
//
// 也接受数字类型（JSON 里 200 会解析成 float64）。
func parseExpectStatus(raw any) (int, int, error) {
	const minCode, maxCode = 100, 599
	switch t := raw.(type) {
	case float64:
		return rangeOfInt(int(t), minCode, maxCode)
	case float32:
		return rangeOfInt(int(t), minCode, maxCode)
	case int:
		return rangeOfInt(t, minCode, maxCode)
	case int64:
		return rangeOfInt(int(t), minCode, maxCode)
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return 0, 0, fmt.Errorf("expect_status 不是整数: %q", t.String())
		}
		return rangeOfInt(int(n), minCode, maxCode)
	case string:
		return parseExpectStatusString(t, minCode, maxCode)
	case nil:
		return 200, 299, nil
	default:
		return 0, 0, fmt.Errorf("expect_status 类型不支持: %T", raw)
	}
}

func parseExpectStatusString(s string, minCode, maxCode int) (int, int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 200, 299, nil
	}
	// 2xx / 3xx 形式
	if len(s) == 3 && strings.HasSuffix(s, "xx") {
		d, err := strconv.Atoi(s[:1])
		if err == nil && d >= 1 && d <= 5 {
			return d * 100, d*100 + 99, nil
		}
		return 0, 0, fmt.Errorf("无法解析 expect_status %q（支持 200、200-299、2xx）", s)
	}
	if i := strings.IndexByte(s, '-'); i > 0 {
		lo, err1 := strconv.Atoi(strings.TrimSpace(s[:i]))
		hi, err2 := strconv.Atoi(strings.TrimSpace(s[i+1:]))
		if err1 != nil || err2 != nil {
			return 0, 0, fmt.Errorf("无法解析 expect_status %q（支持 200、200-299、2xx）", s)
		}
		if lo > hi {
			return 0, 0, fmt.Errorf("expect_status 区间非法: %d-%d", lo, hi)
		}
		if err := checkCode(lo, minCode, maxCode); err != nil {
			return 0, 0, err
		}
		if err := checkCode(hi, minCode, maxCode); err != nil {
			return 0, 0, err
		}
		return lo, hi, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, 0, fmt.Errorf("无法解析 expect_status %q（支持 200、200-299、2xx）", s)
	}
	return rangeOfInt(n, minCode, maxCode)
}

// rangeOfInt 处理单值状态码：返回 [n, n]。
func rangeOfInt(n, minCode, maxCode int) (int, int, error) {
	if err := checkCode(n, minCode, maxCode); err != nil {
		return 0, 0, err
	}
	return n, n, nil
}

func checkCode(n, minCode, maxCode int) error {
	if n < minCode || n > maxCode {
		return fmt.Errorf("expect_status 状态码超出范围: %d", n)
	}
	return nil
}
