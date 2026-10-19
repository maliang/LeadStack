// Package agent 实现 LeadStack 节点 agent 的运行时。
//
// 职责：
//   - 主动连接控制端 WebSocket（/agent/ws），握手时携带 Bearer 接入令牌；
//   - 连接后立即上报 hello，并按 hello_ack / config.push 下发的节奏上报心跳与指标；
//   - 断线后按指数退避（1s→2s→…→60s，±20% 抖动）无限重连，重连后重新 hello；
//   - 断线期间指标写入本地环形缓冲（保留 15 分钟），重连后按原始时间戳补传；
//   - 执行控制端下发的 job.exec 命令任务并回传日志与结果（M0 次级能力）。
//
// 生命周期约束：所有后台 goroutine 都绑定 context，父 context 取消后必须全部退出，
// 不允许泄漏；单次采集失败只跳过对应指标，不允许 panic，也不影响心跳。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/maliang/leadstack/pkg/protocol"
)

const (
	// 重连退避区间：从 1s 开始翻倍，上限 60s，叠加 ±20% 抖动。
	minBackoff = 1 * time.Second
	maxBackoff = 60 * time.Second
	// 连接稳定存活超过该时长后，退避时间重置回 1s。
	stableConnDuration = 30 * time.Second
	// 拨号与单次 hello_ack 等待超时。
	dialTimeout     = 10 * time.Second
	onceAckTimeout  = 5 * time.Second
	maxMessageBytes = 4 << 20 // 4 MiB，入站帧上限
)

// 指标补传时单帧最多携带的样本数，避免单帧过大。
const metricsBatchSize = 100

// Config 是 agent 的运行参数。
type Config struct {
	// Server 是控制端地址，支持 http://、https://、ws://、wss://。
	Server string
	// Token 是接入令牌（握手时的 Bearer 凭证）。
	Token string
	// Version 是 agent 版本号，由构建时注入。
	Version string
	// Once 为 true 时只做一次连接与上报后退出，便于本地自测。
	Once bool
	// Logger 是日志器，nil 时使用默认 info 级别。
	Logger *slog.Logger
}

// Agent 是节点 agent 运行时。
type Agent struct {
	cfg Config
	log *slog.Logger
	// col 采集器（内部串行化 gopsutil 调用，避免并发竞争）。
	col *Collector
	// buf 保存断线期间待补传的指标样本。
	buf *sampleBuffer
	// notify 用来在采样写入缓冲后唤醒发送协程（容量 1，重复通知会合并）。
	notify chan struct{}
	// hbNS / metNS 是当前心跳与指标间隔（纳秒），供多个协程无锁读取。
	hbNS  atomic.Int64
	metNS atomic.Int64
	// jobs 跟踪正在执行的命令任务。
	jobs *jobManager

	rndMu sync.Mutex
	rnd   *rand.Rand
}

// New 创建 agent 实例。
func New(cfg Config) *Agent {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	a := &Agent{
		cfg:    cfg,
		log:    log,
		col:    NewCollector(log),
		buf:    newSampleBuffer(defaultBufferCapacity, defaultBufferRetention),
		notify: make(chan struct{}, 1),
		jobs:   newJobManager(log),
		rnd:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	a.applyIntervals(protocol.DefaultHeartbeatIntervalS, protocol.DefaultMetricsIntervalS)
	return a
}

// Run 启动 agent 主循环，直到 ctx 被取消（或 --once 模式完成一次上报）才返回。
func (a *Agent) Run(ctx context.Context) error {
	if a.cfg.Once {
		return a.runOnce(ctx)
	}

	// 采样循环独立于连接生命周期：断线期间继续采样并写入环形缓冲，重连后补传。
	sampleCtx, stopSampling := context.WithCancel(ctx)
	var sampleWG sync.WaitGroup
	sampleWG.Add(1)
	go func() {
		defer sampleWG.Done()
		a.sampleLoop(sampleCtx)
	}()
	defer func() {
		stopSampling()
		sampleWG.Wait()
	}()

	defer a.jobs.close()

	backoff := minBackoff
	for {
		if ctx.Err() != nil {
			return nil
		}

		conn, err := a.dial(ctx)
		if err != nil {
			wait := a.jitter(backoff)
			a.log.Warn("连接控制端失败，等待重试", "err", err, "backoff", wait)
			if !sleepCtx(ctx, wait) {
				return nil
			}
			backoff = nextBackoff(backoff)
			continue
		}

		startedAt := time.Now()
		a.log.Info("已连接控制端", "server", a.cfg.Server)
		serveErr := a.serve(ctx, conn)
		if ctx.Err() != nil {
			return nil
		}

		// 连接稳定存活过就不再惩罚性退避；短暂连上又断则继续指数增长。
		if time.Since(startedAt) >= stableConnDuration {
			backoff = minBackoff
		}
		wait := a.jitter(backoff)
		a.log.Warn("与控制端连接断开，等待重连", "err", serveErr, "backoff", wait)
		if !sleepCtx(ctx, wait) {
			return nil
		}
		if time.Since(startedAt) < stableConnDuration {
			backoff = nextBackoff(backoff)
		}
	}
}

// sampleLoop 按指标间隔持续采样并写入缓冲，与连接状态无关。
func (a *Agent) sampleLoop(ctx context.Context) {
	// 先立即采一次，保证首次连接或重连后能马上有指标可发。
	a.collectOnce()
	for {
		timer := time.NewTimer(a.metricsInterval())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			a.collectOnce()
		}
	}
}

