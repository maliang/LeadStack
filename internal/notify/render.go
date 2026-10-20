package notify

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"time"
)

// severityLabel 返回严重级别的中文名。
func severityLabel(s Severity) string {
	switch s {
	case SeverityCritical:
		return "严重"
	case SeverityWarning:
		return "警告"
	case SeverityInfo:
		return "信息"
	default:
		if s == "" {
			return "告警"
		}
		return string(s)
	}
}

// statusLabel 返回触发/恢复状态文案。
func statusLabel(recovered bool) string {
	if recovered {
		return "已恢复"
	}
	return "触发"
}

// titleOr 返回 title，空则用 fallback。
func titleOr(title, fallback string) string {
	if strings.TrimSpace(title) == "" {
		return fallback
	}
	return title
}

// normalizeTS 兼容秒与毫秒时间戳（契约用毫秒；防御历史数据里的秒）。
func normalizeTS(ts int64) int64 {
	if ts > 0 && ts < 1_000_000_000_000 {
		return ts * 1000
	}
	return ts
}

// formatTS 把时间戳格式化为本地时间字符串；0 返回空串。
func formatTS(ts int64) string {
	ms := normalizeTS(ts)
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05")
}

// renderText 把 Message 渲染为人类可读文本（text 模板、钉钉/企微/飞书/Slack/Telegram/邮件共用）。
func renderText(msg Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "【%s】%s", statusLabel(msg.Recovered), titleOr(msg.Title, "LeadStack 告警"))
	fmt.Fprintf(&b, "\n级别：%s", severityLabel(msg.Severity))
	if msg.ServerName != "" {
		fmt.Fprintf(&b, "\n服务器：%s", msg.ServerName)
	}
	if msg.CheckName != "" {
		fmt.Fprintf(&b, "\n检测项：%s", msg.CheckName)
	}
	if msg.Value != 0 {
		fmt.Fprintf(&b, "\n数值：%g", msg.Value)
	}
	if msg.Body != "" {
		fmt.Fprintf(&b, "\n%s", msg.Body)
	}
	if ts := formatTS(msg.TS); ts != "" {
		fmt.Fprintf(&b, "\n时间：%s", ts)
	}
	if msg.AlertID != "" {
		fmt.Fprintf(&b, "\n告警ID：%s", msg.AlertID)
	}
	if msg.ConsoleURL != "" {
		fmt.Fprintf(&b, "\n详情：%s", msg.ConsoleURL)
	}
	return b.String()
}

// webhookPayload 是 webhook 渠道 template=json 时的请求体（字段稳定，便于接收方解析）。
type webhookPayload struct {
	Title      string  `json:"title"`
	Body       string  `json:"body"`
	Text       string  `json:"text"`
	Severity   string  `json:"severity"`
	Status     string  `json:"status"`
	ServerName string  `json:"server_name,omitempty"`
	CheckName  string  `json:"check_name,omitempty"`
	Value      float64 `json:"value"`
	TS         int64   `json:"ts"`
	Recovered  bool    `json:"recovered"`
	AlertID    string  `json:"alert_id,omitempty"`
	ConsoleURL string  `json:"console_url,omitempty"`
	SeverityCN string  `json:"severity_label,omitempty"`
}

// statusValue 返回 webhook payload 里的 status 字段值。
func statusValue(recovered bool) string {
	if recovered {
		return "resolved"
	}
	return "firing"
}

// renderJSON 渲染 webhook 的 json 模板请求体。
func renderJSON(msg Message) ([]byte, error) {
	p := webhookPayload{
		Title:      titleOr(msg.Title, "LeadStack 告警"),
		Body:       msg.Body,
		Text:       renderText(msg),
		Severity:   string(msg.Severity),
		Status:     statusValue(msg.Recovered),
		ServerName: msg.ServerName,
		CheckName:  msg.CheckName,
		Value:      msg.Value,
		TS:         normalizeTS(msg.TS),
		Recovered:  msg.Recovered,
		AlertID:    msg.AlertID,
		ConsoleURL: msg.ConsoleURL,
		SeverityCN: severityLabel(msg.Severity),
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("notify: 序列化 webhook 请求体失败: %w", err)
	}
	return b, nil
}

// renderHTML 把消息渲染为 HTML（邮件 multipart/alternative 的 HTML 部分）。
// 内容全部转义后按行转 <br>，避免注入。
func renderHTML(msg Message) string {
	esc := html.EscapeString(renderText(msg))
	esc = strings.ReplaceAll(esc, "\n", "<br>\n")
	return "<!DOCTYPE html>\n<html><body style=\"font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;font-size:14px;line-height:1.6;color:#1f2328\">\n<div>" +
		esc + "</div>\n</body></html>"
}
