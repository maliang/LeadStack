//go:build !windows

package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/maliang/leadstack/pkg/protocol"
)

// waitPidFile 等待 shell/子进程把 PID 写入文件后返回该 PID。
func waitPidFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待 PID 文件 %s 超时", path)
	return 0
}

// processAlive 判断 PID 是否仍存在。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// processGroupAlive 判断进程组是否仍有成员。
func processGroupAlive(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// waitProcessGone 轮询等待进程退出。
func waitProcessGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("进程 %d 仍残留（孤儿进程）", pid)
}

// waitProcessGroupGone 轮询等待整个进程组消失。
func waitProcessGroupGone(t *testing.T, pgid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processGroupAlive(pgid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("进程组 %d 仍残留（孤儿进程组）", pgid)
}

// terminalResult 汇总 mock 控制端从 agent 收到的 term.data 文本。
type terminalResult struct {
	mu   sync.Mutex
	text strings.Builder
}

func (r *terminalResult) append(s string) {
	r.mu.Lock()
	r.text.WriteString(s)
	r.mu.Unlock()
}

func (r *terminalResult) contains(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Contains(r.text.String(), s)
}

func (r *terminalResult) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.text.String()
}

// decodeTermText 解码一条 term.data 载荷的文本内容。
func decodeTermText(t *testing.T, payload json.RawMessage) (string, string) {
	t.Helper()
	var d protocol.TermData
	if err := json.Unmarshal(payload, &d); err != nil {
		return "", ""
	}
	raw, err := base64.StdEncoding.DecodeString(d.B64)
	if err != nil {
		return d.SessionID, ""
	}
	return d.SessionID, string(raw)
}

// readTermUntil 持续读取帧并累积 term.data 文本，直到出现 want 或超时。
func readTermUntil(t *testing.T, conn *websocket.Conn, result *terminalResult, want string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		env, ok := readEnvelope(conn, time.Until(deadline))
		if !ok {
			return false
		}
		if env.Type != protocol.TypeTermData {
			continue
		}
		_, text := decodeTermText(t, env.Payload)
		result.append(text)
		if strings.Contains(text, want) || result.contains(want) {
			return true
		}
	}
	return false
}

// TestTerminalOpenDataClose 验证真实 PTY 会话：打开 → 输入命令 → 读到输出 →
// term.close 后 shell 及其子进程组全部被清理（无孤儿进程）。
func TestTerminalOpenDataClose(t *testing.T) {
	dir := t.TempDir()
	shellPidFile := filepath.Join(dir, "shell.pid")
	childPidFile := filepath.Join(dir, "child.pid")
	const marker = "M5_TERM_READY_MARKER"

	var result terminalResult
	serverErr := make(chan error, 4)
	flowDone := make(chan struct{})
	markerSeen := make(chan struct{})
	allowClose := make(chan struct{})

	srv := newMockServer(t, func(conn *websocket.Conn, _ int) {
		env, ok := readEnvelope(conn, 5*time.Second)
		if !ok || env.Type != protocol.TypeHello {
			serverErr <- fmt.Errorf("首帧不是 hello")
			return
		}
		if err := sendHelloAck(conn, 20, 15); err != nil {
			serverErr <- err
			return
		}
		if err := writeEnvelope(conn, protocol.TypeTermOpen, protocol.TermOpen{
			SessionID: "term-1", Cols: 120, Rows: 40, Shell: "/bin/sh",
		}); err != nil {
			serverErr <- err
			return
		}
		// 等 shell 就绪后输入：记录 shell PID、启动一个后台子进程记录其 PID，
		// 再启动前台长命令（sleep 300）。后台/前台作业在交互式 shell 下通常位于
		// 独立进程组，正好覆盖「只 kill 主进程组会留孤儿」的坑。
		time.Sleep(300 * time.Millisecond)
		input := fmt.Sprintf(
			"echo $$ > %q; sleep 300 & echo $! > %q; echo %s; sleep 300\n",
			shellPidFile, childPidFile, marker)
		if err := writeEnvelope(conn, protocol.TypeTermData, protocol.TermData{
			SessionID: "term-1",
			B64:       base64.StdEncoding.EncodeToString([]byte(input)),
		}); err != nil {
			serverErr <- err
			return
		}
		if !readTermUntil(t, conn, &result, marker, 10*time.Second) {
			serverErr <- fmt.Errorf("未读到终端输出标记")
			return
		}
		// 等测试侧确认 shell/子进程都还活着，再发 term.close。
		close(markerSeen)
		select {
		case <-allowClose:
		case <-time.After(10 * time.Second):
			return
		}
		if err := writeEnvelope(conn, protocol.TypeTermClose, protocol.TermClose{SessionID: "term-1"}); err != nil {
			serverErr <- err
			return
		}
		time.Sleep(300 * time.Millisecond)
		close(flowDone)
	})

	a := New(Config{Server: srv.URL, Token: "test-token", Version: "test", Logger: testLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx) }()

	select {
	case <-markerSeen:
	case <-time.After(20 * time.Second):
		t.Fatal("终端会话建立/输出读取超时")
	}

	shellPid := waitPidFile(t, shellPidFile)
	childPid := waitPidFile(t, childPidFile)
	if shellPid <= 1 || childPid <= 1 {
		t.Fatalf("PID 异常：shell=%d child=%d", shellPid, childPid)
	}
	// 关闭之前两者都必须真的活着，证明后面的清理不是空验证。
	if !processAlive(shellPid) {
		t.Fatalf("会话期间 shell（pid=%d）已不存在", shellPid)
	}
	if !processAlive(childPid) {
		t.Fatalf("会话期间后台子进程（pid=%d）已不存在", childPid)
	}
	childPgid, _ := syscall.Getpgid(childPid)
	t.Logf("shell pid=%d，后台子进程 pid=%d pgid=%d（验证是否独立进程组）", shellPid, childPid, childPgid)
	close(allowClose)
	select {
	case <-flowDone:
	case <-time.After(20 * time.Second):
		t.Fatal("term.close 后会话未结束")
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(10 * time.Second):
		t.Fatal("agent.Run 未在取消后退出")
	}

	waitProcessGone(t, shellPid, 5*time.Second)
	waitProcessGone(t, childPid, 5*time.Second)
	waitProcessGroupGone(t, shellPid, 5*time.Second)
	if childPgid > 0 && childPgid != shellPid {
		// 交互式 shell 给后台作业建了独立进程组，这个组也必须被清理。
		waitProcessGroupGone(t, childPgid, 5*time.Second)
	}
	for _, pid := range listDescendants(shellPid) {
		if processAlive(pid) {
			t.Errorf("shell 仍有残留后代进程：%d", pid)
		}
	}

	select {
	case err := <-serverErr:
		t.Error(err)
	default:
	}
}

