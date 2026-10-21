//go:build !windows

package checker

import (
	"os/exec"
	"syscall"
	"time"
)

// realExecWaitDelay 是超时取消后等待 I/O 管道关闭的兜底时间。
const realExecWaitDelay = 3 * time.Second

// configureRealExec 让测试里的真实执行器与 agent 侧 job.exec 保持同一套超时语义：
// 进程独立进程组 + 取消时向整个进程组发 SIGKILL + 兜底 WaitDelay。
//
// 少了这一层，`sh -c "sleep 5"` 在 Linux（dash）上会出现「ctx 已取消但 Run 仍等到
// sleep 自然结束」的现象：sh 的子进程继承了输出管道，Wait 一直阻塞在拷贝协程上。
func configureRealExec(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = realExecWaitDelay
}
