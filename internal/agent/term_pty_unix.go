//go:build !windows

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// terminalKillTimeout 是结束进程组时等待进程退出的时间：先 SIGHUP，
// 超过该时间再 SIGKILL 兜底。
const terminalKillTimeout = 2 * time.Second

// unixTerminal 是基于 creack/pty 的 shell 进程封装。
type unixTerminal struct {
	master *os.File
	cmd    *exec.Cmd
	// waitDone 在 shell 进程退出（cmd.Wait 返回）后关闭。
	waitDone chan struct{}
}

// startTerminalProcess 在 Unix 上通过 PTY 启动登录 shell。
//
// 关键点：显式 setsid（新会话 + 新进程组，pgid == pid）。
// pty 启动的交互式 shell 会 fork 出子进程，只有让 shell 成为进程组首进程，
// 关闭会话时才能用 kill(-pid) 一次性结束整个进程组，避免孤儿进程。
func startTerminalProcess(shell string, cols, rows int) (terminalProcess, error) {
	cmd := exec.Command(shell, "-l")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		cmd.Dir = home
	}

	// 显式 setsid + 设置控制终端：Ctrl-C / SIGHUP 等信号语义与真实终端一致。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}

	master, err := pty.StartWithAttrs(
		cmd,
		&pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)},
		cmd.SysProcAttr,
	)
	if err != nil {
		return nil, fmt.Errorf("启动 pty: %w", err)
	}

	t := &unixTerminal{master: master, cmd: cmd, waitDone: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(t.waitDone)
	}()
	return t, nil
}

// Read 从 PTY 主端读取 shell 输出。
func (t *unixTerminal) Read(p []byte) (int, error) {
	return t.master.Read(p)
}

// Write 向 PTY 主端写入用户输入。
func (t *unixTerminal) Write(p []byte) (int, error) {
	return t.master.Write(p)
}

// Pid 返回 shell 进程 PID（也是进程组 ID）。
func (t *unixTerminal) Pid() int {
	if t.cmd.Process == nil {
		return 0
	}
	return t.cmd.Process.Pid
}

// Resize 调整 PTY 窗口大小。
func (t *unixTerminal) Resize(cols, rows int) error {
	return pty.Setsize(t.master, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Kill 结束整个会话，保证不留下孤儿 shell。
//
// 交互式 shell 开启 job control 后，前台/后台作业会被放进**独立的进程组**，
// 因此只 kill(-shellPID) 并不够。这里的清理顺序是：
//  1. 先快照 shell 的所有后代 PID（shell 退出后这些进程会被 reparent，无法再找回）；
//  2. 向 shell 进程组发 SIGHUP，并关闭 PTY 主端（内核对终端前台进程组发 SIGHUP）；
//  3. 对快照到的后代逐个 SIGKILL（覆盖已进入独立进程组的作业）；
//  4. 等 shell 退出，超时则 SIGKILL 进程组；
//  5. 再补一次进程组 SIGKILL 与后代快照兜底。
func (t *unixTerminal) Kill() error {
	pid := t.Pid()
	if pid <= 0 {
		_ = t.master.Close()
		return nil
	}

	// 1) 快照后代：交互式 shell 的作业可能位于独立进程组，不能在 shell 退出后找回。
	descendants := listDescendants(pid)

	// 2) SIGHUP 进程组 + 关闭 master（终端挂断会向前台进程组发 SIGHUP）。
	_ = syscall.Kill(-pid, syscall.SIGHUP)
	_ = t.master.Close()

	// 3) 强制结束快照到的后代（含已进入独立进程组的作业）。
	for _, child := range descendants {
		_ = syscall.Kill(child, syscall.SIGKILL)
	}

	// 4) 等待 shell 退出，超时则 SIGKILL 进程组。
	select {
	case <-t.waitDone:
	case <-time.After(terminalKillTimeout):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		select {
		case <-t.waitDone:
		case <-time.After(terminalKillTimeout):
		}
	}

	// 5) 兜底：进程组 + 最新后代快照再清一次。
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	for _, child := range listDescendants(pid) {
		_ = syscall.Kill(child, syscall.SIGKILL)
	}
	return nil
}

// listDescendants 通过 `ps -axo pid=,ppid=` 快照某个进程的全部后代 PID。
// 某些精简系统可能没有 ps，此时返回空列表，进程组清理仍然生效。
func listDescendants(root int) []int {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil
	}
	children := make(map[int][]int)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}

	seen := make(map[int]struct{})
	var walk func(parent int)
	walk = func(parent int) {
		for _, child := range children[parent] {
			if _, ok := seen[child]; ok {
				continue
			}
			seen[child] = struct{}{}
			walk(child)
		}
	}
	walk(root)

	pids := make([]int, 0, len(seen))
	for pid := range seen {
		pids = append(pids, pid)
	}
	return pids
}
