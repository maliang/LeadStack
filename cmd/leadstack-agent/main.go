// Command leadstack-agent 是 LeadStack 的节点 agent 入口。
//
// 用法示例：
//
//	./leadstack-agent --server http://127.0.0.1:8080 --token <接入令牌>
//	./leadstack-agent --server http://127.0.0.1:8080 --token <令牌> --once   # 单次上报后退出，自测用
//	./leadstack-agent --server https://10.0.0.1:8443 --token <令牌> --ca /etc/leadstack/ca.pem  # 自签证书
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/maliang/leadstack/internal/agent"
)

// 构建时通过 -ldflags "-X main.version=... -X main.commit=... -X main.date=..." 注入。
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	var (
		serverAddr  = flag.String("server", "http://127.0.0.1:8080", "控制端地址，支持 http:// 或 https://（自动转 ws/wss）")
		token       = flag.String("token", "", "接入令牌（必填）")
		once        = flag.Bool("once", false, "单次上报后退出，便于本地自测")
		caFile      = flag.String("ca", "", "额外信任的 CA 证书文件（PEM）；控制端用自签证书时使用")
		insecure    = flag.Bool("insecure", false, "跳过 TLS 证书校验（自签且无法安装 CA 时使用，不推荐）")
		verbose     = flag.Bool("verbose", false, "输出调试日志")
		showVersion = flag.Bool("version", false, "打印版本后退出")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("leadstack-agent %s (commit %s, built %s)\n", version, commit, date)
		return
	}
	if *token == "" {
		fmt.Fprintln(os.Stderr, "错误：必须通过 --token 提供接入令牌")
		flag.Usage()
		os.Exit(2)
	}

	logger := newLogger(*verbose)

	// 优雅退出：收到 SIGINT/SIGTERM 后取消 context，所有协程与子进程随之停止。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ag := agent.New(agent.Config{
		Server:      *serverAddr,
		Token:       *token,
		Version:     version,
		Once:        *once,
		CAFile:      strings.TrimSpace(*caFile),
		InsecureTLS: *insecure,
		Logger:      logger,
	})

	logger.Info("leadstack-agent 启动",
		"version", version,
		"server", *serverAddr,
		"once", *once,
		"ca", strings.TrimSpace(*caFile),
		"insecure", *insecure)

	if err := ag.Run(ctx); err != nil {
		logger.Error("agent 异常退出", "err", err)
		os.Exit(1)
	}
	if *once {
		logger.Info("单次上报成功")
		return
	}
	logger.Info("agent 已停止")
}

// newLogger 创建带级别控制的文本日志器。
func newLogger(verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
