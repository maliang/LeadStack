package cron

import (
	"strings"
	"testing"
	"time"
)

func utc(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		panic(err)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

func TestParseBasics(t *testing.T) {
	valid := []string{
		"* * * * *",
		"*/15 * * * *",
		"1-5 * * * *",
		"0 9 * * 1-5",
		"30 2 1 * *",
		"0 0 31 12 *",
		"*/5 9-18 * * 1-5",
		"1,3,5 * * * *",
		"0,30 * * * *",
		"0 0 * * 0",
		"0 0 * * 7",
		"0 0 1 1 *",
		"  */10   *  *  * *  ", // 多余空白
	}
	for _, expr := range valid {
		if err := Validate(expr); err != nil {
			t.Errorf("合法表达式 %q 被判非法: %v", expr, err)
		}
	}
}

func TestParseInvalidHumanErrors(t *testing.T) {
	cases := map[string]string{
		"":              "不能为空",
		"* * * *":       "需要 5 个字段",
		"* * * * * *":   "需要 5 个字段",
		"61 * * * *":    "分钟字段 61 超出 0-59",
		"* 25 * * *":    "小时字段 25 超出 0-23",
		"* * 32 * *":    "日字段 32 超出 1-31",
		"* * 0 * *":     "日字段 0 超出 1-31",
		"* * * 13 *":    "月字段 13 超出 1-12",
		"* * * * 8":     "周字段 8 超出 0-7",
		"*/0 * * * *":   "步长非法",
		"*/abc * * * *": "步长非法",
		"5-1 * * * *":   "起点大于终点",
		"1,,2 * * * *":  "空项",
		"a * * * *":     "不是数字",
		"1- * * * *":    "不是数字",
		"-5 * * * *":    "不是数字",
		"* * * * 周日":    "不是数字",
		"0 0 0 0 0":     "日字段 0 超出 1-31",
	}
	for expr, want := range cases {
		err := Validate(expr)
		if err == nil {
			t.Errorf("非法表达式 %q 未被拒绝", expr)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("表达式 %q 的错误信息 %q 应包含 %q", expr, err.Error(), want)
		}
	}
}

func TestNextStrictlyGreaterThanFrom(t *testing.T) {
	cases := []struct {
		expr string
		from string
		want string
	}{
		// 每分钟
		{"* * * * *", "2026-10-20 11:06:30", "2026-10-20 11:07:00"},
		{"* * * * *", "2026-10-20 11:06:00", "2026-10-20 11:07:00"},
		{"* * * * *", "2026-10-20 23:59:59", "2026-10-21 00:00:00"},
		// 步长
		{"*/15 * * * *", "2026-10-20 11:06:00", "2026-10-20 11:15:00"},
		{"*/15 * * * *", "2026-10-20 11:15:00", "2026-10-20 11:30:00"},
		{"*/15 * * * *", "2026-10-20 11:14:59", "2026-10-20 11:15:00"},
		{"1-5 * * * *", "2026-10-20 11:06:00", "2026-10-20 12:01:00"},
		{"1-5 * * * *", "2026-10-20 11:05:00", "2026-10-20 12:01:00"}, // 严格大于 from
		// 固定时刻
		{"0 9 * * *", "2026-10-20 08:59:59", "2026-10-20 09:00:00"},
		{"0 9 * * *", "2026-10-20 09:00:00", "2026-10-21 09:00:00"},
		{"30 2 * * *", "2026-10-20 00:00:00", "2026-10-20 02:30:00"},
		// 周（0 与 7 都是周日；2026-10-20 是周二）
		{"0 0 * * 0", "2026-10-20 00:00:00", "2026-10-25 00:00:00"},
		{"0 0 * * 7", "2026-10-20 00:00:00", "2026-10-25 00:00:00"},
		{"0 9 * * 1-5", "2026-10-23 10:00:00", "2026-10-26 09:00:00"}, // 周五 → 下周一
		{"0 9 * * 1-5", "2026-10-24 10:00:00", "2026-10-26 09:00:00"}, // 周六 → 下周一
		{"0 0 * * 1", "2026-10-19 00:00:00", "2026-10-26 00:00:00"},   // 周一 00:00 → 下周一
		// 月内（1/31 与短月）
		{"0 0 31 * *", "2026-10-01 00:00:00", "2026-10-31 00:00:00"},
		{"0 0 31 * *", "2026-10-31 00:00:00", "2026-12-31 00:00:00"},
		{"0 0 31 * *", "2026-11-01 00:00:00", "2026-12-31 00:00:00"},
		{"0 0 30 * *", "2026-01-30 00:00:00", "2026-03-30 00:00:00"}, // 2 月没有 30 日
		// 闰年 2/29
		{"0 0 29 2 *", "2026-03-01 00:00:00", "2028-02-29 00:00:00"},
		{"0 0 29 2 *", "2028-02-29 00:00:00", "2032-02-29 00:00:00"},
		{"0 0 29 2 *", "2100-03-01 00:00:00", "2104-02-29 00:00:00"}, // 2100 不是闰年
		{"0 0 29 2 *", "2096-03-01 00:00:00", "2104-02-29 00:00:00"}, // 最长 8 年空档
		// 跨年
		{"59 23 31 12 *", "2026-12-31 23:59:00", "2027-12-31 23:59:00"},
		{"0 0 1 1 *", "2026-06-01 00:00:00", "2027-01-01 00:00:00"},
		// 日/周同时受限 → 或（2026-11-02 周一；下一个是 11-09 周一，而非 12-01）
		{"0 0 1 * 1", "2026-11-02 00:00:00", "2026-11-09 00:00:00"},
		{"0 0 1 * 1", "2026-10-20 00:00:00", "2026-10-26 00:00:00"}, // 下一个是 10-26 周一
		// 小时范围 + 分钟步长
		{"*/10 9-17 * * *", "2026-10-20 08:00:00", "2026-10-20 09:00:00"},
		{"*/10 9-17 * * *", "2026-10-20 17:50:00", "2026-10-21 09:00:00"},
		{"*/10 9-17 * * *", "2026-10-20 09:05:00", "2026-10-20 09:10:00"},
	}
	for _, tc := range cases {
		s, err := Parse(tc.expr)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.expr, err)
			continue
		}
		got, err := s.Next(utc(tc.from))
		if err != nil {
			t.Errorf("%q Next(%s): %v", tc.expr, tc.from, err)
			continue
		}
		want := utc(tc.want)
		if !got.Equal(want) {
			t.Errorf("%q Next(%s) = %s, want %s", tc.expr, tc.from, got.Format(time.RFC3339), want.Format(time.RFC3339))
		}
		if !got.After(utc(tc.from)) {
			t.Errorf("%q Next(%s) 必须严格大于 from，得到 %s", tc.expr, tc.from, got)
		}
		if got.Second() != 0 || got.Nanosecond() != 0 {
			t.Errorf("%q Next 返回值的秒/纳秒必须归零，得到 %s", tc.expr, got)
		}
	}
}

