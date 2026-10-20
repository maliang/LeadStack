//go:build unix

package devssh

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	gliderssh "github.com/gliderlabs/ssh"
)

// servePTYShell 在真实伪终端中启动本机 shell（或把 exec 命令跑在 pty 下）。
//
// DEV-ONLY：它把本机 shell 与文件系统直接暴露给测试客户端，绝不可用于生产。
// stderr 由伪终端自然合并进同一输出流，因此客户端看到的是完整终端语义。
func servePTYShell(sess gliderssh.Session, req gliderssh.Pty, winCh <-chan gliderssh.Window) {
	var cmd *exec.Cmd
	if raw := strings.TrimSpace(sess.RawCommand()); raw != "" {
		cmd = exec.Command("sh", "-c", raw)
	} else {
		shell := strings.TrimSpace(os.Getenv("SHELL"))
		if shell == "" {
			shell = "/bin/sh"
		}
		cmd = exec.Command(shell)
	}
	term := strings.TrimSpace(req.Term)
	if term == "" {
		term = "xterm-256color"
	}
	cmd.Env = append(os.Environ(), "TERM="+term)

	f, err := pty.Start(cmd)
	if err != nil {
		_, _ = io.WriteString(sess.Stderr(), "devssh: 启动 PTY shell 失败: "+err.Error()+"\n")
		_ = sess.Exit(127)
		return
	}
	// pty master 会被多个 goroutine 访问（SIGWINCH 处理 / 连接断开 / 收尾），
	// 必须串行化 Close 与 Setsize，否则 -race 会报 os.File.Fd/Close 竞争。
	var (
		fMu     sync.Mutex
		fClosed bool
	)
	closePTY := func() {
		fMu.Lock()
		defer fMu.Unlock()
		if fClosed {
			return
		}
		fClosed = true
		_ = f.Close()
	}
	defer func() {
		closePTY()
		// pty.Start 让子进程成为会话首进程；这里结束整个进程组，避免孤儿 shell。
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}()

	resize := func(cols, rows int) {
		if cols <= 0 || rows <= 0 {
			return
		}
		fMu.Lock()
		defer fMu.Unlock()
		if fClosed {
			return
		}
		_ = pty.Setsize(f, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	}
	resize(req.Window.Width, req.Window.Height)

	done := make(chan struct{})
	defer close(done)
	// window-change 请求：同步到本机 PTY。
	go func() {
		for {
			select {
			case w, ok := <-winCh:
				if !ok {
					return
				}
				resize(w.Width, w.Height)
			case <-done:
				return
			}
		}
	}()

	// 客户端输入 → pty。
	// 注意：不能在这里因为客户端 stdin EOF 就关闭 master —— exec 类客户端
	// 往往发完命令立刻关 stdin，过早关闭会让最后一段 stderr 丢失。
	go func() {
		_, _ = io.Copy(f, sess)
	}()
	// 连接真正断开（会话 context 取消）时才关闭 master，让 shell 收到挂断。
	go func() {
		select {
		case <-sess.Context().Done():
			closePTY()
		case <-done:
		}
	}()
	// pty 输出（stdout + stderr 已合并）→ 客户端；进程退出后 pty 读端返回 EOF/EIO。
	_, _ = io.Copy(sess, f)
	closePTY()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case <-waitCh:
	case <-time.After(3 * time.Second):
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-waitCh
	}

	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	_ = sess.Exit(exitCode)
}
