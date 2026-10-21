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
		name := strings.ToLower(a.Name)
		osName, arch := platformFromAssetName(name)
		if osName == "" {
			continue
		}
		assets = append(assets, UpdateAsset{
			OS: osName, Arch: arch, URL: a.BrowserDownloadURL, SizeBytes: a.Size, Name: a.Name,
		})
		if osName == runtime.GOOS && arch == runtime.GOARCH {
			upgradable = true
		}
	}

	info = UpdateInfo{
		Current:       c.currentVersion,
		Latest:        release.TagName,
		ReleaseURL:    release.HTMLURL,
		Notes:         truncateNotes(release.Body),
		SchemaChanged: parseSchemaChangedMarker(release.Body),
		Assets:        assets,
		Upgradable:    upgradable,
		CheckedAt:     now,
	}
	if published, perr := time.Parse(time.RFC3339, release.PublishedAt); perr == nil {
		info.PublishedAt = published.UnixMilli()
	}
	info.HasUpdate = versionIsNewer(release.TagName, c.currentVersion)

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
	writeData(w, http.StatusOK, a.updates.Get(r.Context(), false))
}

// handleCheckUpdate 处理 POST /api/v1/system/update/check（强制刷新）。
func (a *API) handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	if a.updates == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "更新检查未启用")
		return
	}
	info := a.updates.Get(r.Context(), true)
	writeData(w, http.StatusOK, info)
}
