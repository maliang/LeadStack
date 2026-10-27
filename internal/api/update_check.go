package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"
)

// updateChecker 负责「检查更新」：带缓存的 GitHub Releases 查询。
type updateChecker struct {
	repo           string
	apiBase        string
	currentVersion string
	interval       time.Duration
	client         *http.Client
	logger         loggerLike

	mu      sync.Mutex
	cached  *UpdateInfo
	lastErr string
	// checksums.txt 缓存（同一发行版周期内只下载一次）
	checksumsURL  string
	checksumsBody string
}

// loggerLike 是检查器需要的最小日志能力（避免与 *slog.Logger 耦合）。
type loggerLike interface {
	Warn(msg string, args ...any)
	Info(msg string, args ...any)
	Debug(msg string, args ...any)
}

// githubRelease 是 GitHub Releases API 的响应子集。
type githubRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

// newUpdateChecker 构造检查器。
func newUpdateChecker(repo, apiBase, currentVersion string, interval time.Duration, logger loggerLike) *updateChecker {
	return &updateChecker{
		repo: repo, apiBase: strings.TrimSuffix(apiBase, "/"),
		currentVersion: currentVersion, interval: interval,
		client: &http.Client{Timeout: 20 * time.Second},
		logger: logger,
	}
}

// Get 返回缓存的检查结果；缓存过期时刷新一次（失败保留旧结果并写 error）。
func (c *updateChecker) Get(ctx context.Context, force bool) UpdateInfo {
	c.mu.Lock()
	cached := c.cached
	fresh := cached != nil && c.interval > 0 &&
		time.Since(time.UnixMilli(cached.CheckedAt)) < c.interval
	c.mu.Unlock()
	if !force && fresh {
		return *cached
	}
	return c.refresh(ctx)
}

// refresh 真正请求 GitHub（失败不影响调用方）。
func (c *updateChecker) refresh(ctx context.Context) UpdateInfo {
	c.mu.Lock()
	previous := c.cached
	c.mu.Unlock()

	now := time.Now().UnixMilli()
	info := UpdateInfo{Current: c.currentVersion, CheckedAt: now}
	if previous != nil {
		info = *previous
		info.CheckedAt = now
		info.Error = ""
	}

	release, err := c.fetchLatestRelease(ctx)
	if err != nil {
		info.Error = err.Error()
		c.mu.Lock()
		c.lastErr = err.Error()
		// 保留上一次的成功结果（latest/has_update 等），仅更新 checked_at/error。
		if previous != nil {
			kept := *previous
			kept.CheckedAt = now
			kept.Error = err.Error()
			c.cached = &kept
			info = kept
		} else {
			info.Current = c.currentVersion
			c.cached = &info
		}
		c.mu.Unlock()
		c.logger.Warn("检查更新失败", "repo", c.repo, "err", err)
		return info
	}

	assets := make([]UpdateAsset, 0, len(release.Assets))
	upgradable := false
	for _, a := range release.Assets {
		// 只认控制端包：发行版里还有 leadstack-agent_* 与 checksums.txt，
		// 且 GitHub 按名字排序返回（leadstack-agent_ 排在 leadstack_ 前面），
		// 单看 os/arch 会误选 agent 包，解包时找不到 leadstack 二进制 → 升级必然失败。
		osName, arch, ok := classifyAsset(a.Name)
		if !ok {
			continue
		}
		assets = append(assets, UpdateAsset{
			OS: osName, Arch: arch, URL: a.BrowserDownloadURL, SizeBytes: a.Size, Name: a.Name,
		})
		if osName == runtime.GOOS && arch == runtime.GOARCH {
			upgradable = true
		}
	}

	// agent 归档单独收集：GitHub 返回顺序里 leadstack-agent_* 排在 leadstack_* 之前，
	// 所以必须按文件名精确匹配，绝不能靠 os/arch 猜（否则控制端会误选 agent 包）。
	agentAssets := make([]AgentUpdateAsset, 0, 2)
	for _, a := range release.Assets {
		osName, arch, ok := classifyAgentAsset(a.Name)
		if !ok {
			continue
		}
		agentAssets = append(agentAssets, AgentUpdateAsset{
			OS: osName, Arch: arch, URL: a.BrowserDownloadURL, SizeBytes: a.Size, Name: a.Name,
		})
	}
	if len(agentAssets) > 0 {
		// 同一次响应内只下载一次 checksums.txt；失败则 sha256 留空但仍返回资产。
		if content := c.fetchChecksums(ctx, agentAssets[0].URL); content != "" {
			for i := range agentAssets {
				agentAssets[i].SHA256 = lookupChecksum(content, agentAssets[i].Name)
			}
		}
	}

	info = UpdateInfo{
		Current:       c.currentVersion,
		Latest:        release.TagName,
		ReleaseURL:    release.HTMLURL,
		Notes:         truncateNotes(release.Body),
		SchemaChanged: parseSchemaChangedMarker(release.Body),
		Assets:        assets,
		AgentAssets:   agentAssets,
		Upgradable:    upgradable,
		CheckedAt:     now,
	}
	if published, perr := time.Parse(time.RFC3339, release.PublishedAt); perr == nil {
		info.PublishedAt = published.UnixMilli()
	}
	info.HasUpdate = versionIsNewer(release.TagName, c.currentVersion)

	// 平台能力：Windows 无法替换运行中的可执行文件，本机自升级**按设计不支持**。
	// 这里把 upgradable 置 false 并给出原因，避免「点升级 → 403」的挫败路径。
	if reason := platformUpgradeUnsupported(); reason != "" {
		info.Upgradable = false
		info.UpgradeNote = reason
	}

	c.mu.Lock()
	c.cached = &info
	c.mu.Unlock()
	c.logger.Info("检查更新完成", "current", info.Current, "latest", info.Latest, "has_update", info.HasUpdate)
	return info
}

