package remote

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// ---------- 假 Runner ----------

type fakeRunner struct {
	mu      sync.Mutex
	cmds    []string
	server  []string
	timeout []int
	fn      func(ctx context.Context, serverID, command string) (string, string, int, error)
}

func (f *fakeRunner) Exec(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
	f.mu.Lock()
	f.cmds = append(f.cmds, command)
	f.server = append(f.server, serverID)
	f.timeout = append(f.timeout, timeoutS)
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, serverID, command)
	}
	return "", "", 0, nil
}

func (f *fakeRunner) lastCommand() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.cmds) == 0 {
		return ""
	}
	return f.cmds[len(f.cmds)-1]
}

// ---------- 命令生成 ----------

func TestProcessesCommandPerOS(t *testing.T) {
	linux, err := ProcessesCommand("linux")
	if err != nil {
		t.Fatalf("linux: %v", err)
	}
	if !strings.Contains(linux, "ps -eo pid=,user=,pcpu=,pmem=,rss=,args=") {
		t.Fatalf("linux 命令不符: %q", linux)
	}
	darwin, err := ProcessesCommand("darwin")
	if err != nil {
		t.Fatalf("darwin: %v", err)
	}
	if !strings.Contains(darwin, "ps -ww -axo pid=,user=,pcpu=,pmem=,rss=,args=") {
		t.Fatalf("darwin 命令不符: %q", darwin)
	}
	if linux == darwin {
		t.Fatalf("linux 与 darwin 的 ps 参数必须不同")
	}
	windows, err := ProcessesCommand("windows")
	if err != nil || !strings.Contains(windows, "tasklist /FO CSV /NH") {
		t.Fatalf("windows 命令不符: %q err=%v", windows, err)
	}
	// 缺省按 linux
	def, err := ProcessesCommand("")
	if err != nil || def != linux {
		t.Fatalf("空 OS 应回退 linux: %q err=%v", def, err)
	}
	if _, err := ProcessesCommand("freebsd"); err == nil {
		t.Fatalf("未知 OS 应报错")
	}
}

func TestPortsCommandPerOS(t *testing.T) {
	linux, err := PortsCommand("linux")
	if err != nil || linux != "ss -tulpn" {
		t.Fatalf("linux=%q err=%v", linux, err)
	}
	darwin, err := PortsCommand("darwin")
	if err != nil {
		t.Fatalf("darwin: %v", err)
	}
	if !strings.HasPrefix(darwin, "lsof -nP -iTCP -sTCP:LISTEN") {
		t.Fatalf("darwin=%q", darwin)
	}
	if !strings.Contains(darwin, "-F") {
		t.Fatalf("darwin 应用字段输出（-F）避免 COMMAND 含空格列错位: %q", darwin)
	}
	if _, err := PortsCommand("windows"); err == nil {
		t.Fatalf("windows 端口采集应明确报不支持")
	}
}

// ---------- 进程解析 ----------

func TestParseProcessesLinux(t *testing.T) {
	// 注意：带表头、空行、以及 command 含空格的情况
	stdout := `    PID USER      %CPU %MEM   RSS COMMAND
      1 root         0.0  0.1 27712 /sbin/init splash
    456 www-data     1.5  2.3 123456 /usr/sbin/nginx -g daemon off;
  78912 alice         99.9 10.0 999999 /opt/app/bin/server --config /etc/app.conf --verbose
`
	procs, err := ParseProcesses("linux", stdout)
	if err != nil {
		t.Fatalf("ParseProcesses: %v", err)
	}
	if len(procs) != 3 {
		t.Fatalf("应解析出 3 条（跳过表头），得到 %d: %+v", len(procs), procs)
	}
	if procs[0].PID != 1 || procs[0].User != "root" || procs[0].RSSKB != 27712 {
		t.Fatalf("第 1 条解析错误: %+v", procs[0])
	}
	if procs[1].Command != "/usr/sbin/nginx -g daemon off;" {
		t.Fatalf("command 里的空格必须保留: %q", procs[1].Command)
	}
	if procs[2].CPUPct != 99.9 || procs[2].MemPct != 10.0 || procs[2].RSSKB != 999999 {
		t.Fatalf("数值解析错误: %+v", procs[2])
	}
	if !strings.Contains(procs[2].Command, "--config /etc/app.conf --verbose") {
		t.Fatalf("多段 command 丢失: %q", procs[2].Command)
	}
}

