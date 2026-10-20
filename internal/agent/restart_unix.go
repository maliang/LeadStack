//go:build !windows

package agent

import (
	"os"
	"syscall"
)

// restartSelf 用 syscall.Exec 原地替换当前进程镜像为新版本。
// 成功后函数不会返回；控制端会看到连接断开，agent 以新版本重新连接并 hello。
func restartSelf(exePath string) error {
	return syscall.Exec(exePath, os.Args, os.Environ())
}
