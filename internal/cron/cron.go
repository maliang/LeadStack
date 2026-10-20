// Package cron 实现 5 字段 cron 表达式的解析、校验、中文描述与下次触发时间计算。
//
// 字段顺序：分 时 日 月 周（周字段 0 与 7 都表示周日）。
// 支持：`*`、`,`、`-`、`*/n`（以及 `a-b/n`）。
//
// 日/周的语义遵循经典 Vixie cron：两个字段都不是 `*` 开头时取**或**（任一命中即触发），
// 否则取**且**；「`*` 开头」包含 `*/n` 这种带步长的写法。
//
// 契约见 docs/ARCHITECTURE.md 第 42 节。
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// fieldSpec 描述一个 cron 字段的取值范围。
type fieldSpec struct {
	name     string
	min, max int
}

var fieldSpecs = [5]fieldSpec{
	{name: "分钟", min: 0, max: 59},
	{name: "小时", min: 0, max: 23},
	{name: "日", min: 1, max: 31},
	{name: "月", min: 1, max: 12},
	{name: "周", min: 0, max: 7}, // 0 与 7 都是周日
}

// Schedule 是解析后的 cron 表达式（只读，可安全并发使用）。
type Schedule struct {
	expr string
	raw  [5]string
	// 命中集合：索引即字段取值；周的索引 0..7（7 会归一化到 0）。
	sets [5][]bool
	// star 表示该字段是否以 `*` 开头（决定日/周是「或」还是「且」）。
	star [5]bool

	minutes []int // 升序命中分钟
	hours   []int // 升序命中小时
}

// Parse 解析 5 字段 cron 表达式；失败返回人话错误（如「小时字段 25 超出 0-23」）。
func Parse(expr string) (*Schedule, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return nil, fmt.Errorf("cron 表达式不能为空")
	}
	parts := strings.Fields(trimmed)
	if len(parts) != 5 {
		return nil, fmt.Errorf("cron 表达式需要 5 个字段（分 时 日 月 周），实际 %d 个: %q", len(parts), trimmed)
	}

	s := &Schedule{expr: trimmed}
	for i, spec := range fieldSpecs {
		set, star, err := parseField(parts[i], spec)
		if err != nil {
			return nil, err
		}
		s.raw[i] = parts[i]
		s.sets[i] = set
		s.star[i] = star
	}
	// 周字段 7 归一到 0（周日）
	if s.sets[4][7] {
		s.sets[4][0] = true
	}
	for v := fieldSpecs[0].min; v <= fieldSpecs[0].max; v++ {
		if s.sets[0][v] {
			s.minutes = append(s.minutes, v)
		}
	}
	for v := fieldSpecs[1].min; v <= fieldSpecs[1].max; v++ {
		if s.sets[1][v] {
			s.hours = append(s.hours, v)
		}
	}
	if len(s.minutes) == 0 || len(s.hours) == 0 {
		return nil, fmt.Errorf("cron 表达式 %q 没有有效的分钟/小时取值", trimmed)
	}
	return s, nil
}

// Validate 只校验表达式是否合法。
func Validate(expr string) error {
	_, err := Parse(expr)
	return err
}

// parseField 解析单个字段，返回命中集合与该字段是否以 `*` 开头。
func parseField(expr string, spec fieldSpec) ([]bool, bool, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, false, fmt.Errorf("%s字段为空", spec.name)
	}
	set := make([]bool, spec.max+1)
	star := strings.HasPrefix(expr, "*")
	for _, part := range strings.Split(expr, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, false, fmt.Errorf("%s字段存在空项（多余的逗号）: %q", spec.name, expr)
		}
		if err := parseFieldPart(part, spec, set); err != nil {
			return nil, false, err
		}
	}
	return set, star, nil
}