func TestParseProcessesDarwin(t *testing.T) {
	// 实测 macOS 输出：pid 右对齐、user 列变宽、command 含空格/括号
	stdout := `    1 root               5.4  0.1  27712 /sbin/launchd
  403 root               0.0  0.0  10272 /usr/libexec/UserEventAgent (System)
  501 maliang            2.2  1.7 123456 /Applications/Google Chrome.app/Contents/MacOS/Google Chrome
`
	procs, err := ParseProcesses("darwin", stdout)
	if err != nil {
		t.Fatalf("ParseProcesses: %v", err)
	}
	if len(procs) != 3 {
		t.Fatalf("应解析 3 条，得到 %d", len(procs))
	}
	if procs[1].Command != "/usr/libexec/UserEventAgent (System)" {
		t.Fatalf("带括号的命令解析错误: %q", procs[1].Command)
	}
	if procs[2].User != "maliang" || !strings.HasSuffix(procs[2].Command, "Google Chrome") {
		t.Fatalf("含空格路径解析错误: %+v", procs[2])
	}
}

func TestParseProcessesToleratesColumns(t *testing.T) {
	cases := []string{
		"",                             // 空输出
		"\n\n   \n",                    // 只有空白
		"  PID USER %CPU %MEM RSS CMD", // 只有表头
		"1 root 0.0 0.1",               // 列数不足
		"abc root 0.0 0.1 100 cmd",     // PID 非数字
		"42 root 0.0 0.1 100",          // 只有 5 列（没有 command）
		"42 root - - - /bin/x",         // 数值列是 "-"
	}
	for _, stdout := range cases {
		procs, err := ParseProcesses("linux", stdout)
		if err != nil {
			t.Fatalf("ParseProcesses(%q) 不应报错: %v", stdout, err)
		}
		for _, p := range procs {
			if p.PID <= 0 {
				t.Fatalf("解析出了非法 PID: %+v (输入 %q)", p, stdout)
			}
		}
	}
	// 5 列（无 command）应仍保留该进程
	procs, _ := ParseProcesses("linux", "42 root 0.0 0.1 100\n")
	if len(procs) != 1 || procs[0].PID != 42 || procs[0].Command != "" {
		t.Fatalf("无 command 行应保留 PID: %+v", procs)
	}
	// 数值列是 "-" 时按 0 处理
	procs, _ = ParseProcesses("linux", "42 root - - - /bin/x\n")
	if len(procs) != 1 || procs[0].CPUPct != 0 || procs[0].RSSKB != 0 {
		t.Fatalf("非法数值应降级为 0: %+v", procs)
	}
}

func TestParseProcessesTasklist(t *testing.T) {
	stdout := `"chrome.exe","1234","Console","1","120,000 K"
"nginx.exe","5678","Services","0","8,192 K"
INFO: No tasks are running which match the specified criteria.
`
	procs, err := ParseProcesses("windows", stdout)
	if err != nil {
		t.Fatalf("ParseProcesses: %v", err)
	}
	if len(procs) != 2 {
		t.Fatalf("应解析 2 条，得到 %d: %+v", len(procs), procs)
	}
	if procs[0].PID != 1234 || procs[0].Command != "chrome.exe" || procs[0].RSSKB != 120000 {
		t.Fatalf("tasklist 第 1 条解析错误: %+v", procs[0])
	}
	if procs[1].RSSKB != 8192 {
		t.Fatalf("带千分位内存解析错误: %+v", procs[1])
	}
}

// ---------- 端口解析 ----------

