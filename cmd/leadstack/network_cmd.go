package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/maliang/leadstack/internal/api"
	"github.com/maliang/leadstack/internal/config"
	"github.com/maliang/leadstack/internal/store"
)

// runNetworkCommand 处理 `leadstack network ...`：离线读写网络设置。
//
// 关键设计（契约 §50.2）：**不启动服务**，直接打开数据库读写 settings，
// 用于「控制台打不开」时修正监听端口 —— 这是端口自锁的逃生通道。
func runNetworkCommand(args []string) error {
	fs := flag.NewFlagSet("network", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dataDir := fs.String("data-dir", envOr(config.EnvDataDir, "./data"), "数据目录（含 leadstack.db）")
	dbPath := fs.String("db", "", "数据库文件路径（默认 <data-dir>/leadstack.db）")
	show := fs.Bool("show", false, "显示当前 settings / 环境变量 / 默认值")
	listen := fs.String("listen", "", "写入控制台监听地址，如 0.0.0.0:9090")
	publicURL := fs.String("public-url", "", "写入控制台对外地址，如 http://1.2.3.4:9090")
	sshPort := fs.Int("default-ssh-port", 0, "写入新建服务器的默认 SSH 端口")
	reset := fs.Bool("reset", false, "清除全部网络设置，回落环境变量/默认值")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("解析参数: %w", err)
	}

	pending := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "listen", "public-url", "default-ssh-port", "reset":
			pending = true
		}
	})
	if !pending && !*show {
		*show = true // 不带动作时默认展示，避免用户空跑一次什么也看不到
	}

	absDataDir, err := filepath.Abs(*dataDir)
	if err != nil {
		return fmt.Errorf("解析数据目录 %q: %w", *dataDir, err)
	}
	db := strings.TrimSpace(*dbPath)
	if db == "" {
		db = filepath.Join(absDataDir, "leadstack.db")
	}
	absDB, err := filepath.Abs(db)
	if err != nil {
		return fmt.Errorf("解析数据库路径 %q: %w", db, err)
	}
	if _, statErr := os.Stat(absDB); statErr != nil {
		return fmt.Errorf("找不到数据库 %s：请确认 --data-dir 指向控制端数据目录（%v）", absDB, statErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, err := store.Open(ctx, absDB)
	if err != nil {
		return fmt.Errorf("打开数据库 %s 失败: %w", absDB, err)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "关闭数据库失败: %v\n", cerr)
		}
	}()

	running := serviceRunning(absDataDir)
	if running {
		fmt.Println("提示：检测到控制端正在运行；设置已写入数据库，需要重启控制端后生效。")
	}

	if *reset {
		if err := api.ResetNetworkSettings(ctx, st); err != nil {
			return fmt.Errorf("清除网络设置失败: %w", err)
		}
		fmt.Println("已清除全部网络设置（监听/对外地址/默认 SSH 端口），后续启动将回落环境变量或默认值。")
	}
	if v := strings.TrimSpace(*listen); v != "" {
		// 离线逃生：只校验格式；即使当前被占用也写入（启动时会自动回退并 WARN）。
		if err := api.SaveNetworkListenForce(ctx, st, v); err != nil {
			return fmt.Errorf("写入监听地址失败: %w", err)
		}
		fmt.Printf("已写入监听地址设置：%s\n", v)
		if berr := api.ValidateListen(v, true); berr != nil {
			fmt.Fprintf(os.Stderr, "警告：%v\n      已写入设置，但该端口当前不可绑定；启动时会自动回退到 env/默认/临时端口。\n", berr)
		}
	}
	if v := strings.TrimSpace(*publicURL); v != "" {
		if err := api.SaveNetworkPublicURL(ctx, st, v); err != nil {
			return fmt.Errorf("写入对外地址失败: %w", err)
		}
		fmt.Printf("已写入对外地址设置：%s\n", v)
	}
	if *sshPort > 0 {
		if err := api.SaveDefaultSSHPort(ctx, st, *sshPort); err != nil {
			return fmt.Errorf("写入默认 SSH 端口失败: %w", err)
		}
		fmt.Printf("已写入默认 SSH 端口设置：%d\n", *sshPort)
	}

	if *show || *reset || strings.TrimSpace(*listen) != "" ||
		strings.TrimSpace(*publicURL) != "" || *sshPort > 0 {
		snap, serr := api.ReadNetworkSnapshot(ctx, st)
		if serr != nil {
			return fmt.Errorf("读取网络设置失败: %w", serr)
		}
		printNetworkSnapshot(snap)
		if pending || *reset {
			fmt.Println("需要重启控制端生效：systemctl restart leadstack（或你的进程管理器）。")
			fmt.Println("打不开控制台时的逃生办法：重启时加 `--listen <可用地址>` 可临时覆盖数据库里的设置。")
		}
	}
	return nil
}

// printNetworkSnapshot 打印 settings / env / default 三者取值。
func printNetworkSnapshot(snap api.NetworkSnapshot) {
	fmt.Println()
	fmt.Println("网络设置（settings / 环境变量 / 默认）")
	fmt.Printf("  监听地址      settings=%-22s env=%-22s default=%s\n",
		orDash(snap.SettingsListen), orDash(snap.EnvListen), snap.DefaultListen)
	fmt.Printf("  对外地址      settings=%-22s env=%-22s default=%s\n",
		orDash(snap.SettingsPublicURL), orDash(snap.EnvPublicURL), "(空，回落到请求 Host)")
	fmt.Printf("  默认 SSH 端口 settings=%-22s env=%-22s default=%d\n",
		orDash(snap.SettingsSSHPort), orDash(snap.EnvSSHPort), snap.DefaultSSHPort)
	fmt.Printf("\n若现在启动，将监听：%s（来源 %s）\n", snap.EffectiveListen, snap.EffectiveListenFrom)
}

// orDash 空值显示为 "—"。
func orDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "—"
	}
	return v
}

// serviceRunning 用 pid 文件判断控制端是否在运行（仅用于提示，不影响写入）。
func serviceRunning(dataDir string) bool {
	pid, err := readPIDFile(filepath.Join(dataDir, pidFileName))
	if err != nil || pid <= 0 {
		return false
	}
	return processAlive(pid)
}

// 保证 encoding/json 在需要时可用（防止后续扩展未使用的导入告警）。
var _ = json.Marshal
