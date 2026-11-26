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
	apiBases       []string
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
	CreatedAt   string `json:"created_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	// SourceAPI / SourceIsGitee 是本次响应实际来源，用于生成备用镜像地址（不参与 JSON）。
	SourceAPI     string `json:"-"`
	SourceIsGitee bool   `json:"-"`
	Assets        []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

// newUpdateChecker 构造检查器；fallbacks 是主源失败后依次尝试的备用 API 基址。
//
// 默认部署把 Gitee 作为 GitHub 的备用源（config.DefaultUpdateAPIBaseGitee）：
// GitHub 不可达或返回非 200 时自动切换，成功结果照常缓存；全部失败才回错误。
func newUpdateChecker(repo, apiBase, currentVersion string, interval time.Duration, logger loggerLike, fallbacks ...string) *updateChecker {
	bases := make([]string, 0, 1+len(fallbacks))
	add := func(base string) {
		base = strings.TrimSuffix(strings.TrimSpace(base), "/")
		if base == "" {
			return
		}
		for _, existing := range bases {
			if existing == base {
				return
			}
		}
		bases = append(bases, base)
	}
	add(apiBase)
	for _, base := range fallbacks {
		add(base)
	}
	return &updateChecker{
		repo: repo, apiBases: bases,
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
			FallbackURL: alternateAssetURL(c.repo, release.SourceIsGitee, release.TagName, a.Name),
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
			FallbackURL: alternateAssetURL(c.repo, release.SourceIsGitee, release.TagName, a.Name),
		})
	}
	if len(agentAssets) > 0 {
		// 同一次响应内只下载一次 checksums.txt；失败则 sha256 留空但仍返回资产。
		if content := c.fetchChecksums(ctx, agentAssets[0].URL, agentAssets[0].FallbackURL); content != "" {
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

// fetchLatestRelease 依次请求主源与备用源，返回第一个成功结果。
//
// 兼容 GitHub 与 Gitee 两套 API：两者都返回 tag_name/name/body/assets，
// 但 Gitee 没有 html_url/published_at/size，字段缺失时在这里归一化。
func (c *updateChecker) fetchLatestRelease(ctx context.Context) (*githubRelease, error) {
	errs := make([]string, 0, len(c.apiBases))
	for _, base := range c.apiBases {
		release, err := c.fetchReleaseFrom(ctx, base)
		if err == nil {
			return release, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", base, err))
		if len(c.apiBases) > 1 {
			c.logger.Warn("更新源不可用，尝试下一个", "api", base, "err", err)
		}
	}
	return nil, fmt.Errorf("所有更新源均不可用（%s）", strings.Join(errs, "；"))
}

// fetchReleaseFrom 请求单个源的 releases/latest 并做跨源字段归一化。
func (c *updateChecker) fetchReleaseFrom(ctx context.Context, apiBase string) (*githubRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", apiBase, c.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "leadstack-updater")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 %s 失败: %w", apiBase, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("API 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return nil, fmt.Errorf("解析更新响应失败: %w", err)
	}
	if release.Draft || release.Prerelease || strings.TrimSpace(release.TagName) == "" {
		return nil, fmt.Errorf("最新发行版不可用（draft/prerelease/无标签）")
	}
	// Gitee API 不返回 html_url / published_at，按响应形状与 API 源判定；
	// 测试用本地 mock 时 apiBase 不含 gitee.com，因此同时看 created_at。
	isGitee := strings.Contains(apiBase, "gitee.com") ||
		(strings.TrimSpace(release.HTMLURL) == "" && strings.TrimSpace(release.CreatedAt) != "")
	release.SourceAPI = apiBase
	release.SourceIsGitee = isGitee
	if strings.TrimSpace(release.HTMLURL) == "" {
		release.HTMLURL = releasePageURL(isGitee, c.repo, release.TagName)
	}
	if strings.TrimSpace(release.PublishedAt) == "" {
		release.PublishedAt = release.CreatedAt
	}
	return &release, nil
}

// alternateAssetURL 返回同名资产在另一个官方镜像源上的下载地址。
//
// 只对官方仓库启用：自建镜像 fork 不一定同步到 Gitee，生成错误地址只会掩盖真实错误。
func alternateAssetURL(repo string, isGitee bool, tag, name string) string {
	if repo != "maliang/leadstack" || strings.TrimSpace(tag) == "" || strings.TrimSpace(name) == "" {
		return ""
	}
	if isGitee {
		return fmt.Sprintf("https://github.com/maliang/leadstack/releases/download/%s/%s", tag, name)
	}
	return fmt.Sprintf("https://gitee.com/maliang47/leadstack/releases/download/%s/%s", tag, name)
}

// releasePageURL 按 API 源推导发行版网页地址（Gitee 与 GitHub 路径不同）。
func releasePageURL(isGitee bool, repo, tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return ""
	}
	if isGitee {
		return fmt.Sprintf("https://gitee.com/%s/releases/tag/%s", repo, tag)
	}
	return fmt.Sprintf("https://github.com/%s/releases/tag/%s", repo, tag)
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

// fetchChecksums 下载与 assetURL 同目录的 checksums.txt；失败后尝试镜像源。
//
// 只取主源 checksums 会让「GitHub API 正常但资产下载很慢/不通」时 agent_assets 的
// sha256 留下空值，自动升级于是静默跳过；这里与升级包下载保持同一套 GitHub↔Gitee 回退。
// 带独立超时，且对同一 URL 做一次内存缓存：检查更新是周期任务，
// 不能每次刷新都重新拉一遍 1MB 的 checksums.txt。
func (c *updateChecker) fetchChecksums(ctx context.Context, assetURL, fallbackURL string) string {
	urls := make([]string, 0, 2)
	for _, raw := range []string{assetURL, fallbackURL} {
		url := checksumsURLFor(UpdateAsset{URL: raw})
		if url == "" {
			continue
		}
		duplicate := false
		for _, existing := range urls {
			if existing == url {
				duplicate = true
				break
			}
		}
		if !duplicate {
			urls = append(urls, url)
		}
	}
	var lastErr string
	for index, url := range urls {
		c.mu.Lock()
		if c.checksumsURL == url && c.checksumsBody != "" {
			body := c.checksumsBody
			c.mu.Unlock()
			return body
		}
		c.mu.Unlock()

		reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			c.logger.Debug("构造 checksums.txt 请求失败", "err", err)
			lastErr = err.Error()
			continue
		}
		req.Header.Set("User-Agent", "leadstack-updater")
		resp, err := c.client.Do(req)
		if err != nil {
			cancel()
			lastErr = err.Error()
			c.logger.Warn("下载 checksums.txt 失败，尝试下一个源", "url", url, "err", err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Sprintf("HTTP %d", resp.StatusCode)
			_ = resp.Body.Close()
			cancel()
			c.logger.Warn("下载 checksums.txt 失败，尝试下一个源", "url", url, "status", resp.StatusCode)
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		cancel()
		if readErr != nil {
			lastErr = readErr.Error()
			c.logger.Warn("读取 checksums.txt 失败，尝试下一个源", "url", url, "err", readErr)
			continue
		}
		body := string(raw)
		c.mu.Lock()
		c.checksumsURL, c.checksumsBody = url, body
		c.mu.Unlock()
		if index > 0 {
			c.logger.Info("checksums.txt 已切换备用源", "url", url)
		}
		return body
	}
	if len(urls) == 0 {
		return ""
	}
	c.logger.Warn("checksums.txt 所有源均失败（agent_assets 的 sha256 将为空）", "err", lastErr)
	return ""
}
