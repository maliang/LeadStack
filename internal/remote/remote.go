// Package remote 在远端主机上采集运维数据：进程列表、监听端口、日志尾部。
//
// 所有命令都通过 ssh（sshx）或 agent（job.exec）在**目标机**执行，因此命令文本必须按
// 目标机 OS（linux/darwin/windows）生成；解析做成纯函数，便于用固定样本做单测。
//
// 跨平台差异（实测 macOS Darwin 27 / Linux procps）：
//
//	进程：linux `ps -eo pid=,user=,pcpu=,pmem=,rss=,args=`
//	      darwin `ps -ww -axo pid=,user=,pcpu=,pmem=,rss=,args=`（BSD ps 必须 -ax + -ww 才不限宽）
//	      windows `tasklist /FO CSV /NH`（CSV 解析，字段带引号、内存带千分位）
//	端口：linux `ss -tulpn`（无 ss 的环境由调用方呈现错误）
//	      darwin `lsof -nP -iTCP -sTCP:LISTEN -F pcn`（-F 机器可读输出，避免 COMMAND 含空格时列错位）
//	日志：`tail -n N <path> [| grep -E -e <pat>] | head -c M`，先 `[ -r ]` 预检可读性
//
// 安全：所有路径/关键字都经单引号转义（shQuote），并在测试里用真实 shell + canary 验证注入不生效。
//
// 契约见 docs/ARCHITECTURE.md 第 43 节。
package remote

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 默认值（契约第 43 节：日志输出上限 256 KB）。
const (
	DefaultTimeoutS    = 15
	DefaultLogLines    = 200
	MaxLogLines        = 5000
	DefaultMaxLogBytes = 256 << 10
	MaxLogBytes        = 8 << 20
)

// Runner 在目标机上执行一条命令。
//
// 与 checker.Executor 同形：jobs.Dispatcher 与 backend 包装的 sshx.Client 都能直接满足，
// 不需要额外适配器。
type Runner interface {
	Exec(ctx context.Context, serverID, command string, timeoutS int) (stdout, stderr string, exitCode int, err error)
}

// Client 绑定一台目标机（Runner + serverID + OS）。
type Client struct {
	runner   Runner
	serverID string
	osName   string
}

// New 构造采集客户端；osName 取 "linux"/"darwin"/"windows"，空值按 linux 处理。
func New(r Runner, serverID, osName string) *Client {
	return &Client{runner: r, serverID: serverID, osName: osName}
}

// OS 返回归一化后的目标机 OS（无法识别时返回原始值）。
func (c *Client) OS() string {
	if norm := normalizeOS(c.osName); norm != "" {
		return norm
	}
	return strings.TrimSpace(c.osName)
}

// Process 是一条进程信息。
type Process struct {
	PID     int     `json:"pid"`
	User    string  `json:"user"`
	CPUPct  float64 `json:"cpu_pct"`
	MemPct  float64 `json:"mem_pct"`
	RSSKB   int64   `json:"rss_kb"`
	Command string  `json:"command"`
}

// Port 是一条监听端口信息。
type Port struct {
	Proto   string `json:"proto"`   // tcp / udp
	Local   string `json:"local"`   // 0.0.0.0:22、[::]:22、127.0.0.1:8080
	PID     int    `json:"pid"`     // 0 = 未知（非 root 看不到别人的进程）
	Process string `json:"process"` // 进程名，未知为空
}

// ListProcesses 采集目标机进程列表。排序与条数截断由调用方负责。
func (c *Client) ListProcesses(ctx context.Context) ([]Process, error) {
	cmd, err := ProcessesCommand(c.osName)
	if err != nil {
		return nil, err
	}
	stdout, stderr, exitCode, err := c.run(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("remote: 进程采集命令退出码 %d: %s", exitCode, snippet(stderr))
	}
	return ParseProcesses(c.osName, stdout)
}

// ListPorts 采集目标机监听端口。
func (c *Client) ListPorts(ctx context.Context) ([]Port, error) {
	cmd, err := PortsCommand(c.osName)
	if err != nil {
		return nil, err
	}
	stdout, stderr, exitCode, err := c.run(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("remote: 端口采集命令退出码 %d: %s", exitCode, snippet(stderr))
	}
	return ParsePorts(c.osName, stdout)
}