func TestParsePortsLinux(t *testing.T) {
	stdout := `Netid State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process
tcp   LISTEN 0      128    0.0.0.0:22         0.0.0.0:*         users:(("sshd",pid=456,fd=3))
tcp   LISTEN 0      511    127.0.0.1:8080     0.0.0.0:*         users:(("leadstack",pid=789,fd=9))
tcp   LISTEN 0      128    [::]:22            [::]:*            users:(("sshd",pid=456,fd=4))
udp   UNCONN 0      0      0.0.0.0:68         0.0.0.0:*         users:(("dhclient",pid=321,fd=6))
tcp   LISTEN 0      128    *:9090             *:*
`
	ports, err := ParsePorts("linux", stdout)
	if err != nil {
		t.Fatalf("ParsePorts: %v", err)
	}
	if len(ports) != 5 {
		t.Fatalf("应解析 5 条，得到 %d: %+v", len(ports), ports)
	}
	if ports[0].Proto != "tcp" || ports[0].Local != "0.0.0.0:22" || ports[0].PID != 456 || ports[0].Process != "sshd" {
		t.Fatalf("第 1 条解析错误: %+v", ports[0])
	}
	if ports[2].Local != "[::]:22" || ports[2].PID != 456 {
		t.Fatalf("IPv6 解析错误: %+v", ports[2])
	}
	if ports[3].Proto != "udp" || ports[3].Process != "dhclient" {
		t.Fatalf("udp 解析错误: %+v", ports[3])
	}
	if ports[4].PID != 0 || ports[4].Process != "" {
		t.Fatalf("无进程信息的行应保留端口、pid=0: %+v", ports[4])
	}
}

func TestParsePortsDarwin(t *testing.T) {
	// 实测 `lsof -nP -iTCP -sTCP:LISTEN -F pcn`（f 行由 lsof 附带输出，必须被忽略）
	stdout := `p811
crapportd
f14
n*:55582
f16
n*:55582
f23
n*:49288
p1234
cnode
f20
n127.0.0.1:3000
p5678
cGoogle Chrome
f30
n[::1]:8080
`
	ports, err := ParsePorts("darwin", stdout)
	if err != nil {
		t.Fatalf("ParsePorts: %v", err)
	}
	if len(ports) != 4 {
		t.Fatalf("应解析 4 条（同一 pid/地址去重），得到 %d: %+v", len(ports), ports)
	}
	if ports[0] != (Port{Proto: "tcp", Local: "*:55582", PID: 811, Process: "rapportd"}) {
		t.Fatalf("第 1 条解析错误: %+v", ports[0])
	}
	if ports[1].Local != "*:49288" || ports[1].PID != 811 {
		t.Fatalf("第 2 条解析错误: %+v", ports[1])
	}
	if ports[2].Local != "127.0.0.1:3000" || ports[2].Process != "node" || ports[2].PID != 1234 {
		t.Fatalf("第 3 条解析错误: %+v", ports[2])
	}
	if ports[3].Local != "[::1]:8080" || ports[3].Process != "Google Chrome" {
		t.Fatalf("含空格的进程名解析错误: %+v", ports[3])
	}
}

func TestParsePortsEmptyAndGarbage(t *testing.T) {
	for _, osName := range []string{"linux", "darwin"} {
		ports, err := ParsePorts(osName, "")
		if err != nil || len(ports) != 0 {
			t.Fatalf("%s 空输出应返回空列表: %v %v", osName, ports, err)
		}
	}
	ports, err := ParsePorts("linux", "garbage\nmore garbage")
	if err != nil || len(ports) != 0 {
		t.Fatalf("垃圾输入应返回空列表: %v %v", ports, err)
	}
	if _, err := ParsePorts("windows", ""); err == nil {
		t.Fatalf("windows 端口解析应报不支持")
	}
}

// ---------- 日志命令与解析 ----------

