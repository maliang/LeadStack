package webui

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// jsPayload 生成指定长度左右的可压缩 JS 文本（重复模式，压缩比稳定）。
func jsPayload(n int) []byte {
	var b strings.Builder
	for b.Len() < n {
		fmt.Fprintf(&b, "export const v%d = %d;\n", b.Len(), b.Len())
	}
	return []byte(b.String())
}

// doRequest 发一个请求并返回记录器。
func doRequest(t *testing.T, h http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// 预压缩表只收「可压缩扩展名 + 原始大小 ≥ gzipMinSize」的文件。
func TestWebUIPrecompressSelectsEligibleFiles(t *testing.T) {
	h := newSPAHandler(fstest.MapFS{
		"assets/app-a1b2.js":  {Data: jsPayload(8 * 1024)},
		"assets/app-a1b2.css": {Data: []byte("body{color:#111}" + strings.Repeat("\n.a{margin:0}", 200))},
		"index.html":          {Data: []byte("<!doctype html>" + strings.Repeat("<p>x</p>", 200))},
		"assets/logo.png":     {Data: bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 1024)},
		"assets/tiny.js":      {Data: []byte("const t=1;")},
	})

	for _, want := range []string{"assets/app-a1b2.js", "assets/app-a1b2.css", "index.html"} {
		if _, ok := h.precompressed[want]; !ok {
			t.Errorf("应预压缩 %s，但表里没有", want)
		}
	}
	for _, unexpected := range []string{"assets/logo.png", "assets/tiny.js"} {
		if _, ok := h.precompressed[unexpected]; ok {
			t.Errorf("%s 不应进入预压缩表", unexpected)
		}
	}
}

// 带 Accept-Encoding: gzip 时返回压缩字节，解压后必须与原始字节逐字节相等。
func TestWebUIServeFileGzipRoundTrip(t *testing.T) {
	raw := jsPayload(64 * 1024)
	h := newSPAHandler(fstest.MapFS{"assets/app-a1b2.js": {Data: raw}})

	rec := doRequest(t, h, http.MethodGet, "/assets/app-a1b2.js", map[string]string{
		"Accept-Encoding": "deflate, gzip",
	})
	res := rec.Result()

	if got := res.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q，期望 gzip", got)
	}
	if got := res.Header.Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("Vary = %q，期望 Accept-Encoding", got)
	}
	if got, want := res.Header.Get("Content-Length"), strconv.Itoa(len(h.precompressed["assets/app-a1b2.js"])); got != want {
		t.Fatalf("Content-Length = %q，期望压缩后长度 %q", got, want)
	}
	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
		t.Fatalf("Content-Type = %q，期望 text/javascript", got)
	}
	if got := rec.Body.Len(); got >= len(raw) {
		t.Fatalf("压缩后长度 %d 不小于原始长度 %d，压缩没有生效", got, len(raw))
	}

	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("解压响应失败: %v", err)
	}
	defer func() {
		if cerr := zr.Close(); cerr != nil {
			t.Fatalf("关闭 gzip reader 失败: %v", cerr)
		}
	}()
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("读取解压内容失败: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("解压后字节与原始不一致：%d vs %d 字节", len(got), len(raw))
	}
}

// 不带 Accept-Encoding 时返回原始字节，且不得出现 Content-Encoding。
func TestWebUIServeFileWithoutGzipHeader(t *testing.T) {
	raw := jsPayload(64 * 1024)
	h := newSPAHandler(fstest.MapFS{"assets/app-a1b2.js": {Data: raw}})

	rec := doRequest(t, h, http.MethodGet, "/assets/app-a1b2.js", nil)
	res := rec.Result()

	if got := res.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q，期望空", got)
	}
	if got := res.Header.Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("Vary = %q，期望 Accept-Encoding", got)
	}
	if got, want := res.Header.Get("Content-Length"), strconv.Itoa(len(raw)); got != want {
		t.Fatalf("Content-Length = %q，期望 %q", got, want)
	}
	if !bytes.Equal(rec.Body.Bytes(), raw) {
		t.Fatalf("响应体与原始字节不一致：%d vs %d 字节", rec.Body.Len(), len(raw))
	}
}

