// Package webui 负责把前端构建产物嵌入二进制，并提供 SPA 回退处理器。
//
// dist 目录是**生成物**：由 Makefile 把 web-next/dist 复制到 internal/webui/dist 后再编译。
// 仓库里只保留 internal/webui/dist/.gitkeep 占位，保证干净检出时 go build / go vet 依然可用
// （//go:embed all:dist 要求目录里至少有一个文件；真实产物不入库，避免构建后工作区变脏）。
package webui

import (
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// distRoot 是嵌入文件系统里的前端产物目录名。
const distRoot = "dist"

const (
	// gzipMinSize 是预压缩的最小文件大小。更小的文件压缩后往往不更小，
	// 还要多付一次解压与内容协商成本，因此不压。
	gzipMinSize = 1024
	// gzipLevel 取 9 档（BestCompression）。压缩只在启动时做一次：当前 dist
	// 44 个可压文件全部压完实测 24-30ms，对启动无可感知影响；而档位直接决定
	// 常驻内存——同一份产物实测 5 档 516.5KiB、7 档 500.2KiB、9 档 492.8KiB，
	// 只有 8 档以上才能守住「预压缩内存 < 500KiB」的预算。请求路径不再压缩，
	// 所以这里没有理由为 CPU 让路。
	gzipLevel = gzip.BestCompression
)

// compressibleExts 是允许预压缩的扩展名白名单（文本类，压缩收益稳定）。
// 位图/字体/安装脚本不在其列：它们本身已压缩或体积小，再压反而更大。
var compressibleExts = map[string]struct{}{
	".html": {},
	".js":   {},
	".mjs":  {},
	".css":  {},
	".json": {},
	".svg":  {},
}

// FS 返回前端产物根目录；调用方通常只需要 Handler。
func FS() (fs.FS, error) {
	sub, err := fs.Sub(distFS, distRoot)
	if err != nil {
		return nil, fmt.Errorf("打开内嵌前端产物: %w", err)
	}
	return sub, nil
}

// Handler 返回 SPA 处理器：
//   - 命中真实静态文件时按文件返回（含 Content-Type 与缓存策略）；
//   - 其余路径回退到 index.html，交给前端路由处理；
//   - 形如 /assets/xxx.js 的缺失资源直接 404，避免把 HTML 当成脚本返回。
func Handler() http.Handler {
	root, err := FS()
	if err != nil {
		slog.Error("加载内嵌前端产物失败", "err", err)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "前端资源不可用", http.StatusInternalServerError)
		})
	}
	return newSPAHandler(root)
}

// spaHandler 提供内嵌静态资源与 SPA 回退。
//
// precompressed 保存「可压缩资源 → gzip 字节」，初始化后只读，
// 因此请求路径上的 map 访问不需要任何锁。
type spaHandler struct {
	root          fs.FS
	precompressed map[string][]byte
}

// newSPAHandler 构造处理器，并在此处一次性完成 gzip 预压缩。
// 抽成构造函数是为了让单测用 fstest.MapFS 注入产物，不依赖构建出的真实 dist。
func newSPAHandler(root fs.FS) *spaHandler {
	return &spaHandler{root: root, precompressed: precompress(root)}
}

