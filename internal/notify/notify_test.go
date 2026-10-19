package notify

import (
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"
)

// recordedReq 是被测服务端收到的一次请求快照。
type recordedReq struct {
	method string
	url    *url.URL
	header http.Header
	body   []byte
}

// recorder 是并发安全的请求记录器，配合 httptest.Server 使用。
type recorder struct {
	mu   sync.Mutex
	reqs []recordedReq
}

// serve 返回固定状态码与响应体的 handler，并记录每个请求。
func (r *recorder) serve(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.reqs = append(r.reqs, recordedReq{
			method: req.Method,
			url:    req.URL,
			header: req.Header.Clone(),
			body:   b,
		})
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// count 返回已收到的请求数。
func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

// last 返回最后一个请求；没有请求时直接失败。
func (r *recorder) last(t *testing.T) recordedReq {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reqs) == 0 {
		t.Fatal("服务端没有收到任何请求")
	}
	return r.reqs[len(r.reqs)-1]
}

// testMessage 是各渠道共用的样例通知。
func testMessage() Message {
	return Message{
		Title:      "CPU 使用率过高",
		Body:       "web-01 的 CPU 使用率持续高于 90%",
		Severity:   SeverityCritical,
		ServerName: "web-01",
		CheckName:  "cpu-high",
		Value:      95.5,
		TS:         1700000000000,
		AlertID:    "alert-1",
		ConsoleURL: "http://console.example.com/alerts/alert-1",
	}
}

// fixedClock 返回固定时钟，便于断言签名时间戳。
func fixedClock(unixMS int64) func() time.Time {
	return func() time.Time { return time.UnixMilli(unixMS) }
}