func TestNextNeverMatches(t *testing.T) {
	for _, expr := range []string{"0 0 30 2 *", "0 0 31 2 *", "0 0 31 4 *"} {
		s, err := Parse(expr)
		if err != nil {
			t.Fatalf("Parse(%q): %v", expr, err)
		}
		if _, err := s.Next(utc("2026-01-01 00:00:00")); err == nil {
			t.Errorf("%q 永不命中，Next 应返回 error", expr)
		}
	}
}

func TestNextPreservesLocation(t *testing.T) {
	shanghai := time.FixedZone("CST", 8*3600)
	from := time.Date(2026, 10, 20, 8, 0, 0, 0, shanghai)
	s, err := Parse("0 9 * * *")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	next, err := s.Next(from)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if next.Location() != shanghai {
		t.Fatalf("时区应沿用 from，得到 %v", next.Location())
	}
	want := time.Date(2026, 10, 20, 9, 0, 0, 0, shanghai)
	if !next.Equal(want) {
		t.Fatalf("Next=%s want %s", next, want)
	}
}

func TestNextIsMonotonic(t *testing.T) {
	// 连续 100 次 Next 必须严格递增且落在命中集合内
	for _, expr := range []string{"*/7 * * * *", "0 0 29 2 *", "0 0 1 * 1", "*/13 3-9 * * 1-5"} {
		s, err := Parse(expr)
		if err != nil {
			t.Fatalf("Parse(%q): %v", expr, err)
		}
		cur := utc("2026-01-01 00:00:00")
		for i := 0; i < 100; i++ {
			next, err := s.Next(cur)
			if err != nil {
				t.Fatalf("%q 第 %d 次 Next: %v", expr, i, err)
			}
			if !next.After(cur) {
				t.Fatalf("%q Next 未严格递增: %s → %s", expr, cur, next)
			}
			if !s.matchesWall(next) {
				t.Fatalf("%q Next 返回了不命中的时刻: %s (%s)", expr, next, s.Describe())
			}
			cur = next
		}
	}
}