// TestTerminalBusy 验证同一时刻只允许一个终端会话，第二个请求收到 busy 提示。
func TestTerminalBusy(t *testing.T) {
	var result terminalResult
	busyCh := make(chan struct{}, 1)
	flowDone := make(chan struct{})

	srv := newMockServer(t, func(conn *websocket.Conn, _ int) {
		env, ok := readEnvelope(conn, 5*time.Second)
		if !ok || env.Type != protocol.TypeHello {
			return
		}
		if err := sendHelloAck(conn, 20, 15); err != nil {
			return
		}
		if err := writeEnvelope(conn, protocol.TypeTermOpen, protocol.TermOpen{
			SessionID: "sess-1", Cols: 80, Rows: 24, Shell: "/bin/sh",
		}); err != nil {
			return
		}
		time.Sleep(300 * time.Millisecond)
		if err := writeEnvelope(conn, protocol.TypeTermOpen, protocol.TermOpen{
			SessionID: "sess-2", Cols: 80, Rows: 24, Shell: "/bin/sh",
		}); err != nil {
			return
		}
		if readTermUntil(t, conn, &result, "已有终端会话", 8*time.Second) {
			select {
			case busyCh <- struct{}{}:
			default:
			}
		}
		_ = writeEnvelope(conn, protocol.TypeTermClose, protocol.TermClose{SessionID: "sess-1"})
		time.Sleep(200 * time.Millisecond)
		close(flowDone)
	})

	a := New(Config{Server: srv.URL, Token: "test-token", Version: "test", Logger: testLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx) }()

	select {
	case <-busyCh:
	case <-time.After(15 * time.Second):
		t.Fatalf("第二个 term.open 未收到 busy 提示，已收到输出：%q", result.String())
	}
	cancel()
	select {
	case <-runErr:
	case <-time.After(10 * time.Second):
		t.Fatal("agent.Run 未在取消后退出")
	}
	select {
	case <-flowDone:
	default:
	}
}

