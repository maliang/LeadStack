// Package webui 负责把前端构建产物嵌入二进制，并提供 SPA 回退处理器。
//
// dist 目录是**生成物**：由 Makefile 把 web/dist 复制到 internal/webui/dist 后再编译。
// 仓库里只保留 internal/webui/dist/.gitkeep 占位，保证干净检出时 go build / go vet 依然可用
// （//go:embed all:dist 要求目录里至少有一个文件；真实产物不入库，避免构建后工作区变脏）。
package webui

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// distRoot 是嵌入文件系统里的前端产物目录名。
const distRoot = "dist"

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
	return &spaHandler{root: root}
}

type spaHandler struct {
	root fs.FS
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
	if path.Ext(name) != "" {
		http.NotFound(w, r)
		return
	}

	index, ok := h.open("index.html")
	if !ok {
		http.Error(w, "前端产物缺失：请先运行 make build（或 make web-embed）生成 UI", http.StatusInternalServerError)
		return
	}
	defer func() {
		if cerr := index.Close(); cerr != nil {
			slog.Warn("关闭 index.html 失败", "err", cerr)
		}
	}()
	h.serveFile(w, r, "index.html", index)
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

// serveFile 输出文件内容，index.html 不缓存，其余静态资源短缓存。
func (h *spaHandler) serveFile(w http.ResponseWriter, r *http.Request, name string, f fs.File) {
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "读取静态文件失败", http.StatusInternalServerError)
		return
	}
	if name == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}

	seeker, ok := f.(io.ReadSeeker)
	if !ok {
		// 理论上 embed.FS 一定实现 io.ReadSeeker；兜底直接整块写出。
		w.Header().Set("Content-Type", contentTypeOf(name))
		if _, err := io.Copy(w, f); err != nil {
			slog.Warn("写出静态文件失败", "name", name, "err", err)
		}
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), seeker)
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

// contentTypeOf 按扩展名推断 Content-Type（仅在兜底分支使用）。
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
