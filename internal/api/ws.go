package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/maliang/leadstack/internal/auth"
	"github.com/maliang/leadstack/internal/store"
	"github.com/maliang/leadstack/pkg/protocol"
)

// WebSocket 连接参数。
const (
	// maxAgentFrameBytes 限制单帧大小，防止 agent 侧异常导致内存膨胀。
	maxAgentFrameBytes = 1 << 20 // 1 MiB
	// agentReadTimeout 是读超时：心跳 20s 一次，给 3 倍以上宽限。
	agentReadTimeout = 75 * time.Second
	// agentWriteTimeout 是写超时。
	agentWriteTimeout = 10 * time.Second
	// agentPingInterval 是服务端主动 ping 的间隔，用于探测半开连接。
	agentPingInterval = 30 * time.Second
	// agentHandleTimeout 是单帧入库操作的处理超时。
	agentHandleTimeout = 15 * time.Second
)

// agentUpgrader 是 WebSocket 升级器。
//
// agent 不是浏览器，没有同源概念，因此放行所有 Origin；
// 真正的鉴权靠握手时的 Bearer enroll-token。
var agentUpgrader = websocket.Upgrader{
	ReadBufferSize:   4096,
	WriteBufferSize:  4096,
	HandshakeTimeout: 10 * time.Second,
	CheckOrigin:      func(*http.Request) bool { return true },
	Error: func(w http.ResponseWriter, r *http.Request, status int, reason error) {
		writeError(w, status, codeInvalidArgument, "WebSocket 握手失败: "+reason.Error())
	},
}

// Hub 维护「服务器 ID → 活跃 agent 连接」的映射。
type Hub struct {
	mu    sync.RWMutex
	conns map[string]*agentConn
}

// NewHub 创建空连接表。
func NewHub() *Hub {
	return &Hub{conns: make(map[string]*agentConn)}
}

// Register 登记一条连接；同一服务器的新连接会顶掉旧连接。
func (h *Hub) Register(serverID string, c *agentConn) {
	if serverID == "" || c == nil {
		return
	}
	h.mu.Lock()
	old := h.conns[serverID]
	h.conns[serverID] = c
	h.mu.Unlock()
	if old != nil && old != c {
		// 同一服务器重复接入（例如 agent 重启后旧连接未及时断开）：关掉旧连接。
		old.close()
	}
}

// Unregister 注销连接；仅当表中仍是同一条连接时才删除，避免误删新连接。
func (h *Hub) Unregister(serverID string, c *agentConn) {
	if serverID == "" {
		return
	}
	h.mu.Lock()
	if cur, ok := h.conns[serverID]; ok && cur == c {
		delete(h.conns, serverID)
	}
	h.mu.Unlock()
}

// Get 返回某服务器的活跃连接。
func (h *Hub) Get(serverID string) (*agentConn, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	c, ok := h.conns[serverID]
	return c, ok
}

// Count 返回当前活跃连接数。
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

// CloseAll 关闭全部活跃连接（优雅关闭时调用；hijack 的连接不受 Shutdown 影响）。
func (h *Hub) CloseAll() {
	h.mu.Lock()
	conns := make([]*agentConn, 0, len(h.conns))
	for id, c := range h.conns {
		conns = append(conns, c)
		delete(h.conns, id)
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.close()
	}
}

// agentConn 是单条 agent 连接的封装。
type agentConn struct {
	ws         *websocket.Conn
	remoteAddr string

	writeMu  sync.Mutex
	mu       sync.RWMutex
	serverID string

	closeOnce sync.Once
}

// setServerID 记录该连接绑定的服务器。
func (c *agentConn) setServerID(id string) {
	c.mu.Lock()
	c.serverID = id
	c.mu.Unlock()
}

// getServerID 读取该连接绑定的服务器。
func (c *agentConn) getServerID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverID
}

// writeEnvelope 发送一帧 JSON 文本消息。
func (c *agentConn) writeEnvelope(env protocol.Envelope) error {
	payload, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("序列化消息 %s: %w", env.Type, err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.ws.SetWriteDeadline(time.Now().Add(agentWriteTimeout)); err != nil {
		return fmt.Errorf("设置写超时: %w", err)
	}
	if err := c.ws.WriteMessage(websocket.TextMessage, payload); err != nil {
		return fmt.Errorf("发送消息 %s: %w", env.Type, err)
	}
	return nil
}

// ping 发送 WebSocket ping 控制帧。
func (c *agentConn) ping() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(agentWriteTimeout))
}