// TailLog 读取目标机日志文件末尾 lines 行（grep 非空时服务端过滤），
// 输出上限 maxBytes，返回（行列表, 是否被截断, error）。
func (c *Client) TailLog(ctx context.Context, path string, lines int, grep string, maxBytes int) ([]string, bool, error) {
	cmd, err := TailCommand(path, lines, grep, maxBytes)
	if err != nil {
		return nil, false, err
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxLogBytes
	}
	if maxBytes > MaxLogBytes {
		maxBytes = MaxLogBytes
	}
	stdout, stderr, exitCode, err := c.run(ctx, cmd)
	if err != nil {
		return nil, false, err
	}
	if exitCode != 0 {
		return nil, false, fmt.Errorf("remote: 读取日志失败（退出码 %d）: %s", exitCode, snippet(stderr))
	}
	return ParseLogOutput(stdout, maxBytes)
}

// run 执行命令（统一注入 serverID / 超时）。
func (c *Client) run(ctx context.Context, command string) (string, string, int, error) {
	if c == nil || c.runner == nil {
		return "", "", -1, errors.New("remote: Runner 未注入")
	}
	stdout, stderr, exitCode, err := c.runner.Exec(ctx, c.serverID, command, DefaultTimeoutS)
	if err != nil {
		return "", "", -1, fmt.Errorf("remote: 目标机执行失败: %w", err)
	}
	return stdout, stderr, exitCode, nil
}

// ---------- 纯函数：命令生成 ----------

// ProcessesCommand 生成进程列表命令。
func ProcessesCommand(osName string) (string, error) {
	switch normalizeOS(osName) {
	case "linux":
		return "ps -eo pid=,user=,pcpu=,pmem=,rss=,args=", nil
	case "darwin":
		// BSD ps：-ax 取全部进程，-ww 不按终端宽度截断 command 列
		return "ps -ww -axo pid=,user=,pcpu=,pmem=,rss=,args=", nil
	case "windows":
		return "tasklist /FO CSV /NH", nil
	default:
		return "", fmt.Errorf("remote: 不支持的 OS %q（进程采集支持 linux/darwin/windows）", osName)
	}
}

// PortsCommand 生成监听端口命令。
func PortsCommand(osName string) (string, error) {
	switch normalizeOS(osName) {
	case "linux":
		return "ss -tulpn", nil
	case "darwin":
		// -F pcn：每个 socket 输出 p<pid>/c<command>/n<name> 记录，避免 COMMAND 含空格时列错位
		return "lsof -nP -iTCP -sTCP:LISTEN -F pcn", nil
	default:
		return "", fmt.Errorf("remote: OS %q 暂不支持端口采集（支持 linux/darwin）", osName)
	}
}

// TailCommand 生成日志尾部命令（path/grep 安全转义；maxBytes 为输出上限）。
func TailCommand(path string, lines int, grep string, maxBytes int) (string, error) {
	if err := validateLogPath(path); err != nil {
		return "", err
	}
	if lines <= 0 {
		lines = DefaultLogLines
	}
	if lines > MaxLogLines {
		lines = MaxLogLines
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxLogBytes
	}
	if maxBytes > MaxLogBytes {
		maxBytes = MaxLogBytes
	}
	if strings.ContainsRune(grep, 0) {
		return "", errors.New("remote: grep 关键字不能包含 NUL")
	}

	var b strings.Builder
	// 先预检可读性：tail 的失败会被管道掩盖（管道退出码取 head），这里显式拦住常见错误。
	b.WriteString("[ -r " + shQuote(path) + " ] || { echo " + shQuote("日志文件不存在或不可读: "+path) + " >&2; exit 1; }; ")
	b.WriteString("tail -n " + strconv.Itoa(lines) + " " + shQuote(path))
	if strings.TrimSpace(grep) != "" {
		// -E 扩展正则；-e 防止关键字以 "-" 开头被当成选项
		b.WriteString(" | grep -E -e " + shQuote(grep))
	}
	// 多读 1 字节用于判断「是否被截断」
	b.WriteString(" | head -c " + strconv.Itoa(maxBytes+1))
	return b.String(), nil
}

// ---------- 纯函数：解析 ----------