func TestTailCommandShape(t *testing.T) {
	cmd, err := TailCommand("/var/log/app.log", 200, "error", 0)
	if err != nil {
		t.Fatalf("TailCommand: %v", err)
	}
	for _, must := range []string{
		"[ -r '/var/log/app.log' ]",
		"tail -n 200 '/var/log/app.log'",
		"grep -E -e 'error'",
		"head -c ",
	} {
		if !strings.Contains(cmd, must) {
			t.Fatalf("命令缺少 %q:\n%s", must, cmd)
		}
	}
	// 无 grep 时不应出现 grep
	cmd, _ = TailCommand("/var/log/app.log", 50, "  ", 1024)
	if strings.Contains(cmd, "grep") {
		t.Fatalf("空 grep 不应拼接 grep: %q", cmd)
	}
	if !strings.Contains(cmd, "head -c 1025") {
		t.Fatalf("maxBytes 应 +1 以便判断截断: %q", cmd)
	}
	// 行数与字节数会被夹紧
	cmd, _ = TailCommand("/x.log", -5, "", -1)
	if !strings.Contains(cmd, "tail -n 200 ") || !strings.Contains(cmd, "head -c 262145") {
		t.Fatalf("默认值/夹紧不符: %q", cmd)
	}
	cmd, _ = TailCommand("/x.log", 999999, "", 0)
	if !strings.Contains(cmd, "tail -n 5000 ") {
		t.Fatalf("行数上限未生效: %q", cmd)
	}
}

func TestTailCommandPathValidation(t *testing.T) {
	bad := []string{"", "var/log/app.log", "relative.log", "/x/log\x00.txt", "/x/log\ninjection"}
	for _, path := range bad {
		if _, err := TailCommand(path, 10, "", 1024); err == nil {
			t.Fatalf("非法路径 %q 应被拒绝", path)
		}
	}
	if _, err := TailCommand("/x.log", 10, "bad\x00grep", 1024); err == nil {
		t.Fatalf("grep 含 NUL 应被拒绝")
	}
	// 中文路径允许（单引号包裹后安全）
	cmd, err := TailCommand("/var/log/应用日志.log", 10, "", 1024)
	if err != nil || !strings.Contains(cmd, "'/var/log/应用日志.log'") {
		t.Fatalf("中文路径应被接受并单引号包裹: %q err=%v", cmd, err)
	}
}

// TestTailCommandInjectionPayloads 是安全用例：注入载荷必须被包进单引号，不得改变命令结构。
func TestTailCommandInjectionPayloads(t *testing.T) {
	payloads := []string{
		"/tmp/a'; rm -rf / #",
		"/tmp/a$(touch /tmp/pwned)",
		"/tmp/a`touch /tmp/pwned`",
		"/tmp/a\"; touch /tmp/pwned; \"",
		"/tmp/a | touch /tmp/pwned",
		"/tmp/a && touch /tmp/pwned",
	}
	for _, path := range payloads {
		cmd, err := TailCommand(path, 10, "", 1024)
		if err != nil {
			// 允许直接拒绝（只要不放行）
			continue
		}
		if !strings.Contains(cmd, shQuote(path)) {
			t.Fatalf("路径必须被单引号包裹: path=%q cmd=%q", path, cmd)
		}
		// 命令结构固定：只应有一个 tail 与一个 head
		if strings.Count(cmd, "tail -n") != 1 || strings.Count(cmd, "head -c") != 1 {
			t.Fatalf("命令结构被注入改变: %q", cmd)
		}
	}
}

func TestParseLogOutput(t *testing.T) {
	lines, truncated, err := ParseLogOutput("a\nb\nc\n", 1024)
	if err != nil || truncated || len(lines) != 3 || lines[2] != "c" {
		t.Fatalf("正常输出解析错误: %v %v %v", lines, truncated, err)
	}
	// 截断在半行上：丢掉不完整行
	lines, truncated, _ = ParseLogOutput("line1\nline2\nline3-partial", 20)
	if !truncated {
		t.Fatalf("应标记截断")
	}
	for _, l := range lines {
		if strings.Contains(l, "partial") {
			t.Fatalf("不完整的最后一行应被丢弃: %v", lines)
		}
	}
	// CRLF 去掉 \r
	lines, _, _ = ParseLogOutput("x\r\ny\r\n", 1024)
	if len(lines) != 2 || lines[0] != "x" || lines[1] != "y" {
		t.Fatalf("CRLF 解析错误: %v", lines)
	}
	// 空输出
	lines, truncated, _ = ParseLogOutput("", 1024)
	if len(lines) != 0 || truncated {
		t.Fatalf("空输出应为空列表: %v %v", lines, truncated)
	}
}

// ---------- Client + 假 Runner ----------

