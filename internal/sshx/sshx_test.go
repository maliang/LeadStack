package sshx_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maliang/leadstack/internal/devssh"
	"github.com/maliang/leadstack/internal/sshx"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func startDevssh(t *testing.T, cfg devssh.Config) *devssh.Server {
	t.Helper()
	srv, err := devssh.Start(cfg)
	if err != nil {
		t.Fatalf("启动 devssh 失败: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func splitAddr(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("拆分地址 %q 失败: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("解析端口失败: %v", err)
	}
	return host, port
}

func passwordOptions(t *testing.T, srv *devssh.Server, knownHostsPath string) sshx.DialOptions {
	t.Helper()
	host, port := splitAddr(t, srv.Addr())
	return sshx.DialOptions{
		Host:           host,
		Port:           port,
		Auth:           sshx.Auth{Username: "devuser", Password: "devpass"},
		Timeout:        5 * time.Second,
		HostKeyPolicy:  sshx.PolicyAcceptNew,
		KnownHostsPath: knownHostsPath,
	}
}

func dialOK(t *testing.T, opts sshx.DialOptions) *sshx.Client {
	t.Helper()
	c, err := sshx.Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func runOK(t *testing.T, c *sshx.Client, cmd string) (string, string, int) {
	t.Helper()
	stdout, stderr, code, err := c.Run(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Run(%q) 失败: %v", cmd, err)
	}
	return stdout, stderr, code
}

func newKeyPair(t *testing.T) (ssh.Signer, []byte, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("NewSignerFromKey: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("MarshalPrivateKey: %v", err)
	}
	return signer, pem.EncodeToMemory(block), ssh.MarshalAuthorizedKey(signer.PublicKey())
}

func TestDialAcceptNewThenRun(t *testing.T) {
	srv := startDevssh(t, devssh.Config{})
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")

	var (
		newHosts []string
		newFP    []string
		opts     = passwordOptions(t, srv, knownHostsPath)
	)
	opts.OnNewHostKey = func(host, fingerprint string) {
		newHosts = append(newHosts, host)
		newFP = append(newFP, fingerprint)
	}

	c := dialOK(t, opts)
	if len(newFP) != 1 {
		t.Fatalf("OnNewHostKey 应回调 1 次，实际 %d 次", len(newFP))
	}
	if !strings.HasPrefix(newFP[0], "SHA256:") {
		t.Fatalf("指纹格式错误: %q", newFP[0])
	}
	if c.HostKeyFingerprint() != newFP[0] {
		t.Fatalf("HostKeyFingerprint() = %q, 回调 = %q", c.HostKeyFingerprint(), newFP[0])
	}
	if newHosts[0] == "" {
		t.Fatal("OnNewHostKey 的 host 不应为空")
	}
	if v := c.ServerVersion(); !strings.HasPrefix(v, "SSH-2.0-") {
		t.Fatalf("ServerVersion = %q", v)
	}

	// known_hosts 必须真的落盘。
	data, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatalf("读取 known_hosts: %v", err)
	}
	if !bytes.Contains(data, []byte("ssh-")) {
		t.Fatalf("known_hosts 内容异常: %q", data)
	}

	// 真实执行：stdout / stderr / 退出码。
	stdout, stderr, code := runOK(t, c, `printf 'hello'; printf 'warn' >&2; exit 0`)
	if stdout != "hello" || stderr != "warn" || code != 0 {
		t.Fatalf("Run 结果异常: stdout=%q stderr=%q code=%d", stdout, stderr, code)
	}
	stdout, stderr, code = runOK(t, c, `printf 'out'; printf 'err' >&2; exit 42`)
	if stdout != "out" || stderr != "err" || code != 42 {
		t.Fatalf("非 0 退出结果异常: stdout=%q stderr=%q code=%d", stdout, stderr, code)
	}

	// 已记录后再次 accept-new：不应再触发回调，且必须通过校验。
	opts.OnNewHostKey = func(host, fingerprint string) {
		t.Fatalf("第二次连接不应触发 OnNewHostKey（host=%s fp=%s）", host, fingerprint)
	}
	c2 := dialOK(t, opts)
	runOK(t, c2, "echo second")

	// strict：known_hosts 已有记录，必须接受。
	strictOpts := opts
	strictOpts.HostKeyPolicy = sshx.PolicyStrict
	strictOpts.OnNewHostKey = nil
	c3 := dialOK(t, strictOpts)
	runOK(t, c3, "echo strict")
}

func TestAcceptNewRejectsChangedHostKey(t *testing.T) {
	srv := startDevssh(t, devssh.Config{})
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	opts := passwordOptions(t, srv, knownHostsPath)

	first := dialOK(t, opts)
	if first.HostKeyFingerprint() == "" {
		t.Fatal("首次连接的指纹为空")
	}

	// 模拟中间人：把 known_hosts 里该主机的公钥替换为另一把。
	newSigner, _, _ := newKeyPair(t)
	addr := srv.Addr()
	line := knownhosts.Line([]string{knownhosts.Normalize(addr)}, newSigner.PublicKey())
	if err := os.WriteFile(knownHostsPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("改写 known_hosts: %v", err)
	}

	for _, policy := range []sshx.HostKeyPolicy{sshx.PolicyAcceptNew, sshx.PolicyStrict} {
		t.Run(string(policy), func(t *testing.T) {
			o := opts
			o.HostKeyPolicy = policy
			c, err := sshx.Dial(context.Background(), o)
			if err == nil {
				c.Close()
				t.Fatal("主机密钥与已记录不一致时必须拒绝连接")
			}
			if !strings.Contains(err.Error(), "主机密钥") {
				t.Fatalf("错误信息应说明主机密钥校验失败: %v", err)
			}
		})
	}
}

func TestStrictRequiresKnownHost(t *testing.T) {
	srv := startDevssh(t, devssh.Config{})
	dir := t.TempDir()

	t.Run("known_hosts 不存在", func(t *testing.T) {
		opts := passwordOptions(t, srv, filepath.Join(dir, "missing"))
		opts.HostKeyPolicy = sshx.PolicyStrict
		if c, err := sshx.Dial(context.Background(), opts); err == nil {
			c.Close()
			t.Fatal("strict 策略在 known_hosts 不存在时应报错")
		}
	})

	t.Run("known_hosts 为空", func(t *testing.T) {
		empty := filepath.Join(dir, "empty")
		if err := os.WriteFile(empty, nil, 0o600); err != nil {
			t.Fatalf("写空 known_hosts: %v", err)
		}
		opts := passwordOptions(t, srv, empty)
		opts.HostKeyPolicy = sshx.PolicyStrict
		if c, err := sshx.Dial(context.Background(), opts); err == nil {
			c.Close()
			t.Fatal("strict 策略在未知主机上应报错")
		}
	})
}

func TestInsecurePolicy(t *testing.T) {
	srv := startDevssh(t, devssh.Config{})
	opts := passwordOptions(t, srv, "")
	opts.HostKeyPolicy = sshx.PolicyInsecure
	opts.OnNewHostKey = nil

	c := dialOK(t, opts)
	if !strings.HasPrefix(c.HostKeyFingerprint(), "SHA256:") {
		t.Fatalf("insecure 策略也应记录指纹: %q", c.HostKeyFingerprint())
	}
	if out, _, code := runOK(t, c, "echo insecure-ok"); out != "insecure-ok\n" || code != 0 {
		t.Fatalf("insecure 连接执行异常: out=%q code=%d", out, code)
	}
}

func TestKeyAuth(t *testing.T) {
	signer, pemBytes, authorized := newKeyPair(t)

	t.Run("未加密私钥", func(t *testing.T) {
		srv := startDevssh(t, devssh.Config{AuthorizedKey: authorized})
		opts := passwordOptions(t, srv, filepath.Join(t.TempDir(), "known_hosts"))
		opts.Auth = sshx.Auth{Username: "devuser", PrivateKey: pemBytes}
		c := dialOK(t, opts)
		if out, _, code := runOK(t, c, "echo key-auth"); out != "key-auth\n" || code != 0 {
			t.Fatalf("公钥认证执行异常: out=%q code=%d", out, code)
		}
	})

	t.Run("带 passphrase 的私钥", func(t *testing.T) {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("生成密钥失败: %v", err)
		}
		block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("pp"))
		if err != nil {
			t.Fatalf("MarshalPrivateKeyWithPassphrase: %v", err)
		}
		pubSigner, err := ssh.NewSignerFromKey(priv)
		if err != nil {
			t.Fatalf("NewSignerFromKey: %v", err)
		}
		srv := startDevssh(t, devssh.Config{AuthorizedKey: ssh.MarshalAuthorizedKey(pubSigner.PublicKey())})
		opts := passwordOptions(t, srv, filepath.Join(t.TempDir(), "known_hosts"))
		opts.Auth = sshx.Auth{Username: "devuser", PrivateKey: pem.EncodeToMemory(block), Passphrase: "pp"}
		c := dialOK(t, opts)
		runOK(t, c, "echo encrypted-key")

		// 错误 passphrase 必须失败。
		badOpts := opts
		badOpts.Auth.Passphrase = "wrong"
		if c, err := sshx.Dial(context.Background(), badOpts); err == nil {
			c.Close()
			t.Fatal("错误 passphrase 应导致 Dial 失败")
		}
	})

	t.Run("未授权私钥", func(t *testing.T) {
		srv := startDevssh(t, devssh.Config{AuthorizedKey: authorized})
		_, otherPEM, _ := newKeyPair(t)
		opts := passwordOptions(t, srv, filepath.Join(t.TempDir(), "known_hosts"))
		opts.Auth = sshx.Auth{Username: "devuser", PrivateKey: otherPEM}
		if c, err := sshx.Dial(context.Background(), opts); err == nil {
			c.Close()
			t.Fatal("未授权私钥应认证失败")
		}
	})
	_ = signer
}

