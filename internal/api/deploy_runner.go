package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/maliang/leadstack/internal/credential"
	"github.com/maliang/leadstack/internal/jobs"
	"github.com/maliang/leadstack/internal/sshx"
	"github.com/maliang/leadstack/internal/store"
)

// SSH 通道参数（部署用）。
const (
	deploySSHIdleTimeout  = 60 * time.Second
	deploySSHReapInterval = 30 * time.Second
	deploySSHDialTimeout  = 10 * time.Second
	deployLogChunkBytes   = 32 << 10
)

// DeployRunnerOptions 是构造部署执行通道的入参。
type DeployRunnerOptions struct {
	Store          *store.Store
	Cipher         *credential.Cipher
	Dispatcher     *jobs.Dispatcher
	KnownHostsPath string
	Logger         *slog.Logger
}

// NewDeployRunner 组装「按 mode 路由」的部署执行通道：ssh 直连 + agent 任务下发。
func NewDeployRunner(opts DeployRunnerOptions) DeployRunner {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	var sshRunner DeployRunner
	if ssh := newSSHDeployRunner(opts.Store, opts.Cipher, opts.KnownHostsPath, logger); ssh != nil {
		sshRunner = ssh
	}
	var agentRunner DeployRunner
	if opts.Dispatcher != nil {
		agentRunner = &agentDeployRunner{
			dispatcher: opts.Dispatcher,
			tap:        newAgentLogTap(opts.Store, logger),
			logger:     logger,
		}
	}
	if sshRunner == nil && agentRunner == nil {
		return nil
	}
	return &deployRoutingRunner{st: opts.Store, ssh: sshRunner, agent: agentRunner}
}

// DeployRunner 在目标机上执行一条命令并**边执行边回调日志**（契约第 25.1 节 Runner）。
type DeployRunner interface {
	Run(ctx context.Context, serverID, command string, timeoutS int,
		onLog func(stream, chunk string)) (stdout, stderr string, exitCode int, err error)
}

// ------------------------------------------------ 按 mode 路由 ------------------------------------------------

// deployRoutingRunner 按服务器接入模式选择执行通道：ssh → SSH 直连；agent → 任务下发。
type deployRoutingRunner struct {
	st    *store.Store
	ssh   DeployRunner
	agent DeployRunner
}

// Close 关闭底层 SSH 连接池（优雅关闭）。
func (r *deployRoutingRunner) Close() {
	if ssh, ok := r.ssh.(*sshDeployRunner); ok && ssh != nil {
		ssh.Close()
	}
}

// 编译期确认接口实现。
var _ DeployRunner = (*deployRoutingRunner)(nil)

// Run 按目标服务器的 mode 选择通道。
func (r *deployRoutingRunner) Run(ctx context.Context, serverID, command string, timeoutS int,
	onLog func(stream, chunk string)) (string, string, int, error) {
	srv, err := r.st.GetServer(ctx, serverID)
	if err != nil {
		return "", "", -1, fmt.Errorf("查询服务器 %s 失败: %w", serverID, err)
	}
	if srv.Mode == store.ModeSSH {
		if r.ssh == nil {
			return "", "", -1, errors.New("SSH 部署通道未启用")
		}
		return r.ssh.Run(ctx, serverID, command, timeoutS, onLog)
	}
	if r.agent == nil {
		return "", "", -1, errors.New("agent 部署通道未启用")
	}
	return r.agent.Run(ctx, serverID, command, timeoutS, onLog)
}

// ------------------------------------------------ SSH 通道 ------------------------------------------------

// sshDeployRunner 用 sshx 在目标机上执行命令，按步骤流式回传输出。
//
// 连接复用：按 serverID 缓存一条已认证连接（多次 Run 共用），空闲 60s 回收；
// 同一连接内串行执行，避免对同一台机器并发建连。
//
// 日志流式程度：sshx.Client.Run 是「收集后返回」，没有字节级流式 API（已报告 lead）。
// 因此这里做**步骤级流式**：每条命令开始时回调一次 system 日志（含命令摘要），
// 命令返回后立刻把 stdout/stderr 分块回传 —— 用户能看到「第几步在跑、上一步输出是什么」。
type sshDeployRunner struct {
	st       *store.Store
	cipher   *credential.Cipher
	logger   *slog.Logger
	known    string
	stopReap chan struct{}
	closeOne sync.Once

	mu     sync.Mutex
	conns  map[string]*deploySSHConn
	closed bool
}

// deploySSHConn 是一条被复用的 SSH 连接。
type deploySSHConn struct {
	client       *sshx.Client
	host         string
	port         int
	credentialID string
	lastUsed     time.Time
	mu           sync.Mutex
}

