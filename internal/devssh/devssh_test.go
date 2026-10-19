package devssh

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func startServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	srv, err := Start(cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	if srv.Addr() == "" {
		t.Fatal("Addr 不应为空")
	}
	if _, _, err := net.SplitHostPort(srv.Addr()); err != nil {
		t.Fatalf("Addr 不是合法 host:port: %q", srv.Addr())
	}
	return srv
}

func dialRaw(t *testing.T, addr, user string, auth ...ssh.AuthMethod) (*ssh.Client, error) {
	t.Helper()
	return ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
}

func mustDialRaw(t *testing.T, addr, user string, auth ...ssh.AuthMethod) *ssh.Client {
	t.Helper()
	c, err := dialRaw(t, addr, user, auth...)
	if err != nil {
		t.Fatalf("握手失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func runRaw(t *testing.T, c *ssh.Client, cmd string) (stdout, stderr string, exitCode int) {
	t.Helper()
	sess, err := c.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	var outBuf, errBuf bytes.Buffer
	sess.Stdout = &outBuf
	sess.Stderr = &errBuf
	err = sess.Run(cmd)
	if err == nil {
		return outBuf.String(), errBuf.String(), 0
	}
	var exitErr *ssh.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Run(%q) 传输层失败: %v", cmd, err)
	}
	return outBuf.String(), errBuf.String(), exitErr.ExitStatus()
}

func newClientKey(t *testing.T) (ssh.Signer, []byte, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成客户端密钥失败: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("NewSignerFromKey: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("MarshalPrivateKey: %v", err)
	}
	authorized := ssh.MarshalAuthorizedKey(signer.PublicKey())
	return signer, pem.EncodeToMemory(block), authorized
}

func TestDefaultsAndRealExec(t *testing.T) {
	srv := startServer(t, Config{})

	c := mustDialRaw(t, srv.Addr(), "devuser", ssh.Password("devpass"))
	if v := string(c.ServerVersion()); !strings.HasPrefix(v, "SSH-2.0-") {
		t.Fatalf("ServerVersion = %q", v)
	}

	// 真实 sh -c：stdout / stderr / 退出码都要保真。
	stdout, stderr, code := runRaw(t, c, `printf 'hello'; printf 'oops' >&2; exit 7`)
	if stdout != "hello" {
		t.Fatalf("stdout = %q", stdout)
	}
	if stderr != "oops" {
		t.Fatalf("stderr = %q", stderr)
	}
	if code != 7 {
		t.Fatalf("exit code = %d, 期望 7", code)
	}

	// 普通成功命令。
	stdout, stderr, code = runRaw(t, c, `echo "$((1+2))"`)
	if stdout != "3\n" || stderr != "" || code != 0 {
		t.Fatalf("成功命令结果异常: stdout=%q stderr=%q code=%d", stdout, stderr, code)
	}
}

func TestBadCredentialsRejected(t *testing.T) {
	srv := startServer(t, Config{Username: "devuser", Password: "devpass"})

	t.Run("错误密码", func(t *testing.T) {
		if c, err := dialRaw(t, srv.Addr(), "devuser", ssh.Password("wrong")); err == nil {
			c.Close()
			t.Fatal("错误密码不应通过认证")
		}
	})
	t.Run("未知用户", func(t *testing.T) {
		if c, err := dialRaw(t, srv.Addr(), "intruder", ssh.Password("devpass")); err == nil {
			c.Close()
			t.Fatal("未知用户不应通过认证")
		}
	})
	t.Run("无认证材料", func(t *testing.T) {
		if c, err := dialRaw(t, srv.Addr(), "devuser"); err == nil {
			c.Close()
			t.Fatal("无认证材料不应通过认证")
		}
	})
}

func TestPublicKeyAuth(t *testing.T) {
	signer, _, authorized := newClientKey(t)
	srv := startServer(t, Config{AuthorizedKey: authorized})

	t.Run("公钥正确", func(t *testing.T) {
		c := mustDialRaw(t, srv.Addr(), "devuser", ssh.PublicKeys(signer))
		stdout, _, code := runRaw(t, c, "echo key-ok")
		if stdout != "key-ok\n" || code != 0 {
			t.Fatalf("公钥认证执行失败: stdout=%q code=%d", stdout, code)
		}
	})

	t.Run("密码与公钥取或", func(t *testing.T) {
		mustDialRaw(t, srv.Addr(), "devuser", ssh.Password("devpass"))
	})

	t.Run("公钥不匹配", func(t *testing.T) {
		otherSigner, _, _ := newClientKey(t)
		if c, err := dialRaw(t, srv.Addr(), "devuser", ssh.PublicKeys(otherSigner)); err == nil {
			c.Close()
			t.Fatal("未授权的公钥不应通过认证")
		}
	})

	t.Run("正确公钥但未知用户", func(t *testing.T) {
		if c, err := dialRaw(t, srv.Addr(), "intruder", ssh.PublicKeys(signer)); err == nil {
			c.Close()
			t.Fatal("未知用户即使公钥正确也不应通过认证")
		}
	})
}

func TestCustomExecutor(t *testing.T) {
	var gotCmd string
	srv := startServer(t, Config{
		Executor: func(cmd string) (string, string, int) {
			gotCmd = cmd
			return "OUT:" + cmd, "ERR:" + cmd, 7
		},
	})
	c := mustDialRaw(t, srv.Addr(), "devuser", ssh.Password("devpass"))
	stdout, stderr, code := runRaw(t, c, "anything --flag=1")
	if gotCmd != "anything --flag=1" {
		t.Fatalf("Executor 收到的命令 = %q", gotCmd)
	}
	if stdout != "OUT:anything --flag=1" || stderr != "ERR:anything --flag=1" || code != 7 {
		t.Fatalf("自定义 Executor 结果异常: stdout=%q stderr=%q code=%d", stdout, stderr, code)
	}
}

func TestInvalidAuthorizedKey(t *testing.T) {
	if srv, err := Start(Config{AuthorizedKey: []byte("not a public key")}); err == nil {
		srv.Close()
		t.Fatal("非法 authorized_keys 内容应导致启动失败")
	}
}

func TestInteractiveShellRejected(t *testing.T) {
	srv := startServer(t, Config{})
	c := mustDialRaw(t, srv.Addr(), "devuser", ssh.Password("devpass"))

	sess, err := c.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	var errBuf bytes.Buffer
	sess.Stderr = &errBuf
	if err := sess.Shell(); err != nil {
		t.Fatalf("Shell: %v", err)
	}
	err = sess.Wait()
	var exitErr *ssh.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("交互式 shell 应返回非 0 退出码, got %v", err)
	}
	if exitErr.ExitStatus() != 1 {
		t.Fatalf("交互式 shell 退出码 = %d, 期望 1", exitErr.ExitStatus())
	}
	if !strings.Contains(errBuf.String(), "exec") {
		t.Fatalf("stderr 应提示使用 exec: %q", errBuf.String())
	}
}

func TestCloseIdempotent(t *testing.T) {
	srv, err := Start(Config{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("首次 Close: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("重复 Close 应返回 nil: %v", err)
	}
	if c, err := dialRaw(t, srv.Addr(), "devuser", ssh.Password("devpass")); err == nil {
		c.Close()
		t.Fatal("关闭后不应再接受连接")
	}
}

func TestDefaultsApplied(t *testing.T) {
	// 空 Config 使用内置默认值：127.0.0.1:0 / devuser / devpass。
	srv := startServer(t, Config{Addr: "127.0.0.1:0"})
	if !strings.HasPrefix(srv.Addr(), "127.0.0.1:") {
		t.Fatalf("默认监听地址异常: %q", srv.Addr())
	}
	mustDialRaw(t, srv.Addr(), "devuser", ssh.Password("devpass"))
}
