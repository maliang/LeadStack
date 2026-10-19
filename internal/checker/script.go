package checker

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// probeScript 把 spec.Target 作为脚本内容直接在目标机执行。
//
// 口径（docs/ARCHITECTURE.md 第 13 节）：
//   - params.exit_zero_ok 默认 true：退出码非 0 视为失败；
//   - params.expect_stdout 非空时要求 stdout 包含该内容；
//   - 脚本内容会进审计日志（由 backend 负责）。
func probeScript(ctx context.Context, spec Spec, exec Executor, serverID string, timeout time.Duration) Result {
	start := time.Now()
	if exec == nil {
		return failResult(0, "script 检测需要目标机执行通道（Executor 为 nil）")
	}
	script := spec.Target
	if strings.TrimSpace(script) == "" {
		return failResult(0, "脚本内容（target）不能为空")
	}
	exitZeroOK := true
	if raw, ok := spec.Params["exit_zero_ok"]; ok && raw != nil {
		exitZeroOK = paramBool(spec.Params, "exit_zero_ok")
	}
	expectStdout := paramString(spec.Params, "expect_stdout")

	stdout, stderr, exitCode, err := exec.Exec(ctx, serverID, script, timeoutSecondsOf(spec))
	lat := elapsedMS(start)
	if err != nil {
		if ctx.Err() != nil {
			return failResult(lat, "目标机执行被取消: %s", ctx.Err().Error())
		}
		return failResult(lat, "目标机执行失败: %s", err.Error())
	}
	if exitZeroOK && exitCode != 0 {
		return failResult(lat, "脚本退出码 %d（期望 0）%s；stdout=%q", exitCode, stderrSuffix(stderr), snippet(stdout, 200))
	}
	if expectStdout != "" && !strings.Contains(stdout, expectStdout) {
		return failResult(lat, "stdout 未包含期望内容 %q（退出码 %d，stdout=%q）",
			expectStdout, exitCode, snippet(stdout, 200))
	}
	msg := fmt.Sprintf("脚本执行成功（退出码 %d）", exitCode)
	if expectStdout != "" {
		msg += fmt.Sprintf("，已匹配期望输出 %q", expectStdout)
	}
	return okResult(lat, 0, "%s", msg)
}