// collectOnce 采集一次指标并写入环形缓冲，然后唤醒发送协程。
func (a *Agent) collectOnce() {
	samples := a.col.Snapshot().Samples()
	if len(samples) == 0 {
		// 所有指标都采集失败时跳过本次上报，不影响心跳。
		a.log.Debug("本次采集没有任何可用指标，跳过")
		return
	}
	a.buf.Add(samples)
	select {
	case a.notify <- struct{}{}:
	default:
		// 已有未处理的通知，合并即可。
	}
}

// dial 建立到控制端的 WebSocket 连接。
func (a *Agent) dial(ctx context.Context) (*wsConn, error) {
	target, err := wsURL(a.cfg.Server)
	if err != nil {
		return nil, err
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: dialTimeout,
		Proxy:            http.ProxyFromEnvironment,
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+a.cfg.Token)

	conn, resp, err := dialer.DialContext(ctx, target, header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("握手失败（HTTP %s）: %w", resp.Status, err)
		}
		return nil, fmt.Errorf("拨号失败: %w", err)
	}
	conn.SetReadLimit(maxMessageBytes)
	return newWSConn(conn, a.log), nil
}

// serve 在单条已建立的连接上运行收发循环，连接断开或 ctx 取消后返回。
func (a *Agent) serve(ctx context.Context, w *wsConn) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer w.close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.writeLoop(ctx)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.readLoop(ctx, func(env protocol.Envelope) {
			a.handleFrame(ctx, w, env)
		})
	}()

	// 连接建立后第一帧必须是 hello；hello_ack 由读循环异步处理并更新上报间隔。
	if err := w.send(ctx, a.helloEnvelope()); err != nil {
		cancel()
		w.close()
		wg.Wait()
		return fmt.Errorf("发送 hello 失败: %w", err)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		a.heartbeatLoop(ctx, w)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.senderLoop(ctx, w)
	}()

	// 等待连接断开或父 ctx 取消。
	select {
	case <-ctx.Done():
	case <-w.closed:
	}
	// 正常退出（SIGINT/SIGTERM）时先发 close 帧；连接已断时该调用无害。
	// 然后取消 ctx 并关闭 socket，解除 ReadMessage 阻塞，等待所有协程退出。
	cancel()
	w.closeGracefully()
	wg.Wait()

	if err := w.Err(); err != nil {
		return err
	}
	return errConnClosed
}

// heartbeatLoop 负责心跳上报：连接建立后立即发一次，之后按当前间隔发送。
func (a *Agent) heartbeatLoop(ctx context.Context, w *wsConn) {
	if err := w.send(ctx, a.heartbeatEnvelope()); err != nil {
		return
	}
	for {
		timer := time.NewTimer(a.heartbeatInterval())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-w.closed:
			timer.Stop()
			return
		case <-timer.C:
			if err := w.send(ctx, a.heartbeatEnvelope()); err != nil {
				return
			}
		}
	}
}

// senderLoop 负责把缓冲中的指标发给控制端：连接建立后先补传积压，之后由采样通知驱动。
func (a *Agent) senderLoop(ctx context.Context, w *wsConn) {
	if err := a.flushBacklog(ctx, w); err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.closed:
			return
		case <-a.notify:
			if err := a.flushBacklog(ctx, w); err != nil {
				return
			}
		}
	}
}