// close 关闭底层连接（幂等）。
func (c *agentConn) close() {
	c.closeOnce.Do(func() {
		if err := c.ws.Close(); err != nil {
			slog.Debug("关闭 agent 连接失败", "err", err)
		}
	})
}

// handleAgentWS 处理 /agent/ws：校验接入令牌 → 升级 → 进入会话循环。
func (a *API) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	plainToken := auth.BearerToken(r)
	if plainToken == "" {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "缺少接入令牌（Authorization: Bearer <token>）")
		return
	}

	tok, err := a.st.FindEnrollTokenByHash(r.Context(), hashEnrollToken(plainToken), a.nowMS())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrExpired):
			a.logger.Warn("agent 接入被拒：令牌已过期", "remote", remoteHost(r))
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "接入令牌已过期")
		case errors.Is(err, store.ErrNotFound):
			a.logger.Warn("agent 接入被拒：令牌无效", "remote", remoteHost(r))
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "接入令牌无效")
		default:
			a.internalError(w, r, err)
		}
		return
	}

	conn, err := agentUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade 内部已通过 agentUpgrader.Error 写过响应，这里只记日志。
		a.logger.Warn("agent WebSocket 升级失败", "remote", remoteHost(r), "err", err)
		return
	}

	ac := &agentConn{ws: conn, remoteAddr: remoteHost(r)}
	a.logger.Info("agent 连接建立", "remote", ac.remoteAddr, "token_id", tok.ID)
	a.runAgentSession(r.Context(), ac, tok, remoteHost(r))
}

// runAgentSession 是单条 agent 连接的主循环。
func (a *API) runAgentSession(ctx context.Context, ac *agentConn, tok *store.EnrollToken, remoteHost string) {
	startedAt := time.Now()
	defer func() {
		if id := ac.getServerID(); id != "" {
			a.hub.Unregister(id, ac)
			// 连接断开：唤醒所有等待该服务器任务结果的检测/下发请求，避免它们空等到超时。
			a.failServerJobs(id, errors.New("agent 连接已断开"))
		}
		ac.close()
		a.logger.Info("agent 连接结束",
			"server_id", ac.getServerID(),
			"remote", remoteHost,
			"duration_s", int64(time.Since(startedAt).Seconds()),
		)
	}()

	ac.ws.SetReadLimit(maxAgentFrameBytes)
	if err := ac.ws.SetReadDeadline(time.Now().Add(agentReadTimeout)); err != nil {
		a.logger.Warn("设置读超时失败", "err", err)
		return
	}
	// 收到 pong 说明对端仍活着，续期读超时。
	ac.ws.SetPongHandler(func(string) error {
		return ac.ws.SetReadDeadline(time.Now().Add(agentReadTimeout))
	})

	pingCtx, cancelPing := context.WithCancel(ctx)
	defer cancelPing()
	go a.agentPingLoop(pingCtx, ac)

	for {
		msgType, data, err := ac.ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				a.logger.Warn("agent 连接异常结束", "remote", remoteHost, "err", err)
			}
			return
		}
		// 只处理 JSON 文本帧，其余类型直接忽略（向前兼容）。
		if msgType != websocket.TextMessage {
			a.logger.Debug("忽略非文本帧", "type", msgType, "remote", remoteHost)
			continue
		}
		if err := ac.ws.SetReadDeadline(time.Now().Add(agentReadTimeout)); err != nil {
			a.logger.Warn("续期读超时失败", "err", err)
			return
		}

		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			a.logger.Warn("忽略无法解析的 agent 帧", "remote", remoteHost, "err", err)
			continue
		}
		a.dispatchAgentFrame(ctx, ac, tok, remoteHost, env)
	}
}

// agentPingLoop 周期性发送 ping，探测半开连接。
func (a *API) agentPingLoop(ctx context.Context, ac *agentConn) {
	ticker := time.NewTicker(agentPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := ac.ping(); err != nil {
				a.logger.Debug("发送 ping 失败，关闭连接", "server_id", ac.getServerID(), "err", err)
				ac.close()
				return
			}
		}
	}
}

