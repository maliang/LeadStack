package notify

import (
	"context"
	"time"
)

// RetryPolicy 控制发送失败后的重试行为。
//
// 契约（task-36）：HTTP 5xx 与超时最多重试 2 次，指数退避，总耗时 <10s；
// 4xx 与业务错误（HTTP 200 + errcode≠0）不重试。
type RetryPolicy struct {
	// MaxRetries 最大重试次数（不含首次尝试）。0 表示不重试。
	MaxRetries int
	// BaseDelay 第一次重试前的等待时间，默认 200ms；之后按 2 倍递增。
	BaseDelay time.Duration
	// MaxDelay 单次退避上限，默认 2s。
	MaxDelay time.Duration
}

// DefaultRetryPolicy 返回生产默认值：2 次重试，200ms/400ms 退避（总计 600ms）。
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxRetries: 2, BaseDelay: 200 * time.Millisecond, MaxDelay: 2 * time.Second}
}

// maxRetriesCap 是 MaxRetries 的防御性上限，避免配置失误导致长时间阻塞。
const maxRetriesCap = 5

// normalized 补齐零值并做防御性收敛。
// 注意：MaxRetries 的 0 是「显式不重试」，不会被补成默认 2——默认值只由 NewClient 填。
func (p RetryPolicy) normalized() RetryPolicy {
	out := p
	if out.MaxRetries < 0 {
		out.MaxRetries = 0
	}
	if out.MaxRetries > maxRetriesCap {
		out.MaxRetries = maxRetriesCap
	}
	if out.BaseDelay <= 0 {
		out.BaseDelay = DefaultRetryPolicy().BaseDelay
	}
	if out.MaxDelay <= 0 {
		out.MaxDelay = DefaultRetryPolicy().MaxDelay
	}
	if out.MaxDelay < out.BaseDelay {
		out.MaxDelay = out.BaseDelay
	}
	return out
}

// backoffDelay 返回第 attempt 次重试前的等待时间（attempt 从 0 开始，指数退避）。
func (p RetryPolicy) backoffDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	// 限制移位次数，避免超大 attempt 溢出。
	if attempt > 20 {
		attempt = 20
	}
	d := p.BaseDelay << uint(attempt)
	if d <= 0 || d > p.MaxDelay {
		d = p.MaxDelay
	}
	return d
}

// sleepCtx 在等待期间响应 ctx 取消。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