// ParseProcesses 解析 ps（linux/darwin）或 tasklist（windows）输出。
func ParseProcesses(osName, stdout string) ([]Process, error) {
	switch normalizeOS(osName) {
	case "linux", "darwin":
		return parsePS(stdout), nil
	case "windows":
		return parseTasklist(stdout)
	default:
		return nil, fmt.Errorf("remote: 不支持的 OS %q", osName)
	}
}

// parsePS 解析 `pid user pcpu pmem rss args...` 输出。
// 容错：列数不足/非数字行（表头、空行）跳过；command 含空格时取剩余全部内容。
func parsePS(stdout string) []Process {
	procs := make([]Process, 0, 64)
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		head, command, ok := splitAfterFields(line, 5)
		if !ok {
			continue // 列数不足：表头或被截断的行
		}
		pid, err := strconv.Atoi(head[0])
		if err != nil {
			continue // 表头（非数字 PID）
		}
		p := Process{PID: pid, User: head[1], Command: strings.TrimSpace(command)}
		p.CPUPct = parseFloatOrZero(head[2])
		p.MemPct = parseFloatOrZero(head[3])
		if rss, err := strconv.ParseInt(head[4], 10, 64); err == nil {
			p.RSSKB = rss
		}
		procs = append(procs, p)
	}
	return procs
}

// splitAfterFields 取前 n 个空白分隔字段，并把第 n 个字段之后的**原始剩余内容**作为 tail 返回，
// 从而保留 command 里的空格（不能用 strings.Fields 后再 join，会丢原始空白）。
func splitAfterFields(line string, n int) (fields []string, tail string, ok bool) {
	fields = make([]string, 0, n)
	i := 0
	for len(fields) < n {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= len(line) {
			return nil, "", false
		}
		start := i
		for i < len(line) && line[i] != ' ' && line[i] != '\t' {
			i++
		}
		fields = append(fields, line[start:i])
	}
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	return fields, line[i:], true
}

// parseTasklist 解析 `tasklist /FO CSV /NH`：字段为 映像名,PID,会话名,会话#,内存。
func parseTasklist(stdout string) ([]Process, error) {
	reader := csv.NewReader(strings.NewReader(stdout))
	reader.FieldsPerRecord = -1
	procs := make([]Process, 0, 64)
	for {
		rec, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			continue // 容错：跳过损坏行 / INFO 提示行
		}
		if len(rec) < 2 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(rec[1]))
		if err != nil {
			continue
		}
		p := Process{PID: pid, Command: strings.TrimSpace(rec[0])}
		if len(rec) >= 5 {
			p.RSSKB = parseMemKB(rec[4])
		}
		procs = append(procs, p)
	}
	return procs, nil
}

// parseMemKB 解析 Windows 的 "120,000 K" 形式内存。
func parseMemKB(raw string) int64 {
	s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "K"))
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, " ", "")
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// ParsePorts 解析 ss（linux）或 lsof -F（darwin）输出。
func ParsePorts(osName, stdout string) ([]Port, error) {
	switch normalizeOS(osName) {
	case "linux":
		return parseSS(stdout), nil
	case "darwin":
		return parseLsof(stdout), nil
	default:
		return nil, fmt.Errorf("remote: 不支持的 OS %q（端口采集支持 linux/darwin）", osName)
	}
}

// ssUsersRe 从 `users:(("sshd",pid=456,fd=3),("x",pid=7,fd=1))` 里取第一个进程名与 pid。
var ssUsersRe = regexp.MustCompile(`\("([^"]*)",pid=(\d+)`)

// parseSS 解析 `ss -tulpn`：
//
//	Netid State  Recv-Q Send-Q Local:Port Peer:Port Process
//	tcp   LISTEN 0      128    0.0.0.0:22 0.0.0.0:*  users:(("sshd",pid=456,fd=3))
func parseSS(stdout string) []Port {
	ports := make([]Port, 0, 32)
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		proto := strings.ToLower(fields[0])
		if proto != "tcp" && proto != "udp" {
			continue // 表头（Netid）或无法识别的行
		}
		local := fields[4]
		if !hasPortSuffix(local) {
			continue
		}
		port := Port{Proto: proto, Local: normalizeLocal(local)}
		if len(fields) > 6 {
			if m := ssUsersRe.FindStringSubmatch(strings.Join(fields[6:], " ")); len(m) == 3 {
				port.Process = m[1]
				if pid, err := strconv.Atoi(m[2]); err == nil {
					port.PID = pid
				}
			}
		}
		ports = append(ports, port)
	}
	return ports
}