// flushBacklog 排空环形缓冲并分批发送；发送失败时把未发出的样本放回缓冲，等重连补传。
func (a *Agent) flushBacklog(ctx context.Context, w *wsConn) error {
	samples := a.buf.Drain()
	for len(samples) > 0 {
		n := min(len(samples), metricsBatchSize)
		batch := samples[:n]
		env := newEnvelope(protocol.TypeMetrics, protocol.Metrics{Samples: batch})
		if err := w.send(ctx, env); err != nil {
			// 当前批次及后续样本都放回缓冲，服务端按 (server_id,metric,ts) 去重。
			a.buf.Add(samples)
			return err
		}
		a.log.Debug("已上报指标", "count", n)
		samples = samples[n:]
	}
	return nil
}

// handleFrame 处理控制端下发的帧；未识别的 type 只忽略，绝不断开连接。
func (a *Agent) handleFrame(ctx context.Context, w *wsConn, env protocol.Envelope) {
	switch env.Type {
	case protocol.TypeHelloAck:
		var ack protocol.HelloAck
		if err := json.Unmarshal(env.Payload, &ack); err != nil {
			a.log.Warn("解析 hello_ack 失败，沿用默认上报节奏", "err", err)
			return
		}
		a.applyIntervals(ack.HeartbeatIntervalS, ack.MetricsIntervalS)
		a.log.Info("收到 hello_ack",
			"server_id", ack.ServerID,
			"heartbeat_s", ack.HeartbeatIntervalS,
			"metrics_s", ack.MetricsIntervalS)

	case protocol.TypeConfigPush:
		var cfg protocol.ConfigPush
		if err := json.Unmarshal(env.Payload, &cfg); err != nil {
			a.log.Warn("解析 config.push 失败", "err", err)
			return
		}
		a.applyConfigPush(cfg)
		a.log.Info("收到 config.push",
			"heartbeat_s", a.heartbeatInterval().Seconds(),
			"metrics_s", a.metricsInterval().Seconds())

	case protocol.TypeJobExec:
		var spec protocol.JobExec
		if err := json.Unmarshal(env.Payload, &spec); err != nil {
			a.log.Warn("解析 job.exec 失败", "err", err)
			return
		}
		// 异步执行，避免阻塞读循环（长任务不能影响心跳收发）。
		a.jobs.start(ctx, w, spec)

	case protocol.TypeTermOpen, protocol.TypeTermResize, protocol.TypeTermClose:
		// M0 不实现交互式终端；忽略而不是断开，保证向前兼容。
		a.log.Debug("M0 暂不支持终端消息，已忽略", "type", env.Type)

	default:
		a.log.Debug("忽略未识别的消息类型", "type", env.Type)
	}
}

// heartbeatEnvelope 采集并构造一条心跳帧。
func (a *Agent) heartbeatEnvelope() protocol.Envelope {
	snap := a.col.Snapshot()
	hb := snap.Heartbeat()
	a.log.Debug("采集心跳",
		"cpu_pct", hb.CPUPct,
		"mem_used_pct", hb.MemUsedPct,
		"disk_used_pct", hb.DiskUsedPt,
		"load1", hb.Load1,
		"uptime_s", hb.UptimeS)
	return newEnvelope(protocol.TypeHeartbeat, hb)
}

// helloEnvelope 构造 hello 帧，携带节点静态信息。
func (a *Agent) helloEnvelope() protocol.Envelope {
	info := a.col.Info()
	a.log.Debug("上报 hello", "hostname", info.Hostname, "os", info.OS, "arch", info.Arch)
	return newEnvelope(protocol.TypeHello, protocol.Hello{
		Hostname:     info.Hostname,
		OS:           info.OS,
		Arch:         info.Arch,
		Kernel:       info.Kernel,
		AgentVersion: a.cfg.Version,
		BootTime:     info.BootTimeMS,
	})
}

