package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/maliang/leadstack/internal/api"
	"github.com/maliang/leadstack/internal/backup"
	"github.com/maliang/leadstack/internal/config"
	"github.com/maliang/leadstack/internal/store"
)

// runUpgradeCommand 处理 `leadstack upgrade ...`。
//
//	leadstack upgrade --watchdog --state <file>   升级看护进程（由升级流程自己 spawn）
//	leadstack upgrade --check                     查询最新版本
//	leadstack upgrade --to v1.2.3                 执行升级（无 UI 场景）
func runUpgradeCommand(args []string) error {
	fs := newUpgradeFlagSet()
	if err := fs.set.Parse(args); err != nil {
		return fmt.Errorf("解析参数: %w", err)
	}
	statePath := strings.TrimSpace(fs.state)
	check := fs.check
	to := strings.TrimSpace(fs.to)
	allowMajor := fs.allowMajor
	dataDir := fs.dataDir

	if fs.watchdog {
		if statePath == "" {
			return fmt.Errorf("--watchdog 需要 --state <state.json>")
		}
		return runUpgradeWatchdog(context.Background(), statePath)
	}

	cfg, err := config.Load("leadstack", serverArgs())
	if err != nil {
		return err
	}
	if dataDir != "" {
		cfg.DataDir = dataDir
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if check {
		runner, cerr := api.NewUpgradeRunner(cfg, version, logger)
		if cerr != nil {
			return cerr
		}
		info, cerr := runner.Check(context.Background())
		if cerr != nil {
			return cerr
		}
		fmt.Printf("当前版本: %s\n最新版本: %s\n是否有更新: %t\n平台可升级: %t\n下载地址: %s\n说明:\n%s\n",
			info.Current, info.Latest, info.HasUpdate, info.Upgradable, info.ReleaseURL, info.Notes)
		return nil
	}
	if to == "" {
		return fmt.Errorf("请指定 --check 或 --to <version>（或 --watchdog --state <file>）")
	}
	runner, err := api.NewUpgradeRunner(cfg, version, logger)
	if err != nil {
		return err
	}
	fmt.Printf("开始升级：%s → %s（会先做一次完整备份）\n", version, to)
	if err := runner.Apply(context.Background(), to, allowMajor); err != nil {
		return err
	}
	fmt.Println("升级流程已启动：进程将被新版本替换，若新版本启动失败会自动回退。")
	return nil
}

// upgradeFlagValues 持有解析结果。
type upgradeFlagValues struct {
	watchdog   bool
	state      string
	check      bool
	to         string
	allowMajor bool
	dataDir    string
	set        *flag.FlagSet
}

// newFlagSet 创建带标准输出的 FlagSet。
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// newUpgradeFlagSet 构造参数解析器（与 runUpgradeCommand 配对，便于测试）。
func newUpgradeFlagSet() *upgradeFlagValues {
	fs := &upgradeFlagValues{}
	fs.set = newFlagSet("upgrade")
	fs.set.BoolVar(&fs.watchdog, "watchdog", false, "以看护进程模式运行（内部使用）")
	fs.set.StringVar(&fs.state, "state", "", "升级状态文件路径（配合 --watchdog）")
	fs.set.BoolVar(&fs.check, "check", false, "只检查最新版本")
	fs.set.StringVar(&fs.to, "to", "", "升级到指定版本，如 v0.3.0")
	fs.set.BoolVar(&fs.allowMajor, "allow-major", false, "允许跨大版本升级")
	fs.set.StringVar(&fs.dataDir, "data-dir", "", "数据目录（可选，覆盖配置）")
	return fs
}

// serverArgs 返回「启动服务」时的参数（CLI 升级后会 exec 回服务模式）。
func serverArgs() []string {
	out := make([]string, 0, len(os.Args))
	for _, arg := range os.Args[1:] {
		if arg == "upgrade" {
			break
		}
		out = append(out, arg)
	}
	return out
}

// ---------------------------------------------------------------- watchdog

// runUpgradeWatchdog 是升级看护进程：确认新版本成功，否则回退。
//
// 时序（这是整个功能唯一值钱的部分）：
//
//	父进程(旧)             watchdog(独立会话)                新进程
//	  ├ 备份/下载/替换 ──────► 启动，记录 deadline
//	  ├ spawn watchdog
//	  └ syscall.Exec ───────► 新二进制启动 ───────────────► 自检(10s 内)
//	                          │                              ├ 通过 → 删 state.json → watchdog 认定成功
//	                          │                              └ 失败/崩溃 → 进程退出（state.json 仍在）
//	                          ├ 发现 state.json 未删 / 超时 / 端口不通
//	                          ├ .prev 换回 <exe>
//	                          ├ 有迁移 → 用升级前备份恢复数据
//	                          └ 重启服务（systemd/launchd/直接 spawn 旧二进制）
//
// 关键点：watchdog 在**独立会话**（Setsid）里运行，父进程 exec 成新二进制后它依然活着；
// 因此即使 systemd `Restart=always` 反复拉起崩溃的新二进制，也只会不断被 watchdog
// 换回 .prev —— 而 watchdog 一旦完成回滚就删除 state.json，systemd 拉起的就变成旧版本。
func runUpgradeWatchdog(ctx context.Context, statePath string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	state, err := api.ReadUpgradeState(statePath)
	if err != nil {
		return err
	}
	if state == nil {
		logger.Info("没有待确认的升级（state.json 不存在），watchdog 退出")
		return nil
	}
	deadline := time.UnixMilli(state.Deadline)
	logger.Warn("升级 watchdog 已启动",
		"target", state.TargetVersion, "from", state.FromVersion,
		"deadline", deadline.Format(time.RFC3339), "listen", state.ActiveListen,
		"schema_changed", state.SchemaChanged)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	listenDeadSince := time.Time{}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			current, rerr := api.ReadUpgradeState(statePath)
			if rerr == nil && current == nil {
				// 新进程自检通过并删除了 state.json → 升级成功。
				logger.Info("新版本自检通过，升级成功", "target", state.TargetVersion)
				_ = api.AppendUpgradeHistory(state.DataDir, &api.UpgradeHistoryEntry{
					TS: time.Now().UnixMilli(), Outcome: "success",
					FromVersion: state.FromVersion, TargetVersion: state.TargetVersion,
					BackupFile: state.BackupFile,
					DurationMS: time.Now().UnixMilli() - state.StartedAt,
				})
				return nil
			}
			// 早期判定：新进程起来后端口应能连通；连续 15s 不通说明它崩了。
			if state.ActiveListen != "" {
				conn, derr := net.DialTimeout("tcp", state.ActiveListen, time.Second)
				if derr == nil {
					_ = conn.Close()
					listenDeadSince = time.Time{}
				} else if listenDeadSince.IsZero() {
					listenDeadSince = time.Now()
				} else if time.Since(listenDeadSince) > 15*time.Second {
					logger.Warn("新版本进程未监听端口，开始回退", "listen", state.ActiveListen)
					return performUpgradeRollback(logger, statePath, state, "新版本未监听 "+state.ActiveListen)
				}
			}
			if time.Now().After(deadline) {
				logger.Warn("等待新版本确认超时，开始自动回退",
					"target", state.TargetVersion, "deadline", deadline.Format(time.RFC3339))
				return performUpgradeRollback(logger, statePath, state, "等待新版本自检超时")
			}
		}
	}
}

