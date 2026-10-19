//go:build windows

package agent

import (
	"context"
	"os/exec"
)

// shellCommand 在 Windows 上通过 cmd /C 执行命令。
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "cmd", "/C", command)
}

// configureProc 配置超时取消行为：Windows 没有进程组语义，直接终止子进程。
func configureProc(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = jobWaitDelay
}