// runOnce 完成一次「连接 → hello → hello_ack → heartbeat + metrics」后退出，
// 供本地自测与联调使用。拿不到 hello_ack 视为失败，保证自测结论可信。
func (a *Agent) runOnce(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	w, err := a.dial(dialCtx)
	if err != nil {
		return err
	}
	defer w.closeGracefully()

	if err := w.writeDirect(a.helloEnvelope()); err != nil {
		return fmt.Errorf("发送 hello 失败: %w", err)
	}

	deadline := time.Now().Add(onceAckTimeout)
	acked := false
	for !acked {
		if time.Now().After(deadline) {
			return fmt.Errorf("等待 hello_ack 超时（%s）", onceAckTimeout)
		}
		_ = w.conn.SetReadDeadline(deadline)
		_, data, err := w.conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("等待 hello_ack 失败: %w", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		switch env.Type {
		case protocol.TypeHelloAck:
			var ack protocol.HelloAck
			if err := json.Unmarshal(env.Payload, &ack); err != nil {
				return fmt.Errorf("解析 hello_ack 失败: %w", err)
			}
			a.applyIntervals(ack.HeartbeatIntervalS, ack.MetricsIntervalS)
			a.log.Info("收到 hello_ack",
				"server_id", ack.ServerID,
				"heartbeat_s", ack.HeartbeatIntervalS,
				"metrics_s", ack.MetricsIntervalS)
			acked = true
		case protocol.TypeConfigPush:
			var push protocol.ConfigPush
			if json.Unmarshal(env.Payload, &push) == nil {
				a.applyConfigPush(push)
			}
		default:
			a.log.Debug("单次模式忽略消息", "type", env.Type)
		}
	}
	_ = w.conn.SetReadDeadline(time.Time{})

	if err := w.writeDirect(a.heartbeatEnvelope()); err != nil {
		return fmt.Errorf("发送 heartbeat 失败: %w", err)
	}
	samples := a.col.Snapshot().Samples()
	if len(samples) > 0 {
		if err := w.writeDirect(newEnvelope(protocol.TypeMetrics, protocol.Metrics{Samples: samples})); err != nil {
			return fmt.Errorf("发送 metrics 失败: %w", err)
		}
	}
	a.log.Info("单次上报完成", "server", a.cfg.Server, "metrics", len(samples))
	return nil
}

// heartbeatInterval 返回当前心跳间隔。
func (a *Agent) heartbeatInterval() time.Duration {
	return time.Duration(a.hbNS.Load())
}

// metricsInterval 返回当前指标采集/上报间隔。
func (a *Agent) metricsInterval() time.Duration {
	return time.Duration(a.metNS.Load())
}

// applyIntervals 应用控制端下发的间隔配置，非正值保持原值。
func (a *Agent) applyIntervals(heartbeatS, metricsS int) {
	if heartbeatS > 0 {
		a.hbNS.Store(int64(time.Duration(heartbeatS) * time.Second))
	}
	if metricsS > 0 {
		a.metNS.Store(int64(time.Duration(metricsS) * time.Second))
	}
}

// applyConfigPush 应用 config.push 中非空的间隔字段。
func (a *Agent) applyConfigPush(cfg protocol.ConfigPush) {
	if cfg.HeartbeatIntervalS != nil {
		a.applyIntervals(*cfg.HeartbeatIntervalS, 0)
	}
	if cfg.MetricsIntervalS != nil {
		a.applyIntervals(0, *cfg.MetricsIntervalS)
	}
}

// jitter 给退避时长叠加 ±20% 的随机抖动。
func (a *Agent) jitter(base time.Duration) time.Duration {
	a.rndMu.Lock()
	f := a.rnd.Float64()
	a.rndMu.Unlock()
	return jitterDuration(base, f)
}

// jitterDuration 按 [0,1) 的随机数给 base 叠加 ±20% 抖动。
func jitterDuration(base time.Duration, f float64) time.Duration {
	return time.Duration(float64(base) * (0.8 + 0.4*f))
}

// nextBackoff 返回下一档退避时长：翻倍并夹在 [minBackoff, maxBackoff] 内。
func nextBackoff(cur time.Duration) time.Duration {
	next := cur * 2
	if next > maxBackoff {
		return maxBackoff
	}
	if next < minBackoff {
		return minBackoff
	}
	return next
}

// sleepCtx 在 ctx 取消前睡眠 d，返回 false 表示 ctx 已取消。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// wsURL 把控制端 HTTP(S) 地址转换成 /agent/ws 的 WebSocket 地址。
func wsURL(server string) (string, error) {
	raw := strings.TrimSpace(server)
	if raw == "" {
		return "", errors.New("控制端地址为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("解析控制端地址失败: %w", err)
	}
	switch u.Scheme {
	case "http", "":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
		// 保持原样
	default:
		return "", fmt.Errorf("不支持的控制端协议 %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("控制端地址缺少主机名: %q", server)
	}
	p := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(p, "/agent/ws") {
		p += "/agent/ws"
	}
	u.Path = p
	return u.String(), nil
}

// newEnvelope 构造统一信封：自动生成 UUID 与 Unix 毫秒时间戳。
func newEnvelope(msgType string, payload any) protocol.Envelope {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte("{}")
	}
	return protocol.Envelope{
		Type:    msgType,
		ID:      uuid.NewString(),
		TS:      nowMillis(),
		Payload: raw,
	}
}

// nowMillis 返回当前 Unix 毫秒时间戳。
func nowMillis() int64 {
	return time.Now().UnixMilli()
}
