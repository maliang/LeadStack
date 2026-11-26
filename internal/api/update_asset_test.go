package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

// realReleaseAssets 是 v0.1.1 真实发行版返回的资产名与顺序（取自 GitHub API）。
//
// 注意：GitHub 按名字排序返回，`leadstack-agent_*` 排在 `leadstack_*` 之前——
// 这正是「按 os/arch 取第一个匹配」会误选 agent 包的原因（真实环境必然复现）。
var realReleaseAssets = []string{
	"checksums.txt",
	"leadstack-agent_0.1.1_darwin_amd64.tar.gz",
	"leadstack-agent_0.1.1_darwin_arm64.tar.gz",
	"leadstack-agent_0.1.1_linux_amd64.tar.gz",
	"leadstack-agent_0.1.1_linux_arm64.tar.gz",
	"leadstack-agent_0.1.1_windows_amd64.zip",
	"leadstack_0.1.1_darwin_amd64.tar.gz",
	"leadstack_0.1.1_darwin_arm64.tar.gz",
	"leadstack_0.1.1_linux_amd64.tar.gz",
	"leadstack_0.1.1_linux_arm64.tar.gz",
	"leadstack_0.1.1_windows_amd64.zip",
}

// TestClassifyAssetSkipsAgentArchives 校验只有控制端产物被当作升级源。
func TestClassifyAssetSkipsAgentArchives(t *testing.T) {
	kept := make([]string, 0, len(realReleaseAssets))
	for _, name := range realReleaseAssets {
		_, _, ok := classifyAsset(name)
		if ok {
			kept = append(kept, name)
		}
	}
	if len(kept) != 5 {
		t.Fatalf("应只保留 5 个控制端产物，实际 %d：%v", len(kept), kept)
	}
	for _, name := range kept {
		if !strings.HasPrefix(name, "leadstack_") {
			t.Fatalf("被保留的资产不是控制端产物：%s", name)
		}
	}
	if len(kept) > 0 {
		if osName, arch, _ := classifyAsset("leadstack-agent_0.1.1_linux_amd64.tar.gz"); osName != "" || arch != "" {
			t.Fatalf("agent 包不应被识别为升级资产（得到 %s/%s）", osName, arch)
		}
	}
}

// TestPickAssetPrefersControlPlane 校验为当前平台挑出的是控制端包（而非 agent 包）。
func TestPickAssetPrefersControlPlane(t *testing.T) {
	info := UpdateInfo{Latest: "v0.1.1", Assets: assetsFromNames(realReleaseAssets)}
	asset, err := (&API{}).pickAsset(info, "v0.1.1")
	if err != nil {
		t.Fatalf("挑选产物失败: %v", err)
	}
	wantPrefix := "leadstack_0.1.1_" + runtime.GOOS + "_" + runtime.GOARCH
	if !strings.HasPrefix(asset.Name, wantPrefix) {
		t.Fatalf("应挑控制端包 %s*，实际挑到 %s", wantPrefix, asset.Name)
	}
	if strings.Contains(asset.Name, "leadstack-agent_") {
		t.Fatalf("绝不能挑 agent 包：%s", asset.Name)
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(asset.Name, ".zip") {
		t.Fatalf("Windows 应挑 zip：%s", asset.Name)
	}
	if runtime.GOOS != "windows" && !strings.HasSuffix(asset.Name, ".tar.gz") {
		t.Fatalf("类 Unix 应挑 tar.gz：%s", asset.Name)
	}
}

// assetsFromNames 用 classifyAsset 把资产名列表转成 UpdateInfo.Assets（与 refresh 同口径）。
func assetsFromNames(names []string) []UpdateAsset {
	out := make([]UpdateAsset, 0, len(names))
	for _, name := range names {
		if osName, arch, ok := classifyAsset(name); ok {
			out = append(out, UpdateAsset{OS: osName, Arch: arch, Name: name, URL: "https://example.invalid/" + name})
		}
	}
	return out
}

// TestRefreshSplitsAgentAssets 校验 agent_assets 与控制端 assets 互斥且带 sha256（SPEC A29 / R15）。
func TestRefreshSplitsAgentAssets(t *testing.T) {
	var baseURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/maliang/leadstack/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		assets := make([]map[string]any, 0, len(realReleaseAssets))
		for _, name := range realReleaseAssets {
			assets = append(assets, map[string]any{
				"name":                 name,
				"browser_download_url": baseURL + "/download/v0.1.1/" + name,
				"size":                 2048,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":     "v0.1.1",
			"html_url":     "https://example.invalid/release",
			"published_at": time.Now().UTC().Format(time.RFC3339),
			"assets":       assets,
		})
	})
	mux.HandleFunc("/download/v0.1.1/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		for _, name := range realReleaseAssets {
			if name == "checksums.txt" {
				continue
			}
			fmt.Fprintf(&b, "%s  %s\n", strings.Repeat("a", 64), name)
		}
		_, _ = io.WriteString(w, b.String())
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	baseURL = srv.URL

	checker := newUpdateChecker("maliang/leadstack", srv.URL, "v0.1.0", time.Hour, noopLogger{})
	info := checker.Get(context.Background(), true)
	if info.Error != "" {
		t.Fatalf("检查失败: %s", info.Error)
	}

	// assets 只含控制端包
	if len(info.Assets) != 5 {
		t.Fatalf("assets 应只有 5 个控制端包，实际 %d", len(info.Assets))
	}
	for _, a := range info.Assets {
		if !strings.HasPrefix(a.Name, "leadstack_") {
			t.Fatalf("assets 混入非控制端包：%s", a.Name)
		}
	}

	// agent_assets 只含 agent 包（4 个 tar.gz），并带上 checksums.txt 里的 sha256
	if len(info.AgentAssets) != 4 {
		t.Fatalf("agent_assets 应有 4 个 tar.gz，实际 %d：%+v", len(info.AgentAssets), info.AgentAssets)
	}
	for _, a := range info.AgentAssets {
		if !strings.HasPrefix(a.Name, agentAssetPrefix) || !strings.HasSuffix(a.Name, ".tar.gz") {
			t.Fatalf("agent_assets 混入非 agent 包：%s", a.Name)
		}
		if a.SHA256 != strings.Repeat("a", 64) {
			t.Fatalf("agent 资产应带 sha256，实际 %q（%s）", a.SHA256, a.Name)
		}
		if a.URL == "" || a.OS == "" || a.Arch == "" {
			t.Fatalf("agent 资产字段缺失: %+v", a)
		}
	}
}