func TestRunContextCancellation(t *testing.T) {
	srv := startDevssh(t, devssh.Config{})
	opts := passwordOptions(t, srv, filepath.Join(t.TempDir(), "known_hosts"))
	c := dialOK(t, opts)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, code, err := c.Run(ctx, "sleep 5")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("context 取消应返回 error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("期望 context.DeadlineExceeded, got %v", err)
	}
	if code != -1 {
		t.Fatalf("取消时 exitCode = %d, 期望 -1", code)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("context 取消未能及时中断会话，耗时 %v", elapsed)
	}

	// 已取消的 context 不应发起执行。
	canceled, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, _, _, err := c.Run(canceled, "echo never"); !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled, got %v", err)
	}
}

func TestRunTransportErrorAfterClose(t *testing.T) {
	srv := startDevssh(t, devssh.Config{})
	opts := passwordOptions(t, srv, filepath.Join(t.TempDir(), "known_hosts"))
	c, err := sshx.Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, _, code, err := c.Run(context.Background(), "echo hi")
	if err == nil {
		t.Fatal("连接关闭后 Run 应返回传输层错误")
	}
	if code != -1 {
		t.Fatalf("传输层失败时 exitCode = %d, 期望 -1", code)
	}
}

func TestCommandNotFoundIsExitCode(t *testing.T) {
	srv := startDevssh(t, devssh.Config{})
	opts := passwordOptions(t, srv, filepath.Join(t.TempDir(), "known_hosts"))
	c := dialOK(t, opts)
	_, _, code := runOK(t, c, "definitely-not-a-command-xyz-123")
	if code != 127 {
		t.Fatalf("命令不存在时 exitCode = %d, 期望 127", code)
	}
}