// fetchLatestRelease 请求 GitHub releases/latest。
func (c *updateChecker) fetchLatestRelease(ctx context.Context) (*githubRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", c.apiBase, c.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "leadstack-updater")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 %s 失败: %w", c.apiBase, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GitHub API 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return nil, fmt.Errorf("解析 GitHub 响应失败: %w", err)
	}
	if release.Draft || release.Prerelease || strings.TrimSpace(release.TagName) == "" {
		return nil, fmt.Errorf("最新发行版不可用（draft/prerelease/无标签）")
	}
	return &release, nil
}

// versionIsNewer 判断 candidate 是否比 current 新（任一解析失败按 false）。
func versionIsNewer(candidate, current string) bool {
	cand, err := ParseSemver(candidate)
	if err != nil {
		return false
	}
	cur, err := ParseSemver(current)
	if err != nil {
		// 当前版本是 dev 之类：只要有合法标签就当作有更新。
		return true
	}
	return cand.Compare(cur) > 0
}

// platformFromAssetName 从产物文件名推断 os/arch（如 leadstack_linux_amd64.tar.gz）。
// classifyAsset 判断资产名是否为控制端升级包，并解析出 os/arch。
//
// 发行版同时包含控制端（leadstack_*）与节点 agent（leadstack-agent_*）两类归档，
// 只有前者可以作为控制端自升级的下载源；checksums.txt 等非归档一律跳过。
func classifyAsset(name string) (osName, arch string, ok bool) {
	base := strings.ToLower(strings.TrimSpace(name))
	if !strings.HasPrefix(base, "leadstack_") {
		return "", "", false
	}
	if !strings.HasSuffix(base, ".tar.gz") && !strings.HasSuffix(base, ".zip") {
		return "", "", false
	}
	osName, arch = platformFromAssetName(base)
	if osName == "" || arch == "" {
		return "", "", false
	}
	return osName, arch, true
}

