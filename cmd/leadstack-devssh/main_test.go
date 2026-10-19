package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// lockedBuffer 让测试可以安全地读取 run 正在写入的输出。
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func waitForPort(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			if port, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && port > 0 {
				return port
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待 --port-file %s 超时", path)
	return 0
}

func TestRunServesRealSSHAndGracefulShutdown(t *testing.T) {
	dir := t.TempDir()
	portFile := filepath.Join(dir, "port")

	var stdout, stderr lockedBuffer
	sigCh := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		done <- run([]string{
			"--addr", "127.0.0.1:0",
			"--user", "devuser",
			"--password", "devpass",
			"--port-file", portFile,
		}, &stdout, &stderr, sigCh)
	}()

	port := waitForPort(t, portFile)

	// 横幅必须可解析且含 DEV-ONLY。
	wantPrefix := "DEV-ONLY listening on 127.0.0.1:"
	deadline := time.Now().Add(3 * time.Second)
	var line string
	for time.Now().Before(deadline) {
		line = firstLine(stdout.String())
		if strings.HasPrefix(line, wantPrefix) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.HasPrefix(line, wantPrefix) {
		t.Fatalf("横幅不可解析: %q", stdout.String())
	}
	if line != wantPrefix+strconv.Itoa(port) {
		t.Fatalf("横幅端口与 --port-file 不一致: banner=%q port=%d", line, port)
	}
	if !strings.Contains(stdout.String(), "DEV-ONLY") {
		t.Fatal("横幅应包含 DEV-ONLY 警告")
	}

	// 用真实 SSH 客户端验证 CLI 启动的服务确实可用。
	client, err := ssh.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), &ssh.ClientConfig{
		User:            "devuser",
		Auth:            []ssh.AuthMethod{ssh.Password("devpass")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("连接 devssh 失败: %v", err)
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	var out bytes.Buffer
	sess.Stdout = &out
	if err := sess.Run("printf 'cli-ok'"); err != nil {
		t.Fatalf("远端执行失败: %v", err)
	}
	if out.String() != "cli-ok" {
		t.Fatalf("远端输出 = %q", out.String())
	}
	_ = sess.Close()

	// SIGTERM 应触发优雅关闭并以 0 退出。
	sigCh <- syscall.SIGTERM
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("优雅关闭返回错误: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("收到 SIGTERM 后未在 5s 内退出")
	}
	if !strings.Contains(stderr.String(), "正在关闭") {
		t.Fatalf("stderr 未提示关闭: %q", stderr.String())
	}
}

func TestWritePortFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "port")
	if err := writePortFile(path, 54321); err != nil {
		t.Fatalf("writePortFile: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 port 文件: %v", err)
	}
	if strings.TrimSpace(string(b)) != "54321" {
		t.Fatalf("port 文件内容 = %q", string(b))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("port 文件权限 = %o, 期望 644", perm)
	}
}

func TestRunErrors(t *testing.T) {
	t.Run("authorized-key-file 不存在", func(t *testing.T) {
		var stdout, stderr lockedBuffer
		err := run([]string{"--authorized-key-file", filepath.Join(t.TempDir(), "missing")}, &stdout, &stderr, make(chan os.Signal, 1))
		if err == nil {
			t.Fatal("缺少公钥文件时应返回错误")
		}
	})

	t.Run("authorized-key-file 内容非法", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "keys")
		if err := os.WriteFile(path, []byte("not a public key"), 0o600); err != nil {
			t.Fatalf("写测试文件: %v", err)
		}
		var stdout, stderr lockedBuffer
		if err := run([]string{"--authorized-key-file", path}, &stdout, &stderr, make(chan os.Signal, 1)); err == nil {
			t.Fatal("非法公钥内容应返回错误")
		}
	})

	t.Run("端口被占用", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("占位监听失败: %v", err)
		}
		defer ln.Close()
		var stdout, stderr lockedBuffer
		if err := run([]string{"--addr", ln.Addr().String()}, &stdout, &stderr, make(chan os.Signal, 1)); err == nil {
			t.Fatal("端口被占用时应返回错误")
		}
	})
}