// parseFieldPart 解析 `*`、`n`、`a-b`、`*/n`、`a-b/n` 这类片段。
func parseFieldPart(part string, spec fieldSpec, set []bool) error {
	rangePart := part
	step := 1
	if i := strings.IndexByte(part, '/'); i >= 0 {
		rangePart = strings.TrimSpace(part[:i])
		stepStr := strings.TrimSpace(part[i+1:])
		n, err := strconv.Atoi(stepStr)
		if err != nil || n <= 0 {
			return fmt.Errorf("%s字段的步长非法: %q（必须为正整数）", spec.name, part)
		}
		step = n
	}

	var lo, hi int
	switch {
	case rangePart == "*":
		lo, hi = spec.min, spec.max
	case strings.Contains(rangePart, "-"):
		bounds := strings.SplitN(rangePart, "-", 2)
		l, err1 := parseFieldNumber(bounds[0], spec)
		if err1 != nil {
			return err1
		}
		h, err2 := parseFieldNumber(bounds[1], spec)
		if err2 != nil {
			return err2
		}
		if l > h {
			return fmt.Errorf("%s字段区间 %s 的起点大于终点", spec.name, part)
		}
		lo, hi = l, h
	default:
		n, err := parseFieldNumber(rangePart, spec)
		if err != nil {
			return err
		}
		lo, hi = n, n
	}

	for v := lo; v <= hi; v += step {
		set[v] = true
	}
	return nil
}

func parseFieldNumber(raw string, spec fieldSpec) (int, error) {
	raw = strings.TrimSpace(raw)
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s字段不是数字: %q", spec.name, raw)
	}
	if n < spec.min || n > spec.max {
		return 0, fmt.Errorf("%s字段 %d 超出 %d-%d", spec.name, n, spec.min, spec.max)
	}
	return n, nil
}

// Describe 返回中文人话描述（给前端预览用）；表达式非法时返回空串。
func Describe(expr string) string {
	s, err := Parse(expr)
	if err != nil {
		return ""
	}
	return s.Describe()
}

// Describe 返回该表达式的中文描述。
func (s *Schedule) Describe() string {
	if s == nil {
		return ""
	}
	datePart := s.describeDatePart()
	timePart := s.describeTimePart()

	switch {
	case datePart == "" && timePart == "每分钟":
		return "每分钟"
	case datePart == "":
		return timePart
	case datePart == "每天":
		return "每天 " + timePart
	case timePart == "每分钟":
		return datePart + "每分钟"
	default:
		return datePart + " " + timePart
	}
}

// describeDatePart 返回日期部分的中文描述；空串表示「每天」。
func (s *Schedule) describeDatePart() string {
	domRaw, monthRaw, dowRaw := s.raw[2], s.raw[3], s.raw[4]
	domStar := s.star[2]
	dowStar := s.star[4]
	monthStar := s.star[3]

	switch {
	case domStar && dowStar && monthStar:
		return ""
	case domStar && dowStar:
		return monthRaw + " 月的每天"
	case dowStar && monthStar:
		return "每月 " + domRaw + " 日"
	case dowStar:
		return "每年 " + monthRaw + " 月 " + domRaw + " 日"
	case domStar && monthStar:
		return "每周" + describeDow(dowRaw)
	case domStar:
		return monthRaw + " 月每周" + describeDow(dowRaw)
	default:
		return "每月 " + domRaw + " 日或每周" + describeDow(dowRaw)
	}
}

// describeTimePart 返回时刻部分的中文描述。
func (s *Schedule) describeTimePart() string {
	minuteRaw, hourRaw := s.raw[0], s.raw[1]

	if hourRaw == "*" {
		switch {
		case minuteRaw == "*":
			return "每分钟"
		case strings.HasPrefix(minuteRaw, "*/"):
			return "每 " + strings.TrimPrefix(minuteRaw, "*/") + " 分钟"
		case minuteRaw == "0":
			return "每小时整点"
		case isSingleNumber(minuteRaw):
			return "每小时第 " + minuteRaw + " 分"
		default:
			return "每小时的第 " + minuteRaw + " 分"
		}
	}
	if isSingleNumber(hourRaw) && isSingleNumber(minuteRaw) {
		h, _ := strconv.Atoi(hourRaw)
		m, _ := strconv.Atoi(minuteRaw)
		return fmt.Sprintf("%02d:%02d", h, m)
	}
	if strings.HasPrefix(hourRaw, "*/") {
		return "每 " + strings.TrimPrefix(hourRaw, "*/") + " 小时的第 " + minuteRaw + " 分"
	}
	if strings.HasPrefix(minuteRaw, "*/") {
		return hourRaw + " 点每 " + strings.TrimPrefix(minuteRaw, "*/") + " 分钟"
	}
	return hourRaw + " 点的第 " + minuteRaw + " 分"
}