// TestAgentAssetsEmptyWhenChecksumsFail 校验 checksums.txt 取不到时仍返回资产、sha256 留空、不报错。
func TestAgentAssetsEmptyWhenChecksumsFail(t *testing.T) {
	var baseURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/maliang/leadstack/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":     "v0.1.1",
			"published_at": time.Now().UTC().Format(time.RFC3339),
			"assets": []map[string]any{
				{"name": "leadstack-agent_0.1.1_linux_amd64.tar.gz",
					"browser_download_url": baseURL + "/download/v0.1.1/leadstack-agent_0.1.1_linux_amd64.tar.gz"},
			},
		})
	})
	// checksums.txt 故意 404
	srv := httptest.NewServer(mux)
	defer srv.Close()
	baseURL = srv.URL

	checker := newUpdateChecker("maliang/leadstack", srv.URL, "v0.1.0", time.Hour, noopLogger{})
	info := checker.Get(context.Background(), true)
	if info.Error != "" {
		t.Fatalf("checksums 失败不应影响检查更新: %s", info.Error)
	}
	if len(info.AgentAssets) != 1 || info.AgentAssets[0].SHA256 != "" {
		t.Fatalf("应返回资产但 sha256 为空: %+v", info.AgentAssets)
	}
	if info.AgentAssets == nil {
		t.Fatal("agent_assets 必须是空数组而不是 null")
	}
}

// TestAgentAssetsEmptyArrayNotNull 校验无 agent 包时序列化为 []（前端无需判 null）。
func TestAgentAssetsEmptyArrayNotNull(t *testing.T) {
	info := UpdateInfo{AgentAssets: make([]AgentUpdateAsset, 0)}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if !strings.Contains(string(raw), `"agent_assets":[]`) {
		t.Fatalf("空 agent_assets 应序列化为 []，实际 %s", raw)
	}
	if _, _, ok := classifyAgentAsset("leadstack_0.1.1_linux_amd64.tar.gz"); ok {
		t.Fatal("控制端包不应被识别为 agent 资产")
	}
	if _, _, ok := classifyAgentAsset("leadstack-agent_0.1.1_windows_amd64.zip"); ok {
		t.Fatal("agent 的 zip 不在 agent_assets 范围内（只认 tar.gz）")
	}
}

