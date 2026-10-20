// Command leadstack 是 LeadStack 控制端入口。
//
// 单进程同时提供 REST API、Web UI 与 agent WebSocket（见 docs/ARCHITECTURE.md 第 2 节）。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/alerting"
	"github.com/maliang/leadstack/internal/api"
	"github.com/maliang/leadstack/internal/auth"
	"github.com/maliang/leadstack/internal/checker"
	"github.com/maliang/leadstack/internal/collector"
	"github.com/maliang/leadstack/internal/config"
	"github.com/maliang/leadstack/internal/credential"
	"github.com/maliang/leadstack/internal/jobs"
	"github.com/maliang/leadstack/internal/notify"
	"github.com/maliang/leadstack/internal/sshx"
	"github.com/maliang/leadstack/internal/store"
	"github.com/maliang/leadstack/internal/term"
)

// 构建信息，由 Makefile/-ldflags 注入。
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// adminUsername 是初始管理员用户名。
const adminUsername = "admin"

// sweepInterval 是离线清扫周期。
const sweepInterval = 5 * time.Second

func main() {
	// --version：只打印后退出，不启动服务、不初始化数据目录（Goreleaser 校验依赖）。
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-version", "version":
			fmt.Printf("leadstack %s (commit %s, built %s)\n", version, commit, date)
			return
		}
	}

	// 子命令分发：只认 os.Args[1] 的三个字面量，其余（含所有 -flag）保持原有「启动服务」行为。
	if isSubcommand(os.Args[1:]) {
		switch os.Args[1] {
		case "backup":
			if err := runBackupCommand(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "leadstack backup: %v\n", err)
				os.Exit(1)
			}
			return
		case "restore":
			if err := runRestoreCommand(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "leadstack restore: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}

	if err := run(); err != nil {
		slog.Error("控制端退出", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(version, os.Args[1:])
	if err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	logger.Info("LeadStack 控制端启动中",
		"version", version, "commit", commit, "build_date", date,
		"listen", cfg.Listen, "data_dir", cfg.DataDir, "db", cfg.DBPath)

	if err := cfg.EnsureDataDir(); err != nil {
		return err
	}

	// 关键顺序：**先应用暂存恢复，再打开数据库**（否则可能以旧库运行在新文件之上）。
	if err := applyPendingRestoreIfAny(cfg, logger); err != nil {
		return err
	}

	// pid 文件供 CLI `restore` 判断「服务是否已停止」。
	if err := writePIDFile(cfg.DataDir); err != nil {
		logger.Warn("写入 pid 文件失败（CLI restore 将无法检测服务占用）", "err", err)
	} else {
		defer removePIDFile(cfg.DataDir)
	}

	// 收到 SIGINT/SIGTERM 后取消 ctx，触发优雅关闭。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			logger.Warn("关闭数据库失败", "err", cerr)
		}
	}()

	if err := st.Migrate(ctx); err != nil {
		return err
	}
	logger.Info("数据库就绪", "path", st.Path())

	if err := ensureAdmin(ctx, st, cfg, logger); err != nil {
		return err
	}

	secret, err := auth.LoadOrCreateSecret(ctx, st)
	if err != nil {
		return err
	}
	authMgr, err := auth.NewManager(secret, cfg.TokenTTL)
	if err != nil {
		return err
	}

	// 纳管：主密钥（LEADSTACK_MASTER_KEY 或 <data-dir>/master.key）→ 凭证加解密器。
	cipher, err := buildCipher(cfg, logger)
	if err != nil {
		return err
	}

	// 纳管：agentless 采集器，同时提供 SSH 连通性测试与主机信息采集能力。
	sshClient, err := collector.New(collector.Options{
		Store:          st,
		Cipher:         cipher,
		Logger:         logger,
		Interval:       cfg.SSHInterval,
		Concurrency:    cfg.SSHConcurrency,
		KnownHostsPath: filepath.Join(cfg.DataDir, "known_hosts"),
	})
	if err != nil {
		return err
	}

	// 检测：任务下发（agent 通道）。Hub 实现 jobs.Sender，Dispatcher 实现 checker.Executor。
	// 先建 Hub，再建依赖它的 Dispatcher，最后把同一个 Hub 注入 API。
	hub := api.NewHub()
	hubSender := api.NewHubSender(hub)
	dispatcher := jobs.NewDispatcher(hubSender, jobs.Options{Logger: logger})

	// 检测：检测执行通道——agent 走 job 下发，ssh 走 SSH 执行通道（带连接复用）。
	sshExec := api.NewSSHExecutor(api.SSHExecutorOptions{
		Store:          st,
		Cipher:         cipher,
		Logger:         logger,
		KnownHostsPath: filepath.Join(cfg.DataDir, "known_hosts"),
		Concurrency:    cfg.CheckConcurrency,
	})
	if sshExec != nil {
		defer sshExec.Close()
	}
	routingExec := api.NewRoutingExecutor(st, dispatcher, sshExec, logger)

	// 运维：运维命令通道（日志/进程/端口/定时任务/agent 文件）复用同一个按 mode 路由的执行器。
	var sftpFactory api.SFTPFactory
	if sshExec != nil {
		sftpFactory = sshExec
	}
	// 运维：ssh 模式的终端会话（agent 模式由 api 内部经 WS 转发实现）。
	termOpener, termErr := term.NewSSHOpener(term.SSHOpenerOptions{
		Resolve: func(ctx context.Context, serverID string) (sshx.PTYOptions, error) {
			return api.ResolveSSHPTYOptions(ctx, st, cipher, filepath.Join(cfg.DataDir, "known_hosts"), serverID)
		},
	})
	if termErr != nil {
		logger.Warn("SSH 终端通道初始化失败（终端功能将不可用）", "err", termErr)
		termOpener = nil
	}

	// 告警：通知客户端 → 渠道发送器 → 告警引擎 → 事件桥。
	notifyClient := notify.NewClient(notify.Options{})
	notifier := api.NewNotifyAdapter(notifyClient)
	alertStore := api.NewAlertStore(st, logger)
	alertNotifier := api.NewAlertNotifier(st, cipher, notifier, logger)
	alertEngine := alerting.New(alerting.Deps{
		Store: alertStore,
		Notify: func(ctx context.Context, a alerting.Alert, channels []string, recovered bool) {
			if alertNotifier == nil {
				return
			}
			alertNotifier.Notify(ctx, a, channels, recovered)
		},
		Now: func() int64 { return store.NowMS() },
	})
	alertBridge := api.NewAlertEventBridge(st, alertEngine, logger)

	// 检测：检测调度器（Store 适配器把服务器的 mode 一并带出）；
	// OnStateChange 是 告警 的关键接线：检测翻转 → 告警事件。
	checkStore := api.NewCheckerStore(st, logger)
	scheduler := checker.NewScheduler(checker.Deps{
		Store:         checkStore,
		Exec:          routingExec,
		Concurrency:   cfg.CheckConcurrency,
		OnStateChange: alertBridge.OnCheckStateChange,
	})

	// 部署：部署执行通道（ssh 直连 + agent 任务下发）与异步编排器。
	deployRunner := api.NewDeployRunner(api.DeployRunnerOptions{
		Store:          st,
		Cipher:         cipher,
		Dispatcher:     dispatcher,
		KnownHostsPath: filepath.Join(cfg.DataDir, "known_hosts"),
		Logger:         logger,
	})
	deployOrch := api.NewDeployOrchestrator(api.DeployOrchestratorOptions{
		Store:       st,
		Runner:      deployRunner,
		Concurrency: cfg.DeployConcurrency,
		PublicURL:   cfg.PublicURL,
		DeployRoot:  cfg.DeployRoot,
		Logger:      logger,
	})
	if closer, ok := deployRunner.(interface{ Close() }); ok && closer != nil {
		defer closer.Close()
	}
	if deployOrch != nil {
		defer deployOrch.Close()
	}

	// 备份：备份服务（口令解析与归档格式由 internal/backup 负责）。
	backupService, err := api.NewBackupService(cfg, logger)
	if err != nil {
		return err
	}

	srv, err := api.New(api.Options{
		Config:           cfg,
		Store:            st,
		Auth:             authMgr,
		Cipher:           cipher,
		SSH:              sshClient,
		CheckRunner:      api.NewCheckRunner(scheduler, logger),
		Jobs:             dispatcher,
		Hub:              hub,
		Backups:          backupService,
		MaxSchemaVersion: currentSchemaVersion,
		RemoteExec:       routingExec,
		SFTP:             sftpFactory,
		TermOpener:       termOpener,
		Alerts:           alertEngine,
		Notifier:         notifier,
		Deploy:           deployOrch,
		Logger:           logger,
		Version:          version,
	})
	if err != nil {
		return err
	}

	// 后台清扫：45 秒无心跳即判定离线并记事件；同时推进降采样与保留策略清理。
	sweepCtx, cancelSweep := context.WithCancel(ctx)
	defer cancelSweep()
	go srv.RunSweeper(sweepCtx, sweepInterval)

	// agentless 采集循环（mode=ssh）。
	collectCtx, cancelCollect := context.WithCancel(ctx)
	defer cancelCollect()
	go sshClient.Run(collectCtx)

	// 检测调度循环；检测项增删改时由 API 层调用 Wake() 触发重排。
	checkCtx, cancelChecks := context.WithCancel(ctx)
	defer cancelChecks()
	go scheduler.Start(checkCtx)

	// 把部署日志旁路接到 HubSender：agent 模式的 job.log 实时写入 release_logs。
	srv.WireAgentLogTap(hubSender)

	// 定时任务引擎（按 cron 触发，独立 goroutine）。
	cronCtx, cancelCron := context.WithCancel(ctx)
	defer cancelCron()
	go srv.RunScheduledTaskEngine(cronCtx, cfg.CronTick)

	// 定时备份循环（独立 goroutine，与清扫/告警解耦）。
	backupCtx, cancelBackups := context.WithCancel(ctx)
	defer cancelBackups()
	go srv.RunBackupScheduler(backupCtx)

	// 告警评估循环：服务器上下线巡检 + 指标阈值评估。
	alertCtx, cancelAlerts := context.WithCancel(ctx)
	defer cancelAlerts()
	go alertBridge.Run(alertCtx, cfg.AlertEvalInterval)

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// 不设置 WriteTimeout：/agent/ws 是长连接。
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("HTTP 服务已就绪", "addr", cfg.Listen, "web", "http://"+cfg.Listen)
		err := httpSrv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("监听 %s: %w", cfg.Listen, err)
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		logger.Info("收到退出信号，开始优雅关闭")
	}

	cancelSweep()
	cancelCollect()
	cancelChecks()
	cancelAlerts()
	cancelBackups()
	cancelCron()
	// WebSocket 连接已被 hijack，http.Server.Shutdown 不会关闭它们，需要显式处理。
	srv.Hub().CloseAll()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("优雅关闭超时: %w", err)
	}
	logger.Info("已优雅关闭")
	return nil
}

