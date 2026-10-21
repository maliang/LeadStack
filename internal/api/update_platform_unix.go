//go:build !windows

package api

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// platformUpgradeUnsupported 返回不支持自升级的原因（空串表示支持）。
func platformUpgradeUnsupported() string { return "" }

// swapExecutable 执行 A/B 替换：<exe> → <exe>.prev，新文件 → <exe>。
//
// 两步都是同一文件系统内的 rename（原子）；第二步失败会把 .prev 换回去，
// 保证任何时刻 <exe> 都是一个完整可执行的文件（而不是半截）。
func swapExecutable(exe, newExe string) error {
	prev := exe + upgradePrevSuffix
	_ = os.Remove(prev) // 上一次残留的 .prev（已过保留期）先清掉
	if err := os.Rename(exe, prev); err != nil {
		return fmt.Errorf("备份当前可执行文件失败: %w", err)
	}
	if err := os.Rename(newExe, exe); err != nil {
		// 回滚第一步，避免 <exe> 缺失导致服务再也起不来。
		if rbErr := os.Rename(prev, exe); rbErr != nil {
			return fmt.Errorf("替换可执行文件失败: %v；且回滚失败: %v（原文件在 %s）", err, rbErr, prev)
		}
		return fmt.Errorf("替换可执行文件失败（已回滚）: %w", err)
	}
	if err := os.Chmod(exe, 0o755); err != nil {
		return fmt.Errorf("设置可执行权限失败: %w", err)
	}
	return nil
}

// RollbackExecutable 把 .prev 换回 <exe>，坏版本留档为 <exe>.failed-<ts>（watchdog 用）。
func RollbackExecutable(exe string) error { return rollbackExecutable(exe) }

// rollbackExecutable 把 .prev 换回 <exe>，坏版本留档为 <exe>.failed-<ts>。
func rollbackExecutable(exe string) error {
	prev := exe + upgradePrevSuffix
	if _, err := os.Stat(prev); err != nil {
		return fmt.Errorf("找不到回退用的旧版本 %s: %w", prev, err)
	}
	failed := exe + upgradeFailedPrefix + strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.Rename(exe, failed); err != nil {
		return fmt.Errorf("留存失败版本失败: %w", err)
	}
	if err := os.Rename(prev, exe); err != nil {
		// 尽力恢复：把失败版本放回去，至少保证 exec 路径存在。
		_ = os.Rename(failed, exe)
		return fmt.Errorf("回退旧版本失败: %w", err)
	}
	if err := os.Chmod(exe, 0o755); err != nil {
		return fmt.Errorf("设置回退后权限失败: %w", err)
	}
	return nil
}

// spawnUpgradeWatchdog 启动独立 watchdog 进程（detached，不随父进程退出）。
//
// 关键：Setsid 让 watchdog 脱离父进程的会话/进程组——父进程随后会 syscall.Exec
// 变成新二进制，即使新二进制立刻崩溃，watchdog 仍然活着并执行回退。
func spawnUpgradeWatchdog(watchdogExe string, dataDir, statePath string, state *UpgradeState) (int, error) {
	exe := watchdogExe
	if exe == "" {
		found, err := os.Executable()
		if err != nil {
			return 0, fmt.Errorf("定位可执行文件失败: %w", err)
		}
		exe = found
	}
	logPath := filepath.Join(dataDir, "upgrade", "watchdog.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o750); err != nil {
		return 0, fmt.Errorf("创建 watchdog 日志目录失败: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("打开 watchdog 日志失败: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(exe, "upgrade", "--watchdog", "--state", statePath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // 独立会话：父进程 exec/退出都不影响它
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("启动 watchdog 失败: %w", err)
	}
	pid := cmd.Process.Pid
	// 立刻释放子进程句柄（不 Wait），否则会变成僵尸并阻塞。
	_ = cmd.Process.Release()
	return pid, nil
}

// execSelf 用新二进制替换当前进程（unix exec，不 fork）。
func execSelf(path string, args []string) error {
	if err := syscall.Exec(path, args, os.Environ()); err != nil {
		return fmt.Errorf("exec %s 失败: %w", path, err)
	}
	return nil
}