func TestDialValidation(t *testing.T) {
	// 缺 host。
	if _, err := sshx.Dial(context.Background(), sshx.DialOptions{
		Auth:          sshx.Auth{Username: "u", Password: "p"},
		HostKeyPolicy: sshx.PolicyInsecure,
	}); err == nil {
		t.Fatal("缺 Host 应报错")
	}
	// 缺认证材料。
	if _, err := sshx.Dial(context.Background(), sshx.DialOptions{
		Host:          "127.0.0.1",
		Auth:          sshx.Auth{Username: "u"},
		HostKeyPolicy: sshx.PolicyInsecure,
	}); err == nil {
		t.Fatal("缺认证材料应报错")
	}
	// 未知策略。
	if _, err := sshx.Dial(context.Background(), sshx.DialOptions{
		Host:          "127.0.0.1",
		Port:          2222,
		Auth:          sshx.Auth{Username: "u", Password: "p"},
		HostKeyPolicy: sshx.HostKeyPolicy("bogus"),
	}); err == nil {
		t.Fatal("未知 HostKeyPolicy 应报错")
	}
}

func TestDialContextCanceled(t *testing.T) {
	srv := startDevssh(t, devssh.Config{})
	opts := passwordOptions(t, srv, filepath.Join(t.TempDir(), "known_hosts"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c, err := sshx.Dial(ctx, opts); err == nil {
		c.Close()
		t.Fatal("已取消的 context 不应建立连接")
	}
}