// HEAD 不得写 body，但响应头（含压缩后 Content-Length）要完整。
func TestWebUIServeFileHeadHasNoBody(t *testing.T) {
	raw := jsPayload(64 * 1024)
	h := newSPAHandler(fstest.MapFS{"assets/app-a1b2.js": {Data: raw}})
	wantLen := strconv.Itoa(len(h.precompressed["assets/app-a1b2.js"]))

	cases := []struct {
		name    string
		headers map[string]string
		wantEnc string
		wantLen string
	}{
		{name: "gzip", headers: map[string]string{"Accept-Encoding": "gzip"}, wantEnc: "gzip", wantLen: wantLen},
		{name: "identity", headers: nil, wantEnc: "", wantLen: strconv.Itoa(len(raw))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(t, h, http.MethodHead, "/assets/app-a1b2.js", tc.headers)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d，期望 200", rec.Code)
			}
			if rec.Body.Len() != 0 {
				t.Fatalf("HEAD 返回了 %d 字节 body，期望 0", rec.Body.Len())
			}
			if got := rec.Header().Get("Content-Encoding"); got != tc.wantEnc {
				t.Fatalf("Content-Encoding = %q，期望 %q", got, tc.wantEnc)
			}
			if got := rec.Header().Get("Content-Length"); got != tc.wantLen {
				t.Fatalf("Content-Length = %q，期望 %q", got, tc.wantLen)
			}
		})
	}
}

// 小于 gzipMinSize 的文件不压，即使客户端接受 gzip。
func TestWebUIServeFileSmallFileNotCompressed(t *testing.T) {
	raw := []byte("const t=1;")
	h := newSPAHandler(fstest.MapFS{"assets/tiny.js": {Data: raw}})

	rec := doRequest(t, h, http.MethodGet, "/assets/tiny.js", map[string]string{"Accept-Encoding": "gzip"})
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("小文件被压缩了：Content-Encoding = %q", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), raw) {
		t.Fatalf("响应体与原始字节不一致")
	}
}

// .png 不在可压缩白名单，即使足够大也不压。
func TestWebUIServeFilePNGNotCompressed(t *testing.T) {
	raw := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 1024)
	h := newSPAHandler(fstest.MapFS{"assets/logo.png": {Data: raw}})

	rec := doRequest(t, h, http.MethodGet, "/assets/logo.png", map[string]string{"Accept-Encoding": "gzip"})
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("PNG 被压缩了：Content-Encoding = %q", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), raw) {
		t.Fatalf("响应体与原始字节不一致")
	}
}

// 缓存策略：assets/ 长缓存不可变；index.html 必须协商；其余短缓存。
func TestWebUICacheControlPolicy(t *testing.T) {
	indexHTML := []byte("<!doctype html>" + strings.Repeat("<p>x</p>", 200))
	h := newSPAHandler(fstest.MapFS{
		"index.html":         {Data: indexHTML},
		"assets/app-a1b2.js": {Data: jsPayload(8 * 1024)},
		"favicon.svg":        {Data: []byte("<svg/>")},
	})

	cases := []struct {
		target string
		want   string
	}{
		{target: "/assets/app-a1b2.js", want: "public, max-age=31536000, immutable"},
		{target: "/index.html", want: "no-cache"},
		{target: "/favicon.svg", want: "public, max-age=3600"},
		// SPA 回退也走 index.html，必须是 no-cache，不能被当成可长缓存资源。
		{target: "/servers/1", want: "no-cache"},
	}
	for _, tc := range cases {
		rec := doRequest(t, h, http.MethodGet, tc.target, nil)
		if got := rec.Header().Get("Cache-Control"); got != tc.want {
			t.Errorf("%s Cache-Control = %q，期望 %q", tc.target, got, tc.want)
		}
	}
}

// Accept-Encoding 解析：支持多编码与 q 值，gzip;q=0 视为显式拒收。
func TestWebUIAcceptsGzip(t *testing.T) {
	cases := map[string]bool{
		"":                      false,
		"gzip":                  true,
		"GZIP":                  true,
		"deflate, gzip":         true,
		"gzip;q=0":              false,
		"gzip; q=0":             false,
		"br":                    false,
		"*":                     false,
		"deflate, gzip;q=0.5":   true,
		"gzip;q=0.500, deflate": true,
	}
	for header, want := range cases {
		if got := acceptsGzip(header); got != want {
			t.Errorf("acceptsGzip(%q) = %v，期望 %v", header, got, want)
		}
	}
}