// 编译期确认接口实现。
var _ DeployRunner = (*sshDeployRunner)(nil)

// newSSHDeployRunner 创建 SSH 部署通道；cipher 为 nil 时返回 nil。
func newSSHDeployRunner(st *store.Store, cipher *credential.Cipher, knownHosts string, logger *slog.Logger) *sshDeployRunner {
	if st == nil || cipher == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	r := &sshDeployRunner{
		st: st, cipher: cipher, logger: logger, known: knownHosts,
		stopReap: make(chan struct{}), conns: map[string]*deploySSHConn{},
	}
	go r.reapLoop()
	return r
}

// Run 在目标机上执行一条命令。
func (r *sshDeployRunner) Run(ctx context.Context, serverID, command string, timeoutS int,
	onLog func(stream, chunk string)) (string, string, int, error) {
	if timeoutS <= 0 {
		timeoutS = 300
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutS)*time.Second)
	defer cancel()

	entry, err := r.acquire(runCtx, serverID)
	if err != nil {
		return "", "", -1, err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()

	stdout, stderr, exitCode, err := entry.client.Run(runCtx, command)
	// 无论成败都把已产生的输出交给上层（排障最需要这个）。
	emitChunks(onLog, "stdout", stdout)
	emitChunks(onLog, "stderr", stderr)
	if err != nil {
		r.drop(serverID, entry) // 传输层失败：丢弃连接，下次重连
		return stdout, stderr, exitCode, err
	}
	r.touch(serverID, entry)
	return stdout, stderr, exitCode, nil
}

// acquire 返回可复用的连接（必要时新建）。
func (r *sshDeployRunner) acquire(ctx context.Context, serverID string) (*deploySSHConn, error) {
	srv, err := r.st.GetServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("查询服务器 %s 失败: %w", serverID, err)
	}
	r.mu.Lock()
	entry, ok := r.conns[serverID]
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return nil, errors.New("SSH 部署通道已关闭")
	}
	if ok && entry.matches(srv) {
		return entry, nil
	}
	if ok {
		r.drop(serverID, entry)
	}
	return r.dial(ctx, serverID)
}

// matches 判断缓存连接是否仍与服务器地址/凭证一致。
func (c *deploySSHConn) matches(srv *store.Server) bool {
	credID := ""
	if srv.CredentialID != nil {
		credID = *srv.CredentialID
	}
	return c.host == srv.Host && c.port == srv.Port && c.credentialID == credID
}

// dial 建立一条新的 SSH 连接。
func (r *sshDeployRunner) dial(ctx context.Context, serverID string) (*deploySSHConn, error) {
	srv, err := r.st.GetServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("查询服务器 %s 失败: %w", serverID, err)
	}
	if srv.CredentialID == nil || *srv.CredentialID == "" {
		return nil, errors.New("服务器未绑定 SSH 凭证")
	}
	cred, err := r.st.GetCredential(ctx, *srv.CredentialID)
	if err != nil {
		return nil, fmt.Errorf("读取凭证失败: %w", err)
	}
	plain, err := r.cipher.Decrypt(cred.SecretEnc)
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
		Host: srv.Host, Port: srv.Port,
		Auth: sshx.Auth{
			Username: secret.Username, Password: secret.Password,
			PrivateKey: secret.PrivateKey, Passphrase: secret.Passphrase,
		},
		Timeout:        deploySSHDialTimeout,
		HostKeyPolicy:  policy,
		KnownHostsPath: r.known,
		OnNewHostKey: func(host, fingerprint string) {
			r.logger.Info("部署通道首次见到主机密钥，已记录",
				"server_id", serverID, "host", host, "fingerprint", fingerprint)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败: %w", err)
	}

	entry := &deploySSHConn{
		client: client, host: srv.Host, port: srv.Port,
		credentialID: *srv.CredentialID, lastUsed: time.Now(),
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		if cerr := client.Close(); cerr != nil {
			r.logger.Debug("关闭多余 SSH 连接失败", "err", cerr)
		}
		return nil, errors.New("SSH 部署通道已关闭")
	}
	if existing, ok := r.conns[serverID]; ok && existing != entry {
		r.mu.Unlock()
		if cerr := client.Close(); cerr != nil {
			r.logger.Debug("关闭多余 SSH 连接失败", "err", cerr)
		}
		return existing, nil
	}
	r.conns[serverID] = entry
	r.mu.Unlock()
	return entry, nil
}

