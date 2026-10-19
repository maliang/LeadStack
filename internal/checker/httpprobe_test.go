package checker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbeHTTPOKAndLatencyIncludesBody(t *testing.T) {
	const bodyDelay = 150 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 先发状态行与响应头，再慢慢写 body：
		// 如果延迟只算到「收到响应头」，下面的断言就会失败。
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(bodyDelay)
		_, _ = io.WriteString(w, "hello leadstack keyword")
	}))
	defer srv.Close()

	r := Probe(context.Background(), Spec{
		Kind:   KindHTTP,
		Target: srv.URL,
		Params: map[string]any{"keyword": "keyword"},
	}, nil, "srv-1")
	if !r.OK {
		t.Fatalf("期望成功，得到 %+v", r)
	}
	if r.StatusCode != 200 {
		t.Fatalf("StatusCode=%d want 200", r.StatusCode)
	}
	if r.LatencyMS < bodyDelay.Milliseconds() {
		t.Fatalf("延迟必须算到读完 body：LatencyMS=%d < %d", r.LatencyMS, bodyDelay.Milliseconds())
	}
	if !strings.Contains(r.Message, "keyword") {
		t.Fatalf("成功摘要应说明关键字匹配: %q", r.Message)
	}
}

func TestProbeHTTPStatusExpectations(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/nf", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/created", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cases := []struct {
		name    string
		path    string
		params  map[string]any
		ok      bool
		message string
	}{
		{name: "默认 2xx 通过", path: "/ok", params: nil, ok: true},
		{name: "默认 2xx 拒绝 404", path: "/nf", params: nil, ok: false, message: "不在期望范围 2xx"},
		{name: "单值 404", path: "/nf", params: map[string]any{"expect_status": "404"}, ok: true},
		{name: "JSON 数字单值", path: "/created", params: map[string]any{"expect_status": float64(201)}, ok: true},
		{name: "区间命中", path: "/created", params: map[string]any{"expect_status": "200-299"}, ok: true},
		{name: "区间未命中", path: "/ok", params: map[string]any{"expect_status": "400-499"}, ok: false, message: "不在期望范围 4xx"},
		{name: "Nxx 写法", path: "/created", params: map[string]any{"expect_status": "2xx"}, ok: true},
		{name: "非法 expect_status", path: "/ok", params: map[string]any{"expect_status": "abc"}, ok: false, message: "expect_status 参数非法"},
		{name: "越界 expect_status", path: "/ok", params: map[string]any{"expect_status": "999"}, ok: false, message: "expect_status 参数非法"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Probe(context.Background(), Spec{Kind: KindHTTP, Target: srv.URL + tc.path, Params: tc.params}, nil, "srv-1")
			if r.OK != tc.ok {
				t.Fatalf("OK=%v want %v (message=%q)", r.OK, tc.ok, r.Message)
			}
			if tc.message != "" && !strings.Contains(r.Message, tc.message) {
				t.Fatalf("message=%q 应包含 %q", r.Message, tc.message)
			}
		})
	}
}

func TestProbeHTTPKeywordMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "nothing to see here")
	}))
	defer srv.Close()

	r := Probe(context.Background(), Spec{
		Kind:   KindHTTP,
		Target: srv.URL,
		Params: map[string]any{"keyword": "needle"},
	}, nil, "srv-1")
	if r.OK {
		t.Fatalf("关键字缺失不应成功: %+v", r)
	}
	if !strings.Contains(r.Message, "未包含关键字") || r.StatusCode != 200 {
		t.Fatalf("message=%q status=%d", r.Message, r.StatusCode)
	}
}

func TestProbeHTTPBodyLimit(t *testing.T) {
	const chunk = 64 << 10
	total := 2 << 20 // 2MB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, chunk)
		for i := range buf {
			buf[i] = 'x'
		}
		written := 0
		for written < total {
			n := chunk
			if total-written < n {
				n = total - written
			}
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			written += n
		}
		_, _ = io.WriteString(w, "needle-at-the-end")
	}))
	defer srv.Close()

	// 关键字在 1MB 之后：读取上限生效，判定「未包含」并说明已截断。
	r := Probe(context.Background(), Spec{
		Kind:   KindHTTP,
		Target: srv.URL,
		Params: map[string]any{"keyword": "needle-at-the-end"},
	}, nil, "srv-1")
	if r.OK {
		t.Fatalf("超过 1MB 的关键字不应被匹配到: %+v", r)
	}
	if !strings.Contains(r.Message, "截断") {
		t.Fatalf("应说明已按 1MB 截断: %q", r.Message)
	}

	// 开头就在上限内的关键字必须能匹配到。
	r2 := Probe(context.Background(), Spec{
		Kind:   KindHTTP,
		Target: srv.URL,
		Params: map[string]any{"keyword": "xxx"},
	}, nil, "srv-1")
	if !r2.OK {
		t.Fatalf("上限内的关键字应匹配成功: %+v", r2)
	}
}

func TestProbeHTTPFollowsRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/new", http.StatusFound)
	})
	mux.HandleFunc("/new", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "landed")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r := Probe(context.Background(), Spec{
		Kind:   KindHTTP,
		Target: srv.URL + "/old",
		Params: map[string]any{"keyword": "landed"},
	}, nil, "srv-1")
	if !r.OK {
		t.Fatalf("应跟随重定向: %+v", r)
	}
	if !strings.Contains(r.Message, "重定向") {
		t.Fatalf("摘要应提到重定向: %q", r.Message)
	}
}

func TestProbeHTTPInsecure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "tls-ok")
	}))
	defer srv.Close()

	r := Probe(context.Background(), Spec{Kind: KindHTTP, Target: srv.URL}, nil, "srv-1")
	if r.OK {
		t.Fatalf("默认应校验证书，自签证书必须失败: %+v", r)
	}

	r2 := Probe(context.Background(), Spec{
		Kind:   KindHTTP,
		Target: srv.URL,
		Params: map[string]any{"insecure": true, "keyword": "tls-ok"},
	}, nil, "srv-1")
	if !r2.OK {
		t.Fatalf("insecure=true 时应跳过证书校验: %+v", r2)
	}
}

func TestProbeHTTPMethod(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	r := Probe(context.Background(), Spec{
		Kind:   KindHTTP,
		Target: srv.URL,
		Params: map[string]any{"method": "post"},
	}, nil, "srv-1")
	if !r.OK || got != http.MethodPost {
		t.Fatalf("method 应生效: ok=%v got=%q message=%q", r.OK, got, r.Message)
	}

	r2 := Probe(context.Background(), Spec{
		Kind:   KindHTTP,
		Target: srv.URL,
		Params: map[string]any{"method": "GET; rm -rf /"},
	}, nil, "srv-1")
	if r2.OK || !strings.Contains(r2.Message, "method 参数非法") {
		t.Fatalf("非法 method 应被拒绝: %+v", r2)
	}
}

func TestProbeHTTPBadURLAndEmptyTarget(t *testing.T) {
	r := Probe(context.Background(), Spec{Kind: KindHTTP, Target: "://bad"}, nil, "srv-1")
	if r.OK || !strings.Contains(r.Message, "URL 非法") {
		t.Fatalf("非法 URL 应失败: %+v", r)
	}
	r2 := Probe(context.Background(), Spec{Kind: KindHTTP, Target: "  "}, nil, "srv-1")
	if r2.OK || !strings.Contains(r2.Message, "不能为空") {
		t.Fatalf("空 URL 应失败: %+v", r2)
	}
}

func TestProbeHTTPTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	start := time.Now()
	r := Probe(context.Background(), Spec{Kind: KindHTTP, Target: srv.URL, TimeoutS: 1}, nil, "srv-1")
	if r.OK {
		t.Fatalf("超时应失败: %+v", r)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("超时未生效，耗时 %s", elapsed)
	}
	if !strings.Contains(r.Message, "超时") && !strings.Contains(r.Message, "取消") {
		t.Fatalf("message=%q 应说明超时", r.Message)
	}
}

func TestProbeHTTPContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r := Probe(ctx, Spec{Kind: KindHTTP, Target: srv.URL, TimeoutS: 5}, nil, "srv-1")
	if r.OK {
		t.Fatalf("ctx 取消后不应成功: %+v", r)
	}
}

func TestProbeHTTPConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	r := Probe(context.Background(), Spec{Kind: KindHTTP, Target: url, TimeoutS: 2}, nil, "srv-1")
	if r.OK {
		t.Fatalf("已关闭的服务不应成功: %+v", r)
	}
	if !strings.Contains(r.Message, "HTTP 请求失败") {
		t.Fatalf("message=%q", r.Message)
	}
}

func TestParseExpectStatus(t *testing.T) {
	cases := []struct {
		in      any
		lo, hi  int
		wantErr bool
	}{
		{in: "200", lo: 200, hi: 200},
		{in: "200-299", lo: 200, hi: 299},
		{in: "2xx", lo: 200, hi: 299},
		{in: "5XX", lo: 500, hi: 599},
		{in: float64(301), lo: 301, hi: 301},
		{in: "", lo: 200, hi: 299},
		{in: nil, lo: 200, hi: 299},
		{in: "abc", wantErr: true},
		{in: "99", wantErr: true},
		{in: "300-200", wantErr: true},
		{in: "6xx", wantErr: true},
		{in: []any{200}, wantErr: true},
	}
	for i, tc := range cases {
		lo, hi, err := parseExpectStatus(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("用例 %d (%v) 期望报错，得到 %d-%d", i, tc.in, lo, hi)
			}
			continue
		}
		if err != nil {
			t.Errorf("用例 %d (%v) 意外报错: %v", i, tc.in, err)
			continue
		}
		if lo != tc.lo || hi != tc.hi {
			t.Errorf("用例 %d (%v) = %d-%d want %d-%d", i, tc.in, lo, hi, tc.lo, tc.hi)
		}
	}
}

func TestStatusRangeText(t *testing.T) {
	if got := statusRangeText(200, 299); got != "2xx" {
		t.Errorf("2xx: %q", got)
	}
	if got := statusRangeText(404, 404); got != "404" {
		t.Errorf("single: %q", got)
	}
	if got := statusRangeText(200, 250); got != "200-250" {
		t.Errorf("range: %q", got)
	}
}
