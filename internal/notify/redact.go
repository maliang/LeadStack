package notify

import (
	"net/url"
	"sort"
	"strings"
)

// 本文件是安全红线：任何进入错误信息或日志的文本都必须先经过这里的清洗。

const (
	// redactKeep 是脱敏时保留的明文字符数。
	redactKeep = 4
	// redactedSuffix 是脱敏后缀。
	redactedSuffix = "***"
)

// sensitiveKeyParts 是「键名中包含即视为密钥」的片段（大小写不敏感）。
// 覆盖冻结契约里的 secret/password/bot_token，以及 webhook URL 里的 access_token/key 段。
var sensitiveKeyParts = []string{"secret", "password", "passwd", "pwd", "token", "key"}

// isSensitiveKey 报告配置键名/查询参数名是否属于密钥类字段。
func isSensitiveKey(key string) bool {
	lk := strings.ToLower(strings.TrimSpace(key))
	for _, p := range sensitiveKeyParts {
		if strings.Contains(lk, p) {
			return true
		}
	}
	return false
}

// sensitiveHeaderParts 是「HTTP 头名中包含即视为携带凭证」的片段。
var sensitiveHeaderParts = []string{
	"authorization", "auth", "key", "token", "secret", "cookie", "password", "credential",
}

// isSensitiveHeader 报告 HTTP 头名是否通常承载凭证（Authorization / X-Api-Key 等）。
func isSensitiveHeader(name string) bool {
	ln := strings.ToLower(strings.TrimSpace(name))
	for _, p := range sensitiveHeaderParts {
		if strings.Contains(ln, p) {
			return true
		}
	}
	return false
}

// Redact 脱敏：保留前 4 位 + "***"；长度不足 4 位时全部隐去。
func Redact(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= redactKeep {
		return redactedSuffix
	}
	return s[:redactKeep] + redactedSuffix
}

// maskURL 返回脱敏后的 URL，以及该 URL 中出现的 token 类明文（用于清洗错误文本）。
// 覆盖两类形态：查询参数中的 token/secret/key/sign/password 等；telegram 的 /bot<token>/ 路径段。
func maskURL(raw string) (string, []string) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		// 解析失败时不能把原文返回给调用方（可能含 token）。
		return "<无法解析的地址>", []string{raw}
	}
	var secrets []string
	add := func(s string) {
		if s != "" {
			secrets = append(secrets, s)
		}
	}

	if u.User != nil {
		if pw, ok := u.User.Password(); ok {
			add(pw)
			u.User = url.UserPassword(u.User.Username(), Redact(pw))
		}
	}

	q := u.Query()
	changed := false
	for k, vals := range q {
		if !isSensitiveKey(k) {
			continue
		}
		for i, v := range vals {
			add(v)
			vals[i] = Redact(v)
		}
		q[k] = vals
		changed = true
	}
	if changed {
		// url.Values.Encode 会把脱敏后的 "*" 转义成 %2A，这里还原成可读形式。
		u.RawQuery = strings.ReplaceAll(q.Encode(), "%2A", "*")
	}

	// telegram: /bot<token>/sendMessage
	if segs := strings.Split(u.Path, "/"); len(segs) > 0 {
		for i, seg := range segs {
			if strings.HasPrefix(seg, "bot") && len(seg) > len("bot") {
				tok := seg[len("bot"):]
				add(tok)
				segs[i] = "bot" + Redact(tok)
			}
		}
		u.Path = strings.Join(segs, "/")
		u.RawPath = ""
	}
	return strings.ReplaceAll(u.String(), "%2A", "*"), dedupe(secrets)
}

// pathCredentialMinLen 是「通用渠道 URL 末段当作凭证」的长度阈值。
// 例如 https://example.com/api/v1/incoming/9f8e7d6c5b4a3210 的末段应被脱敏，
// 而 /hook、/send 这类短路径段保持原样，避免破坏可读性。
const pathCredentialMinLen = 16

// maskURLForKind 在通用脱敏之外，按渠道类型处理「凭证在 URL 路径里」的情况：
//   - slack:   /services/<team>/<channel>/<secret>
//   - feishu:  /open-apis/bot/v2/hook/<hook-id>
//   - 其它渠道: 末段长度 >= 16 时按 token 处理
//
// 返回脱敏后的 URL 与该 URL 中的明文清单。
func maskURLForKind(kind Kind, raw string) (string, []string) {
	masked, toks := maskURL(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return masked, toks
	}
	segs := strings.Split(u.Path, "/")
	idx := -1
	for i := len(segs) - 1; i >= 0; i-- {
		if segs[i] != "" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return masked, toks
	}
	seg := segs[idx]
	// 已经是 telegram 的 /bot<token>/ 形态则由 maskURL 处理，这里不重复。
	if strings.HasPrefix(seg, "bot") {
		return masked, toks
	}
	mustMask := kind == KindFeishu || kind == KindSlack
	if !mustMask && len(seg) < pathCredentialMinLen {
		return masked, toks
	}
	toks = append(toks, seg)

	mu, err := url.Parse(masked)
	if err != nil {
		return masked, dedupe(toks)
	}
	segs[idx] = Redact(seg)
	mu.Path = strings.Join(segs, "/")
	mu.RawPath = ""
	return strings.ReplaceAll(mu.String(), "%2A", "*"), dedupe(toks)
}