// TestTerminalIdleTimeout 验证空闲超时会自动关闭会话并结束进程组。
func TestTerminalIdleTimeout(t *testing.T) {
	oldTimeout := terminalIdleTimeout
	terminalIdleTimeout = 400 * time.Millisecond
	defer func() { terminalIdleTimeout = oldTimeout }()

	pidFile := filepath.Join(t.TempDir(), "idle.pid")
	var result terminalResult
	idleClosed := make(chan struct{})

	srv := newMockServer(t, func(conn *websocket.Conn, _ int) {
		env, ok := readEnvelope(conn, 5*time.Second)
		if !ok || env.Type != protocol.TypeHello {
			return
		}
		if err := sendHelloAck(conn, 20, 15); err != nil {
			return
		}
		if err := writeEnvelope(conn, protocol.TypeTermOpen, protocol.TermOpen{
			SessionID: "idle-1", Cols: 80, Rows: 24, Shell: "/bin/sh",
		}); err != nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
		_ = writeEnvelope(conn, protocol.TypeTermData, protocol.TermData{
			SessionID: "idle-1",
			B64:       base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("echo $$ > %q\n", pidFile))),
		})
		if readTermUntil(t, conn, &result, "空闲", 10*time.Second) {
			close(idleClosed)
		}
		time.Sleep(150 * time.Millisecond)
	})

	a := New(Config{Server: srv.URL, Token: "test-token", Version: "test", Logger: testLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx) }()

	select {
	case <-idleClosed:
	case <-time.After(15 * time.Second):
		t.Fatalf("空闲会话未自动关闭，已收到输出：%q", result.String())
	}
	pid := waitPidFile(t, pidFile)
	waitProcessGone(t, pid, 5*time.Second)
	waitProcessGroupGone(t, pid, 5*time.Second)

	cancel()
	select {
	case <-runErr:
	case <-time.After(10 * time.Second):
		t.Fatal("agent.Run 未在取消后退出")
	}
}

// TestTerminalConnCloseKillsProcessGroup 验证控制端连接断开（WS 关闭）时，
// 即使没有 term.close，agent 也会结束整个 shell 会话，不留孤儿进程。
func TestTerminalConnCloseKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	shellPidFile := filepath.Join(dir, "shell.pid")
	childPidFile := filepath.Join(dir, "child.pid")
	var result terminalResult
	firstConnReady := make(chan struct{})
	closeNow := make(chan struct{})
	block := make(chan struct{})
	defer close(block)

	srv := newMockServer(t, func(conn *websocket.Conn, connNo int) {
		if connNo > 1 {
			// 后续重连保持静默，避免反复触发新的终端。
			<-block
			return
		}
		env, ok := readEnvelope(conn, 5*time.Second)
		if !ok || env.Type != protocol.TypeHello {
			return
		}
		if err := sendHelloAck(conn, 20, 15); err != nil {
			return
		}
		if err := writeEnvelope(conn, protocol.TypeTermOpen, protocol.TermOpen{
			SessionID: "conn-1", Cols: 80, Rows: 24, Shell: "/bin/sh",
		}); err != nil {
			return
		}
		time.Sleep(300 * time.Millisecond)
		input := fmt.Sprintf(
			"echo $$ > %q; sleep 300 & echo $! > %q; echo CONN_CLOSE_READY; sleep 300\n",
			shellPidFile, childPidFile)
		if err := writeEnvelope(conn, protocol.TypeTermData, protocol.TermData{
			SessionID: "conn-1",
			B64:       base64.StdEncoding.EncodeToString([]byte(input)),
		}); err != nil {
			return
		}
		if !readTermUntil(t, conn, &result, "CONN_CLOSE_READY", 10*time.Second) {
			return
		}
		// 等测试侧确认进程都还活着后，再返回（handler 返回即关闭 WS 连接）。
		close(firstConnReady)
		select {
		case <-closeNow:
		case <-time.After(10 * time.Second):
		}
	})

	a := New(Config{Server: srv.URL, Token: "test-token", Version: "test", Logger: testLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx) }()

	select {
	case <-firstConnReady:
	case <-time.After(20 * time.Second):
		t.Fatal("等待终端会话建立超时")
	}
	shellPid := waitPidFile(t, shellPidFile)
	childPid := waitPidFile(t, childPidFile)
	if !processAlive(shellPid) || !processAlive(childPid) {
		t.Fatalf("断连前进程应存活：shell=%d alive=%v，child=%d alive=%v",
			shellPid, processAlive(shellPid), childPid, processAlive(childPid))
	}
	childPgid, _ := syscall.Getpgid(childPid)
	t.Logf("断连前 shell pid=%d，子进程 pid=%d pgid=%d", shellPid, childPid, childPgid)

	// 关闭 WS 连接（handler 返回），不取消 agent ctx；
	// 仅靠 serve 退出时的 CloseAll 清理，验证「WS 断开 → 无孤儿进程」。
	close(closeNow)
	waitProcessGone(t, shellPid, 8*time.Second)
	waitProcessGone(t, childPid, 8*time.Second)
	waitProcessGroupGone(t, shellPid, 8*time.Second)
	if childPgid > 0 && childPgid != shellPid {
		waitProcessGroupGone(t, childPgid, 8*time.Second)
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(10 * time.Second):
		t.Fatal("agent.Run 未在取消后退出")
	}
}
