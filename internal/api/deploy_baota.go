package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/maliang/leadstack/internal/credential"
	"github.com/maliang/leadstack/internal/panel/baota"
	"github.com/maliang/leadstack/internal/store"
)

// PanelClient 是宝塔面板操作的最小集合（便于用替身做单测）。
type PanelClient interface {
	Ping(ctx context.Context) error
	EnsureSite(ctx context.Context, req baota.AddSiteRequest) (*baota.Site, bool, error)
	UploadFile(ctx context.Context, dir, filename string, content []byte) error
	Unzip(ctx context.Context, zipPath, destDir string) error
	ZipDir(ctx context.Context, dir, zipPath string) error
}

// PanelClientFactory 按配置构造面板客户端（测试可注入替身）。
type PanelClientFactory func(cfg baota.Config) (PanelClient, error)

// 宝塔面板部署的常量。
const (
	// baotaWWWRoot 是宝塔面板的默认站点根目录。
	baotaWWWRoot = "/www/wwwroot"
	// baotaMaxArtifactBytes 是经面板 API 上传的制品上限（SaveFileBody 走 base64，过大会拖垮面板）。
	baotaMaxArtifactBytes = 16 << 20
	// baotaHealthTimeout 是健康检查总超时。
	baotaHealthTimeout = 30 * time.Second
)