// performUpgradeRollback 执行完整回退：换回二进制 → 恢复数据（如有迁移）→ 重启服务 → 记录历史。
func performUpgradeRollback(logger *slog.Logger, statePath string, state *api.UpgradeState, reason string) error {
	logger.Warn("开始回退升级", "reason", reason, "from", state.TargetVersion)

	restored := make([]string, 0, 2)
	// 1) 有迁移：先把数据恢复成升级前备份（否则旧二进制配新库可能起不来）。
	if state.SchemaChanged && strings.TrimSpace(state.BackupFile) != "" {
		pass, perr := resolvePassphrase(state.DataDir, "", true)
		if perr != nil {
			logger.Error("回退时无法解析备份口令，跳过数据恢复", "err", perr)
		} else {
			res, rerr := backup.Restore(backup.RestoreOptions{
				File: state.BackupFile, DataDir: state.DataDir, Passphrase: pass,
				MaxSchemaVersion: currentSchemaVersion,
			})
			if rerr != nil {
				logger.Error("回退时恢复数据失败（旧二进制可能无法启动，请人工处理）",
					"backup", state.BackupFile, "err", rerr)
			} else {
				restored = res.RestoredFiles
				logger.Warn("已从升级前备份恢复数据", "files", restored, "pre_restore", res.PreRestoreDir)
			}
		}
	}

	// 2) 二进制换回旧版本。
	if err := api.RollbackExecutable(state.NewExe); err != nil {
		logger.Error("回退二进制失败（服务可能仍在新版本上）", "err", err)
	} else {
		logger.Warn("已换回旧版本二进制", "exe", state.NewExe,
			"failed_saved_as", state.NewExe+".failed-*")
	}

	// 3) 重启服务（systemd/launchd；都没有就直接拉起旧二进制）。
	if err := restartServiceAfterRollback(logger, state); err != nil {
		logger.Error("回退后重启服务失败，请人工启动", "err", err)
	}

	// 4) 清理标记并记录历史（删掉 state.json 后，Restart=always 拉起的也是旧版本）。
	_ = api.RemoveUpgradeState(statePath)
	_ = api.AppendUpgradeHistory(state.DataDir, &api.UpgradeHistoryEntry{
		TS: time.Now().UnixMilli(), Outcome: "rollback",
		FromVersion: state.FromVersion, TargetVersion: state.TargetVersion,
		BackupFile: state.BackupFile, RestoredFiles: restored, Reason: reason,
		PrevExe:    state.PrevExe,
		DurationMS: time.Now().UnixMilli() - state.StartedAt,
	})
	logger.Warn("升级已回退完成", "reason", reason, "restored_files", restored)

	// 5) 审计：回退记一条（服务刚被换回旧版本，直接开库写）。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if st, oerr := store.Open(ctx, filepath.Join(state.DataDir, "leadstack.db")); oerr == nil {
		detail := fmt.Sprintf(`{"from":%q,"to":%q,"outcome":"rollback","reason":%q,"restored_files":%d}`,
			state.FromVersion, state.TargetVersion, reason, len(restored))
		resourceType := "system"
		if aerr := st.InsertAuditLog(ctx, &store.AuditLog{
			TS: time.Now().UnixMilli(), Actor: "system", Action: "system.upgrade_rollback",
			ResourceType: &resourceType, Detail: &detail,
		}); aerr != nil {
			logger.Warn("写入回退审计失败", "err", aerr)
		}
		if cerr := st.Close(); cerr != nil {
			logger.Debug("关闭审计数据库连接失败", "err", cerr)
		}
	} else {
		logger.Warn("回退审计写库失败（数据库可能正被恢复）", "err", oerr)
	}
	return nil
}