// buildCipher 准备主密钥并构造凭证加解密器。
//
// 密钥来源优先级：LEADSTACK_MASTER_KEY（hex）> <data-dir>/master.key（不存在则随机生成）。
// 安全要求：密钥文件必须 0600；**任何日志都不得输出密钥内容**。
func buildCipher(cfg *config.Config, logger *slog.Logger) (*credential.Cipher, error) {
	key := cfg.MasterKey
	if len(key) == 0 {
		path := cfg.MasterKeyPath()
		loaded, err := credential.LoadOrCreateMasterKey(path)
		if err != nil {
			return nil, fmt.Errorf("加载主密钥 %s: %w", path, err)
		}
		key = loaded
		if err := ensureMasterKeyPerm(path, logger); err != nil {
			return nil, err
		}
		logger.Info("主密钥已就绪", "source", "file", "path", path)
	} else {
		logger.Info("主密钥已就绪", "source", "env", "env", config.EnvMasterKey)
	}

	cipher, err := credential.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("构造凭证加解密器: %w", err)
	}
	return cipher, nil
}

// ensureMasterKeyPerm 确保主密钥文件权限为 0600；权限过宽时先告警再收紧。
func ensureMasterKeyPerm(path string, logger *slog.Logger) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("检查主密钥文件 %s: %w", path, err)
	}
	if info.Mode().Perm() == 0o600 {
		return nil
	}
	logger.Warn("主密钥文件权限过宽，正在收紧为 0600",
		"path", path, "old_mode", fmt.Sprintf("%04o", info.Mode().Perm()))
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("收紧主密钥文件权限 %s: %w", path, err)
	}
	return nil
}

