// Package collector 实现 agentless（mode=ssh）采集：控制端通过 SSH 主动拉取主机信息与指标。
//
// 行为契约见 docs/ARCHITECTURE.md 第 10 节：
//   - 按 LEADSTACK_SSH_INTERVAL（默认 30s）采集；
//   - 连续 3 次失败置 offline 并写 last_error；成功一次即恢复 online；
//   - 并发有上限（默认 8），且不对同一台机器并发采集。
package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/credential"
	"github.com/maliang/leadstack/internal/hostinfo"
	"github.com/maliang/leadstack/internal/sshx"
	"github.com/maliang/leadstack/internal/store"
	"github.com/maliang/leadstack/pkg/protocol"
)

// 默认参数。
const (
	// DefaultInterval 是采集间隔（配置项 LEADSTACK_SSH_INTERVAL 的默认值同源）。
	DefaultInterval = 30 * time.Second
	// DefaultConcurrency 是采集最大并发数。
	DefaultConcurrency = 8
	// DefaultDialTimeout 是单次 SSH 连接超时。
	DefaultDialTimeout = 10 * time.Second
	// DefaultOpTimeout 是单台服务器一轮采集的总超时。
	DefaultOpTimeout = 30 * time.Second
	// offlineAfterFailures 是连续失败多少次判定离线（契约第 10 节）。
	offlineAfterFailures = 3
)

// Options 是构造 Collector 的入参。
type Options struct {
	Store       *store.Store
	Cipher      *credential.Cipher
	Logger      *slog.Logger
	Interval    time.Duration
	Timeout     time.Duration
	Concurrency int
	// KnownHostsPath 是 accept-new / strict 策略使用的 known_hosts 文件路径。
	KnownHostsPath string
}

// Collector 是 agentless 采集器。
type Collector struct {
	store   *store.Store
	cipher  *credential.Cipher
	logger  *slog.Logger
	sampler *hostinfo.Sampler

	interval    time.Duration
	timeout     time.Duration
	concurrency int
	knownHosts  string

	sem chan struct{}

	mu       sync.Mutex
	failures map[string]int
	inflight map[string]bool
}

// New 校验依赖并构造采集器。
func New(opts Options) (*Collector, error) {
	if opts.Store == nil {
		return nil, errors.New("初始化采集器: store 为空")
	}
	if opts.Cipher == nil {
		return nil, errors.New("初始化采集器: cipher 为空")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultOpTimeout
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	return &Collector{
		store:       opts.Store,
		cipher:      opts.Cipher,
		logger:      logger,
		sampler:     hostinfo.NewSampler(), // 全局单例：Sampler 有状态，复用才能算出区间 CPU
		interval:    interval,
		timeout:     timeout,
		concurrency: concurrency,
		knownHosts:  opts.KnownHostsPath,
		sem:         make(chan struct{}, concurrency),
		failures:    make(map[string]int),
		inflight:    make(map[string]bool),
	}, nil
}

// Interval 返回采集间隔。
func (c *Collector) Interval() time.Duration { return c.interval }

// Run 启动采集循环，直到 ctx 被取消。
func (c *Collector) Run(ctx context.Context) {
	c.logger.Info("agentless 采集循环启动",
		"interval", c.interval.String(),
		"concurrency", c.concurrency,
		"known_hosts", c.knownHosts,
	)

	// 启动先跑一轮，避免新接入的服务器等一个完整间隔。
	c.CollectOnce(ctx)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			c.logger.Info("agentless 采集循环退出")
			return
		case <-ticker.C:
			c.CollectOnce(ctx)
		}
	}
}

// CollectOnce 执行一轮采集并等待全部完成（轮次之间不重叠，天然避免同一台机器并发采集）。
func (c *Collector) CollectOnce(ctx context.Context) {
	servers, err := c.store.ListServersByMode(ctx, store.ModeSSH)
	if err != nil {
		c.logger.Warn("agentless 采集: 读取服务器列表失败", "err", err)
		return
	}
	if len(servers) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, srv := range servers {
		if !c.beginCollect(srv.ID) {
			c.logger.Debug("agentless 采集: 该服务器已有采集在途，跳过", "server_id", srv.ID)
			continue
		}
		srv := srv
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer c.endCollect(srv.ID)

			select {
			case c.sem <- struct{}{}:
				defer func() { <-c.sem }()
			case <-ctx.Done():
				return
			}
			c.collectOne(ctx, srv)
		}()
	}
	wg.Wait()
}

// beginCollect 尝试独占一台服务器的采集权。
func (c *Collector) beginCollect(serverID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inflight[serverID] {
		return false
	}
	c.inflight[serverID] = true
	return true
}

