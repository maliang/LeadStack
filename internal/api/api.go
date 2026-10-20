// Package api 实现控制端的 HTTP/WebSocket 接口层。
//
// 路由与响应格式严格遵循 docs/ARCHITECTURE.md 第 3、4 节：
//   - REST 前缀 /api/v1，成功 {"data":...}，失败 {"error":{code,message}}；
//   - /healthz 与 /api/v1/auth/login 免鉴权，其余需要 Bearer JWT；
//   - /agent/ws 使用 Bearer enroll-token 握手。
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/auth"
	"github.com/maliang/leadstack/internal/config"
	"github.com/maliang/leadstack/internal/credential"
	"github.com/maliang/leadstack/internal/store"
	"github.com/maliang/leadstack/internal/term"
	"github.com/maliang/leadstack/internal/webui"
	"github.com/maliang/leadstack/pkg/protocol"
)

// Options 是构造 API 服务的入参。
type Options struct {
	Config *config.Config
	Store  *store.Store
	Auth   *auth.Manager
	Logger *slog.Logger
	// Cipher 用于凭证加解密；为 nil 时凭证相关接口返回 503。
	Cipher *credential.Cipher
	// SSH 提供连通性测试与主机信息采集能力；为 nil 时相关接口返回 503。
	SSH SSHService
	// CheckRunner 提供检测调度能力（立即执行 + 唤醒）；为 nil 时 /checks/{id}/run 返回 503。
	CheckRunner CheckRunner
	// Jobs 接收 agent 的 job.* 帧（由 jobs.Dispatcher 实现）；为 nil 时这些帧被忽略。
	Jobs JobRouter
	// Hub 可注入活跃连接表；为 nil 时内部新建（调用方可先建 Hub 再构造依赖它的下发器）。
	Hub *Hub
	// Alerts 是告警引擎端口（*alerting.Engine）；为 nil 时规则/渠道写操作不触发 Reload。
	Alerts AlertEngine
	// Notifier 是通知渠道端口（*NotifyAdapter）；为 nil 时渠道配置不做强校验、测试发送返回未启用。
	Notifier Notifier
	// Deploy 是发布编排端口；为 nil 时创建发布只落库不执行（run 保持 pending）。
	Deploy DeployOrchestrator
	// Backups 是备份能力端口；为 nil 时备份接口返回 503、定时备份不启动。
	Backups BackupService
	// MaxSchemaVersion 是当前程序支持的最高迁移版本，用于拒绝「来自更新版本」的备份；
	// 为 0 时取 defaultMaxSchemaVersion。
	MaxSchemaVersion int
	// RemoteExec 是运维命令执行通道（日志/进程/端口/定时任务/agent 文件用）。
	RemoteExec RemoteExecutor
	// SFTP 是 ssh 模式的文件通道；为 nil 时文件接口对 ssh 服务器不可用。
	SFTP SFTPFactory
	// TermOpener 是 ssh 终端通道（internal/term 的 SSHOpener）。
	TermOpener term.Opener
	// ActiveListen 是当前进程实际监听的地址（由 cmd 在 net.Listen 之后注入）。
	ActiveListen string
	Version      string
	StartedAt    time.Time
}

// API 汇聚处理器的所有依赖。
type API struct {
	cfg              *config.Config
	st               *store.Store
	authMgr          *auth.Manager
	cipher           *credential.Cipher
	ssh              SSHService
	checks           CheckRunner
	jobs             JobRouter
	alerts           AlertEngine
	notifier         Notifier
	deploy           DeployOrchestrator
	artifacts        *artifactStore
	logTap           *agentLogTap
	backups          BackupService
	maxSchemaVersion int
	remoteExec       RemoteExecutor
	sftp             SFTPFactory
	termOpener       term.Opener
	activeListen     string
	termTickets      sync.Map // ticket → *terminalTicket
	agentTerms       sync.Map // sessionID → *agentTermSession
	// backupWake 用于「设置变更后立即重排定时备份」，容量 1 的非阻塞信号。
	backupWake chan struct{}
	logger     *slog.Logger
	version    string
	startedAt  time.Time
	web        http.Handler
	hub        *Hub
}