// parseLsof 解析 `lsof -nP -iTCP -sTCP:LISTEN -F pcn` 的字段输出：
//
//	p456
//	csshd
//	n*:22
func parseLsof(stdout string) []Port {
	ports := make([]Port, 0, 32)
	seen := make(map[string]bool, 32)
	pid, process := 0, ""
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			if n, err := strconv.Atoi(strings.TrimSpace(line[1:])); err == nil {
				pid = n
			}
		case 'c':
			process = strings.TrimSpace(line[1:])
		case 'n':
			local := strings.TrimSpace(line[1:])
			if !hasPortSuffix(local) {
				continue
			}
			// 同一进程可能有多个 fd 监听同一地址：按 (pid, local, process) 去重
			key := fmt.Sprintf("%d|%s|%s", pid, normalizeLocal(local), process)
			if seen[key] {
				continue
			}
			seen[key] = true
			ports = append(ports, Port{Proto: "tcp", Local: normalizeLocal(local), PID: pid, Process: process})
		}
	}
	return ports
}

// hasPortSuffix 判断地址是否以 `:端口` 结尾（IPv6 `[::]:80` 也成立）。
func hasPortSuffix(addr string) bool {
	i := strings.LastIndexByte(addr, ':')
	if i < 0 || i == len(addr)-1 {
		return false
	}
	_, err := strconv.Atoi(addr[i+1:])
	return err == nil
}

// normalizeLocal 去掉 IPv6/通配地址两端的方括号并统一通配写法。
func normalizeLocal(local string) string {
	local = strings.TrimPrefix(local, "(")
	local = strings.TrimSuffix(local, ")")
	if i := strings.LastIndexByte(local, ':'); i > 0 {
		host := local[:i]
		port := local[i+1:]
		switch host {
		case "", "*":
			return "*:" + port
		case "[::]":
			return "[::]:" + port
		}
	}
	return local
}

// ParseLogOutput 把 tail 输出切成行；truncated 表示是否因 maxBytes 被截断，
// 被截断时会丢掉最后一条不完整的行。
func ParseLogOutput(stdout string, maxBytes int) ([]string, bool, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxLogBytes
	}
	truncated := len(stdout) > maxBytes
	raw := stdout
	if truncated {
		raw = raw[:maxBytes]
	}
	lines := strings.Split(raw, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	} else if truncated && len(lines) > 0 {
		// 截断切在半行上：丢掉不完整的最后一行
		lines = lines[:len(lines)-1]
	}
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	return lines, truncated, nil
}

// ---------- 内部工具 ----------

// normalizeOS 归一化 OS 名；空值按 linux；无法识别返回 ""。
func normalizeOS(osName string) string {
	switch strings.ToLower(strings.TrimSpace(osName)) {
	case "", "linux":
		return "linux"
	case "darwin", "mac", "macos", "osx":
		return "darwin"
	case "windows", "win":
		return "windows"
	default:
		return ""
	}
}

// validateLogPath 校验日志路径：绝对路径、无控制字符/NUL、合法 UTF-8（长度上限 1024）。
func validateLogPath(path string) error {
	if path == "" {
		return errors.New("remote: 日志路径不能为空")
	}
	if len(path) > 1024 {
		return fmt.Errorf("remote: 日志路径过长（%d 字节，上限 1024）", len(path))
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("remote: 日志路径必须是绝对路径: %q", snippet(path))
	}
	if !utf8.ValidString(path) {
		return errors.New("remote: 日志路径不是合法的 UTF-8")
	}
	for _, r := range path {
		if r == 0 || r == '\n' || r == '\r' {
			return fmt.Errorf("remote: 日志路径不能包含控制字符/NUL: %q", snippet(path))
		}
	}
	return nil
}

// shQuote 把值包成 POSIX 单引号字面量（内部单引号用 '\” 转义），杜绝命令注入。
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func parseFloatOrZero(raw string) float64 {
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return f
}

// snippet 把值截断压行，用于错误信息。
func snippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 120
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