// touch 更新最近使用时间。
func (r *sshDeployRunner) touch(serverID string, entry *deploySSHConn) {
	r.mu.Lock()
	if cur, ok := r.conns[serverID]; ok && cur == entry {
		entry.lastUsed = time.Now()
	}
	r.mu.Unlock()
}

// drop 关闭并移除缓存连接。
func (r *sshDeployRunner) drop(serverID string, entry *deploySSHConn) {
	r.mu.Lock()
	if cur, ok := r.conns[serverID]; ok && cur == entry {
		delete(r.conns, serverID)
	}
	r.mu.Unlock()
	if err := entry.client.Close(); err != nil {
		r.logger.Debug("关闭 SSH 连接失败", "server_id", serverID, "err", err)
	}
}

// reapLoop 周期回收空闲连接。
func (r *sshDeployRunner) reapLoop() {
	ticker := time.NewTicker(deploySSHReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stopReap:
			return
		case <-ticker.C:
			r.reapIdle()
		}
	}
}

// reapIdle 关闭空闲超时的连接。
func (r *sshDeployRunner) reapIdle() {
	cutoff := time.Now().Add(-deploySSHIdleTimeout)
	var expired []*deploySSHConn
	r.mu.Lock()
	for id, entry := range r.conns {
		if entry.lastUsed.Before(cutoff) {
			expired = append(expired, entry)
			delete(r.conns, id)
		}
	}
	r.mu.Unlock()
	for _, entry := range expired {
		if err := entry.client.Close(); err != nil {
			r.logger.Debug("回收部署 SSH 连接失败", "err", err)
		}
	}
}

// Close 关闭全部连接（优雅关闭）。
func (r *sshDeployRunner) Close() {
	if r == nil {
		return
	}
	r.closeOne.Do(func() {
		close(r.stopReap)
		r.mu.Lock()
		r.closed = true
		conns := r.conns
		r.conns = map[string]*deploySSHConn{}
		r.mu.Unlock()
		for id, entry := range conns {
			if err := entry.client.Close(); err != nil {
				r.logger.Debug("关闭 SSH 连接失败", "server_id", id, "err", err)
			}
		}
	})
}

// ------------------------------------------------ agent 通道 ------------------------------------------------

// agentDeployRunner 用 jobs.Dispatcher 下发部署命令。
//
// 流式日志：Dispatcher 只返回最终结果，但 job.log 帧会经过本进程的 Hub 转发路径，
// 因此这里配合 agentLogTap —— 若某次下发的 job 被 tap 捕获到实时日志，就不再重复写最终结果；
// 否则（未被捕获）退化为「结束一次性落库」，保证日志不丢。
type agentDeployRunner struct {
	dispatcher *jobs.Dispatcher
	tap        *agentLogTap
	logger     *slog.Logger
}

// 编译期确认接口实现。
var _ DeployRunner = (*agentDeployRunner)(nil)

// Run 下发命令并等待结束。
//
// 发布 run 的 ID 通过 context 传入（DeployRunner 接口签名由契约冻结，不能加参数）：
// 编排器在调用前 withDeployRun(ctx, releaseRunID)，这里据此把实时日志旁路到正确的 run。
func (r *agentDeployRunner) Run(ctx context.Context, serverID, command string, timeoutS int,
	onLog func(stream, chunk string)) (string, string, int, error) {
	var claim *tapClaim
	if r.tap != nil {
		if releaseRunID := deployRunIDFromContext(ctx); releaseRunID != "" {
			claim = r.tap.Claim(serverID, releaseRunID)
			defer claim.Release()
		}
	}

	stdout, stderr, exitCode, err := r.dispatcher.Exec(ctx, serverID, command, timeoutS)

	// tap 未捕获到实时日志时，用最终结果兜底（不丢日志）。
	if claim == nil || !claim.Captured() {
		emitChunks(onLog, "stdout", stdout)
		emitChunks(onLog, "stderr", stderr)
	}
	return stdout, stderr, exitCode, err
}

// deployRunCtxKey 是「当前执行的发布 run」在 context 中的键。
type deployRunCtxKey struct{}

// withDeployRun 把发布 run ID 放进 context（供 agent 通道的日志旁路使用）。
func withDeployRun(ctx context.Context, releaseRunID string) context.Context {
	return context.WithValue(ctx, deployRunCtxKey{}, releaseRunID)
}