// minScrubLen 是参与「全局明文替换」的最短长度。
// 更短的取值（例如 URL 里的 monkey=1）全局替换会误伤 IP、时间戳等正常文本，
// 这类取值改为通过「整条 URL 精确替换」在 URL 层面脱敏。
const minScrubLen = 5

// urlRewrite 是一次「原始 URL → 脱敏 URL」的精确替换。
type urlRewrite struct {
	raw    string
	masked string
}

// scrubber 负责把错误文本中的密钥明文清洗干净，同时尽量不破坏可读的诊断信息。
type scrubber struct {
	kind    Kind
	secrets []string
	urls    []urlRewrite
}

// newScrubber 依据渠道配置构造清洗器：密钥类字段、URL 中的 token 段、headers 取值全部纳入。
func newScrubber(ch Channel) *scrubber {
	s := &scrubber{kind: ch.Kind}
	for k, v := range ch.Config {
		if strings.EqualFold(k, "headers") {
			for _, hv := range headerValues(v) {
				s.addSecret(hv)
			}
		}
		str, ok := v.(string)
		if !ok {
			continue
		}
		if isSensitiveKey(k) {
			s.addSecret(str)
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "url", "webhook", "api_base":
			s.addURL(str)
		}
	}
	return s
}

// addSecret 加入一个明文（过短的取值不参与全局替换，避免破坏诊断信息）。
func (s *scrubber) addSecret(v string) {
	if len(v) >= minScrubLen {
		s.secrets = append(s.secrets, v)
	}
}

// addURL 登记一条 URL：整条精确替换为脱敏形式，并把 URL 中的 token 段加入明文清单。
func (s *scrubber) addURL(raw string) {
	if raw == "" {
		return
	}
	masked, toks := maskURLForKind(s.kind, raw)
	if masked != raw {
		s.urls = append(s.urls, urlRewrite{raw: raw, masked: masked})
	}
	for _, t := range toks {
		s.addSecret(t)
	}
}

// scrub 清洗文本：先按整条 URL 精确替换，再做长明文的全局替换。
func (s *scrubber) scrub(text string) string {
	if s == nil || text == "" {
		return text
	}
	// 长 URL 优先，避免短 URL 是长 URL 前缀时破坏替换结果。
	urls := make([]urlRewrite, len(s.urls))
	copy(urls, s.urls)
	sort.Slice(urls, func(i, j int) bool { return len(urls[i].raw) > len(urls[j].raw) })
	for _, r := range urls {
		text = strings.ReplaceAll(text, r.raw, r.masked)
	}

	sorted := make([]string, 0, len(s.secrets))
	for _, sec := range s.secrets {
		if sec != "" {
			sorted = append(sorted, sec)
		}
	}
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	for _, sec := range sorted {
		text = strings.ReplaceAll(text, sec, Redact(sec))
	}
	return text
}

// headerValues 从 headers 配置里取出所有字符串取值（支持 map[string]any 与 map[string]string）。
func headerValues(v any) []string {
	var out []string
	switch t := v.(type) {
	case map[string]any:
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
	case map[string]string:
		for _, s := range t {
			out = append(out, s)
		}
	}
	return out
}

// MaskConfig 返回渠道配置的脱敏副本（供 API 列表/详情或日志使用），不修改入参。
// 规则：密钥类键（secret/password/bot_token/...）→ 前 4 位 + ***；
// url/webhook/api_base → 抹掉 token 段；headers 中名字敏感的取值同样脱敏。
func MaskConfig(ch Channel) map[string]any {
	out := make(map[string]any, len(ch.Config))
	for k, v := range ch.Config {
		s, isStr := v.(string)
		if isStr && isSensitiveKey(k) {
			out[k] = Redact(s)
			continue
		}
		if isStr {
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "url", "webhook", "api_base":
				masked, _ := maskURLForKind(ch.Kind, s)
				out[k] = masked
				continue
			}
		}
		if strings.EqualFold(k, "headers") {
			out[k] = maskHeaders(v)
			continue
		}
		out[k] = v
	}
	return out
}

// maskHeaders 只脱敏「头名像凭证」的取值，其余原样返回（便于界面展示与编辑）。
func maskHeaders(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, item := range t {
			if s, ok := item.(string); ok && isSensitiveHeader(k) {
				out[k] = Redact(s)
				continue
			}
			out[k] = item
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(t))
		for k, s := range t {
			if isSensitiveHeader(k) {
				out[k] = Redact(s)
				continue
			}
			out[k] = s
		}
		return out
	default:
		return v
	}
}

// dedupe 去重并保持稳定顺序。
func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