// TestRefreshFallsBackToGitee 校验 GitHub 主源失败时自动切到 Gitee，
// 并把 Gitee 缺失的 html_url/published_at/size 归一化成可用字段。
func TestRefreshFallsBackToGitee(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "github unavailable", http.StatusBadGateway)
	}))
	defer primary.Close()

	var giteeURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/maliang/leadstack/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		assets := make([]map[string]any, 0, len(realReleaseAssets))
		for _, name := range realReleaseAssets {
			assets = append(assets, map[string]any{
				"name":                 name,
				"browser_download_url": giteeURL + "/download/v0.1.15/" + name,
				// Gitee 不返回 size
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":   "v0.1.15",
			"name":       "v0.1.15",
			"body":       "<!-- schema_changed: false -->",
			"created_at": "2026-11-23T10:14:24+08:00",
			"assets":     assets,
		})
	})
	mux.HandleFunc("/download/v0.1.15/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		for _, name := range realReleaseAssets {
			if name == "checksums.txt" {
				continue
			}
			fmt.Fprintf(&b, "%s  %s\n", strings.Repeat("b", 64), name)
		}
		_, _ = io.WriteString(w, b.String())
	})
	gitee := httptest.NewServer(mux)
	defer gitee.Close()
	giteeURL = gitee.URL

	checker := newUpdateChecker("maliang/leadstack", primary.URL, "v0.1.0", time.Hour, noopLogger{}, gitee.URL)
	info := checker.Get(context.Background(), true)
	if info.Error != "" {
		t.Fatalf("主源失败时应回退 Gitee，实际错误: %s", info.Error)
	}
	if info.Latest != "v0.1.15" || !info.HasUpdate {
		t.Fatalf("回退结果不符: %+v", info)
	}
	if info.ReleaseURL != "https://gitee.com/maliang/leadstack/releases/tag/v0.1.15" {
		t.Fatalf("Gitee 发行版地址应按 tag 推导，实际 %q", info.ReleaseURL)
	}
	if info.PublishedAt == 0 {
		t.Fatalf("Gitee created_at 应回填 published_at")
	}
	if len(info.Assets) != 5 || len(info.AgentAssets) != 4 {
		t.Fatalf("资产拆分不符: control=%d agent=%d", len(info.Assets), len(info.AgentAssets))
	}
	for _, asset := range info.Assets {
		if !strings.HasPrefix(asset.FallbackURL, "https://github.com/maliang/leadstack/releases/download/") {
			t.Fatalf("Gitee 资产应带 GitHub 备用地址，实际 %q", asset.FallbackURL)
		}
	}
	for _, asset := range info.AgentAssets {
		if asset.SHA256 != strings.Repeat("b", 64) {
			t.Fatalf("Gitee checksums 未生效: %+v", asset)
		}
	}
}

// TestDownloadReleaseAssetFallsBack 校验主地址 5xx 时自动换备用镜像，
// 并用「实际成功的 URL」取同源 checksums.txt 完成校验。
func TestDownloadReleaseAssetFallsBack(t *testing.T) {
	env := newTestEnv(t)
	payload := []byte("archive-bytes")
	sum := sha256.Sum256(payload)
	name := "leadstack_0.1.15_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "github asset unavailable", http.StatusBadGateway)
	}))
	defer primary.Close()

	var fallbackBase string
	mux := http.NewServeMux()
	mux.HandleFunc("/download/v0.1.15/"+name, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("/download/v0.1.15/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	})
	fallback := httptest.NewServer(mux)
	defer fallback.Close()
	fallbackBase = fallback.URL + "/download/v0.1.15/"

	asset := UpdateAsset{
		OS: runtime.GOOS, Arch: runtime.GOARCH, Name: name,
		URL: primary.URL + "/download/v0.1.15/" + name, FallbackURL: fallbackBase + name,
	}
	path, usedURL, err := env.api.downloadReleaseAsset(context.Background(), asset, "v0.1.15")
	if err != nil {
		t.Fatalf("主地址失败时应回退备用源: %v", err)
	}
	if usedURL != asset.FallbackURL {
		t.Fatalf("应返回实际成功的备用 URL，实际 %q", usedURL)
	}
	if err := env.api.verifyAssetChecksum(context.Background(), asset, usedURL, path); err != nil {
		t.Fatalf("备用源下载后应按同源 checksums 校验通过: %v", err)
	}
}

func TestFetchChecksumsFallsBackToMirror(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "primary checksums unavailable", http.StatusBadGateway)
	}))
	defer primary.Close()

	const want = "abcdef  leadstack-agent_1.2.3_linux_amd64.tar.gz\n"
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/checksums.txt") {
			t.Fatalf("备用源请求了意外路径: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, want)
	}))
	defer fallback.Close()

	checker := &updateChecker{client: fallback.Client(), logger: noopLogger{}}
	got := checker.fetchChecksums(
		context.Background(),
		primary.URL+"/download/v1.2.3/leadstack-agent_1.2.3_linux_amd64.tar.gz",
		fallback.URL+"/download/v1.2.3/leadstack-agent_1.2.3_linux_amd64.tar.gz",
	)
	if got != want {
		t.Fatalf("主源 checksums 失败后应使用备用源，实际 %q", got)
	}
}
