package devssh

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// syncBuffer 让测试可以一边读一边断言。
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func waitForOutput(t *testing.T, buf *syncBuffer, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), substr) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待输出 %q 超时，当前输出：%q", substr, buf.String())
}

func TestPTYShell(t *testing.T) {
	srv := startServer(t, Config{})
	client := mustDialRaw(t, srv.Addr(), "devuser", ssh.Password("devpass"))

	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if err := sess.RequestPty("xterm-256color", 30, 100, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		t.Fatalf("RequestPty: %v", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	out := &syncBuffer{}
	sess.Stdout = out
	if err := sess.Shell(); err != nil {
		t.Fatalf("Shell: %v", err)
	}

	if _, err := fmt.Fprintf(stdin, "echo pty-ok\n"); err != nil {
		t.Fatalf("写 stdin: %v", err)
	}
	waitForOutput(t, out, "pty-ok", 5*time.Second)

	// stderr 必须合并进终端流。
	if _, err := fmt.Fprintf(stdin, "echo merged-stderr >&2\n"); err != nil {
		t.Fatalf("写 stdin: %v", err)
	}
	waitForOutput(t, out, "merged-stderr", 5*time.Second)

	// window-change 不应中断会话。
	if err := sess.WindowChange(40, 120); err != nil {
		t.Fatalf("WindowChange: %v", err)
	}
	if _, err := fmt.Fprintf(stdin, "echo after-resize\n"); err != nil {
		t.Fatalf("写 stdin: %v", err)
	}
	waitForOutput(t, out, "after-resize", 5*time.Second)

	if _, err := fmt.Fprintf(stdin, "exit 0\n"); err != nil {
		t.Fatalf("写 stdin: %v", err)
	}
	if err := sess.Wait(); err != nil {
		t.Fatalf("shell 退出异常: %v", err)
	}
}

func TestPTYExecCommand(t *testing.T) {
	srv := startServer(t, Config{})
	client := mustDialRaw(t, srv.Addr(), "devuser", ssh.Password("devpass"))

	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	if err := sess.RequestPty("xterm", 24, 80, nil); err != nil {
		t.Fatalf("RequestPty: %v", err)
	}
	out, err := sess.Output("printf 'pty-exec-ok'; printf 'pty-exec-err' >&2")
	if err != nil {
		t.Fatalf("Run under pty: %v", err)
	}
	if !strings.Contains(string(out), "pty-exec-ok") || !strings.Contains(string(out), "pty-exec-err") {
		t.Fatalf("pty 下 exec 输出异常: %q", string(out))
	}
}

func TestSFTPSubsystem(t *testing.T) {
	srv := startServer(t, Config{})
	sshClient := mustDialRaw(t, srv.Addr(), "devuser", ssh.Password("devpass"))

	sc, err := sftp.NewClient(sshClient)
	if err != nil {
		t.Fatalf("创建 SFTP 客户端失败（子系统未启用？）: %v", err)
	}
	defer sc.Close()

	localDir := t.TempDir()
	subDir := filepath.Join(localDir, "sub")
	if err := sc.Mkdir(subDir); err != nil {
		t.Fatalf("SFTP Mkdir: %v", err)
	}
	remoteFile := filepath.Join(subDir, "payload.bin")
	content := make([]byte, 32*1024)
	rng := rand.New(rand.NewSource(42))
	for i := range content {
		content[i] = byte(rng.Intn(256))
	}
	content[0] = 0x00
	content[1] = 0xFF

	f, err := sc.Create(remoteFile)
	if err != nil {
		t.Fatalf("SFTP Create: %v", err)
	}
	if _, err := f.Write(content); err != nil {
		t.Fatalf("SFTP Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("SFTP Close: %v", err)
	}

	// 真实落盘到本机（devssh 与测试同机）。
	onDisk, err := os.ReadFile(remoteFile)
	if err != nil {
		t.Fatalf("读取本机文件失败: %v", err)
	}
	if !bytes.Equal(onDisk, content) {
		t.Fatalf("SFTP 写入内容不一致（%d vs %d 字节）", len(onDisk), len(content))
	}

	fi, err := sc.Stat(remoteFile)
	if err != nil {
		t.Fatalf("SFTP Stat: %v", err)
	}
	if fi.IsDir() || fi.Size() != int64(len(content)) {
		t.Fatalf("SFTP Stat 结果异常: dir=%v size=%d", fi.IsDir(), fi.Size())
	}

	rf, err := sc.Open(remoteFile)
	if err != nil {
		t.Fatalf("SFTP Open: %v", err)
	}
	got, err := io.ReadAll(rf)
	_ = rf.Close()
	if err != nil {
		t.Fatalf("SFTP Read: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("SFTP 读回内容不一致")
	}

	entries, err := sc.ReadDir(subDir)
	if err != nil {
		t.Fatalf("SFTP ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "payload.bin" || entries[0].IsDir() {
		t.Fatalf("SFTP ReadDir 结果异常: %+v", entries)
	}

	if err := sc.Remove(remoteFile); err != nil {
		t.Fatalf("SFTP Remove: %v", err)
	}
	if err := sc.RemoveDirectory(subDir); err != nil {
		t.Fatalf("SFTP RemoveDirectory: %v", err)
	}
}

func TestSFTPBinaryRoundTripLarge(t *testing.T) {
	// 跨多个 SFTP 分包的二进制内容（含全 0 与全 1 字节）。
	srv := startServer(t, Config{})
	sshClient := mustDialRaw(t, srv.Addr(), "devuser", ssh.Password("devpass"))
	sc, err := sftp.NewClient(sshClient)
	if err != nil {
		t.Fatalf("创建 SFTP 客户端: %v", err)
	}
	defer sc.Close()

	content := make([]byte, 256*1024)
	for i := 0; i < len(content); i += 4 {
		binary.BigEndian.PutUint32(content[i:], uint32(i))
	}
	path := filepath.Join(t.TempDir(), "large.bin")
	f, err := sc.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.Write(content); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	f, err = sc.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != len(content) {
		t.Fatalf("长度不一致: %d vs %d", len(got), len(content))
	}
	if !bytes.Equal(got, content) {
		t.Fatal("大文件二进制内容不一致")
	}
}
