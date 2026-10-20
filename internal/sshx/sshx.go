// Package sshx 是基于 golang.org/x/crypto/ssh 的 SSH 客户端封装。
//
// 契约见 docs/ARCHITECTURE.md 第 8.2 节（已冻结，签名不得改名）。
// 该包只依赖标准库与 golang.org/x/crypto，不得 import internal/store 或 internal/api。
package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	// DefaultTimeout 是 Dial 的默认连接 + 握手超时。
	DefaultTimeout = 10 * time.Second
	// DefaultPort 是未指定端口时的默认 SSH 端口。
	DefaultPort = 22
)

// Runner 是可执行远程命令的最小抽象；hostinfo 只依赖它，便于测试替身。
type Runner interface {
	Run(ctx context.Context, cmd string) (stdout, stderr string, exitCode int, err error)
}

// Auth 描述一次 SSH 认证所需的材料。
type Auth struct {
	Username   string
	Password   string
	PrivateKey []byte // PEM
	Passphrase string
}

// HostKeyPolicy 是主机密钥校验策略。
type HostKeyPolicy string

const (
	// PolicyInsecure 不校验主机密钥，仅限用户明确选择。
	PolicyInsecure HostKeyPolicy = "insecure"
	// PolicyAcceptNew 首次见到主机密钥时记录，之后不一致必须拒绝连接。
	PolicyAcceptNew HostKeyPolicy = "accept-new"
	// PolicyStrict 只接受 known_hosts 中已存在的主机密钥。
	PolicyStrict HostKeyPolicy = "strict"

	// 以下是等价的别名常量（取值完全相同），方便按 HostKeyPolicyXxx 命名引用。
	HostKeyPolicyInsecure  = PolicyInsecure
	HostKeyPolicyAcceptNew = PolicyAcceptNew
	HostKeyPolicyStrict    = PolicyStrict
)

// DialOptions 是 Dial 的参数。
type DialOptions struct {
	Host           string
	Port           int
	Auth           Auth
	Timeout        time.Duration                  // 默认 10s
	HostKeyPolicy  HostKeyPolicy                  // 默认 "accept-new"
	KnownHostsPath string                         // accept-new / strict 时使用
	OnNewHostKey   func(host, fingerprint string) // accept-new 首次见到主机密钥时回调（落库）
}

// Client 是一条已建立的 SSH 连接。
type Client struct {
	client      *ssh.Client
	version     string
	fingerprint string
}

// dialSSH 建立底层 SSH 连接（含认证与主机密钥校验），供 Dial / PTY / SFTP 复用。
func dialSSH(ctx context.Context, o DialOptions) (*ssh.Client, string, string, error) {
	if err := validateDialOptions(o); err != nil {
		return nil, "", "", err
	}

	port := o.Port
	if port <= 0 {
		port = DefaultPort
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	policy := o.HostKeyPolicy
	if policy == "" {
		policy = PolicyAcceptNew
	}

	hostKeyCallback, fingerprint, err := buildHostKeyCallback(policy, o.KnownHostsPath, o.OnNewHostKey)
	if err != nil {
		return nil, "", "", err
	}
	authMethods, err := buildAuthMethods(o.Auth)
	if err != nil {
		return nil, "", "", err
	}

	addr := net.JoinHostPort(o.Host, strconv.Itoa(port))
	cfg := &ssh.ClientConfig{
		User:            o.Auth.Username,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         timeout,
	}

	// 始终施加 Timeout；若调用方 ctx 的 deadline 更早，context 会自动取较小值。
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, "", "", fmt.Errorf("sshx: 连接 %s 失败: %w", addr, err)
	}

	type handshakeResult struct {
		conn  ssh.Conn
		chans <-chan ssh.NewChannel
		reqs  <-chan *ssh.Request
		err   error
	}
	resultCh := make(chan handshakeResult, 1)
	go func() {
		c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
		resultCh <- handshakeResult{conn: c, chans: chans, reqs: reqs, err: err}
	}()

	var res handshakeResult
	select {
	case res = <-resultCh:
	case <-dialCtx.Done():
		_ = conn.Close()
		// 关闭底层连接后握手 goroutine 必定返回；最多再等 3 秒防止极端情况。
		select {
		case <-resultCh:
		case <-time.After(3 * time.Second):
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, "", "", fmt.Errorf("sshx: 连接 %s 被取消: %w", addr, ctxErr)
		}
		return nil, "", "", fmt.Errorf("sshx: 连接 %s 超时（%s）", addr, timeout)
	}
	if res.err != nil {
		_ = conn.Close()
		return nil, "", "", fmt.Errorf("sshx: 与 %s 握手/认证失败: %w", addr, res.err)
	}

	return ssh.NewClient(res.conn, res.chans, res.reqs), string(res.conn.ServerVersion()), fingerprint(), nil
}