// dispatchAgentFrame 按消息类型分发；单帧失败只记日志，绝不因此断开连接。
func (a *API) dispatchAgentFrame(ctx context.Context, ac *agentConn, tok *store.EnrollToken, remoteHost string, env protocol.Envelope) {
	handleCtx, cancel := context.WithTimeout(ctx, agentHandleTimeout)
	defer cancel()

	var err error
	switch env.Type {
	case protocol.TypeHello:
		err = a.onAgentHello(handleCtx, ac, tok, remoteHost, env)
	case protocol.TypeHeartbeat:
		err = a.onAgentHeartbeat(handleCtx, ac, tok, remoteHost, env)
	case protocol.TypeMetrics:
		err = a.onAgentMetrics(handleCtx, ac, tok, remoteHost, env)
	case protocol.TypeHostInfo:
		err = a.onAgentHostInfo(handleCtx, ac, tok, remoteHost, env)
	case protocol.TypeJobAck, protocol.TypeJobLog, protocol.TypeJobDone:
		// M2：任务帧真正转给下发器（jobs.Dispatcher），agent 模式的 process/script 检测依赖此链路。
		result, err := a.dispatchJobFrame(ac.getServerID(), env)
		if err == nil && result.Handled {
			a.logger.Debug("已处理任务帧", "type", env.Type, "server_id", ac.getServerID(), "run_id", result.RunID)
		} else if err == nil {
			a.logger.Debug("任务下发器未装配，忽略任务帧", "type", env.Type, "server_id", ac.getServerID())
		} else {
			a.logger.Warn("处理任务帧失败", "type", env.Type, "server_id", ac.getServerID(), "err", err)
		}
	case protocol.TypeTermData:
		// 终端输出：按 session_id 转发给对应的 Web 终端。
		var data protocol.TermData
		if len(env.Payload) > 0 {
			if uerr := json.Unmarshal(env.Payload, &data); uerr != nil {
				a.logger.Debug("解析 term.data 失败", "err", uerr)
				break
			}
		}
		a.routeAgentTermData(data.SessionID, data.B64)
	default:
		a.logger.Debug("忽略未识别的消息类型", "type", env.Type, "server_id", ac.getServerID())
	}
	if err != nil {
		a.logger.Warn("处理 agent 消息失败", "type", env.Type, "server_id", ac.getServerID(), "err", err)
	}
}

// onAgentHello 处理 hello：绑定/创建服务器记录，回 hello_ack。
func (a *API) onAgentHello(ctx context.Context, ac *agentConn, tok *store.EnrollToken, remoteHost string, env protocol.Envelope) error {
	var hello protocol.Hello
	if len(env.Payload) > 0 {
		if err := json.Unmarshal(env.Payload, &hello); err != nil {
			return fmt.Errorf("解析 hello 载荷: %w", err)
		}
	}

	serverID, created, err := a.ensureServer(ctx, ac, tok, remoteHost, hello)
	if err != nil {
		return err
	}
	if created {
		a.logger.Info("agent 首次接入，已创建服务器记录",
			"server_id", serverID, "hostname", hello.Hostname, "remote", remoteHost)
	}

	if err := a.st.ApplyServerIdentity(ctx, serverID, remoteHost, store.ServerIdentity{
		AgentVersion: strings.TrimSpace(hello.AgentVersion),
		OS:           strings.TrimSpace(hello.OS),
		Arch:         strings.TrimSpace(hello.Arch),
		Kernel:       strings.TrimSpace(hello.Kernel),
		Hostname:     strings.TrimSpace(hello.Hostname),
	}); err != nil {
		return err
	}
	if err := a.touchServer(ctx, serverID); err != nil {
		return err
	}

	ack := protocol.HelloAck{
		ServerID:           serverID,
		HeartbeatIntervalS: protocol.DefaultHeartbeatIntervalS,
		MetricsIntervalS:   protocol.DefaultMetricsIntervalS,
	}
	payload, err := json.Marshal(ack)
	if err != nil {
		return fmt.Errorf("序列化 hello_ack: %w", err)
	}
	if err := ac.writeEnvelope(protocol.Envelope{
		Type:    protocol.TypeHelloAck,
		ID:      uuid.NewString(),
		TS:      a.nowMS(),
		Payload: payload,
	}); err != nil {
		return err
	}
	a.logger.Info("agent 握手完成",
		"server_id", serverID,
		"hostname", hello.Hostname,
		"os", hello.OS,
		"arch", hello.Arch,
		"agent_version", hello.AgentVersion,
	)
	return nil
}

