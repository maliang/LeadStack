//go:build !windows

package agent

import (
	"context"
	"os/exec"
	"syscall"
)

// shellCommand 在类 Unix 系统上通过 sh -c 执行命令。
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", command)
}

// configureProc 配置进程组与超时取消行为：
// 命令在其自己的进程组中启动，超时取消时向整个进程组发送 SIGKILL，
// 避免 sh 的子进程残留。
func configureProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = jobWaitDelay
}