// endCollect 释放采集权。
func (c *Collector) endCollect(serverID string) {
	c.mu.Lock()
	delete(c.inflight, serverID)
	c.mu.Unlock()
}

// collectOne 采集单台服务器一次。
func (c *Collector) collectOne(ctx context.Context, srv store.Server) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	secret, err := c.secretFor(ctx, srv)
	if err != nil {
		// 未绑定凭证属于配置问题，不计入失败次数，只记录原因。
		c.recordConfigError(ctx, srv.ID, err)
		return
	}

	client, err := c.dial(ctx, srv, secret)
	if err != nil {
		c.recordFailure(ctx, srv, fmt.Errorf("SSH 连接失败: %w", err))
		return
	}
	defer func() {
		if cerr := client.Close(); cerr != nil {
			c.logger.Debug("关闭 SSH 连接失败", "server_id", srv.ID, "err", cerr)
		}
	}()

	samples, err := c.sampler.Collect(ctx, srv.ID, client)
	if err != nil {
		c.recordFailure(ctx, srv, fmt.Errorf("采集指标失败: %w", err))
		return
	}

	// 采集期间服务器可能已被删除：写库前确认一次，避免留下孤儿指标。
	if _, err := c.store.GetServer(ctx, srv.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.logger.Debug("服务器已删除，跳过本轮采集结果", "server_id", srv.ID)
			return
		}
		c.logger.Warn("确认服务器是否存在失败", "server_id", srv.ID, "err", err)
		return
	}

	nowMS := store.NowMS()
	if len(samples) > 0 {
		if _, err := c.store.InsertSamples(ctx, srv.ID, toStoreSamples(samples)); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return // 服务器已被删除
			}
			c.recordFailure(ctx, srv, fmt.Errorf("写入指标失败: %w", err))
			return
		}
	}

	c.recordSuccess(ctx, srv, nowMS)

	// 顺带刷新一次主机信息（同一连接内完成，避免额外建连开销）。
	if info, err := hostinfo.Collect(ctx, client); err == nil {
		c.saveHostInfo(ctx, srv.ID, info)
	} else {
		c.logger.Debug("agentless 采集: 主机信息采集失败已跳过", "server_id", srv.ID, "err", err)
	}
}

// secretFor 读取并解密服务器绑定的 SSH 凭证。
func (c *Collector) secretFor(ctx context.Context, srv store.Server) (credential.Secret, error) {
	if srv.CredentialID == nil || strings.TrimSpace(*srv.CredentialID) == "" {
		return credential.Secret{}, errors.New("未绑定 SSH 凭证")
	}
	cred, err := c.store.GetCredential(ctx, *srv.CredentialID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return credential.Secret{}, errors.New("绑定的 SSH 凭证不存在")
		}
		return credential.Secret{}, fmt.Errorf("读取凭证失败: %w", err)
	}
	plain, err := c.cipher.Decrypt(cred.SecretEnc)
	if err != nil {
		return credential.Secret{}, fmt.Errorf("解密凭证失败: %w", err)
	}
	secret, err := credential.Unmarshal(plain)
	if err != nil {
		return credential.Secret{}, fmt.Errorf("解析凭证失败: %w", err)
	}
	return secret, nil
}

// dial 按服务器的策略建立 SSH 连接。
func (c *Collector) dial(ctx context.Context, srv store.Server, secret credential.Secret) (*sshx.Client, error) {
	policy := sshx.HostKeyPolicy(srv.HostKeyPolicy)
	if policy == "" {
		policy = sshx.PolicyAcceptNew
	}
	serverID := srv.ID
	client, err := sshx.Dial(ctx, sshx.DialOptions{
		Host: srv.Host,
		Port: srv.Port,
		Auth: sshx.Auth{
			Username:   secret.Username,
			Password:   secret.Password,
			PrivateKey: secret.PrivateKey,
			Passphrase: secret.Passphrase,
		},
		Timeout:        DefaultDialTimeout,
		HostKeyPolicy:  policy,
		KnownHostsPath: c.knownHosts,
		OnNewHostKey: func(host, fingerprint string) {
			// known_hosts 由 sshx 负责落盘，这里只留一条审计日志（绝不记录密钥内容）。
			c.logger.Info("首次见到主机密钥，已记录", "server_id", serverID, "host", host, "fingerprint", fingerprint)
		},
	})
	if err != nil {
		return nil, err
	}
	return client, nil
}