// precompress 在启动时把可压缩静态资源预压成 gzip 字节。
//
// 为什么预压缩，而不是逐请求压：
//   - 前端产物在构建期就固定了，同一份 JS/CSS 会被每个用户反复请求；
//     逐请求重新压是纯重复 CPU，会把大 bundle 的压缩耗时摊到响应延迟上，
//     还会抬高 P99 与 GC 压力（每次分配新的 gzip.Writer 与输出缓冲）。
//   - 预压缩把这份成本挪到启动一次，请求路径只剩「一次 map 查找 + 一次 Write」，
//     延迟与并发数解耦。
//
// 内存代价：当前 dist 中「可压缩扩展名且 ≥1KiB」的文件共 44 个、原始合计
// 1,867,872 B（约 1.78MiB），gzip(BestCompression) 后合计 504,644 B（约
// 492.8KiB，约 27%），即新增常驻内存约 0.5MiB，与构建产物同阶、与并发量无关。
// 压缩结果只读，多请求共享同一份字节，不会按连接复制；map 只在初始化时写，
// 之后无锁读取。若产物中新增了大体积资源，该数值随产物线性增长，需要重新评估。
//
// 失败策略：单个文件遍历/读取/压缩失败只记 Warn 并跳过该文件；整体遍历失败
// 则返回 nil——处理器退化为不压缩，静态服务绝不能因为预压缩失败而起不来。
func precompress(root fs.FS) map[string][]byte {
	assets := make(map[string][]byte)
	err := fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// 单个条目读不了不影响其它文件，降级为「该项不压」。
			slog.Warn("遍历前端产物失败，跳过该项", "name", p, "err", err)
			return nil
		}
		if d.IsDir() || !isCompressible(p) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			slog.Warn("读取前端产物元信息失败，跳过预压缩", "name", p, "err", err)
			return nil
		}
		if info.Size() < gzipMinSize {
			return nil
		}
		raw, err := fs.ReadFile(root, p)
		if err != nil {
			slog.Warn("读取前端产物失败，跳过预压缩", "name", p, "err", err)
			return nil
		}
		gz, err := gzipBytes(raw)
		if err != nil {
			slog.Warn("压缩前端产物失败，跳过预压缩", "name", p, "err", err)
			return nil
		}
		assets[p] = gz
		return nil
	})
	if err != nil {
		slog.Warn("预压缩前端产物失败，降级为不压缩", "err", err)
		return nil
	}
	return assets
}

// gzipBytes 返回 raw 的 gzip 字节（level 见 gzipLevel）。
func gzipBytes(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzipLevel)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(raw); err != nil {
		// 写失败时仍尝试 Close 释放内部状态，但以首个错误为准。
		if cerr := zw.Close(); cerr != nil {
			slog.Debug("关闭 gzip writer 失败", "err", cerr)
		}
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// isCompressible 判断路径是否属于可预压缩的文本资源。
func isCompressible(name string) bool {
	_, ok := compressibleExts[strings.ToLower(path.Ext(name))]
	return ok
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "方法不被允许", http.StatusMethodNotAllowed)
		return
	}

	name := cleanPath(r.URL.Path)
	if file, ok := h.open(name); ok {
		defer func() {
			if cerr := file.Close(); cerr != nil {
				slog.Warn("关闭静态文件失败", "name", name, "err", cerr)
			}
		}()
		h.serveFile(w, r, name, file)
		return
	}

	// 带扩展名的资源找不到就是 404，不能回退成 HTML。
	// 例外是 index.html 本身（cleanPath 把 "/" 映射成它）：干净检出（dist 里只有
	// .gitkeep）时要用占位页给出「先 make build」的提示，而不是 404。
	if path.Ext(name) != "" && name != "index.html" {
		http.NotFound(w, r)
		return
	}

	index, ok := h.open("index.html")
	if !ok {
		h.servePlaceholder(w, r)
		return
	}
	defer func() {
		if cerr := index.Close(); cerr != nil {
			slog.Warn("关闭 index.html 失败", "err", cerr)
		}
	}()
	h.serveFile(w, r, "index.html", index)
}