// deployRunIDFromContext 读取 context 里的发布 run ID。
func deployRunIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(deployRunCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// emitChunks 把一段输出按固定大小切块回调，避免单条日志过大。
func emitChunks(onLog func(stream, chunk string), stream, content string) {
	if onLog == nil || content == "" {
		return
	}
	for len(content) > 0 {
		n := deployLogChunkBytes
		if n > len(content) {
			n = len(content)
		}
		onLog(stream, content[:n])
		content = content[n:]
	}
}

// ------------------------------------------------ agent 实时日志旁路 ------------------------------------------------

// agentLogTap 把 agent 的 job.log 帧旁路一份到发布日志。
//
// 关联方式：部署通道在调用 Dispatcher.Exec 前 Claim 一个「槽位」，
// HubSender 在发送该次 job.exec 时从信封里解析出 runID 并绑定到槽位；
// 之后该 runID 的 job.log 帧就会被路由到对应的 release_run。
// 若关联失败（例如同机同时有检测任务），退化为运行结束后一次性落库，不会丢日志。
type agentLogTap struct {
	st     *store.Store
	logger *slog.Logger

	mu      sync.Mutex
	claims  map[string]*tapClaim // serverID → 当前待绑定的槽位
	byRun   map[string]*tapClaim // runID → 槽位
	bound   map[string]*tapClaim // releaseRunID → 槽位（快速判定是否已捕获）
	maxKeep int
}

// tapClaim 是一次「等待绑定」的声明。
type tapClaim struct {
	serverID     string
	releaseRunID string
	captured     bool
	boundRunID   string
	released     bool
	tap          *agentLogTap
}

// newAgentLogTap 创建旁路器。
func newAgentLogTap(st *store.Store, logger *slog.Logger) *agentLogTap {
	if st == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &agentLogTap{
		st: st, logger: logger,
		claims: map[string]*tapClaim{}, byRun: map[string]*tapClaim{}, bound: map[string]*tapClaim{},
		maxKeep: 256,
	}
}

// Claim 声明「接下来这台服务器上的 job.exec 属于某个发布 run」。
func (t *agentLogTap) Claim(serverID string, releaseRunID string) *tapClaim {
	if t == nil {
		return &tapClaim{}
	}
	claim := &tapClaim{serverID: serverID, releaseRunID: releaseRunID, tap: t}
	t.mu.Lock()
	t.claims[serverID] = claim
	t.bound[releaseRunID] = claim
	if len(t.bound) > t.maxKeep {
		for id, old := range t.bound {
			if old.released {
				delete(t.bound, id)
			}
		}
	}
	t.mu.Unlock()
	return claim
}

// Bind 把一次 job.exec 的 runID 绑定到等待中的槽位（由 HubSender 调用）。
func (t *agentLogTap) Bind(serverID, runID string) {
	if t == nil || runID == "" {
		return
	}
	t.mu.Lock()
	claim, ok := t.claims[serverID]
	if ok {
		delete(t.claims, serverID)
		claim.boundRunID = runID
		t.byRun[runID] = claim
	}
	t.mu.Unlock()
}

// Handle 处理一条 agent 日志帧：命中绑定则写入发布日志。
func (t *agentLogTap) Handle(serverID, runID, stream, chunk string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	claim, ok := t.byRun[runID]
	t.mu.Unlock()
	if !ok {
		return
	}
	if _, err := t.st.AppendReleaseLog(context.Background(), claim.releaseRunID, stream, chunk, store.NowMS()); err != nil {
		t.logger.Warn("旁路写入发布日志失败", "release_run_id", claim.releaseRunID, "err", err)
		return
	}
	t.mu.Lock()
	claim.captured = true
	t.mu.Unlock()
}

// Release 结束一次声明（Run 返回后调用），并清理 runID 绑定。
func (c *tapClaim) Release() {
	if c == nil || c.tap == nil {
		return
	}
	t := c.tap
	t.mu.Lock()
	c.released = true
	if c.boundRunID != "" {
		delete(t.byRun, c.boundRunID)
	}
	if cur, ok := t.claims[c.serverID]; ok && cur == c {
		delete(t.claims, c.serverID)
	}
	delete(t.bound, c.releaseRunID)
	t.mu.Unlock()
}

// Captured 返回本次执行是否已通过帧旁路写入过日志。
func (c *tapClaim) Captured() bool {
	if c == nil {
		return false
	}
	c.tap.mu.Lock()
	defer c.tap.mu.Unlock()
	return c.captured
}

// extractRunIDFromEnvelope 从 job.exec 信封里解析 runID（type 必须是 job.exec）。
func extractRunIDFromEnvelope(envelope []byte) string {
	trimmed := strings.TrimSpace(string(envelope))
	if trimmed == "" {
		return ""
	}
	var env struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		return ""
	}
	if env.Type != "job.exec" {
		return ""
	}
	return env.ID
}