func TestClientListProcessesWithFakeRunner(t *testing.T) {
	runner := &fakeRunner{fn: func(ctx context.Context, serverID, command string) (string, string, int, error) {
		if !strings.Contains(command, "ps -ww -axo") {
			t.Errorf("darwin 应使用 -ww -axo: %q", command)
		}
		return "  1 root 0.0 0.1 100 /sbin/launchd\n", "", 0, nil
	}}
	c := New(runner, "srv-1", "darwin")
	procs, err := c.ListProcesses(context.Background())
	if err != nil {
		t.Fatalf("ListProcesses: %v", err)
	}
	if len(procs) != 1 || procs[0].PID != 1 {
		t.Fatalf("结果不符: %+v", procs)
	}
	if c.OS() != "darwin" {
		t.Fatalf("OS()=%q", c.OS())
	}
	if runner.server[0] != "srv-1" || runner.timeout[0] != DefaultTimeoutS {
		t.Fatalf("serverID/timeout 未透传: %+v %+v", runner.server, runner.timeout)
	}
}

func TestClientErrors(t *testing.T) {
	// 命令非 0 退出 → 带 stderr 的错误
	runner := &fakeRunner{fn: func(ctx context.Context, serverID, command string) (string, string, int, error) {
		return "", "ps: command not found", 127, nil
	}}
	c := New(runner, "srv-1", "linux")
	if _, err := c.ListProcesses(context.Background()); err == nil || !strings.Contains(err.Error(), "127") ||
		!strings.Contains(err.Error(), "ps: command not found") {
		t.Fatalf("非 0 退出应带原因: %v", err)
	}

	// 传输出错 → 包装错误
	runner = &fakeRunner{fn: func(ctx context.Context, serverID, command string) (string, string, int, error) {
		return "", "", -1, errors.New("ssh: connection closed")
	}}
	c = New(runner, "srv-1", "linux")
	if _, err := c.ListProcesses(context.Background()); err == nil || !strings.Contains(err.Error(), "ssh: connection closed") {
		t.Fatalf("传输错误应被包装: %v", err)
	}

	// Runner 未注入
	if _, err := New(nil, "s", "linux").ListProcesses(context.Background()); err == nil {
		t.Fatalf("nil Runner 应报错")
	}
	// 不支持的 OS
	if _, err := New(&fakeRunner{}, "s", "plan9").ListProcesses(context.Background()); err == nil {
		t.Fatalf("未知 OS 应报错")
	}
}

func TestClientTailLogWithFakeRunner(t *testing.T) {
	runner := &fakeRunner{fn: func(ctx context.Context, serverID, command string) (string, string, int, error) {
		if !strings.Contains(command, "grep -E -e 'error'") {
			t.Errorf("grep 未拼接: %q", command)
		}
		return "error 1\nerror 2\nerror 3\n", "", 0, nil
	}}
	c := New(runner, "srv-1", "linux")
	lines, truncated, err := c.TailLog(context.Background(), "/var/log/app.log", 100, "error", 64)
	if err != nil {
		t.Fatalf("TailLog: %v", err)
	}
	if truncated || len(lines) != 3 || lines[0] != "error 1" {
		t.Fatalf("结果不符: %v truncated=%v", lines, truncated)
	}

	// 文件不可读 → 命令退出码非 0 → 错误信息可读
	runner.fn = func(ctx context.Context, serverID, command string) (string, string, int, error) {
		return "", "日志文件不存在或不可读: /nope.log", 1, nil
	}
	if _, _, err := c.TailLog(context.Background(), "/nope.log", 10, "", 1024); err == nil ||
		!strings.Contains(err.Error(), "不存在或不可读") {
		t.Fatalf("不可读文件应报错: %v", err)
	}
}

// ---------- 真实 shell 冒烟（验证命令在目标机语法正确） ----------

func runShell(t *testing.T, command string) (string, string, int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", command)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out.String(), errb.String(), ee.ExitCode()
		}
		t.Fatalf("执行失败: %v", err)
	}
	return out.String(), errb.String(), 0
}

