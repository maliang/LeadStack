package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// countingServer 返回一个按脚本应答、并统计请求次数的服务端。
func countingServer(t *testing.T, fn func(n int32, w http.ResponseWriter, r *http.Request)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fn(n.Add(1), w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func TestRetryOn5xxThenSuccess(t *testing.T) {
	srv, n := countingServer(t, func(n int32, w http.ResponseWriter, r *http.Request) {
		if n <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("temporarily down"))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	c := NewClient(Options{Retry: &RetryPolicy{MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: 4 * time.Millisecond}})
	if err := c.Send(context.Background(), webhookChannel(srv.URL), testMessage()); err != nil {
		t.Fatalf("重试后应当成功: %v", err)
	}
	if got := n.Load(); got != 3 {
		t.Errorf("请求次数 = %d, want 3（1 次成功 + 2 次重试）", got)
	}
}

func TestRetryStopsAfterMaxRetriesOn5xx(t *testing.T) {
	srv, n := countingServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("still down"))
	})

	c := NewClient(Options{Retry: &RetryPolicy{MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: 4 * time.Millisecond}})
	err := c.Send(context.Background(), webhookChannel(srv.URL), testMessage())
	if err == nil {
		t.Fatal("持续 5xx 应当最终失败")
	}
	if got := n.Load(); got != 3 {
		t.Errorf("请求次数 = %d, want 3（首次 + 2 次重试）", got)
	}
	if KindOf(err) != ErrorKindServer {
		t.Errorf("Kind = %s, want server", KindOf(err))
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("错误信息应包含状态码: %v", err)
	}
}

// 默认策略下 3 次尝试的总耗时必须远小于 10s（契约：总计 <10s）。
func TestRetryDefaultPolicyTotalUnder10s(t *testing.T) {
	srv, n := countingServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c := NewClient(Options{}) // 默认策略
	start := time.Now()
	err := c.Send(context.Background(), webhookChannel(srv.URL), testMessage())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("应返回错误")
	}
	if got := n.Load(); got != 3 {
		t.Errorf("请求次数 = %d, want 3", got)
	}
	if elapsed >= 10*time.Second {
		t.Errorf("重试总耗时 %v 超过 10s", elapsed)
	}
	if elapsed < 500*time.Millisecond {
		t.Errorf("默认退避似乎没生效（%v，期望 ≥600ms 的退避）", elapsed)
	}
}

func TestRetryOnTimeout(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		time.Sleep(150 * time.Millisecond)
	}))
	defer srv.Close()

	c := NewClient(Options{
		Timeout: 30 * time.Millisecond,
		Retry:   &RetryPolicy{MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: 4 * time.Millisecond},
	})
	err := c.Send(context.Background(), webhookChannel(srv.URL), testMessage())
	if err == nil {
		t.Fatal("持续超时应当最终失败")
	}
	if KindOf(err) != ErrorKindTimeout {
		t.Errorf("Kind = %s, want timeout", KindOf(err))
	}
	if got := n.Load(); got != 3 {
		t.Errorf("请求次数 = %d, want 3（超时也要重试）", got)
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv, n := countingServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			})
			c := NewClient(Options{Retry: &RetryPolicy{MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: 4 * time.Millisecond}})
			err := c.Send(context.Background(), webhookChannel(srv.URL), testMessage())
			if err == nil {
				t.Fatal("4xx 应当失败")
			}
			if got := n.Load(); got != 1 {
				t.Errorf("4xx 不应重试，请求次数 = %d", got)
			}
		})
	}
}

func TestNoRetryOnBusinessError(t *testing.T) {
	srv, n := countingServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":310000,"errmsg":"sign not match"}`))
	})
	c := NewClient(Options{Retry: &RetryPolicy{MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: 4 * time.Millisecond}})
	ch := Channel{Kind: KindDingTalk, Config: map[string]any{
		"webhook": srv.URL + "/robot/send?access_token=fake",
		"secret":  "SECtestsecret0123456789abcdef",
	}}
	err := c.Send(context.Background(), ch, testMessage())
	if err == nil {
		t.Fatal("业务错误应当失败")
	}
	if got := n.Load(); got != 1 {
		t.Errorf("业务错误不应重试，请求次数 = %d", got)
	}
	if KindOf(err) != ErrorKindProvider {
		t.Errorf("Kind = %s, want provider", KindOf(err))
	}
}

func TestNoRetryOnConnectFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := NewClient(Options{Retry: &RetryPolicy{MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: 4 * time.Millisecond}})
	start := time.Now()
	err := c.Send(context.Background(), webhookChannel(url), testMessage())
	if err == nil {
		t.Fatal("应返回错误")
	}
	if KindOf(err) != ErrorKindConnect {
		t.Errorf("Kind = %s, want connect", KindOf(err))
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("连接失败不应重试，耗时 %v", elapsed)
	}
}

func TestRetryDisabledByPolicy(t *testing.T) {
	srv, n := countingServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c := NewClient(Options{Retry: &RetryPolicy{}}) // 显式 0 次重试
	if err := c.Send(context.Background(), webhookChannel(srv.URL), testMessage()); err == nil {
		t.Fatal("应返回错误")
	}
	if got := n.Load(); got != 1 {
		t.Errorf("请求次数 = %d, want 1", got)
	}
}

func TestRetryStopsWhenContextCanceledDuringBackoff(t *testing.T) {
	srv, n := countingServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c := NewClient(Options{Retry: &RetryPolicy{MaxRetries: 2, BaseDelay: 300 * time.Millisecond, MaxDelay: 300 * time.Millisecond}})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := c.Send(ctx, webhookChannel(srv.URL), testMessage())
	if err == nil {
		t.Fatal("应返回错误")
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Errorf("ctx 取消后应立即返回，耗时 %v", elapsed)
	}
	if got := n.Load(); got != 1 {
		t.Errorf("取消后不应再发请求，请求次数 = %d", got)
	}
}

func TestBackoffDelayMath(t *testing.T) {
	p := RetryPolicy{MaxRetries: 2, BaseDelay: 100 * time.Millisecond, MaxDelay: 150 * time.Millisecond}.normalized()
	if got := p.backoffDelay(0); got != 100*time.Millisecond {
		t.Errorf("delay(0) = %v", got)
	}
	if got := p.backoffDelay(1); got != 150*time.Millisecond {
		t.Errorf("delay(1) 应被 MaxDelay 截断为 150ms，实际 %v", got)
	}
	if got := p.backoffDelay(5); got != 150*time.Millisecond {
		t.Errorf("delay(5) = %v", got)
	}
	def := DefaultRetryPolicy()
	if def.MaxRetries != 2 {
		t.Errorf("默认重试次数 = %d, want 2", def.MaxRetries)
	}
	var total time.Duration
	for i := 0; i < def.MaxRetries; i++ {
		total += def.normalized().backoffDelay(i)
	}
	if total >= 10*time.Second {
		t.Errorf("默认退避总和 %v 超过 10s", total)
	}
	// 显式 0 次重试不会被补齐成默认值。
	if got := (&RetryPolicy{}).normalized().MaxRetries; got != 0 {
		t.Errorf("显式禁用重试被改写为 %d", got)
	}
	// 防御性上限。
	if got := (&RetryPolicy{MaxRetries: 99}).normalized().MaxRetries; got != maxRetriesCap {
		t.Errorf("MaxRetries 上限 = %d", got)
	}
}
