package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/maliang/leadstack/internal/checker"
	"github.com/maliang/leadstack/internal/credential"
	"github.com/maliang/leadstack/internal/sshx"
	"github.com/maliang/leadstack/internal/store"
)

// SSH 执行通道参数。
const (
	// sshExecIdleTimeout 是空闲连接回收时间。
	sshExecIdleTimeout = 90 * time.Second
	// sshExecReapInterval 是空闲连接巡检周期。
	sshExecReapInterval = 30 * time.Second
	// sshExecDialTimeout 是单次建连超时。
	sshExecDialTimeout = 10 * time.Second
	// sshExecDefaultTimeoutS 是未指定 timeout_s 时的默认命令超时。
	sshExecDefaultTimeoutS = 10
)

// SSHExecutor 用 SSH 在目标机上执行检测命令（检测项的 ssh 通道）。
//
// 连接复用取舍：30 台机器 × 10 秒间隔 = 3 次/秒，若每次都新建连接会把对端 sshd
// 的握手/认证开销放大到不可接受，因此这里**按 serverID 缓存已认证连接**：
//   - 同一连接内串行执行（加锁），不会对同一台机器并发建连或并发跑命令；
//   - 连接空闲超过 sshExecIdleTimeout 由巡检协程回收；
//   - 传输层报错（连接被对端/中间设备断开）时丢弃该连接并重试一次新连接；
//   - 服务器地址或凭证变化时按需重连（每次 Exec 读一次服务器行做比对）；
//   - 全局并发由信号量限制（默认取 LEADSTACK_CHECK_CONCURRENCY）。
type SSHExecutor struct {
	st       *store.Store
	cipher   *credential.Cipher
	logger   *slog.Logger
	known    string
	idle     time.Duration
	sem      chan struct{}
	closeOne sync.Once
	stopReap chan struct{}

	mu     sync.Mutex
	conns  map[string]*pooledSSHConn
	closed bool
}

// pooledSSHConn 是一条被复用的 SSH 连接。
type pooledSSHConn struct {
	client       *sshx.Client
	host         string
	port         int
	credentialID string
	lastUsed     time.Time
	// mu 保证同一台机器一次只跑一条命令（sshx.Client 未承诺并发安全）。
	mu sync.Mutex
}

// 编译期确认接口实现。
var _ checker.Executor = (*SSHExecutor)(nil)

// SSHExecutorOptions 是构造 SSHExecutor 的入参。
type SSHExecutorOptions struct {
	Store          *store.Store
	Cipher         *credential.Cipher
	Logger         *slog.Logger
	KnownHostsPath string
	Concurrency    int
}

// NewSSHExecutor 创建 SSH 执行通道；cipher 为 nil 时返回 nil（无凭证加密能力）。
func NewSSHExecutor(opts SSHExecutorOptions) *SSHExecutor {
	if opts.Store == nil || opts.Cipher == nil {
		return nil
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 8
	}
	e := &SSHExecutor{
		st:       opts.Store,
		cipher:   opts.Cipher,
		logger:   logger,
		known:    opts.KnownHostsPath,
		idle:     sshExecIdleTimeout,
		sem:      make(chan struct{}, concurrency),
		stopReap: make(chan struct{}),
		conns:    make(map[string]*pooledSSHConn),
	}
	go e.reapLoop()
	return e
}

// Exec 在目标服务器上执行一条命令。
func (e *SSHExecutor) Exec(ctx context.Context, serverID, command string, timeoutS int) (stdout, stderr string, exitCode int, err error) {
	if e == nil {
		return "", "", -1, errors.New("SSH 执行通道未启用")
	}
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		return "", "", -1, ctx.Err()
	}

	if timeoutS <= 0 {
		timeoutS = sshExecDefaultTimeoutS
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutS)*time.Second)
	defer cancel()

	entry, err := e.acquireConn(ctx, serverID)
	if err != nil {
		return "", "", -1, err
	}

	// 同一台机器串行执行：避免同一连接上并发跑命令，也避免触发对端并发限制。
	entry.mu.Lock()
	defer entry.mu.Unlock()

	stdout, stderr, exitCode, err = entry.client.Run(runCtx, command)
	if err == nil {
		e.touch(serverID, entry)
		return stdout, stderr, exitCode, nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		e.touch(serverID, entry)
		return stdout, stderr, exitCode, fmt.Errorf("命令执行超时（%ds）: %w", timeoutS, err)
	}

	// 传输层失败：丢弃连接后用新连接重试一次（幂等失败场景下比直接报错体验好）。
	e.drop(serverID, entry)
	e.logger.Debug("SSH 执行失败，改用新连接重试", "server_id", serverID, "err", err)

	retry, derr := e.dial(ctx, serverID)
	if derr != nil {
		return "", "", -1, fmt.Errorf("SSH 执行失败: %w（重连也失败: %v）", err, derr)
	}
	retry.mu.Lock()
	defer retry.mu.Unlock()
	stdout, stderr, exitCode, err = retry.client.Run(runCtx, command)
	if err != nil {
		e.drop(serverID, retry)
		return stdout, stderr, exitCode, fmt.Errorf("SSH 执行失败: %w", err)
	}
	e.touch(serverID, retry)
	return stdout, stderr, exitCode, nil
}

// acquireConn 返回可用的连接（优先复用缓存）。
func (e *SSHExecutor) acquireConn(ctx context.Context, serverID string) (*pooledSSHConn, error) {
	srv, err := e.st.GetServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("查询服务器 %s 失败: %w", serverID, err)
	}

	e.mu.Lock()
	entry, ok := e.conns[serverID]
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return nil, errors.New("SSH 执行通道已关闭")
	}

	if ok && entry.matches(srv) {
		return entry, nil
	}
	if ok {
		// 地址或凭证变了：旧连接立即作废。
		e.drop(serverID, entry)
	}
	return e.dial(ctx, serverID)
}

