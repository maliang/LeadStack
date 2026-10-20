package sshx_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maliang/leadstack/internal/devssh"
	"github.com/maliang/leadstack/internal/sshx"
)

// m5SyncBuf 让测试可以一边读终端一边断言。
type m5SyncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *m5SyncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *m5SyncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func waitContains(t *testing.T, buf *m5SyncBuf, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), substr) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待终端输出 %q 超时，当前输出：%q", substr, buf.String())
}

func newPTY(t *testing.T, opts sshx.PTYOptions) (*sshx.PTYSession, *m5SyncBuf, <-chan error) {
	t.Helper()
	p, err := sshx.PTY(context.Background(), opts)
	if err != nil {
		t.Fatalf("PTY: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	buf := &m5SyncBuf{}
	readErr := make(chan error, 1)
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := p.Read(b)
			if n > 0 {
				_, _ = buf.Write(b[:n])
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()
	return p, buf, readErr
}

func ptyTestOptions(t *testing.T, srv *devssh.Server) sshx.PTYOptions {
	t.Helper()
	return sshx.PTYOptions{
		DialOptions: passwordOptions(t, srv, filepath.Join(t.TempDir(), "known_hosts")),
		Term:        "xterm-256color",
		Cols:        100,
		Rows:        30,
	}
}

func TestPTYInteractiveShell(t *testing.T) {
	srv := startDevssh(t, devssh.Config{})
	p, buf, readErr := newPTY(t, ptyTestOptions(t, srv))

	if p.ServerVersion() == "" || !strings.HasPrefix(p.HostKeyFingerprint(), "SHA256:") {
		t.Fatalf("PTY 元信息异常: version=%q fp=%q", p.ServerVersion(), p.HostKeyFingerprint())
	}

	if _, err := p.Write([]byte("echo pty-ok\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitContains(t, buf, "pty-ok", 10*time.Second)

	// stderr 必须合并进同一终端流（伪终端语义）。
	if _, err := p.Write([]byte("echo stderr-merged >&2\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitContains(t, buf, "stderr-merged", 10*time.Second)

	// resize 不应报错也不应中断会话。
	if err := p.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if _, err := p.Write([]byte("stty size 2>/dev/null || true\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitContains(t, buf, "40 120", 10*time.Second)

	if _, err := p.Write([]byte("exit 0\n")); err != nil {
		t.Fatalf("Write exit: %v", err)
	}
	select {
	case err := <-readErr:
		if err != nil && !errors.Is(err, io.EOF) {
			// 远端 shell 正常退出时允许 io.EOF；其它错误视为失败。
			var exitErr interface{ ExitStatus() int }
			if !errors.As(err, &exitErr) {
				t.Fatalf("shell 退出时 Read 返回异常错误: %v", err)
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shell 退出后 Read 未被唤醒")
	}
}

func TestPTYContextCancelWakesRead(t *testing.T) {
	srv := startDevssh(t, devssh.Config{})
	opts := ptyTestOptions(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	p, err := sshx.PTY(ctx, opts)
	if err != nil {
		t.Fatalf("PTY: %v", err)
	}
	defer p.Close()

	readErr := make(chan error, 1)
	go func() {
		b := make([]byte, 1024)
		for {
			if _, err := p.Read(b); err != nil {
				readErr <- err
				return
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-readErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ctx 取消应唤醒 Read 并返回 context.Canceled，实际: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ctx 取消后 Read 未被唤醒（goroutine 泄漏）")
	}
}

func TestPTYCloseWakesRead(t *testing.T) {
	srv := startDevssh(t, devssh.Config{})
	p, _, readErr := newPTY(t, ptyTestOptions(t, srv))

	time.Sleep(100 * time.Millisecond)
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("Close 后 Read 应返回错误")
		}
		if !errors.Is(err, sshx.ErrSessionClosed) && !errors.Is(err, io.EOF) {
			t.Fatalf("Close 后 Read 错误不明确: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close 后 Read 未被唤醒")
	}
	if err := p.Resize(80, 24); err == nil {
		t.Fatal("关闭后的 Resize 应返回错误")
	}
}

func TestPTYServerDisconnectWakesRead(t *testing.T) {
	srv := startDevssh(t, devssh.Config{})
	opts := ptyTestOptions(t, srv)

	p, err := sshx.PTY(context.Background(), opts)
	if err != nil {
		t.Fatalf("PTY: %v", err)
	}
	defer p.Close()

	readErr := make(chan error, 1)
	go func() {
		b := make([]byte, 1024)
		for {
			if _, err := p.Read(b); err != nil {
				readErr <- err
				return
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	// 模拟连接断开（服务端整体关闭），Read 必须被唤醒而不是永久阻塞。
	if err := srv.Close(); err != nil {
		t.Fatalf("关闭 devssh: %v", err)
	}
	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("连接断开后 Read 应返回错误")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("连接断开后 Read 未被唤醒（goroutine 泄漏）")
	}
}

func TestRunStreamChunks(t *testing.T) {
	srv := startDevssh(t, devssh.Config{})
	c := dialOK(t, passwordOptions(t, srv, filepath.Join(t.TempDir(), "known_hosts")))

	var (
		mu     sync.Mutex
		chunks []string
	)
	stdout, stderr, code, err := c.RunStream(context.Background(),
		`printf 'out-1'; sleep 0.15; printf 'out-2'; printf 'err-1' >&2; exit 5`,
		func(stream, chunk string) {
			mu.Lock()
			chunks = append(chunks, stream+":"+chunk)
			mu.Unlock()
		})
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if code != 5 {
		t.Fatalf("exitCode = %d, 期望 5", code)
	}
	if stdout != "out-1out-2" {
		t.Fatalf("stdout = %q", stdout)
	}
	if stderr != "err-1" {
		t.Fatalf("stderr = %q", stderr)
	}

	mu.Lock()
	defer mu.Unlock()
	var joinedOut, joinedErr strings.Builder
	for _, chunk := range chunks {
		switch {
		case strings.HasPrefix(chunk, "stdout:"):
			joinedOut.WriteString(strings.TrimPrefix(chunk, "stdout:"))
		case strings.HasPrefix(chunk, "stderr:"):
			joinedErr.WriteString(strings.TrimPrefix(chunk, "stderr:"))
		default:
			t.Fatalf("非法 stream 标记: %q", chunk)
		}
	}
	if joinedOut.String() != "out-1out-2" || joinedErr.String() != "err-1" {
		t.Fatalf("回调分块拼接不一致: out=%q err=%q", joinedOut.String(), joinedErr.String())
	}
}

func TestRunStreamContextCancel(t *testing.T) {
	srv := startDevssh(t, devssh.Config{})
	c := dialOK(t, passwordOptions(t, srv, filepath.Join(t.TempDir(), "known_hosts")))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, code, err := c.RunStream(ctx, "sleep 5", nil)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ctx 取消应返回 DeadlineExceeded，实际: %v", err)
	}
	if code != -1 {
		t.Fatalf("取消时 exitCode = %d, 期望 -1", code)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("RunStream 取消未及时生效，耗时 %v", elapsed)
	}
}

func newSFTP(t *testing.T, srv *devssh.Server) *sshx.SFTPSession {
	t.Helper()
	opts := sshx.SFTPOptions{DialOptions: passwordOptions(t, srv, filepath.Join(t.TempDir(), "known_hosts"))}
	s, err := sshx.SFTP(context.Background(), opts)
	if err != nil {
		t.Fatalf("SFTP: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSFTPBinaryRoundTripAndList(t *testing.T) {
	srv := startDevssh(t, devssh.Config{})
	s := newSFTP(t, srv)

	base := t.TempDir()
	upDir := filepath.Join(base, "upload")
	if err := s.Mkdir(context.Background(), upDir); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	content := make([]byte, 64*1024)
	if _, err := rand.Read(content); err != nil {
		t.Fatalf("生成随机内容: %v", err)
	}
	content[0] = 0x00
	content[1] = 0xFF
	content[2] = 0x00

	filePath := filepath.Join(upDir, "payload.bin")
	n, err := s.Write(context.Background(), filePath, bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != int64(len(content)) {
		t.Fatalf("写入字节数 = %d", n)
	}

	// 二进制往返必须逐字节一致。
	got, err := s.Read(context.Background(), filePath)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("二进制内容不一致（got %d, want %d）", len(got), len(content))
	}

	// 流式 Open 也必须一致。
	rc, err := s.Open(context.Background(), filePath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	streamed, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("ReadAll(Open): %v", err)
	}
	if !bytes.Equal(streamed, content) {
		t.Fatal("流式读取内容不一致")
	}

	// Stat：文件、大小、mtime、mode。
	info, err := s.Stat(context.Background(), filePath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.IsDir || info.Size != int64(len(content)) || info.Name != "payload.bin" {
		t.Fatalf("Stat 结果异常: %+v", info)
	}
	if info.ModTime <= 0 || info.Mode == "" {
		t.Fatalf("Stat 缺少 mtime/mode: %+v", info)
	}

	// List：文件条目必须 IsDir=false 且带 size/mtime/mode。
	fileEntries, err := s.List(context.Background(), upDir)
	if err != nil {
		t.Fatalf("List(upDir): %v", err)
	}
	if len(fileEntries) != 1 || fileEntries[0].Name != "payload.bin" || fileEntries[0].IsDir {
		t.Fatalf("List 文件条目异常: %+v", fileEntries)
	}
	if fileEntries[0].Size != int64(len(content)) || fileEntries[0].ModTime <= 0 || fileEntries[0].Mode == "" {
		t.Fatalf("List 缺少 size/mtime/mode: %+v", fileEntries[0])
	}
	if fileEntries[0].Path != filePath {
		t.Fatalf("List 条目 Path = %q, 期望 %q", fileEntries[0].Path, filePath)
	}

	// List：目录与文件要能区分。
	if err := s.Mkdir(context.Background(), filepath.Join(base, "zdir")); err != nil {
		t.Fatalf("Mkdir zdir: %v", err)
	}
	entries, err := s.List(context.Background(), base)
	if err != nil {
		t.Fatalf("List(base): %v", err)
	}
	if len(entries) != 2 || !entries[0].IsDir || !entries[1].IsDir {
		t.Fatalf("List 未区分目录: %+v", entries)
	}
	if entries[0].Name != "upload" || entries[1].Name != "zdir" {
		t.Fatalf("List 排序异常: %+v", entries)
	}

	if err := s.Remove(context.Background(), filePath); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := s.Remove(context.Background(), upDir); err != nil {
		t.Fatalf("Remove(dir): %v", err)
	}
	if err := s.Remove(context.Background(), filepath.Join(base, "zdir")); err != nil {
		t.Fatalf("Remove(zdir): %v", err)
	}
	entries, err = s.List(context.Background(), base)
	if err != nil {
		t.Fatalf("List(empty): %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("删除后目录非空: %+v", entries)
	}
}

func TestSFTPErrorsAndContext(t *testing.T) {
	srv := startDevssh(t, devssh.Config{})
	s := newSFTP(t, srv)
	missing := filepath.Join(t.TempDir(), "missing.bin")

	if _, err := s.Stat(context.Background(), missing); err == nil {
		t.Fatal("Stat 不存在的文件应报错")
	}
	if _, err := s.Read(context.Background(), missing); err == nil {
		t.Fatal("Read 不存在的文件应报错")
	}
	if _, err := s.Write(context.Background(), filepath.Join(t.TempDir(), "nodir", "x"), bytes.NewReader([]byte("x")), 1); err == nil {
		t.Fatal("写入不存在目录应报错")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.List(ctx, "/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消 ctx 的 List 应返回 context.Canceled，实际: %v", err)
	}

	if s.ServerVersion() == "" || !strings.HasPrefix(s.HostKeyFingerprint(), "SHA256:") {
		t.Fatalf("SFTP 元信息异常: version=%q fp=%q", s.ServerVersion(), s.HostKeyFingerprint())
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.List(context.Background(), "/"); err == nil {
		t.Fatal("关闭后的 SFTP 操作应报错")
	}
}

func TestSFTPWriteSizeMismatchCleansUp(t *testing.T) {
	srv := startDevssh(t, devssh.Config{})
	s := newSFTP(t, srv)
	dir := t.TempDir()
	path := filepath.Join(dir, "size-mismatch.bin")

	// 声明 size=100 但实际只写 5 字节：应报错并清理半成品。
	if _, err := s.Write(context.Background(), path, bytes.NewReader([]byte("hello")), 100); err == nil {
		t.Fatal("声明大小与实际不符应报错")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("失败的上传应清理半成品: %v", err)
	}
}