// defaultPanelFactory 用真实宝塔客户端。
func defaultPanelFactory(cfg baota.Config) (PanelClient, error) {
	c, err := baota.New(cfg)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// runOneBaota 通过宝塔面板 API 完成一次部署。
//
// 与 builtin（SSH/agent 执行 shell 计划）并列的第二条执行通道，流程：
//
//	连接校验 → 确保站点存在（已有站点绝不改写）→ 读取并校验制品 →
//	备份站点目录（best-effort）→ 上传 zip → 解压覆盖 →（可选）健康检查 → 汇总状态
//
// 任一步失败都会尽量把站点目录恢复成备份版本，并把失败步骤写进 release_run。
func (o *deployOrchestrator) runOneBaota(ctx context.Context, release *store.Release, app *store.App,
	artifact *store.Artifact, run store.ReleaseRun, logf func(stream, chunk string)) {

	logf("system", "=== 宝塔面板部署通道 ===\n")
	if o.cipher == nil {
		o.finishRun(ctx, run, store.ReleaseStatusFailed, -1, "宝塔面板部署需要启用凭证加密（cipher 未初始化）", nil, logf)
		return
	}
	srv, err := o.st.GetServer(ctx, run.ServerID)
	if err != nil {
		o.finishRun(ctx, run, store.ReleaseStatusFailed, -1, "读取服务器失败: "+err.Error(), nil, logf)
		return
	}
	if srv.PanelCredentialID == nil || strings.TrimSpace(*srv.PanelCredentialID) == "" {
		o.finishRun(ctx, run, store.ReleaseStatusFailed, -1,
			"该服务器未绑定宝塔面板凭据（应用部署方式为 baota）", nil, logf)
		return
	}
	client, err := o.panelClientFor(ctx, *srv.PanelCredentialID)
	if err != nil {
		o.finishRun(ctx, run, store.ReleaseStatusFailed, -1, err.Error(), nil, logf)
		return
	}

	siteName := strings.TrimSpace(derefStr(app.SiteName))
	if siteName == "" {
		siteName = app.Slug
	}
	sitePath := path.Join(baotaWWWRoot, siteName)
	port := 80
	if app.SitePort != nil && *app.SitePort > 0 {
		port = *app.SitePort
	}
	siteKind := strings.ToLower(strings.TrimSpace(derefStr(app.SiteType)))
	if siteKind == "" {
		siteKind = "static"
	}
	logf("system", fmt.Sprintf("面板站点：%s（%s，端口 %d，类型 %s）\n", siteName, sitePath, port, siteKind))

	// 1) 连接校验（地址/密钥/白名单）。
	logf("system", "--- [1/6] 校验面板连接 ---\n")
	if err := client.Ping(ctx); err != nil {
		o.finishRun(ctx, run, store.ReleaseStatusFailed, -1, "面板连接失败: "+err.Error(), strPtrOrNil("panel.ping"), logf)
		return
	}

	// 2) 确保站点存在；已存在的站点只复用，不改写其目录/端口。
	logf("system", "--- [2/6] 确保站点存在 ---\n")
	site, created, err := client.EnsureSite(ctx, baota.AddSiteRequest{
		Name: siteName, Path: sitePath, Port: port, Kind: siteKind, Note: "LeadStack 自动部署",
	})
	if err != nil {
		o.finishRun(ctx, run, store.ReleaseStatusFailed, -1, "创建/查询站点失败: "+err.Error(), strPtrOrNil("panel.site"), logf)
		return
	}
	if created {
		logf("system", "已新建站点 "+site.Name+"\n")
	} else {
		logf("system", "复用已有站点 "+site.Name+"（未改动其配置）\n")
		if p := strings.TrimSpace(site.Path); p != "" && p != sitePath {
			sitePath = p // 以面板上已有的目录为准
			logf("system", "以面板记录为准，部署目录改为 "+sitePath+"\n")
		}
	}

	// 3) 读取制品并校验（宝塔通道要求 zip）。
	logf("system", "--- [3/6] 读取并校验制品 ---\n")
	data, err := o.loadArtifactForPanel(ctx, artifact)
	if err != nil {
		o.finishRun(ctx, run, store.ReleaseStatusFailed, -1, err.Error(), strPtrOrNil("artifact"), logf)
		return
	}
	if len(data) > baotaMaxArtifactBytes {
		o.finishRun(ctx, run, store.ReleaseStatusFailed, -1, fmt.Sprintf(
			"制品 %d 字节超过宝塔 API 上传上限 %d 字节；请改用 builtin（SSH/agent）通道或拆分制品",
			len(data), baotaMaxArtifactBytes), strPtrOrNil("artifact"), logf)
		return
	}
	logf("system", fmt.Sprintf("制品 %d 字节，sha256 校验通过（如已配置）\n", len(data)))

	// 4) 备份当前站点目录（best-effort，失败只告警）。
	backupPath := fmt.Sprintf("%s.backup-%d.zip", sitePath, time.Now().Unix())
	logf("system", "--- [4/6] 备份当前站点目录 ---\n")
	backupOK := true
	if err := client.ZipDir(ctx, sitePath, backupPath); err != nil {
		backupOK = false
		logf("system", "备份失败（将无法自动回滚）: "+err.Error()+"\n")
	} else {
		logf("system", "已备份到 "+backupPath+"\n")
	}

	// 5) 上传 zip 并解压覆盖。
	logf("system", "--- [5/6] 上传并解压制品 ---\n")
	remoteZip := fmt.Sprintf("%s-release-%s.zip", siteName, release.ID)
	if err := client.UploadFile(ctx, baotaWWWRoot, remoteZip, data); err != nil {
		o.finishRun(ctx, run, store.ReleaseStatusFailed, -1, "上传制品失败: "+err.Error(), strPtrOrNil("upload"), logf)
		return
	}
	remoteZipPath := path.Join(baotaWWWRoot, remoteZip)
	if err := client.Unzip(ctx, remoteZipPath, sitePath); err != nil {
		logf("system", "解压失败，尝试回滚到备份版本\n")
		if backupOK {
			if rbErr := client.Unzip(ctx, backupPath, sitePath); rbErr != nil {
				logf("system", "回滚失败: "+rbErr.Error()+"\n")
			} else {
				logf("system", "已回滚到备份版本\n")
			}
		}
		o.finishRun(ctx, run, store.ReleaseStatusFailed, -1, "解压站点失败: "+err.Error(), strPtrOrNil("unzip"), logf)
		return
	}

	// 6) 健康检查（可选）：端口上取站点根/指定路径。
	if hp := strings.TrimSpace(derefStr(app.HealthcheckPath)); hp != "" {
		logf("system", "--- [6/6] 健康检查 "+hp+" ---\n")
		if err := waitHTTPHealthy(ctx, fmt.Sprintf("http://%s:%d%s", srv.Host, port, normalizeHealthPath(hp))); err != nil {
			logf("system", "健康检查失败: "+err.Error()+"\n")
			if backupOK {
				if rbErr := client.Unzip(ctx, backupPath, sitePath); rbErr != nil {
					logf("system", "回滚失败: "+rbErr.Error()+"\n")
				} else {
					logf("system", "已回滚到备份版本\n")
				}
			}
			o.finishRun(ctx, run, store.ReleaseStatusFailed, -1, "部署后健康检查失败: "+err.Error(), strPtrOrNil("healthcheck"), logf)
			return
		}
		logf("system", "健康检查通过\n")
	}

	msg := fmt.Sprintf("宝塔面板部署成功：站点 %s（%s）", siteName, sitePath)
	o.finishRun(ctx, run, store.ReleaseStatusSuccess, 0, msg, nil, logf)
}

// panelClientFor 读取服务器绑定的面板凭据并构造客户端。
func (o *deployOrchestrator) panelClientFor(ctx context.Context, credentialID string) (PanelClient, error) {
	cred, err := o.st.GetCredential(ctx, credentialID)
	if err != nil {
		return nil, fmt.Errorf("读取面板凭据失败: %w", err)
	}
	if cred.Kind != string(credential.KindBaota) {
		return nil, fmt.Errorf("凭据 %s 不是宝塔面板类型（实际 %s）", cred.Name, cred.Kind)
	}
	plain, err := o.cipher.Decrypt(cred.SecretEnc)
	if err != nil {
		return nil, fmt.Errorf("解密面板凭据失败: %w", err)
	}
	secret, err := credential.Unmarshal(plain)
	if err != nil {
		return nil, fmt.Errorf("解析面板凭据失败: %w", err)
	}
	if strings.TrimSpace(secret.PanelURL) == "" || strings.TrimSpace(secret.APIKey) == "" {
		return nil, errors.New("面板凭据缺少 panel_url 或 api_key")
	}
	factory := o.panelFactory
	if factory == nil {
		factory = defaultPanelFactory
	}
	return factory(baota.Config{
		BaseURL:            secret.PanelURL,
		APIKey:             secret.APIKey,
		InsecureSkipVerify: secret.InsecureSkipVerify,
	})
}

// loadArtifactForPanel 读取制品内容（upload 走控制端存储，url 走下载），并做 sha256/zip 校验。
func (o *deployOrchestrator) loadArtifactForPanel(ctx context.Context, artifact *store.Artifact) ([]byte, error) {
	if artifact == nil {
		return nil, errors.New("宝塔面板部署需要制品（zip）")
	}
	var data []byte
	switch artifact.Kind {
	case store.ArtifactKindUpload:
		if o.artifactsDir == "" {
			return nil, errors.New("制品存储目录未配置，无法读取上传制品")
		}
		raw, err := os.ReadFile(filepath.Join(o.artifactsDir, artifactFileName(artifact.ID)))
		if err != nil {
			return nil, fmt.Errorf("读取制品文件失败: %w", err)
		}
		data = raw
	case store.ArtifactKindURL:
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.Location, nil)
		if err != nil {
			return nil, fmt.Errorf("构造制品下载请求失败: %w", err)
		}
		client := &http.Client{Timeout: 60 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("下载制品失败: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("下载制品失败：HTTP %d", resp.StatusCode)
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, baotaMaxArtifactBytes+1))
		if err != nil {
			return nil, fmt.Errorf("读取制品响应失败: %w", err)
		}
		data = raw
	default:
		return nil, fmt.Errorf("宝塔面板部署不支持制品类型 %q（请使用 zip 上传制品）", artifact.Kind)
	}
	if artifact.SHA256 != nil && strings.TrimSpace(*artifact.SHA256) != "" {
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, strings.TrimSpace(*artifact.SHA256)) {
			return nil, fmt.Errorf("制品 sha256 校验失败：期望 %s，实际 %s", *artifact.SHA256, got)
		}
	}
	if len(data) < 4 || !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return nil, errors.New("宝塔面板部署要求 zip 制品（解压由面板完成），当前制品不是 zip")
	}
	return data, nil
}

