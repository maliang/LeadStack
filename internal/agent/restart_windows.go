//go:build windows

package agent

import (
	"os"
	"os/exec"
)

// restartSelf 在 Windows 上启动新版本进程，然后退出旧进程。
//
// Windows 无法 exec 替换进程镜像，因此启动带相同参数的新进程；
// 旧进程退出后，新进程即可正常写入升级后的 exe，遗留的 .old 会在下次启动时清理。
func restartSelf(exePath string) error {
	cmd := exec.Command(exePath, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	// 新进程已接管，退出旧进程（控制端会看到断线并由新进程重新连接）。
	os.Exit(0)
	return nil
}
