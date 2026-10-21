package api

import (
	"strings"
	"sync"
	"time"
)

// loginPolicy 是登录保护的策略（契约 §54），来自配置，可在运行期只读使用。
type loginPolicy struct {
	// MaxAttempts 是账号连续失败锁定阈值；<=0 表示关闭账号锁定。
	MaxAttempts int
	// LockDuration 是锁定时长。
	LockDuration time.Duration
	// IPLimit 是同一 IP 在 IPWindow 内允许的登录请求数；<=0 表示关闭 IP 限流。
	IPLimit int
	// IPWindow 是 IP 限流窗口。
	IPWindow time.Duration
}

// defaultLoginPolicy 是未配置时的默认值。
func defaultLoginPolicy() loginPolicy {
	return loginPolicy{
		MaxAttempts:  5,
		LockDuration: 15 * time.Minute,
		IPLimit:      20,
		IPWindow:     time.Minute,
	}
}

// failState 是进程内失败计数（仅用于**未知用户名**，已存在用户以数据库为准）。
type failState struct {
	count int
	last  time.Time
}

// maxUnknownTracked 限制进程内跟踪的未知用户名数量，防止内存放大。
const maxUnknownTracked = 4096

// loginThrottle 提供两层保护：
//   - IP 维度：滑动窗口限流（进程内）；
//   - 未知用户名维度：进程内失败计数（已存在用户的锁定落库，见 store.RecordLoginFailure）。
type loginThrottle struct {
	mu     sync.Mutex
	policy loginPolicy
	now    func() time.Time

	ipHits     map[string][]time.Time
	unknown    map[string]*failState
	unknownLRU []string // 简易淘汰顺序（插入序），超出上限时丢最早的键
}

// newLoginThrottle 构造限流器。
func newLoginThrottle(policy loginPolicy) *loginThrottle {
	t := &loginThrottle{
		policy:  policy,
		now:     time.Now,
		ipHits:  make(map[string][]time.Time, 64),
		unknown: make(map[string]*failState, 64),
	}
	return t
}

// setPolicy 在运行期更新策略（测试与热更新用）。
func (t *loginThrottle) setPolicy(policy loginPolicy) {
	t.mu.Lock()
	t.policy = policy
	t.mu.Unlock()
}

// allowIP 判断该来源 IP 是否仍在限流窗口内；不允许时返回建议等待时长。
func (t *loginThrottle) allowIP(ip string) (bool, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.policy.IPLimit <= 0 || t.policy.IPWindow <= 0 {
		return true, 0
	}
	key := strings.TrimSpace(ip)
	now := t.now()
	windowStart := now.Add(-t.policy.IPWindow)
	hits := t.ipHits[key]
	keep := hits[:0]
	for _, ts := range hits {
		if ts.After(windowStart) {
			keep = append(keep, ts)
		}
	}
	if len(keep) >= t.policy.IPLimit {
		t.ipHits[key] = keep
		// 最早的一次滑出窗口即可放行。
		retry := keep[0].Add(t.policy.IPWindow).Sub(now)
		if retry < time.Second {
			retry = time.Second
		}
		return false, retry
	}
	keep = append(keep, now)
	t.ipHits[key] = keep
	return true, 0
}

// noteUnknownFailure 记录一次未知用户名的失败并判断是否应临时锁定（进程内）。
func (t *loginThrottle) noteUnknownFailure(username string) (locked bool, remaining int) {
	if t.policy.MaxAttempts <= 0 {
		return false, 0
	}
	key := strings.ToLower(strings.TrimSpace(username))
	t.mu.Lock()
	defer t.mu.Unlock()
	st, ok := t.unknown[key]
	now := t.now()
	if !ok || now.Sub(st.last) > t.policy.LockDuration {
		st = &failState{}
		if !ok {
			t.unknownLRU = append(t.unknownLRU, key)
			if len(t.unknownLRU) > maxUnknownTracked {
				oldest := t.unknownLRU[0]
				t.unknownLRU = t.unknownLRU[1:]
				delete(t.unknown, oldest)
			}
		}
		t.unknown[key] = st
	}
	st.count++
	st.last = now
	if st.count >= t.policy.MaxAttempts {
		return true, 0
	}
	return false, t.policy.MaxAttempts - st.count
}

// unknownLocked 判断未知用户名当前是否处于临时锁定状态。
func (t *loginThrottle) unknownLocked(username string) (bool, time.Duration) {
	if t.policy.MaxAttempts <= 0 {
		return false, 0
	}
	key := strings.ToLower(strings.TrimSpace(username))
	t.mu.Lock()
	defer t.mu.Unlock()
	st, ok := t.unknown[key]
	if !ok {
		return false, 0
	}
	now := t.now()
	if now.Sub(st.last) > t.policy.LockDuration {
		delete(t.unknown, key)
		return false, 0
	}
	if st.count >= t.policy.MaxAttempts {
		retry := st.last.Add(t.policy.LockDuration).Sub(now)
		if retry < time.Second {
			retry = time.Second
		}
		return true, retry
	}
	return false, 0
}

// resetUnknown 在成功登录或管理员解锁时清理进程内计数。
func (t *loginThrottle) resetUnknown(username string) {
	key := strings.ToLower(strings.TrimSpace(username))
	t.mu.Lock()
	delete(t.unknown, key)
	t.mu.Unlock()
}
