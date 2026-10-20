package hostimport

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseBasic(t *testing.T) {
	text := "10.0.0.1\n10.0.0.2:2222\n# 注释行\n\nhost3.example.com\n"
	entries, err := Parse(text, DefaultPort)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("应解析 3 行（注释与空行忽略），得到 %d: %+v", len(entries), entries)
	}
	want := []struct {
		host string
		port int
		line int
	}{
		{"10.0.0.1", 22, 1},
		{"10.0.0.2", 2222, 2},
		{"host3.example.com", 22, 5},
	}
	for i, w := range want {
		if entries[i].Host != w.host || entries[i].Port != w.port || entries[i].Line != w.line {
			t.Fatalf("第 %d 条不符: %+v want %+v", i, entries[i], w)
		}
		if entries[i].Err != "" {
			t.Fatalf("第 %d 条不应有错误: %q", i, entries[i].Err)
		}
	}
	if entries[0].Addr() != "10.0.0.1:22" || entries[1].Addr() != "10.0.0.2:2222" {
		t.Fatalf("Addr() 不符: %q %q", entries[0].Addr(), entries[1].Addr())
	}
}

func TestParseIPv6(t *testing.T) {
	text := strings.Join([]string{
		"[::1]:22",
		"[::1]:2200",
		"[2001:db8::1]",
		"2001:db8::2",
		"::1",
	}, "\n")
	entries, err := Parse(text, DefaultPort)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("应解析 5 条，得到 %d: %+v", len(entries), entries)
	}
	checks := []struct {
		host string
		port int
		addr string
	}{
		{"::1", 22, "[::1]:22"},
		{"::1", 2200, "[::1]:2200"},
		{"2001:db8::1", 22, "[2001:db8::1]:22"},
		{"2001:db8::2", 22, "[2001:db8::2]:22"},
		{"::1", 22, "[::1]:22"},
	}
	for i, c := range checks {
		if entries[i].Host != c.host || entries[i].Port != c.port || entries[i].Addr() != c.addr {
			t.Fatalf("第 %d 条 IPv6 解析不符: %+v (Addr=%q) want %+v", i, entries[i], entries[i].Addr(), c)
		}
	}
	// 第 0 与第 4 条都是 [::1]:22 → 第 4 条必须被标记重复
	if entries[4].Err == "" || !strings.Contains(entries[4].Err, "重复") {
		t.Fatalf("IPv6 重复去重失败: %+v", entries[4])
	}
}

func TestParseDeduplicatesNormalizedAddr(t *testing.T) {
	// 同一地址的不同写法必须被识别为重复
	text := "10.0.0.1\n10.0.0.1:22\n10.0.0.2:2200\n"
	entries, err := Parse(text, 22)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("应保留 3 条（含重复行），得到 %d", len(entries))
	}
	if entries[0].Err != "" || entries[2].Err != "" {
		t.Fatalf("首个出现的地址与不同端口的地址都不应报错: %+v", entries)
	}
	if !strings.Contains(entries[1].Err, "与第 1 行重复") {
		t.Fatalf("第 2 行应报与第 1 行重复（10.0.0.1 与 10.0.0.1:22 是同一地址）: %+v", entries[1])
	}
	if valid := Valid(entries); len(valid) != 2 {
		t.Fatalf("Valid 应只返回 2 条，得到 %d", len(valid))
	}
}

func TestParseInvalidLines(t *testing.T) {
	cases := map[string]string{
		"host:0":        "端口 0 超出 1-65535",
		"host:65536":    "端口 65536 超出 1-65535",
		"host:abc":      "端口不是数字",
		"host:":         "端口为空",
		"host:-1":       "超出 1-65535",
		"host:22:33":    "不是合法的 IPv6 地址", // 多个冒号但不是「主机:端口」
		"http://x":      "端口不是数字",
		"bad host":      "空白",
		"host;rm -rf /": "非法字符",
		"host$(id)":     "非法字符",
		"[::1]:":        "端口为空",
		"[::1]:99999":   "超出 1-65535",
		"[::1]x":        "方括号后",
		"[::1":          "缺少右方括号",
		".example.com":  "不能以 . 开头",
		"example..com":  "连续的 .",
		"  # 只有注释":      "",
		"":              "",
	}
	for line, want := range cases {
		entries, err := Parse(line, 22)
		if err != nil {
			t.Fatalf("Parse(%q): %v", line, err)
		}
		if want == "" {
			// 注释/空行应被忽略
			if len(entries) != 0 {
				t.Fatalf("%q 应被忽略，得到 %+v", line, entries)
			}
			continue
		}
		if len(entries) != 1 {
			t.Fatalf("%q 应产生 1 条错误记录，得到 %+v", line, entries)
		}
		if !strings.Contains(entries[0].Err, want) {
			t.Fatalf("%q 的错误 %q 应包含 %q", line, entries[0].Err, want)
		}
		if entries[0].OK() {
			t.Fatalf("%q 不应被判为 OK", line)
		}
	}
}

