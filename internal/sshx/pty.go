package sshx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// DefaultTerm 是 PTY 默认终端类型。
	DefaultTerm = "xterm-256color"
	// DefaultCols / DefaultRows 是 PTY 默认窗口大小。
	DefaultCols = 80
	DefaultRows = 24
)

// ErrSessionClosed 表示会话已被本地主动关闭。
var ErrSessionClosed = errors.New("sshx: 会话已关闭")

// PTYOptions 是打开交互式终端会话的参数。
//
// 连接参数复用 DialOptions；Term/Cols/Rows 为 PTY 属性，均有默认值
// （xterm-256color / 80x24）。
type PTYOptions struct {
	DialOptions
	Term string
	Cols int
	Rows int
}

// PTYSession 是一个带伪终端的交互式 SSH 会话（stdin/stdout/stderr 合并为同一终端流）。
//
// Read/Write 与 term.Session 语义一致；ctx 取消或连接断开会唤醒阻塞中的 Read：
//   - ctx 取消 → Read 返回 ctx.Err()；
//   - 本地 Close → Read 返回 ErrSessionClosed；
//   - 远端 shell 正常退出 → Read 返回 io.EOF；
//   - 远端退出码非 0 → Read 返回 *ssh.ExitError；
//   - 连接断开 → Read 返回具体传输层错误。
type PTYSession struct {
	client      *ssh.Client
	sess        *ssh.Session
	stdin       *io.PipeWriter
	out         *io.PipeReader
	outW        *io.PipeWriter
	version     string
	fingerprint string

	mu        sync.Mutex
	cause     error
	closeOnce sync.Once
	outOnce   sync.Once
	exited    chan struct{}
}

// PTY 打开一个带 pty-req 的交互式 shell 会话。
//
// 安全约束：不接受任意命令，只打开登录 shell（避免调用方把它当 exec 用）。
func PTY(ctx context.Context, opts PTYOptions) (*PTYSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	client, version, fingerprint, err := dialSSH(ctx, opts.DialOptions)
	if err != nil {
		return nil, err
	}

	sess, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("sshx: 创建终端会话失败: %w", err)
	}

	term := opts.Term
	if term == "" {
		term = DefaultTerm
	}
	cols, rows := opts.Cols, opts.Rows
	if cols <= 0 {
		cols = DefaultCols
	}
	if rows <= 0 {
		rows = DefaultRows
	}

	// 申请真正的 pty：否则远端不会分配伪终端，表现为没有回显、行编辑异常，
	// 部分程序（top/vim）直接失败。
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sess.RequestPty(term, rows, cols, modes); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("sshx: 申请 pty-req 失败（远端可能未启用 PTY）: %w", err)
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	sess.Stdin = inR
	// 终端语义：stderr 必须合并进同一个流，否则用户看不到错误输出。
	sess.Stdout = outW
	sess.Stderr = outW

	p := &PTYSession{
		client:      client,
		sess:        sess,
		stdin:       inW,
		out:         outR,
		outW:        outW,
		version:     version,
		fingerprint: fingerprint,
		exited:      make(chan struct{}),
	}

	if err := sess.Shell(); err != nil {
		_ = inW.CloseWithError(err)
		_ = outW.CloseWithError(err)
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("sshx: 启动交互 shell 失败: %w", err)
	}

	go func() {
		waitErr := sess.Wait()
		p.finalize(waitErr)
	}()

	// ctx 取消看门狗：关闭会话以唤醒阻塞中的 Read，并兜底关闭输出管道。
	go func() {
		select {
		case <-ctx.Done():
			p.shutdown(ctx.Err())
		case <-p.exited:
			return
		}
		select {
		case <-p.exited:
		case <-time.After(2 * time.Second):
			p.closeOutput(ctx.Err())
		}
	}()

	return p, nil
}

// finalize 在远端会话结束时收尾：记录原因并关闭输出管道，唤醒所有 Read。
func (p *PTYSession) finalize(waitErr error) {
	p.mu.Lock()
	if p.cause == nil {
		p.cause = waitErr
	}
	cause := p.cause
	p.mu.Unlock()

	// shell 结束后输入方向也立即失效，避免 Write 阻塞。
	_ = p.stdin.CloseWithError(cause)
	p.closeOutput(cause)
	close(p.exited)
}

// closeOutput 只关闭一次输出管道；err 为 nil/io.EOF 时表现为正常 EOF。
func (p *PTYSession) closeOutput(err error) {
	p.outOnce.Do(func() {
		if err == nil || errors.Is(err, io.EOF) {
			_ = p.outW.Close()
			return
		}
		_ = p.outW.CloseWithError(err)
	})
}

// shutdown 主动关闭会话（幂等）。
func (p *PTYSession) shutdown(cause error) {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		if p.cause == nil {
			p.cause = cause
		}
		p.mu.Unlock()
		_ = p.stdin.CloseWithError(cause)
		_ = p.sess.Close()
		_ = p.client.Close()
	})
}

// Read 读取终端输出（stdout 与 stderr 合并）。
func (p *PTYSession) Read(b []byte) (int, error) {
	if p == nil || p.out == nil {
		return 0, ErrSessionClosed
	}
	return p.out.Read(b)
}

// Write 把输入写入终端。
func (p *PTYSession) Write(b []byte) (int, error) {
	if p == nil || p.stdin == nil {
		return 0, ErrSessionClosed
	}
	n, err := p.stdin.Write(b)
	if err != nil {
		if cause := p.errCause(); cause != nil {
			return n, cause
		}
		return n, fmt.Errorf("sshx: 写终端失败: %w", err)
	}
	return n, nil
}

// Resize 调整终端窗口大小（cols x rows）。
func (p *PTYSession) Resize(cols, rows int) error {
	if p == nil || p.sess == nil {
		return ErrSessionClosed
	}
	if cause := p.errCause(); cause != nil {
		return cause
	}
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("sshx: 非法终端尺寸 %dx%d", cols, rows)
	}
	// x/crypto 的 WindowChange 参数顺序是 (height, width)。
	if err := p.sess.WindowChange(rows, cols); err != nil {
		return fmt.Errorf("sshx: 调整终端大小失败: %w", err)
	}
	return nil
}

// Close 关闭终端会话；可重复调用。
func (p *PTYSession) Close() error {
	if p == nil {
		return nil
	}
	p.shutdown(ErrSessionClosed)
	select {
	case <-p.exited:
	case <-time.After(2 * time.Second):
		// 极端情况下（远端无响应）也要唤醒 Read，避免 goroutine 泄漏。
		p.closeOutput(ErrSessionClosed)
	}
	return nil
}

// Done 在会话结束时关闭，便于调用方退出转发循环。
func (p *PTYSession) Done() <-chan struct{} {
	if p == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return p.exited
}

// Err 返回会话结束原因（未结束时为 nil）。
func (p *PTYSession) Err() error {
	if p == nil {
		return ErrSessionClosed
	}
	return p.errCause()
}

func (p *PTYSession) errCause() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cause
}

// ServerVersion 返回远端 SSH 版本串。
func (p *PTYSession) ServerVersion() string {
	if p == nil {
		return ""
	}
	return p.version
}

// HostKeyFingerprint 返回主机密钥指纹 "SHA256:<base64>"。
func (p *PTYSession) HostKeyFingerprint() string {
	if p == nil {
		return ""
	}
	return p.fingerprint
}
