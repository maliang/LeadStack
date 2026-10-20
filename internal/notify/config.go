package notify

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 本文件提供渠道 Config（map[string]any，来自 JSON 反序列化）的取值辅助。
// 纪律：所有错误信息只允许出现「配置项名」，绝不允许带上配置值（可能是密钥）。

// cfgRaw 取原始配置值；nil 与缺失视为不存在。
func cfgRaw(cfg map[string]any, key string) (any, bool) {
	if cfg == nil {
		return nil, false
	}
	v, ok := cfg[key]
	if !ok || v == nil {
		return nil, false
	}
	return v, true
}

// cfgString 取非空字符串配置。返回 (值, 是否存在, 错误)。
func cfgString(cfg map[string]any, key string) (string, bool, error) {
	v, ok := cfgRaw(cfg, key)
	if !ok {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", false, fmt.Errorf("notify: 配置项 %s 必须是字符串", key)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false, nil
	}
	return s, true, nil
}

// cfgText 取标量文本配置，数字类型也接受（例如 telegram 的 chat_id 可能是数字）。
func cfgText(cfg map[string]any, key string) (string, bool, error) {
	v, ok := cfgRaw(cfg, key)
	if !ok {
		return "", false, nil
	}
	switch n := v.(type) {
	case string:
		s := strings.TrimSpace(n)
		if s == "" {
			return "", false, nil
		}
		return s, true, nil
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64), true, nil
	case int:
		return strconv.Itoa(n), true, nil
	case int64:
		return strconv.FormatInt(n, 10), true, nil
	case json.Number:
		return n.String(), true, nil
	default:
		return "", false, fmt.Errorf("notify: 配置项 %s 必须是字符串或数字", key)
	}
}

// cfgInt 取整数配置，缺失时返回 def。
func cfgInt(cfg map[string]any, key string, def int) (int, error) {
	v, ok := cfgRaw(cfg, key)
	if !ok {
		return def, nil
	}
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		if n != float64(int(n)) {
			return 0, fmt.Errorf("notify: 配置项 %s 必须是整数", key)
		}
		return int(n), nil
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, fmt.Errorf("notify: 配置项 %s 必须是整数", key)
		}
		return int(i), nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0, fmt.Errorf("notify: 配置项 %s 必须是整数", key)
		}
		return i, nil
	default:
		return 0, fmt.Errorf("notify: 配置项 %s 必须是整数", key)
	}
}

// cfgBool 取布尔配置；接受 bool 与 "true"/"false" 字符串。
func cfgBool(cfg map[string]any, key string, def bool) (bool, error) {
	v, ok := cfgRaw(cfg, key)
	if !ok {
		return def, nil
	}
	switch b := v.(type) {
	case bool:
		return b, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(b)) {
		case "true", "1", "yes", "on":
			return true, nil
		case "false", "0", "no", "off":
			return false, nil
		}
		return false, fmt.Errorf("notify: 配置项 %s 必须是布尔值", key)
	default:
		return false, fmt.Errorf("notify: 配置项 %s 必须是布尔值", key)
	}
}

// cfgSeconds 取秒数配置（<=0 或缺失时返回 def）。
func cfgSeconds(cfg map[string]any, key string, def time.Duration) (time.Duration, error) {
	if _, ok := cfgRaw(cfg, key); !ok {
		return def, nil
	}
	secs, err := cfgInt(cfg, key, 0)
	if err != nil {
		return def, err
	}
	if secs <= 0 {
		return def, nil
	}
	return time.Duration(secs) * time.Second, nil
}

// cfgStringSlice 取字符串数组配置；也接受单个字符串（按逗号切分），方便手填。
func cfgStringSlice(cfg map[string]any, key string) ([]string, error) {
	v, ok := cfgRaw(cfg, key)
	if !ok {
		return nil, nil
	}
	switch t := v.(type) {
	case string:
		return splitList(t), nil
	case []string:
		return cleanList(t), nil
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("notify: 配置项 %s 的元素必须是字符串", key)
			}
			out = append(out, strings.TrimSpace(s))
		}
		if len(out) == 0 {
			return nil, nil
		}
		return cleanList(out), nil
	default:
		return nil, fmt.Errorf("notify: 配置项 %s 必须是字符串数组", key)
	}
}

// cfgStringMap 取 map[string]string 配置（如 webhook 的 headers）。
func cfgStringMap(cfg map[string]any, key string) (map[string]string, error) {
	v, ok := cfgRaw(cfg, key)
	if !ok {
		return nil, nil
	}
	switch t := v.(type) {
	case map[string]string:
		return t, nil
	case map[string]any:
		out := make(map[string]string, len(t))
		for k, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("notify: 配置项 %s 的 %s 必须是字符串", key, k)
			}
			out[strings.TrimSpace(k)] = s
		}
		return out, nil
	default:
		return nil, fmt.Errorf("notify: 配置项 %s 必须是键值对", key)
	}
}

func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
