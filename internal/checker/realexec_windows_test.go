//go:build windows

package checker

import (
	"os/exec"
	"time"
)

// realExecWaitDelay 是超时取消后等待 I/O 管道关闭的兜底时间。
const realExecWaitDelay = 3 * time.Second

// configureRealExec 在 Windows 上只设置兜底 WaitDelay：
// 测试用 POSIX shell 语义，Windows 下相关用例本就跳过，不做进程组处理。
func configureRealExec(cmd *exec.Cmd) {
	cmd.WaitDelay = realExecWaitDelay
}