// isSubcommand 判断是否为 CLI 子命令（backup / restore）。
//
// 只认 os.Args[1] 的字面量：`leadstack -listen ...` 等仍走「启动服务」。
func isSubcommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "backup", "restore":
		return true
	default:
		return false
	}
}

// ensureAdmin 在用户表为空时创建初始管理员。
//
// 密码来源：优先 LEADSTACK_ADMIN_PASSWORD/-admin-password；否则随机生成并打印一次。
func ensureAdmin(ctx context.Context, st *store.Store, cfg *config.Config, logger *slog.Logger) error {
	count, err := st.CountUsers(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	password := cfg.AdminPassword
	generated := false
	if password == "" {
		pw, err := auth.GeneratePassword(20)
		if err != nil {
			return err
		}
		password = pw
		generated = true
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("生成管理员密码哈希: %w", err)
	}
	user := &store.User{
		ID:           uuid.NewString(),
		Username:     adminUsername,
		PasswordHash: hash,
		Role:         "admin",
		CreatedAt:    store.NowMS(),
	}
	if err := st.CreateUser(ctx, user); err != nil {
		return fmt.Errorf("创建初始管理员: %w", err)
	}

	logger.Info("已创建初始管理员账号", "username", user.Username, "password_from_env", !generated)
	if generated {
		printInitialPassword(user.Username, password)
	}
	return nil
}

// printInitialPassword 把随机初始密码打印到控制台（仅首次创建时调用一次）。
func printInitialPassword(username, password string) {
	fmt.Printf(`
============================================================
  LeadStack 控制端首次启动，已创建初始管理员
------------------------------------------------------------
  用户名：%s
  初始密码：%s
  上述密码仅在本次创建时打印一次，请立即登录并妥善保存。
============================================================

`, username, password)
}

// newLogger 构造 slog 文本日志器。
func newLogger(level slog.Level) *slog.Logger {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}
