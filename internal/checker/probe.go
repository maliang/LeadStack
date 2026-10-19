package checker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// nowMS 是 Result.TS 的时间来源（Unix 毫秒）。测试可临时替换。
var nowMS = func() int64 { return time.Now().UnixMilli() }

// Probe 执行一次探测。exec 仅在 Kind 为 process/script 时被使用，其余可为 nil。
// 任何失败都要转成 Result{OK:false, Message:...}，不要返回 error（除 ctx 取消）。
func Probe(ctx context.Context, spec Spec, exec Executor, serverID string) Result {
	timeout := timeoutOf(spec)
	switch spec.Kind {
	case KindTCP:
		return probeTCP(ctx, spec, timeout)
	case KindHTTP:
		return probeHTTP(ctx, spec, timeout)
	case KindPing:
		return probePing(ctx, spec, timeout)
	case KindProcess:
		return probeProcess(ctx, spec, exec, serverID, timeout)
	case KindScript:
		return probeScript(ctx, spec, exec, serverID, timeout)
	default:
		return failResult(0, "未知检测类型: %q", string(spec.Kind))
	}
}

// timeoutOf 返回探测超时，未设置或非法时取 DefaultTimeoutS。
func timeoutOf(spec Spec) time.Duration {
	s := spec.TimeoutS
	if s <= 0 {
		s = DefaultTimeoutS
	}
	return time.Duration(s) * time.Second
}

// timeoutSecondsOf 返回给 Executor 的超时秒数。
func timeoutSecondsOf(spec Spec) int {
	s := spec.TimeoutS
	if s <= 0 {
		s = DefaultTimeoutS
	}
	return s
}

// failThresholdOf 返回 params.fail_threshold（默认 3，下限 1）。
// 该阈值属于「状态口径」，由本包按 docs/ARCHITECTURE.md 第 15 节实现。
func failThresholdOf(spec Spec) int {
	n := paramInt(spec.Params, "fail_threshold", DefaultFailThresh)
	if n < 1 {
		n = 1
	}
	return n
}

func okResult(latencyMS int64, statusCode int, format string, args ...any) Result {
	return Result{
		OK:         true,
		LatencyMS:  latencyMS,
		StatusCode: statusCode,
		Message:    fmt.Sprintf(format, args...),
		TS:         nowMS(),
	}
}

func failResult(latencyMS int64, format string, args ...any) Result {
	return Result{
		OK:        false,
		LatencyMS: latencyMS,
		Message:   fmt.Sprintf(format, args...),
		TS:        nowMS(),
	}
}

func failResultStatus(latencyMS int64, statusCode int, format string, args ...any) Result {
	r := failResult(latencyMS, format, args...)
	r.StatusCode = statusCode
	return r
}

// snippet 把多行输出压成一行并截断，用于返回给控制台的 message，
// 避免把目标机的完整输出灌进数据库。
func snippet(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	if max > 0 && len(s) > max {
		r := []rune(s)
		if len(r) > max {
			return string(r[:max]) + "…"
		}
	}
	return s
}

// stderrSuffix 生成「；stderr: …」后缀，stderr 为空时返回空串。
func stderrSuffix(stderr string) string {
	if s := snippet(stderr, 200); s != "" {
		return "；stderr: " + s
	}
	return ""
}

// isTimeout 判断错误是否为超时（net.Error 或 context 超时）。
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	type timeout interface{ Timeout() bool }
	var t timeout
	if errors.As(err, &t) {
		return t.Timeout()
	}
	return false
}

// elapsedMS 返回从 start 到现在的毫秒数（下限 0）。
func elapsedMS(start time.Time) int64 {
	d := time.Since(start).Milliseconds()
	if d < 0 {
		return 0
	}
	return d
}