// normalizeHealthPath 保证路径以 / 开头。
func normalizeHealthPath(p string) string {
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

// waitHTTPHealthy 轮询目标地址直到 2xx/3xx 或超时。
func waitHTTPHealthy(ctx context.Context, url string) error {
	deadline := time.Now().Add(baotaHealthTimeout)
	client := &http.Client{Timeout: 5 * time.Second}
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode < 400 {
					return nil
				}
				lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			} else {
				lastErr = err
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			if lastErr == nil {
				lastErr = errors.New("超时")
			}
			return lastErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// derefStr 安全解引用字符串指针。
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// appSiteConfig 是应用部署方式与面板站点配置的规整结果。
type appSiteConfig struct {
	Provider        string
	SiteName        *string
	SitePort        *int
	SiteType        *string
	HealthcheckPath *string
}

// siteNamePattern 约束面板站点名（同时作为默认域名占位）。
var siteNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// normalizeAppSite 校验并规整「创建应用」时的 provider 与站点配置。
func (a *API) normalizeAppSite(provider, siteName string, sitePort *int, siteType, healthcheckPath string) (appSiteConfig, error) {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" {
		p = store.AppProviderBuiltin
	}
	cfg := appSiteConfig{Provider: p}
	switch p {
	case store.AppProviderBuiltin:
		return cfg, nil
	case store.AppProviderBaota:
	default:
		return cfg, fmt.Errorf("provider 只能是 builtin 或 baota")
	}
	name := strings.TrimSpace(siteName)
	if name != "" {
		if !siteNamePattern.MatchString(name) {
			return cfg, fmt.Errorf("site_name 只能是 [A-Za-z0-9._-]（≤100 字符）")
		}
		cfg.SiteName = &name
	}
	if sitePort != nil {
		if *sitePort <= 0 || *sitePort > 65535 {
			return cfg, fmt.Errorf("site_port 必须在 1-65535 之间")
		}
		port := *sitePort
		cfg.SitePort = &port
	}
	st := strings.ToLower(strings.TrimSpace(siteType))
	if st == "" {
		st = "static"
	}
	if st != "static" && st != "php" {
		return cfg, fmt.Errorf("site_type 目前只支持 static 或 php（node 站点的进程管理不在 v1 范围）")
	}
	cfg.SiteType = &st
	hp := strings.TrimSpace(healthcheckPath)
	if hp != "" {
		if !strings.HasPrefix(hp, "/") {
			return cfg, fmt.Errorf("healthcheck_path 必须以 / 开头")
		}
		if strings.ContainsAny(hp, " \t\r\n") {
			return cfg, fmt.Errorf("healthcheck_path 不能包含空白字符")
		}
		cfg.HealthcheckPath = &hp
	}
	return cfg, nil
}

// normalizeAppSitePatch 校验并规整「修改应用」时的 provider 与站点配置（nil 表示不改）。
func (a *API) normalizeAppSitePatch(provider *string, siteName *string, sitePort *int, siteType *string, healthcheckPath *string) (appSiteConfig, error) {
	cfg := appSiteConfig{}
	if provider != nil {
		p := strings.ToLower(strings.TrimSpace(*provider))
		if p != store.AppProviderBuiltin && p != store.AppProviderBaota {
			return cfg, fmt.Errorf("provider 只能是 builtin 或 baota")
		}
		cfg.Provider = p
	}
	if siteName != nil {
		name := strings.TrimSpace(*siteName)
		if name != "" && !siteNamePattern.MatchString(name) {
			return cfg, fmt.Errorf("site_name 只能是 [A-Za-z0-9._-]（≤100 字符）")
		}
		cfg.SiteName = &name
	}
	if sitePort != nil {
		if *sitePort < 0 || *sitePort > 65535 {
			return cfg, fmt.Errorf("site_port 必须在 0-65535 之间（0 表示清除）")
		}
		port := *sitePort
		cfg.SitePort = &port
	}
	if siteType != nil {
		st := strings.ToLower(strings.TrimSpace(*siteType))
		if st != "" && st != "static" && st != "php" {
			return cfg, fmt.Errorf("site_type 目前只支持 static 或 php")
		}
		cfg.SiteType = &st
	}
	if healthcheckPath != nil {
		hp := strings.TrimSpace(*healthcheckPath)
		if hp != "" && (!strings.HasPrefix(hp, "/") || strings.ContainsAny(hp, " \t\r\n")) {
			return cfg, fmt.Errorf("healthcheck_path 必须以 / 开头且不能包含空白字符")
		}
		cfg.HealthcheckPath = &hp
	}
	return cfg, nil
}
