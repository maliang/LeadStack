// Package hostimport 解析「批量导入主机」的粘贴文本。
//
// 每行 host[:port]，`#` 注释与空行忽略（行内 `#` 之后也按注释处理）；
// 支持 IPv6（`[::1]:22`、`[2001:db8::1]`、裸 `::1`）；同一 host:port 去重（第二次起标为错误）；
// 单次最多 MaxLines 条有效行。契约见 docs/ARCHITECTURE.md 第 45 节。
package hostimport

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"
)

// 契约上限（第 45 节：单次上限 500 行）。
const (
	MaxLines    = 500
	DefaultPort = 22
	// maxHostLen 是主机名长度上限（DNS 名称上限 253）。
	maxHostLen = 253
)

// ErrTooManyLines 表示有效行数超过 MaxLines（backend 映射成 HTTP 400）。
var ErrTooManyLines = errors.New("导入行数超过上限")

// forbiddenHostRunes 是主机名里一律拒绝的字符：空白、控制字符与 shell 元字符。
// （主机名不会拼进命令，但导入内容会进日志/UI，这里按纵深防御处理。）
const forbiddenHostRunes = ";|&$`\"'(){}<>*?!#/\\"

// Entry 是单行解析结果；Err 非空表示该行有问题（行号从 1 开始，指原始文本的行）。
type Entry struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	Line int    `json:"line"`
	Raw  string `json:"raw"`
	Err  string `json:"error,omitempty"`
}

// OK 表示该行可以创建服务器。
func (e Entry) OK() bool { return e.Err == "" && e.Host != "" }

// Addr 返回 `host:port` 形式（IPv6 自动加方括号），便于直接用于 SSH 连接与去重。
func (e Entry) Addr() string {
	if e.Host == "" {
		return ""
	}
	if strings.Contains(e.Host, ":") {
		return "[" + e.Host + "]:" + strconv.Itoa(e.Port)
	}
	return e.Host + ":" + strconv.Itoa(e.Port)
}

// Parse 解析粘贴文本。
//
// 逐行问题（端口非法、主机名非法、重复）放进 Entry.Err，不中断整体；
// 只有「有效行数超过 MaxLines」返回 error（errors.Is(err, ErrTooManyLines)）。
func Parse(text string, defaultPort int) ([]Entry, error) {
	if defaultPort <= 0 || defaultPort > 65535 {
		defaultPort = DefaultPort
	}
	lines := strings.Split(text, "\n")
	entries := make([]Entry, 0, len(lines))
	seen := make(map[string]int, len(lines)) // addr → 首次出现的行号
	effective := 0

	for i, raw := range lines {
		lineNo := i + 1
		raw = strings.TrimRight(raw, "\r")
		if i == 0 {
			raw = strings.TrimPrefix(raw, "\ufeff") // 去掉 Windows/记事本 BOM
		}
		content := raw
		if j := strings.IndexByte(content, '#'); j >= 0 {
			content = content[:j]
		}
		content = strings.TrimSpace(content)
		if content == "" {
			continue // 注释行 / 空行：完全忽略
		}

		effective++
		if effective > MaxLines {
			return nil, fmt.Errorf("%w：单次最多 %d 行（注释与空行不计），实际已超过 %d 行",
				ErrTooManyLines, MaxLines, MaxLines)
		}

		entry := Entry{Line: lineNo, Raw: strings.TrimSpace(raw)}
		host, port, err := splitHostPort(content, defaultPort)
		if err != nil {
			entry.Err = err.Error()
			entries = append(entries, entry)
			continue
		}
		entry.Host, entry.Port = host, port
		addr := entry.Addr()
		if first, dup := seen[addr]; dup {
			entry.Err = fmt.Sprintf("与第 %d 行重复（%s）", first, addr)
			entries = append(entries, entry)
			continue
		}
		seen[addr] = lineNo
		entries = append(entries, entry)
	}
	return entries, nil
}

// Valid 只返回可用的行（后端逐条创建时用）。
func Valid(entries []Entry) []Entry {
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.OK() {
			out = append(out, e)
		}
	}
	return out
}

// splitHostPort 解析单行的 host[:port]，支持带方括号的 IPv6。
func splitHostPort(s string, defaultPort int) (string, int, error) {
	host := s
	portStr := ""

	// 行尾冒号：`host:` / `[::1]:` 都应明确报错，而不是静默用默认端口
	if strings.HasSuffix(s, ":") {
		return "", 0, errors.New("端口为空（行尾多余冒号）")
	}

	switch {
	case strings.HasPrefix(s, "["):
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return "", 0, errors.New("IPv6 地址缺少右方括号 ]")
		}
		host = s[1:end]
		rest := strings.TrimSpace(s[end+1:])
		switch {
		case rest == "":
			// 用默认端口
		case strings.HasPrefix(rest, ":"):
			portStr = rest[1:]
		default:
			return "", 0, fmt.Errorf("方括号后只能是 :端口，得到 %q", rest)
		}
	case strings.Count(s, ":") == 1:
		i := strings.IndexByte(s, ':')
		host, portStr = s[:i], s[i+1:]
	case strings.Count(s, ":") > 1:
		// 裸 IPv6（不带端口），例如 ::1 / 2001:db8::1
		host = s
	}

	host = strings.TrimSpace(host)
	if host == "" {
		if portStr == "" && strings.HasSuffix(s, ":") {
			return "", 0, errors.New("端口为空（行尾多余冒号）")
		}
		return "", 0, errors.New("主机名为空")
	}
	if err := validateHost(host); err != nil {
		return "", 0, err
	}

	port := defaultPort
	if strings.TrimSpace(portStr) != "" {
		p, err := strconv.Atoi(strings.TrimSpace(portStr))
		if err != nil {
			return "", 0, fmt.Errorf("端口不是数字: %q", strings.TrimSpace(portStr))
		}
		if p < 1 || p > 65535 {
			return "", 0, fmt.Errorf("端口 %d 超出 1-65535", p)
		}
		port = p
	}
	return host, port, nil
}

// validateHost 校验主机名 / IP：允许 Unicode 字母（IDN）、字母数字、`.`、`-`、`_` 与 IPv4/IPv6。
func validateHost(host string) error {
	if len(host) > maxHostLen {
		return fmt.Errorf("主机名过长（%d 字节，上限 %d）", len(host), maxHostLen)
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	// 含冒号但不是合法 IP：多半是想写 IPv6 却写错了（如 host:22:33），必须拒绝
	if strings.Contains(host, ":") {
		return fmt.Errorf("不是合法的 IPv6 地址: %q", host)
	}
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return errors.New("主机名不能以 . 开头或结尾")
	}
	if strings.Contains(host, "..") {
		return errors.New("主机名不能包含连续的 .")
	}
	for _, r := range host {
		if r == ' ' || r == '\t' {
			return errors.New("主机名不能包含空白字符")
		}
		if !unicode.IsPrint(r) || strings.ContainsRune(forbiddenHostRunes, r) {
			return fmt.Errorf("主机名含非法字符 %q（只允许字母/数字/./-/_ 或 IP）", r)
		}
	}
	return nil
}
