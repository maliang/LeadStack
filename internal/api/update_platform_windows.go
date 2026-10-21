//go:build windows

package api

import "errors"

// platformUpgradeUnsupported 在 Windows 上明确禁用自升级（不做半成品）。
func platformUpgradeUnsupported() string {
	return "Windows 不支持控制台自升级：请手动下载新版本替换可执行文件后重启"
}

// swapExecutable 在 Windows 上不可用。
func swapExecutable(exe, newExe string) error {
	return errors.New("Windows 不支持 A/B 替换可执行文件（运行中的文件无法被替换）")
}

// RollbackExecutable 在 Windows 上不可用。
func RollbackExecutable(exe string) error { return rollbackExecutable(exe) }

// rollbackExecutable 在 Windows 上不可用。
func rollbackExecutable(exe string) error {
	return errors.New("Windows 不支持自动回退可执行文件")
}

// spawnUpgradeWatchdog 在 Windows 上不可用。
func spawnUpgradeWatchdog(watchdogExe, dataDir, statePath string, state *UpgradeState) (int, error) {
	return 0, errors.New("Windows 不支持升级 watchdog")
}

// execSelf 在 Windows 上不可用。
func execSelf(path string, args []string) error {
	return errors.New("Windows 不支持进程内 exec")
}