// recordSuccess 记录一次成功采集：清空错误、状态置 online、必要时写上线事件。
func (c *Collector) recordSuccess(ctx context.Context, srv store.Server, nowMS int64) {
	c.mu.Lock()
	prevFailures := c.failures[srv.ID]
	delete(c.failures, srv.ID)
	c.mu.Unlock()

	status := protocol.StatusOnline
	// 传空串（非 nil）表示清空 last_error：采集恢复成功后不再展示过期故障。
	clearError := ""
	if err := c.store.UpdateServerCollectionState(ctx, srv.ID, &status, &nowMS, &clearError); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			c.logger.Warn("更新采集状态失败", "server_id", srv.ID, "err", err)
		}
		return
	}
	if srv.Status != protocol.StatusOnline {
		c.logger.Info("agentless 采集成功，服务器上线",
			"server_id", srv.ID, "prev_status", srv.Status, "prev_failures", prevFailures)
		c.recordEvent(ctx, srv.ID, store.EventKindOnline, "SSH 采集成功，服务器上线")
	}
}

// recordFailure 记录一次采集失败：累计 3 次置 offline 并写事件。
func (c *Collector) recordFailure(ctx context.Context, srv store.Server, cause error) {
	c.mu.Lock()
	c.failures[srv.ID]++
	failures := c.failures[srv.ID]
	c.mu.Unlock()

	msg := cause.Error()
	c.logger.Warn("agentless 采集失败", "server_id", srv.ID, "failures", failures, "err", msg)

	if failures < offlineAfterFailures {
		// 还没到阈值：只更新错误原因，保持原状态。
		if err := c.store.UpdateServerCollectionState(ctx, srv.ID, nil, nil, &msg); err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				c.logger.Warn("记录采集错误失败", "server_id", srv.ID, "err", err)
			}
		}
		return
	}

	status := protocol.StatusOffline
	if err := c.store.UpdateServerCollectionState(ctx, srv.ID, &status, nil, &msg); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			c.logger.Warn("更新采集状态失败", "server_id", srv.ID, "err", err)
		}
		return
	}
	if srv.Status != protocol.StatusOffline {
		c.logger.Info("agentless 连续采集失败，服务器离线", "server_id", srv.ID, "failures", failures)
		c.recordEvent(ctx, srv.ID, store.EventKindOffline,
			fmt.Sprintf("SSH 采集连续失败 %d 次，判定为离线：%s", failures, msg))
	}
}

// recordConfigError 记录配置类问题（例如未绑定凭证），不计入失败次数。
func (c *Collector) recordConfigError(ctx context.Context, serverID string, cause error) {
	msg := cause.Error()
	c.logger.Warn("agentless 采集跳过", "server_id", serverID, "reason", msg)
	if err := c.store.UpdateServerCollectionState(ctx, serverID, nil, nil, &msg); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			c.logger.Warn("记录采集跳过原因失败", "server_id", serverID, "err", err)
		}
	}
}

// recordEvent 写一条服务器事件；失败只记日志。
func (c *Collector) recordEvent(ctx context.Context, serverID, kind, message string) {
	ev := &store.Event{
		ID:       uuid.NewString(),
		ServerID: &serverID,
		Kind:     kind,
		Message:  message,
		TS:       store.NowMS(),
	}
	if err := c.store.InsertEvent(ctx, ev); err != nil {
		c.logger.Warn("写入事件失败", "server_id", serverID, "kind", kind, "err", err)
	}
}

// saveHostInfo 把主机信息序列化后落库。
func (c *Collector) saveHostInfo(ctx context.Context, serverID string, info hostinfo.Info) {
	if info.CollectedAt == 0 {
		info.CollectedAt = store.NowMS()
	}
	raw, err := jsonMarshal(info)
	if err != nil {
		c.logger.Warn("序列化主机信息失败", "server_id", serverID, "err", err)
		return
	}
	if err := c.store.UpsertServerHostInfo(ctx, serverID, string(raw), info.CollectedAt); err != nil {
		c.logger.Warn("写入主机信息失败", "server_id", serverID, "err", err)
		return
	}
	if err := c.store.UpdateServerSystemInfo(ctx, serverID, info.OS, info.Arch, info.Kernel); err != nil {
		c.logger.Debug("同步系统信息到服务器记录失败", "server_id", serverID, "err", err)
	}
}

// toStoreSamples 把协议采样转换成持久化模型。
func toStoreSamples(samples []protocol.Sample) []store.MetricSample {
	out := make([]store.MetricSample, 0, len(samples))
	for _, smp := range samples {
		out = append(out, store.MetricSample{Metric: smp.Metric, TS: smp.TS, Value: smp.Value})
	}
	return out
}