// 真实 dist（干净检出时只有 .gitkeep）也必须能起处理器并给出 200。
func TestWebUIHandlerWithEmbeddedDist(t *testing.T) {
	h := Handler()
	rec := doRequest(t, h, http.MethodGet, "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d，期望 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "LeadStack") {
		t.Fatalf("GET / 既不是真实首页也不是占位页")
	}
}

// 干净检出（dist 里只有 .gitkeep）时 "/" 必须回退到占位页而不是 404；
// 带扩展名的缺失资源仍然保持 404（不能被 SPA 回退成 HTML）。
func TestWebUIHandlerFallsBackToPlaceholderOnCleanCheckout(t *testing.T) {
	h := newSPAHandler(fstest.MapFS{".gitkeep": {Data: []byte{}}})

	rec := doRequest(t, h, http.MethodGet, "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d，期望 200（占位页）", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "make build") {
		t.Fatalf("占位页应提示先 make build，实际 body=%q", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("占位页 Cache-Control = %q，期望 no-store", got)
	}

	missing := doRequest(t, h, http.MethodGet, "/assets/missing.js", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("缺失资源 status = %d，期望 404", missing.Code)
	}
}

// failingWalkFS 让整树遍历失败，但单文件仍可打开：用于验证初始化降级。
type failingWalkFS struct{ fs.FS }

func (f failingWalkFS) ReadDir(string) ([]fs.DirEntry, error) {
	return nil, errors.New("模拟遍历失败")
}

// unreadableFS 让指定文件打不开，其余照常：用于验证单文件失败只跳过该项。
type unreadableFS struct {
	fs.FS
	bad string
}

func (f unreadableFS) Open(name string) (fs.File, error) {
	if name == f.bad {
		return nil, errors.New("模拟读取失败")
	}
	return f.FS.Open(name)
}

func (f unreadableFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(f.FS, name)
}

// 预压缩整体失败时只记 Warn：处理器照常构造，资源以未压缩形式正常返回，
// 服务绝不能因为压缩失败而起不来。
func TestWebUIPrecompressDegradesOnWalkError(t *testing.T) {
	raw := jsPayload(8 * 1024)
	h := newSPAHandler(failingWalkFS{fstest.MapFS{"assets/app-a1b2.js": {Data: raw}}})
	if len(h.precompressed) != 0 {
		t.Fatalf("遍历失败后不应留下预压缩条目，实际 %d 条", len(h.precompressed))
	}

	rec := doRequest(t, h, http.MethodGet, "/assets/app-a1b2.js", map[string]string{"Accept-Encoding": "gzip"})
	if rec.Code != http.StatusOK {
		t.Fatalf("降级后 status = %d，期望 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("降级后不应返回压缩体，Content-Encoding = %q", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), raw) {
		t.Fatalf("降级后响应体与原始字节不一致：%d vs %d 字节", rec.Body.Len(), len(raw))
	}
}

// 单个文件读失败只跳过该项，其余文件仍被预压缩。
func TestWebUIPrecompressSkipsUnreadableFile(t *testing.T) {
	good := jsPayload(8 * 1024)
	bad := jsPayload(8 * 1024)
	base := fstest.MapFS{
		"assets/good-a1.js":   {Data: good},
		"assets/broken-a1.js": {Data: bad},
	}
	h := newSPAHandler(unreadableFS{FS: base, bad: "assets/broken-a1.js"})

	if _, ok := h.precompressed["assets/good-a1.js"]; !ok {
		t.Fatal("可读文件应仍被预压缩")
	}
	if _, ok := h.precompressed["assets/broken-a1.js"]; ok {
		t.Fatal("读失败的文件不应进入预压缩表")
	}

	rec := doRequest(t, h, http.MethodGet, "/assets/good-a1.js", map[string]string{"Accept-Encoding": "gzip"})
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("可读文件 Content-Encoding = %q，期望 gzip", got)
	}
}
