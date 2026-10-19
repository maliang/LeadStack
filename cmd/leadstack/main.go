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

	"github.com/maliang/leadstack/internal/api"
	"github.com/maliang/leadstack/internal/auth"
	"github.com/maliang/leadstack/internal/checker"
	"github.com/maliang/leadstack/internal/collector"
	"github.com/maliang/leadstack/internal/config"
	"github.com/maliang/leadstack/internal/credential"
	"github.com/maliang/leadstack/internal/jobs"
	"github.com/maliang/leadstack/internal/store"
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

	// M1：主密钥（LEADSTACK_MASTER_KEY 或 <data-dir>/master.key）→ 凭证加解密器。
	cipher, err := buildCipher(cfg, logger)
	if err != nil {
		return err
	}

	// M1：agentless 采集器，同时提供 SSH 连通性测试与主机信息采集能力。
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

	// M2：任务下发（agent 通道）。Hub 实现 jobs.Sender，Dispatcher 实现 checker.Executor。
	// 先建 Hub，再建依赖它的 Dispatcher，最后把同一个 Hub 注入 API。
	hub := api.NewHub()
	dispatcher := jobs.NewDispatcher(api.NewHubSender(hub), jobs.Options{Logger: logger})

	// M2：检测执行通道——agent 走 job 下发，ssh 走 SSH 执行通道（带连接复用）。
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

	// M2：检测调度器（Store 适配器把服务器的 mode 一并带出）。
	checkStore := api.NewCheckerStore(st, logger)
	scheduler := checker.NewScheduler(checker.Deps{
		Store:       checkStore,
		Exec:        routingExec,
		Concurrency: cfg.CheckConcurrency,
		OnStateChange: func(checkID, serverID string, ok bool, since int64) {
			logger.Info("检测状态变化", "check_id", checkID, "server_id", serverID, "ok", ok, "since", since)
		},
	})

	srv, err := api.New(api.Options{
		Config:      cfg,
		Store:       st,
		Auth:        authMgr,
		Cipher:      cipher,
		SSH:         sshClient,
		CheckRunner: api.NewCheckRunner(scheduler, logger),
		Jobs:        dispatcher,
		Hub:         hub,
		Logger:      logger,
		Version:     version,
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