// matchesWall 校验某个时刻的墙钟值是否命中表达式（用于自证 Next 的返回值）。
func (s *Schedule) matchesWall(t time.Time) bool {
	if !s.sets[0][t.Minute()] || !s.sets[1][t.Hour()] || !s.sets[3][int(t.Month())] {
		return false
	}
	return s.dayMatches(t.Year(), int(t.Month()), t.Day())
}

func TestDescribe(t *testing.T) {
	cases := map[string]string{
		"* * * * *":        "每分钟",
		"*/15 * * * *":     "每 15 分钟",
		"0 * * * *":        "每小时整点",
		"30 * * * *":       "每小时第 30 分",
		"0 9 * * *":        "09:00",
		"30 2 * * *":       "02:30",
		"0 9 * * 1-5":      "每周一至周五 09:00",
		"0 9 * * 0":        "每周日 09:00",
		"0 9 * * 7":        "每周日 09:00",
		"0 9 * * 1,3,5":    "每周一,三,五 09:00",
		"0 0 1 * *":        "每月 1 日 00:00",
		"0 12 1,15 * *":    "每月 1,15 日 12:00",
		"0 0 31 12 *":      "每年 12 月 31 日 00:00",
		"0 0 1 1 *":        "每年 1 月 1 日 00:00",
		"*/5 * * * *":      "每 5 分钟",
		"*/5 9-18 * * 1-5": "每周一至周五 9-18 点每 5 分钟",
		"0 0 29 2 *":       "每年 2 月 29 日 00:00",
	}
	for expr, want := range cases {
		if got := Describe(expr); got != want {
			t.Errorf("Describe(%q) = %q, want %q", expr, got, want)
		}
	}
	// 非法表达式返回空串（不 panic）
	for _, expr := range []string{"", "bad", "* * *"} {
		if got := Describe(expr); got != "" {
			t.Errorf("Describe(%q) 非法时应返回空串，得到 %q", expr, got)
		}
	}
}

func TestScheduleStringAndNil(t *testing.T) {
	s, err := Parse("*/5 * * * *")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.String() != "*/5 * * * *" {
		t.Fatalf("String()=%q", s.String())
	}
	var nilS *Schedule
	if nilS.String() != "" {
		t.Fatalf("nil String 应为空")
	}
	if nilS.Describe() != "" {
		t.Fatalf("nil Describe 应为空")
	}
	if _, err := nilS.Next(time.Now()); err == nil {
		t.Fatalf("nil Next 应返回 error")
	}
}

func TestParseConcurrentSafety(t *testing.T) {
	// Schedule 只读，可并发使用（-race 下必须干净）
	s, err := Parse("*/7 9-18 * * 1-5")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			cur := utc("2026-01-01 00:00:00")
			for j := 0; j < 50; j++ {
				next, err := s.Next(cur)
				if err != nil {
					return
				}
				cur = next
			}
			_ = s.Describe()
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