// restartServiceAfterRollback 重启控制端服务。
func restartServiceAfterRollback(logger *slog.Logger, state *api.UpgradeState) error {
	// 1) systemd
	if unit := strings.TrimSpace(os.Getenv("LEADSTACK_SYSTEMD_UNIT")); unit != "" {
		if err := exec.Command("systemctl", "restart", unit).Run(); err == nil {
			logger.Info("已通过 systemd 重启服务", "unit", unit)
			return nil
		} else {
			logger.Warn("systemctl 重启失败，尝试其它方式", "unit", unit, "err", err)
		}
	}
	// 2) launchd
	if label := strings.TrimSpace(os.Getenv("LEADSTACK_LAUNCHD_LABEL")); label != "" {
		target := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
		if err := exec.Command("launchctl", "kickstart", "-k", target).Run(); err == nil {
			logger.Info("已通过 launchd 重启服务", "label", label)
			return nil
		} else {
			logger.Warn("launchctl 重启失败，尝试直接拉起", "label", label, "err", err)
		}
	}
	// 3) 直接拉起旧二进制（开发/无服务管理器场景）。
	//
	// 注意：watchdog 自己是跑在 <exe>.prev 上的，回退后该路径已被 rename 成 <exe>，
	// 所以这里必须用 state.NewExe，而不是 os.Executable()。
	exe := strings.TrimSpace(state.NewExe)
	if exe == "" {
		found, ferr := os.Executable()
		if ferr != nil {
			return fmt.Errorf("定位当前可执行文件失败: %w", ferr)
		}
		exe = found
	}
	logPath := filepath.Join(state.DataDir, "upgrade", "rollback-restart.log")
	if mkErr := os.MkdirAll(filepath.Dir(logPath), 0o750); mkErr != nil {
		return fmt.Errorf("创建日志目录失败: %w", mkErr)
	}
	logFile, openErr := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if openErr != nil {
		return fmt.Errorf("打开日志文件失败: %w", openErr)
	}
	defer func() { _ = logFile.Close() }()

	args := state.Args
	if len(args) == 0 {
		args = []string{exe}
	} else {
		// state.Args 记录的是完整 os.Args（含 argv[0]），替换成当前路径以保证正确 exec。
		args = append([]string{exe}, args[1:]...)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = os.Environ()
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("直接拉起旧版本失败: %w", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	logger.Info("已直接拉起旧版本进程", "pid", pid)
	return nil
}

var _ = strconv.Itoa