// onAgentHeartbeat 处理 heartbeat：刷新最后心跳时间并顺带落库轻量指标。
func (a *API) onAgentHeartbeat(ctx context.Context, ac *agentConn, tok *store.EnrollToken, remoteHost string, env protocol.Envelope) error {
	var hb protocol.Heartbeat
	if len(env.Payload) > 0 {
		if err := json.Unmarshal(env.Payload, &hb); err != nil {
			return fmt.Errorf("解析 heartbeat 载荷: %w", err)
		}
	}

	serverID, _, err := a.ensureServer(ctx, ac, tok, remoteHost, protocol.Hello{})
	if err != nil {
		return err
	}
	if err := a.touchServer(ctx, serverID); err != nil {
		return err
	}

	// 心跳里携带的读数直接作为指标落库，保证新接入的服务器立刻有 CPU/内存/负载可展示。
	nowMS := a.nowMS()
	samples := []store.MetricSample{
		{Metric: protocol.MetricCPUPct, TS: nowMS, Value: hb.CPUPct},
		{Metric: protocol.MetricMemUsedPct, TS: nowMS, Value: hb.MemUsedPct},
		{Metric: protocol.MetricDiskUsedPt, TS: nowMS, Value: hb.DiskUsedPt},
		{Metric: protocol.MetricLoad1, TS: nowMS, Value: hb.Load1},
	}
	if _, err := a.st.InsertSamples(ctx, serverID, samples); err != nil {
		return err
	}
	return nil
}

// onAgentMetrics 处理 metrics：批量入库，按 (server_id, metric, ts) 去重。
func (a *API) onAgentMetrics(ctx context.Context, ac *agentConn, tok *store.EnrollToken, remoteHost string, env protocol.Envelope) error {
	var batch protocol.Metrics
	if len(env.Payload) > 0 {
		if err := json.Unmarshal(env.Payload, &batch); err != nil {
			return fmt.Errorf("解析 metrics 载荷: %w", err)
		}
	}
	if len(batch.Samples) == 0 {
		return nil
	}

	serverID, _, err := a.ensureServer(ctx, ac, tok, remoteHost, protocol.Hello{})
	if err != nil {
		return err
	}
	if err := a.touchServer(ctx, serverID); err != nil {
		return err
	}

	nowMS := a.nowMS()
	samples := make([]store.MetricSample, 0, len(batch.Samples))
	skipped := 0
	for _, smp := range batch.Samples {
		if !isKnownMetric(smp.Metric) {
			skipped++
			continue
		}
		ts := smp.TS
		if ts <= 0 {
			ts = nowMS
		}
		samples = append(samples, store.MetricSample{Metric: smp.Metric, TS: ts, Value: smp.Value})
	}
	written, err := a.st.InsertSamples(ctx, serverID, samples)
	if err != nil {
		return err
	}
	a.logger.Debug("指标已入库",
		"server_id", serverID, "received", len(batch.Samples), "written", written, "skipped", skipped)
	return nil
}