// describeDow 把周字段翻译成中文（周一/周日/一至周五/一,三,五 等）。
func describeDow(raw string) string {
	names := map[int]string{0: "日", 1: "一", 2: "二", 3: "三", 4: "四", 5: "五", 6: "六", 7: "日"}
	if raw == "*" {
		return "每天"
	}
	// 单个数字
	if isSingleNumber(raw) {
		n, _ := strconv.Atoi(raw)
		if name, ok := names[n]; ok {
			return name
		}
		return raw
	}
	// 纯区间 a-b（可带 /n）
	base := raw
	if i := strings.IndexByte(base, '/'); i >= 0 {
		base = base[:i]
	}
	if parts := strings.SplitN(base, "-", 2); len(parts) == 2 && isSingleNumber(parts[0]) && isSingleNumber(parts[1]) {
		lo, _ := strconv.Atoi(parts[0])
		hi, _ := strconv.Atoi(parts[1])
		if lo == 0 && hi == 6 {
			return "一至周日"
		}
		return names[lo] + "至周" + names[hi]
	}
	// 逗号列表
	items := strings.Split(raw, ",")
	out := make([]string, 0, len(items))
	for _, it := range items {
		it = strings.TrimSpace(it)
		if isSingleNumber(it) {
			n, _ := strconv.Atoi(it)
			out = append(out, names[n])
			continue
		}
		out = append(out, it)
	}
	return strings.Join(out, ",")
}

func isSingleNumber(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Next 返回**严格大于** from 的下一次触发时刻（秒与纳秒归零，时区沿用 from）。
// 若表达式永远不可能命中（例如 2 月 31 日），返回 error。
func (s *Schedule) Next(from time.Time) (time.Time, error) {
	if s == nil {
		return time.Time{}, fmt.Errorf("cron: Schedule 为 nil")
	}
	loc := from.Location()
	// 严格大于 from：从「下一分钟」开始找
	cursor := from.Truncate(time.Minute).Add(time.Minute)
	// 闰年 2/29 的最长等待是 8 年（2096 → 2104，2100 不是闰年），10 年足够覆盖
	limit := cursor.AddDate(10, 0, 0)

	for !cursor.After(limit) {
		y, mon, day := cursor.Date()
		if !s.sets[3][int(mon)] {
			// 月份不命中：跳到下个月 1 日 00:00
			cursor = time.Date(y, mon+1, 1, 0, 0, 0, 0, loc)
			continue
		}
		if !s.dayMatches(y, int(mon), day) {
			cursor = time.Date(y, mon, day+1, 0, 0, 0, 0, loc)
			continue
		}
		for _, h := range s.hours {
			if h < cursor.Hour() {
				continue
			}
			for _, m := range s.minutes {
				if h == cursor.Hour() && m < cursor.Minute() {
					continue
				}
				cand := time.Date(y, mon, day, h, m, 0, 0, loc)
				if !cand.Before(cursor) {
					return cand, nil
				}
			}
		}
		// 当天没有更晚的时刻 → 下一天 00:00
		cursor = time.Date(y, mon, day+1, 0, 0, 0, 0, loc)
	}
	return time.Time{}, fmt.Errorf("cron 表达式 %q 在 %s 之后 10 年内没有触发时间（可能是「2 月 31 日」这类永不命中的组合）",
		s.expr, from.Format(time.RFC3339))
}

// dayMatches 判断某一天是否命中（日/周按 Vixie 语义组合）。
func (s *Schedule) dayMatches(y, mon, day int) bool {
	domOK := s.sets[2][day]
	dow := int(time.Date(y, time.Month(mon), day, 12, 0, 0, 0, time.UTC).Weekday())
	dowOK := s.sets[4][dow]

	domStar, dowStar := s.star[2], s.star[4]
	switch {
	case domStar && dowStar:
		return true
	case domStar:
		return dowOK
	case dowStar:
		return domOK
	default:
		return domOK || dowOK
	}
}

// String 返回原始表达式。
func (s *Schedule) String() string {
	if s == nil {
		return ""
	}
	return s.expr
}