func platformFromAssetName(name string) (osName, arch string) {
	lower := strings.ToLower(name)
	oses := []string{"linux", "darwin", "windows", "freebsd"}
	arches := []string{"amd64", "arm64", "armv7", "386"}
	for _, candidate := range oses {
		if strings.Contains(lower, candidate) {
			osName = candidate
			break
		}
	}
	for _, candidate := range arches {
		if strings.Contains(lower, candidate) {
			arch = candidate
			break
		}
	}
	if osName == "" || arch == "" {
		return "", ""
	}
	return osName, arch
}

// ------------------------------------------------ REST ------------------------------------------------

// handleGetUpdate 处理 GET /api/v1/system/update（缓存）。
func (a *API) handleGetUpdate(w http.ResponseWriter, r *http.Request) {
	if a.updates == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "更新检查未启用")
		return
	}
	writeData(w, http.StatusOK, normalizeUpdateInfo(a.updates.Get(r.Context(), false)))
}

// handleCheckUpdate 处理 POST /api/v1/system/update/check（强制刷新）。
func (a *API) handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	if a.updates == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "更新检查未启用")
		return
	}
	writeData(w, http.StatusOK, normalizeUpdateInfo(a.updates.Get(r.Context(), true)))
}

// normalizeUpdateInfo 保证对外的 assets 与 agent_assets 永远是数组。
//
// 首次检查就失败（离线/私有网络）时 refresh 只能返回零值，若直接序列化会得到
// "assets": null 与 "agent_assets": null，前端 Array.isArray 的判定就会走两条分支，
// 空数组场景很容易崩。两者是同一份响应的并列字段，形状必须一致，所以统一在此兜底。
func normalizeUpdateInfo(info UpdateInfo) UpdateInfo {
	if info.Assets == nil {
		info.Assets = make([]UpdateAsset, 0)
	}
	if info.AgentAssets == nil {
		info.AgentAssets = make([]AgentUpdateAsset, 0)
	}
	return info
}

// agentAssetPrefix 是 agent 归档的文件名前缀（与控制端 leadstack_ 前缀刻意区分）。
const agentAssetPrefix = "leadstack-agent_"

// classifyAgentAsset 从文件名解析 agent 归档，只认 leadstack-agent_<ver>_<os>_<arch>.tar.gz。
//
// 与控制端 classifyAsset 互斥：任何一方都不应匹配到对方的包。
func classifyAgentAsset(name string) (osName, arch string, ok bool) {
	if !strings.HasPrefix(name, agentAssetPrefix) || !strings.HasSuffix(name, ".tar.gz") {
		return "", "", false
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(name, agentAssetPrefix), ".tar.gz")
	parts := strings.Split(rest, "_")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// fetchChecksums 下载与 assetURL 同目录的 checksums.txt；失败返回空串（不阻断检查更新）。
//
// 带独立超时，且对同一 URL 做一次内存缓存：检查更新是周期任务，
// 不能每次刷新都重新拉一遍 1MB 的 checksums.txt。
func (c *updateChecker) fetchChecksums(ctx context.Context, assetURL string) string {
	url := checksumsURLFor(UpdateAsset{URL: assetURL})
	if url == "" {
		return ""
	}
	c.mu.Lock()
	if c.checksumsURL == url && c.checksumsBody != "" {
		body := c.checksumsBody
		c.mu.Unlock()
		return body
	}
	c.mu.Unlock()

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		c.logger.Debug("构造 checksums.txt 请求失败", "err", err)
		return ""
	}
	req.Header.Set("User-Agent", "leadstack-updater")
	resp, err := c.client.Do(req)
	if err != nil {
		c.logger.Warn("下载 checksums.txt 失败（agent_assets 的 sha256 将为空）", "url", url, "err", err)
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		c.logger.Warn("下载 checksums.txt 失败（agent_assets 的 sha256 将为空）", "url", url, "status", resp.StatusCode)
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		c.logger.Warn("读取 checksums.txt 失败", "url", url, "err", err)
		return ""
	}
	body := string(raw)
	c.mu.Lock()
	c.checksumsURL, c.checksumsBody = url, body
	c.mu.Unlock()
	return body
}
