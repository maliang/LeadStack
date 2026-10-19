// Command leadstack-devssh 启动一个测试专用 SSH 服务。
//
// 它只用于开发与端到端验收（本机没有 sshd），**禁止用于生产环境**。
// 命令行契约见 docs/ARCHITECTURE.md 第 8.4 节与 M1 任务说明：
//
//	leadstack-devssh --addr 127.0.0.1:0 --user devuser --password devpass \
//	                 --authorized-key-file <path> --port-file <path>
//
// 行为：stdout 打印可解析横幅 `DEV-ONLY listening on 127.0.0.1:<port>`；
// --port-file 写入纯数字端口并 fsync；支持密码与公钥认证（取或）；
// 收到 SIGINT/SIGTERM 时优雅关闭。
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/maliang/leadstack/internal/devssh"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr, nil); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "DEV-ONLY %v\n", err)
		os.Exit(1)
	}
}

// run 是 main 的可测试实现；signals 为 nil 时订阅进程信号。
func run(args []string, stdout, stderr io.Writer, signals <-chan os.Signal) error {
	fs := flag.NewFlagSet("leadstack-devssh", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		addr     = fs.String("addr", "127.0.0.1:0", "监听地址；端口传 0 由系统分配")
		user     = fs.String("user", "devuser", "固定登录用户名")
		password = fs.String("password", "devpass", "固定登录密码")
		keyFile  = fs.String("authorized-key-file", "", "可选：authorized_keys 格式的公钥文件（每行一个公钥）")
		portFile = fs.String("port-file", "", "可选：启动后把实际监听端口（纯数字）写入该文件")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := devssh.Config{
		Addr:     *addr,
		Username: *user,
		Password: *password,
	}
	if *keyFile != "" {
		keyBytes, err := os.ReadFile(*keyFile)
		if err != nil {
			return fmt.Errorf("读取 --authorized-key-file 失败: %w", err)
		}
		cfg.AuthorizedKey = keyBytes
	}

	srv, err := devssh.Start(cfg)
	if err != nil {
		return fmt.Errorf("启动失败: %w", err)
	}
	defer srv.Close()

	actualAddr := srv.Addr()
	if *portFile != "" {
		_, portStr, err := net.SplitHostPort(actualAddr)
		if err != nil {
			return fmt.Errorf("解析监听地址 %q 失败: %w", actualAddr, err)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return fmt.Errorf("解析监听端口 %q 失败: %w", portStr, err)
		}
		if err := writePortFile(*portFile, port); err != nil {
			return fmt.Errorf("写入 --port-file %q 失败: %w", *portFile, err)
		}
	}

	// 验收脚本依赖这一行（前缀固定）。
	fmt.Fprintf(stdout, "DEV-ONLY listening on %s\n", actualAddr)
	fmt.Fprintf(stdout, "DEV-ONLY 警告：这是测试专用 SSH 服务，账号 %q，禁止用于生产环境。\n", cfg.Username)
	if len(cfg.AuthorizedKey) > 0 {
		fmt.Fprintf(stdout, "DEV-ONLY 已启用公钥认证（password 与 publickey 取或）。\n")
	}

	if signals == nil {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		signals = ch
	}
	sig := <-signals
	fmt.Fprintf(stderr, "DEV-ONLY 收到 %s，正在关闭...\n", sig)
	if err := srv.Close(); err != nil {
		fmt.Fprintf(stderr, "DEV-ONLY 关闭时出错: %v\n", err)
	}
	return nil
}

// writePortFile 把端口以纯数字写入 path 并 fsync，便于验收脚本轮询等待。
func writePortFile(path string, port int) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "%d\n", port); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