// matches 判断缓存连接是否仍与服务器的地址/凭证一致。
func (c *pooledSSHConn) matches(srv *store.Server) bool {
	credID := ""
	if srv.CredentialID != nil {
		credID = *srv.CredentialID
	}
	return c.host == srv.Host && c.port == srv.Port && c.credentialID == credID
}

// dial 建立一条新的 SSH 连接并登记到连接表。
func (e *SSHExecutor) dial(ctx context.Context, serverID string) (*pooledSSHConn, error) {
	srv, err := e.st.GetServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("查询服务器 %s 失败: %w", serverID, err)
	}
	if srv.CredentialID == nil || *srv.CredentialID == "" {
		return nil, errors.New("服务器未绑定 SSH 凭证")
	}
	cred, err := e.st.GetCredential(ctx, *srv.CredentialID)
	if err != nil {
		return nil, fmt.Errorf("读取凭证失败: %w", err)
	}
	plain, err := e.cipher.Decrypt(cred.SecretEnc)
	if err != nil {
		return nil, fmt.Errorf("解密凭证失败: %w", err)
	}
	secret, err := credential.Unmarshal(plain)
	if err != nil {
		return nil, fmt.Errorf("解析凭证失败: %w", err)
	}

	policy := sshx.HostKeyPolicy(srv.HostKeyPolicy)
	if policy == "" {
		policy = sshx.PolicyAcceptNew
	}
	client, err := sshx.Dial(ctx, sshx.DialOptions{
		Host: srv.Host,
		Port: srv.Port,
		Auth: sshx.Auth{
			Username:   secret.Username,
			Password:   secret.Password,
			PrivateKey: secret.PrivateKey,
			Passphrase: secret.Passphrase,
		},
		Timeout:        sshExecDialTimeout,
		HostKeyPolicy:  policy,
		KnownHostsPath: e.known,
		OnNewHostKey: func(host, fingerprint string) {
			// 只记审计日志，绝不记录密钥内容。
			e.logger.Info("检测通道首次见到主机密钥，已记录",
				"server_id", serverID, "host", host, "fingerprint", fingerprint)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败: %w", err)
	}

	entry := &pooledSSHConn{
		client:       client,
		host:         srv.Host,
		port:         srv.Port,
		credentialID: *srv.CredentialID,
		lastUsed:     time.Now(),
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		if cerr := client.Close(); cerr != nil {
			e.logger.Debug("关闭 SSH 连接失败", "err", cerr)
		}
		return nil, errors.New("SSH 执行通道已关闭")
	}
	// 并发建连时保留先到者，后到者关闭。
	if existing, ok := e.conns[serverID]; ok && existing != entry {
		e.mu.Unlock()
		if cerr := client.Close(); cerr != nil {
			e.logger.Debug("关闭多余 SSH 连接失败", "err", cerr)
		}
		return existing, nil
	}
	e.conns[serverID] = entry
	e.mu.Unlock()
	return entry, nil
}

// touch 更新最近使用时间。
func (e *SSHExecutor) touch(serverID string, entry *pooledSSHConn) {
	e.mu.Lock()
	if cur, ok := e.conns[serverID]; ok && cur == entry {
		entry.lastUsed = time.Now()
	}
	e.mu.Unlock()
}

// drop 关闭并从连接表移除连接。
func (e *SSHExecutor) drop(serverID string, entry *pooledSSHConn) {
	e.mu.Lock()
	if cur, ok := e.conns[serverID]; ok && cur == entry {
		delete(e.conns, serverID)
	}
	e.mu.Unlock()
	if err := entry.client.Close(); err != nil {
		e.logger.Debug("关闭 SSH 连接失败", "server_id", serverID, "err", err)
	}
}

// reapLoop 周期性回收空闲连接。
func (e *SSHExecutor) reapLoop() {
	ticker := time.NewTicker(sshExecReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-e.stopReap:
			return
		case <-ticker.C:
			e.reapIdle()
		}
	}
}

// reapIdle 关闭空闲超时的连接。
func (e *SSHExecutor) reapIdle() {
	cutoff := time.Now().Add(-e.idle)
	var expired []*pooledSSHConn

	e.mu.Lock()
	for id, entry := range e.conns {
		if entry.lastUsed.Before(cutoff) {
			expired = append(expired, entry)
			delete(e.conns, id)
		}
	}
	e.mu.Unlock()

	for _, entry := range expired {
		if err := entry.client.Close(); err != nil {
			e.logger.Debug("回收空闲 SSH 连接失败", "err", err)
		}
	}
	if len(expired) > 0 {
		e.logger.Debug("回收空闲 SSH 连接", "count", len(expired))
	}
}

// Close 关闭全部缓存连接并停止巡检（优雅关调用）。
func (e *SSHExecutor) Close() {
	if e == nil {
		return
	}
	e.closeOne.Do(func() {
		close(e.stopReap)
		e.mu.Lock()
		e.closed = true
		conns := e.conns
		e.conns = make(map[string]*pooledSSHConn)
		e.mu.Unlock()

		for id, entry := range conns {
			if err := entry.client.Close(); err != nil {
				e.logger.Debug("关闭 SSH 连接失败", "server_id", id, "err", err)
			}
		}
	})
}

// PoolSize 返回当前缓存的连接数（供测试与运维观察）。
func (e *SSHExecutor) PoolSize() int {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.conns)
}