func TestRealShellProcesses(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("在 macOS 上验证 darwin 命令真实可用")
	}
	cmd, err := ProcessesCommand("darwin")
	if err != nil {
		t.Fatalf("ProcessesCommand: %v", err)
	}
	stdout, stderr, code := runShell(t, cmd)
	if code != 0 {
		t.Fatalf("darwin ps 命令失败 code=%d stderr=%q", code, stderr)
	}
	procs, err := ParseProcesses("darwin", stdout)
	if err != nil {
		t.Fatalf("ParseProcesses: %v", err)
	}
	if len(procs) < 5 {
		t.Fatalf("进程数异常少: %d", len(procs))
	}
	found := false
	for _, p := range procs {
		if p.PID == os.Getpid() {
			found = true
			if p.Command == "" {
				t.Fatalf("当前进程 command 不应为空: %+v", p)
			}
			break
		}
	}
	if !found {
		t.Fatalf("未在进程列表里找到测试进程 pid=%d", os.Getpid())
	}
}

func TestRealShellPorts(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("在 macOS 上验证 darwin 命令真实可用")
	}
	cmd, err := PortsCommand("darwin")
	if err != nil {
		t.Fatalf("PortsCommand: %v", err)
	}
	stdout, stderr, code := runShell(t, cmd)
	if code != 0 {
		t.Fatalf("darwin lsof 命令失败 code=%d stderr=%q", code, stderr)
	}
	ports, err := ParsePorts("darwin", stdout)
	if err != nil {
		t.Fatalf("ParsePorts: %v", err)
	}
	for _, p := range ports {
		if p.Proto != "tcp" || !hasPortSuffix(p.Local) {
			t.Fatalf("端口解析结果异常: %+v", p)
		}
	}
	t.Logf("本机监听端口 %d 个，示例: %+v", len(ports), ports[:min(3, len(ports))])
}

func TestRealShellTailLog(t *testing.T) {
	dir := t.TempDir()
	// 路径里带单引号，验证转义「能让合法路径工作」而不只是「能拦住坏路径」
	path := filepath.Join(dir, "it's app.log")
	if err := os.WriteFile(path, []byte("info a\nerror b\ninfo c\nerror d\n"), 0o644); err != nil {
		t.Fatalf("写日志: %v", err)
	}
	cmd, err := TailCommand(path, 3, "error", 4096)
	if err != nil {
		t.Fatalf("TailCommand: %v", err)
	}
	stdout, stderr, code := runShell(t, cmd)
	if code != 0 {
		t.Fatalf("tail 命令失败 code=%d stderr=%q cmd=%s", code, stderr, cmd)
	}
	lines, truncated, err := ParseLogOutput(stdout, 4096)
	if err != nil || truncated {
		t.Fatalf("解析失败: %v truncated=%v", err, truncated)
	}
	if len(lines) != 2 || lines[0] != "error b" || lines[1] != "error d" {
		t.Fatalf("grep 过滤结果不符: %v", lines)
	}

	// 截断：maxBytes 很小时 must 报告 truncated 且丢掉半行
	cmd, _ = TailCommand(path, 100, "", 12)
	stdout, _, code = runShell(t, cmd)
	if code != 0 {
		t.Fatalf("tail 命令失败 code=%d", code)
	}
	lines, truncated, _ = ParseLogOutput(stdout, 12)
	if !truncated {
		t.Fatalf("小上限应标记截断: %v", lines)
	}
	// 命令只允许 maxBytes+1 字节回来（多读 1 字节用于判断截断）
	if len(stdout) > 13 {
		t.Fatalf("命令输出超出上限: %d 字节", len(stdout))
	}
}

// TestRealShellTailLogInjection 真实执行注入载荷：不得产生副作用。
func TestRealShellTailLogInjection(t *testing.T) {
	dir := t.TempDir()
	canary := filepath.Join(dir, "pwned")
	payloads := []string{
		dir + "/a'; touch " + canary + "; echo '",
		dir + "/a$(touch " + canary + ")",
		dir + "/a`touch " + canary + "`",
		dir + "/a\"; touch " + canary + "; \"",
	}
	for _, path := range payloads {
		cmd, err := TailCommand(path, 10, "", 1024)
		if err != nil {
			continue
		}
		runShell(t, cmd) // 文件不存在 → 非 0，但绝不能产生副作用
		if _, statErr := os.Stat(canary); statErr == nil {
			t.Fatalf("注入生效！path=%q", path)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