// Dial 建立 SSH 连接（含认证与主机密钥校验）。
func Dial(ctx context.Context, o DialOptions) (*Client, error) {
	client, version, fingerprint, err := dialSSH(ctx, o)
	if err != nil {
		return nil, err
	}
	return &Client{client: client, version: version, fingerprint: fingerprint}, nil
}

func validateDialOptions(o DialOptions) error {
	if o.Host == "" {
		return errors.New("sshx: Host 不能为空")
	}
	if o.Auth.Username == "" {
		return errors.New("sshx: Auth.Username 不能为空")
	}
	if len(o.Auth.PrivateKey) == 0 && o.Auth.Password == "" {
		return errors.New("sshx: 必须提供 Password 或 PrivateKey")
	}
	return nil
}

func buildAuthMethods(a Auth) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if len(a.PrivateKey) > 0 {
		var (
			signer ssh.Signer
			err    error
		)
		if a.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(a.PrivateKey, []byte(a.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(a.PrivateKey)
		}
		if err != nil {
			var missing *ssh.PassphraseMissingError
			if errors.As(err, &missing) {
				return nil, errors.New("sshx: 私钥已加密，必须提供 Passphrase")
			}
			return nil, fmt.Errorf("sshx: 解析私钥失败: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if a.Password != "" {
		methods = append(methods, ssh.Password(a.Password))
	}

	if len(methods) == 0 {
		return nil, errors.New("sshx: 没有可用的认证方式")
	}
	return methods, nil
}

// buildHostKeyCallback 按策略构造主机密钥校验回调，并返回一个读取本次连接
// 实际主机密钥指纹的函数（"SHA256:<base64>"）。
func buildHostKeyCallback(policy HostKeyPolicy, path string, onNew func(host, fingerprint string)) (ssh.HostKeyCallback, func() string, error) {
	var (
		mu sync.Mutex
		fp string
	)
	record := func(key ssh.PublicKey) {
		mu.Lock()
		fp = ssh.FingerprintSHA256(key)
		mu.Unlock()
	}
	fingerprint := func() string {
		mu.Lock()
		defer mu.Unlock()
		return fp
	}

	switch policy {
	case PolicyInsecure:
		// 仅供用户明确选择；仍记录指纹用于展示。
		return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			record(key)
			return nil
		}, fingerprint, nil

	case PolicyAcceptNew, PolicyStrict:
		if path == "" {
			defaultPath, err := defaultKnownHostsPath()
			if err != nil {
				return nil, nil, err
			}
			path = defaultPath
		}

		if policy == PolicyStrict {
			if _, err := os.Stat(path); err != nil {
				return nil, nil, fmt.Errorf("sshx: strict 策略需要可读的 known_hosts 文件 %s: %w", path, err)
			}
			checker, err := knownhosts.New(path)
			if err != nil {
				return nil, nil, fmt.Errorf("sshx: 加载 known_hosts %s 失败: %w", path, err)
			}
			return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
				if err := checker(hostname, remote, key); err != nil {
					return fmt.Errorf("sshx: 主机密钥校验失败（strict：只接受 known_hosts 中已记录的主机密钥，可能被中间人攻击）: %w", err)
				}
				record(key)
				return nil
			}, fingerprint, nil
		}

		// accept-new
		if err := ensureKnownHostsFile(path); err != nil {
			return nil, nil, err
		}
		checker, err := knownhosts.New(path)
		if err != nil {
			return nil, nil, fmt.Errorf("sshx: 加载 known_hosts %s 失败: %w", path, err)
		}
		return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			err := checker(hostname, remote, key)
			if err == nil {
				record(key)
				return nil
			}

			var keyErr *knownhosts.KeyError
			if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
				// known_hosts 中尚无该主机：记录首次见到的指纹。
				newFingerprint := ssh.FingerprintSHA256(key)
				if appendErr := appendKnownHost(path, hostname, key); appendErr != nil {
					return appendErr
				}
				record(key)
				if onNew != nil {
					onNew(knownhosts.Normalize(hostname), newFingerprint)
				}
				return nil
			}

			// 已记录但密钥不一致 —— 必须拒绝，防中间人。
			return fmt.Errorf("sshx: 主机密钥与已记录的不一致，拒绝连接（accept-new：可能被中间人攻击）: %w", err)
		}, fingerprint, nil

	default:
		return nil, nil, fmt.Errorf("sshx: 未知 HostKeyPolicy %q（应为 insecure / accept-new / strict）", policy)
	}
}

func defaultKnownHostsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("sshx: 无法确定用户目录以定位 known_hosts: %w", err)
	}
	return filepath.Join(home, ".ssh", "known_hosts"), nil
}

func ensureKnownHostsFile(path string) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("sshx: 创建 known_hosts 目录失败: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("sshx: 创建 known_hosts %s 失败: %w", path, err)
	}
	return f.Close()
}

// knownHostsMu 串行化 known_hosts 的追加写，避免并发 Dial 交错写坏文件。
var knownHostsMu sync.Mutex

func appendKnownHost(path, hostname string, key ssh.PublicKey) error {
	knownHostsMu.Lock()
	defer knownHostsMu.Unlock()

	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("sshx: 打开 known_hosts %s 失败: %w", path, err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		_ = f.Close()
		return fmt.Errorf("sshx: 写入 known_hosts 失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sshx: 落盘 known_hosts 失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("sshx: 关闭 known_hosts 失败: %w", err)
	}
	return nil
}

// syncBuffer 是并发安全的 bytes.Buffer（Run 的读取可能与子 goroutine 写入并发）。
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Run 在远端执行一条命令。
// 命令非 0 退出返回 exitCode，且 err 为 nil；传输层失败才返回 err。
func (c *Client) Run(ctx context.Context, cmd string) (stdout, stderr string, exitCode int, err error) {
	if c == nil || c.client == nil {
		return "", "", -1, errors.New("sshx: 连接未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", "", -1, err
	}

	sess, err := c.client.NewSession()
	if err != nil {
		return "", "", -1, fmt.Errorf("sshx: 创建 SSH 会话失败: %w", err)
	}
	defer sess.Close()

	outBuf := &syncBuffer{}
	errBuf := &syncBuffer{}
	sess.Stdout = outBuf
	sess.Stderr = errBuf

	done := make(chan error, 1)
	go func() {
		done <- sess.Run(cmd)
	}()

	select {
	case runErr := <-done:
		code, classErr := classifyExit(runErr)
		return outBuf.String(), errBuf.String(), code, classErr
	case <-ctx.Done():
		// context 取消：关闭会话即可中断远端命令；不返回部分输出以免误判。
		_ = sess.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		return "", "", -1, ctx.Err()
	}
}

// classifyExit 把 ssh.Session.Run 的返回错误分类：
//   - nil            -> exitCode 0
//   - *ssh.ExitError -> 远端退出码，不是 error
//   - 其它            -> 传输层失败（-1 + error）
func classifyExit(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitStatus(), nil
	}
	var missing *ssh.ExitMissingError
	if errors.As(err, &missing) {
		return -1, fmt.Errorf("sshx: 远端未返回退出状态: %w", err)
	}
	return -1, fmt.Errorf("sshx: 执行命令失败: %w", err)
}

// ServerVersion 返回远端 SSH 版本串，形如 "SSH-2.0-OpenSSH_9.6"。
func (c *Client) ServerVersion() string {
	if c == nil {
		return ""
	}
	return c.version
}

// HostKeyFingerprint 返回主机密钥指纹 "SHA256:<base64>"。
func (c *Client) HostKeyFingerprint() string {
	if c == nil {
		return ""
	}
	return c.fingerprint
}

// Close 关闭连接。
func (c *Client) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	err := c.client.Close()
	if err != nil && errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