// placeholderHTML 是前端产物缺失时的内置占位页。
//
// 干净检出（dist 里只有 .gitkeep）直接用 go run 启动时，给出可操作提示比 500 更有用；
// 真实产物由 make build / make web-embed 内嵌后，这里永远不会被用到。
const placeholderHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<title>LeadStack</title></head><body>
<p>前端产物尚未构建：请运行 <code>make build</code>（开发调试可用 <code>make dev</code>）。</p>
<p>Web UI bundle is missing: run <code>make build</code> first.</p>
</body></html>
`

// servePlaceholder 返回内置占位页（200 + no-store），仅在前端产物缺失时触发。
func (h *spaHandler) servePlaceholder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.WriteString(w, placeholderHTML); err != nil {
		slog.Warn("写入占位页失败", "err", err)
	}
}

// open 尝试打开静态文件；目录或不存在都返回 false。
func (h *spaHandler) open(name string) (fs.File, bool) {
	if name == "" || !fs.ValidPath(name) {
		return nil, false
	}
	f, err := h.root.Open(name)
	if err != nil {
		return nil, false
	}
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		if cerr := f.Close(); cerr != nil {
			slog.Warn("关闭静态文件失败", "name", name, "err", cerr)
		}
		return nil, false
	}
	return f, true
}

// serveFile 输出文件内容。
//
// 命中预压缩表且客户端接受 gzip 时直接写出压缩字节（Content-Length 为压缩后长度）；
// 否则交回 http.ServeContent 原样输出，保留 Range / If-Modified-Since 语义。
// 两条分支都会设置 Vary: Accept-Encoding——同一 URL 的响应体随该请求头变化，
// 缓存键必须带上它，否则会互相污染。
func (h *spaHandler) serveFile(w http.ResponseWriter, r *http.Request, name string, f fs.File) {
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "读取静态文件失败", http.StatusInternalServerError)
		return
	}
	setCacheControl(w, name)

	gz, hasGzip := h.precompressed[name]
	if hasGzip {
		w.Header().Set("Vary", "Accept-Encoding")
	}

	// Range 请求不走压缩分支：压缩后的字节偏移与原文件不再对应，
	// 用整包压缩响应去满足 Range 只会给客户端一段错误数据。
	if hasGzip && r.Header.Get("Range") == "" && acceptsGzip(r.Header.Get("Accept-Encoding")) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", contentTypeOf(name))
		w.Header().Set("Content-Length", strconv.Itoa(len(gz)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return // HEAD 只回响应头，不写 body
		}
		if _, err := w.Write(gz); err != nil {
			slog.Warn("写出压缩静态文件失败", "name", name, "err", err)
		}
		return
	}

	seeker, ok := f.(io.ReadSeeker)
	if !ok {
		// 理论上 embed.FS 一定实现 io.ReadSeeker；兜底直接整块写出。
		w.Header().Set("Content-Type", contentTypeOf(name))
		if r.Method == http.MethodHead {
			return
		}
		if _, err := io.Copy(w, f); err != nil {
			slog.Warn("写出静态文件失败", "name", name, "err", err)
		}
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), seeker)
}

// setCacheControl 设定缓存策略：
//   - index.html 是入口，必须每次协商（no-cache），否则发版后用户会拿着旧 HTML
//     去引旧 bundle；
//   - assets/ 下的文件名带 Vite 内容哈希，内容变则 URL 变，可以放心 immutable
//     长缓存一年；
//   - 其余（favicon、install.sh 等）沿用 1 小时短缓存。
func setCacheControl(w http.ResponseWriter, name string) {
	switch {
	case name == "index.html":
		w.Header().Set("Cache-Control", "no-cache")
	case strings.HasPrefix(name, "assets/"):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	default:
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
}

// acceptsGzip 判断 Accept-Encoding 是否接受 gzip。
// 支持逗号分隔的多种编码与 q 值；gzip;q=0 视为显式拒收。
// 通配符 "*" 按保守策略不视为接受 gzip：宁可少压一次，也不给不支持的客户端发压缩体。
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		enc := strings.ToLower(strings.TrimSpace(part))
		if enc != "gzip" && !strings.HasPrefix(enc, "gzip;") {
			continue
		}
		// 只消掉空白，便于识别 "gzip; q=0" 这类显式拒收。
		if strings.HasSuffix(strings.ReplaceAll(enc, " ", ""), "q=0") {
			return false
		}
		return true
	}
	return false
}

// cleanPath 把 URL 路径规整成 fs.ValidPath 可接受的相对路径。
func cleanPath(urlPath string) string {
	p := path.Clean("/" + strings.TrimSpace(urlPath))
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." {
		return "index.html"
	}
	return p
}

// contentTypeOf 按扩展名推断 Content-Type（压缩分支与兜底分支共用；
// 走 http.ServeContent 的分支由它自己嗅探，不经过这里）。
func contentTypeOf(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}