// onAgentHostInfo 处理 host_info：落库主机信息快照（契约第 9 节）。
//
// agent 上线后立即上报一次，之后每 6 小时一次；失败不影响心跳与指标。
func (a *API) onAgentHostInfo(ctx context.Context, ac *agentConn, tok *store.EnrollToken, remoteHost string, env protocol.Envelope) error {
	var info protocol.HostInfo
	if len(env.Payload) > 0 {
		if err := json.Unmarshal(env.Payload, &info); err != nil {
			return fmt.Errorf("解析 host_info 载荷: %w", err)
		}
	}

	serverID, _, err := a.ensureServer(ctx, ac, tok, remoteHost, protocol.Hello{})
	if err != nil {
		return err
	}
	if err := a.touchServer(ctx, serverID); err != nil {
		return err
	}

	collectedAt := info.CollectedAt
	if collectedAt <= 0 {
		collectedAt = a.nowMS()
		info.CollectedAt = collectedAt
	}
	raw, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("序列化 host_info: %w", err)
	}
	if err := a.st.UpsertServerHostInfo(ctx, serverID, string(raw), collectedAt); err != nil {
		return err
	}
	if err := a.st.UpdateServerSystemInfo(ctx, serverID, info.OS, info.Arch, info.Kernel); err != nil {
		a.logger.Debug("同步系统信息到服务器记录失败", "server_id", serverID, "err", err)
	}

	a.logger.Info("收到 agent 主机信息",
		"server_id", serverID, "hostname", info.Hostname, "os", info.OS, "arch", info.Arch,
		"cpu_cores", info.CPUCores, "mem_total_bytes", info.MemTotalBytes)
	return nil
}

// ensureServer 解析当前连接对应的服务器记录：
//  1. 已解析过则直接返回；
//  2. 令牌已绑定服务器且该服务器仍存在，则复用；
//  3. 否则新建服务器记录并把令牌绑定过去（首次接入）。
func (a *API) ensureServer(ctx context.Context, ac *agentConn, tok *store.EnrollToken, remoteHost string, hello protocol.Hello) (serverID string, created bool, err error) {
	if id := ac.getServerID(); id != "" {
		return id, false, nil
	}

	if tok.ServerID != nil && *tok.ServerID != "" {
		existing, getErr := a.st.GetServer(ctx, *tok.ServerID)
		switch {
		case getErr == nil:
			ac.setServerID(existing.ID)
			a.hub.Register(existing.ID, ac)
			// 续期令牌绑定：首次接入时写入 used_at，重连时保持原值。
			if err := a.st.BindEnrollToken(ctx, tok.ID, existing.ID, a.nowMS()); err != nil {
				return "", false, err
			}
			return existing.ID, false, nil
		case errors.Is(getErr, store.ErrNotFound):
			// 服务器已被删除，走新建流程（删除时已自动解绑令牌）。
		default:
			return "", false, getErr
		}
	}

	name := strings.TrimSpace(hello.Hostname)
	if name == "" {
		name = remoteHost
	}
	if name == "" {
		name = "未命名节点"
	}
	host := remoteHost
	if host == "" {
		host = name
	}

	srv := &store.Server{
		ID:        uuid.NewString(),
		Name:      name,
		Host:      host,
		Port:      22,
		Mode:      "agent",
		TagsJSON:  "[]",
		Status:    protocol.StatusUnknown,
		CreatedAt: a.nowMS(),
	}
	if err := a.st.CreateServer(ctx, srv); err != nil {
		return "", false, err
	}
	// 首次接入：标记令牌已用并绑定服务器。
	if err := a.st.BindEnrollToken(ctx, tok.ID, srv.ID, a.nowMS()); err != nil {
		return "", false, err
	}
	ac.setServerID(srv.ID)
	a.hub.Register(srv.ID, ac)
	return srv.ID, true, nil
}

// touchServer 刷新最后心跳时间，并在状态跃迁为在线时记一条事件。
func (a *API) touchServer(ctx context.Context, serverID string) error {
	prev, err := a.st.TouchServerHeartbeat(ctx, serverID, a.nowMS())
	if err != nil {
		return err
	}
	if prev != protocol.StatusOnline {
		a.logger.Info("服务器上线", "server_id", serverID, "prev_status", prev)
		a.recordEvent(serverID, store.EventKindOnline, "服务器上线（收到 agent 心跳）")
	}
	return nil
}

// isKnownMetric 判断指标名是否在契约约定的固定集合内。
func isKnownMetric(metric string) bool {
	switch metric {
	case protocol.MetricCPUPct,
		protocol.MetricMemUsedPct,
		protocol.MetricMemUsedB,
		protocol.MetricLoad1,
		protocol.MetricDiskUsedPt,
		protocol.MetricNetRxBytes,
		protocol.MetricNetTxBytes,
		protocol.MetricProcCount:
		return true
	default:
		return false
	}
}