// New 校验依赖并构造 API。
func New(opts Options) (*API, error) {
	if opts.Config == nil {
		return nil, errors.New("初始化 API: 配置为空")
	}
	if opts.Store == nil {
		return nil, errors.New("初始化 API: store 为空")
	}
	if opts.Auth == nil {
		return nil, errors.New("初始化 API: auth 管理器为空")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	hub := opts.Hub
	if hub == nil {
		hub = NewHub()
	}
	artifacts, err := newArtifactStore(opts.Config.ArtifactsDir(), opts.Config.ArtifactMaxBytes())
	if err != nil {
		return nil, err
	}
	startedAt := opts.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	return &API{
		cfg:              opts.Config,
		st:               opts.Store,
		authMgr:          opts.Auth,
		cipher:           opts.Cipher,
		ssh:              opts.SSH,
		checks:           opts.CheckRunner,
		jobs:             opts.Jobs,
		alerts:           opts.Alerts,
		notifier:         opts.Notifier,
		deploy:           opts.Deploy,
		artifacts:        artifacts,
		logTap:           newAgentLogTap(opts.Store, logger),
		backups:          opts.Backups,
		remoteExec:       opts.RemoteExec,
		sftp:             opts.SFTP,
		termOpener:       opts.TermOpener,
		maxSchemaVersion: resolvedMaxSchemaVersion(opts.MaxSchemaVersion),
		backupWake:       make(chan struct{}, 1),
		logger:           logger,
		version:          version,
		startedAt:        startedAt,
		web:              webui.Handler(),
		hub:              hub,
	}, nil
}

// resolvedMaxSchemaVersion 返回生效的最高迁移版本（未注入时用兜底常量）。
func resolvedMaxSchemaVersion(v int) int {
	if v > 0 {
		return v
	}
	return defaultMaxSchemaVersion
}

// Logger 返回服务日志器。
func (a *API) Logger() *slog.Logger { return a.logger }

// Hub 返回活跃 agent 连接表（供 纳管 的任务下发复用）。
func (a *API) Hub() *Hub { return a.hub }

// WireAgentLogTap 把 agent 日志旁路接到 HubSender：部署任务下发时绑定 runID，
// 之后该 run 的 job.log 帧会被实时写入 release_logs（agent 模式的流式日志）。
func (a *API) WireAgentLogTap(sender *HubSender) {
	if a == nil || sender == nil || a.logTap == nil {
		return
	}
	sender.SetSendJobObserver(a.logTap.Bind)
}

// uptimeSeconds 返回进程已运行秒数。
func (a *API) uptimeSeconds() int64 {
	d := time.Since(a.startedAt)
	if d < 0 {
		return 0
	}
	return int64(d.Seconds())
}

// nowMS 是当前 Unix 毫秒时间戳的间接层，便于测试替换。
func (a *API) nowMS() int64 { return store.NowMS() }

// offlineThresholdMS 返回离线判定阈值（毫秒），来自契约的 OfflineAfterS。
func (a *API) offlineThresholdMS() int64 {
	return int64(protocol.OfflineAfterS) * 1000
}

// recordEvent 写一条服务器事件；失败只记日志，不影响主流程。
func (a *API) recordEvent(serverID, kind, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ev := &store.Event{
		ID:      uuid.NewString(),
		Kind:    kind,
		Message: message,
		TS:      a.nowMS(),
	}
	if serverID != "" {
		ev.ServerID = &serverID
	}
	if err := a.st.InsertEvent(ctx, ev); err != nil {
		a.logger.Warn("写入事件失败", "kind", kind, "server_id", serverID, "err", err)
	}
}

// serveSPA 把请求交给内嵌前端产物处理。
func (a *API) serveSPA(w http.ResponseWriter, r *http.Request) {
	a.web.ServeHTTP(w, r)
}

// internalError 统一处理内部错误：记日志 + 返回 500，不向客户端泄露细节。
func (a *API) internalError(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.Error("请求处理失败", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, codeInternal, "服务器内部错误")
}

// mapStoreError 把 store 层错误映射为 HTTP 状态码与错误码。
func mapStoreError(err error) (status int, code, message string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, codeNotFound, "资源不存在"
	case errors.Is(err, store.ErrConflict):
		return http.StatusConflict, codeConflict, "资源已存在或冲突"
	case errors.Is(err, store.ErrExpired):
		return http.StatusUnauthorized, codeUnauthorized, "接入令牌已过期"
	default:
		return 0, "", ""
	}
}

// writeStoreError 是 mapStoreError 的便捷封装；非业务错误一律 500。
func (a *API) writeStoreError(w http.ResponseWriter, r *http.Request, err error) {
	if status, code, message := mapStoreError(err); status != 0 {
		writeError(w, status, code, message)
		return
	}
	a.internalError(w, r, err)
}