func TestParseInlineCommentAndCRLFAndBOM(t *testing.T) {
	text := "\ufeff10.0.0.1:2222 # 生产机\r\n10.0.0.2\t# 测试机\r\n"
	entries, err := Parse(text, 22)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("应解析 2 条，得到 %d: %+v", len(entries), entries)
	}
	if entries[0].Host != "10.0.0.1" || entries[0].Port != 2222 {
		t.Fatalf("BOM/行内注释/CRLF 处理错误: %+v", entries[0])
	}
	if entries[1].Host != "10.0.0.2" || entries[1].Port != 22 {
		t.Fatalf("Tab 后行内注释处理错误: %+v", entries[1])
	}
}

func TestParseLineLimit(t *testing.T) {
	// 恰好 500 行：通过
	lines := make([]string, MaxLines)
	for i := range lines {
		lines[i] = fmt.Sprintf("10.0.%d.%d", i/256, i%256)
	}
	entries, err := Parse(strings.Join(lines, "\n"), 22)
	if err != nil {
		t.Fatalf("恰好 %d 行不应报错: %v", MaxLines, err)
	}
	if len(entries) != MaxLines {
		t.Fatalf("应解析 %d 条，得到 %d", MaxLines, len(entries))
	}

	// 501 行：返回 ErrTooManyLines
	lines = append(lines, "10.1.0.1")
	_, err = Parse(strings.Join(lines, "\n"), 22)
	if err == nil || !errors.Is(err, ErrTooManyLines) {
		t.Fatalf("超过 %d 行应返回 ErrTooManyLines，得到 %v", MaxLines, err)
	}

	// 注释与空行不计入上限：500 有效行 + 600 注释行仍应通过
	withComments := make([]string, 0, 1100)
	for i := 0; i < 600; i++ {
		withComments = append(withComments, "# 注释 "+fmt.Sprint(i))
	}
	withComments = append(withComments, lines[:MaxLines]...)
	if _, err := Parse(strings.Join(withComments, "\n"), 22); err != nil {
		t.Fatalf("注释行不应计入行数上限: %v", err)
	}
}

func TestParseUnicodeHostAndLength(t *testing.T) {
	entries, err := Parse("例子.中国\nlocalhost\n", 22)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if entries[0].Host != "例子.中国" || entries[0].Err != "" {
		t.Fatalf("Unicode 主机名应被接受: %+v", entries[0])
	}
	tooLong := strings.Repeat("a", 254)
	entries, _ = Parse(tooLong, 22)
	if len(entries) != 1 || !strings.Contains(entries[0].Err, "过长") {
		t.Fatalf("超长主机名应报错: %+v", entries)
	}
}

func TestParseDefaultPortFallback(t *testing.T) {
	// 非法 defaultPort 回落到 22
	entries, err := Parse("10.0.0.9", 0)
	if err != nil || entries[0].Port != DefaultPort {
		t.Fatalf("非法默认端口应回落 %d: %+v err=%v", DefaultPort, entries, err)
	}
	entries, _ = Parse("10.0.0.9", 2222)
	if entries[0].Port != 2222 {
		t.Fatalf("默认端口应为 2222: %+v", entries[0])
	}
}

func TestParseEmptyText(t *testing.T) {
	for _, text := range []string{"", "\n\n", "# only comments\n# more\n"} {
		entries, err := Parse(text, 22)
		if err != nil {
			t.Fatalf("Parse(%q): %v", text, err)
		}
		if len(entries) != 0 {
			t.Fatalf("Parse(%q) 应为空，得到 %+v", text, entries)
		}
	}
}
