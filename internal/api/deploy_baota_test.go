package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/maliang/leadstack/internal/credential"
	"github.com/maliang/leadstack/internal/panel/baota"
	"github.com/maliang/leadstack/internal/store"
)

// fakePanel 记录面板调用序列，并可注入失败点。
type fakePanel struct {
	mu        sync.Mutex
	calls     []string
	failUnzip bool
	unzipped  []string
	sites     map[string]*baota.Site
}

func (f *fakePanel) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakePanel) Ping(context.Context) error {
	f.record("ping")
	return nil
}

func (f *fakePanel) EnsureSite(_ context.Context, req baota.AddSiteRequest) (*baota.Site, bool, error) {
	f.record("ensure-site:" + req.Name + ":" + req.Path)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sites == nil {
		f.sites = map[string]*baota.Site{}
	}
	if site, ok := f.sites[req.Name]; ok {
		return site, false, nil
	}
	site := &baota.Site{Name: req.Name, Path: req.Path, Port: req.Port}
	f.sites[req.Name] = site
	return site, true, nil
}

func (f *fakePanel) UploadFile(_ context.Context, dir, filename string, content []byte) error {
	f.record("upload:" + dir + "/" + filename + ":" + strconv.Itoa(len(content)))
	return nil
}

func (f *fakePanel) Unzip(_ context.Context, zipPath, destDir string) error {
	f.record("unzip:" + zipPath + "->" + destDir)
	f.mu.Lock()
	f.unzipped = append(f.unzipped, zipPath)
	fail := f.failUnzip
	f.mu.Unlock()
	if fail && !strings.Contains(zipPath, ".backup-") {
		return errors.New("解压失败（测试注入）")
	}
	return nil
}

func (f *fakePanel) ZipDir(_ context.Context, dir, zipPath string) error {
	f.record("zip:" + dir + "->" + zipPath)
	return nil
}

func (f *fakePanel) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// baotaFixture 准备一次 baota 发布所需的全部数据。
type baotaFixture struct {
	env    *testEnv
	orch   *deployOrchestrator
	panel  *fakePanel
	app    *store.App
	server *store.Server
	rel    *store.Release
}

func newBaotaFixture(t *testing.T, provider string) *baotaFixture {
	t.Helper()
	env := newTestEnv(t)
	ctx := context.Background()

	// 1) 宝塔凭据（用测试主密钥加密）
	secret := credential.Secret{
		Kind: credential.KindBaota, Username: "baota",
		PanelURL: "https://panel.example.com:8888", APIKey: "test-key",
	}
	plain, err := credential.Marshal(secret)
	if err != nil {
		t.Fatalf("序列化凭据失败: %v", err)
	}
	enc, err := env.cipher.Encrypt(plain)
	if err != nil {
		t.Fatalf("加密凭据失败: %v", err)
	}
	cred := &store.Credential{
		ID: "cred-baota", Name: "面板", Kind: string(credential.KindBaota),
		Username: "baota", SecretEnc: enc, CreatedAt: store.NowMS(),
	}
	if err := env.st.CreateCredential(ctx, cred); err != nil {
		t.Fatalf("创建凭据失败: %v", err)
	}

	// 2) 服务器（绑定面板凭据）
	credID := cred.ID
	srv := &store.Server{
		ID: "srv-baota", Name: "bt-host", Host: "10.9.9.9", Port: 22, Mode: store.ModeSSH,
		Status: "online", CreatedAt: store.NowMS(), PanelCredentialID: &credID,
	}
	if err := env.st.CreateServer(ctx, srv); err != nil {
		t.Fatalf("创建服务器失败: %v", err)
	}

	// 3) 应用（provider=baota + 站点配置）
	siteName := "shop.example.com"
	sitePort := 8080
	app := &store.App{
		ID: "app-baota", Name: "商城", Slug: "shop", CreatedAt: store.NowMS(),
		Provider: provider, SiteName: &siteName, SitePort: &sitePort, SiteType: strPtrOrNil("static"),
	}
	if err := env.st.CreateApp(ctx, app); err != nil {
		t.Fatalf("创建应用失败: %v", err)
	}

	// 4) 上传型 zip 制品
	artID := "art-baota"
	zipBytes := buildTestZip(t)
	if err := os.MkdirAll(env.cfg.ArtifactsDir(), 0o750); err != nil {
		t.Fatalf("创建制品目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.cfg.ArtifactsDir(), artifactFileName(artID)), zipBytes, 0o600); err != nil {
		t.Fatalf("写入制品失败: %v", err)
	}
	sumRaw := sha256.Sum256(zipBytes)
	sum := hex.EncodeToString(sumRaw[:])
	art := &store.Artifact{
		ID: artID, Kind: store.ArtifactKindUpload, Location: artID,
		SHA256: &sum, SizeBytes: int64(len(zipBytes)), CreatedAt: store.NowMS(),
	}
	if err := env.st.CreateArtifact(ctx, art); err != nil {
		t.Fatalf("创建制品失败: %v", err)
	}

	// 5) 模板（baota 通道不使用模板内容，占位即可）
	tmpl := &store.DeployTemplate{
		ID: "tmpl-baota", Name: "空模板", Kind: store.TemplateKindBinary,
		Content: "", CreatedAt: store.NowMS(), UpdatedAt: store.NowMS(),
	}
	if err := env.st.CreateDeployTemplate(ctx, tmpl); err != nil {
		t.Fatalf("创建模板失败: %v", err)
	}

	// 6) 发布 + 一个 pending run
	rel := &store.Release{
		ID: "rel-baota", AppID: app.ID, TemplateID: tmpl.ID, Version: "1.0.0",
		ArtifactID: &artID, TemplateSnapshot: "", Status: store.ReleaseStatusPending,
		CreatedAt: store.NowMS(), Concurrency: 1,
	}
	run := store.ReleaseRun{
		ID: "run-baota", ReleaseID: rel.ID, ServerID: srv.ID,
		Status: store.ReleaseStatusPending,
	}
	if err := env.st.CreateRelease(ctx, rel, []store.ReleaseRun{run}); err != nil {
		t.Fatalf("创建发布失败: %v", err)
	}

	// 7) 编排器（注入假面板）
	panel := &fakePanel{}
	orch := newDeployOrchestrator(env.st, nil, DeployOrchestratorOptions{
		PlanRunner:   &fakePlanRunner{},
		Store:        env.st,
		Cipher:       env.cipher,
		ArtifactsDir: env.cfg.ArtifactsDir(),
		PanelFactory: func(baota.Config) (PanelClient, error) { return panel, nil },
		Logger:       env.api.logger,
	})
	if orch == nil {
		t.Fatal("构造编排器失败")
	}
	return &baotaFixture{env: env, orch: orch, panel: panel, app: app, server: srv, rel: rel}
}

// buildTestZip 造一个最小合法的 zip。
func buildTestZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("index.html")
	if err != nil {
		t.Fatalf("创建 zip 条目失败: %v", err)
	}
	if _, err := w.Write([]byte("<html>ok</html>")); err != nil {
		t.Fatalf("写入 zip 条目失败: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buf.Bytes()
}

// TestBaotaDeploySuccess 校验面板通道的调用序列与成功状态。
func TestBaotaDeploySuccess(t *testing.T) {
	f := newBaotaFixture(t, store.AppProviderBaota)
	ctx := context.Background()

	// 直接跑一次发布（runRelease 同步执行 pending run）
	f.orch.runRelease(ctx, f.rel.ID)

	run, err := f.env.st.GetReleaseRun(ctx, f.rel.ID, f.server.ID)
	if err != nil {
		t.Fatalf("读取发布任务失败: %v", err)
	}
	if run.Status != store.ReleaseStatusSuccess {
		t.Fatalf("应部署成功，实际 %s（message=%v）", run.Status, derefStr(run.Message))
	}
	calls := f.panel.snapshot()
	want := []string{"ping", "ensure-site:shop.example.com:/www/wwwroot/shop.example.com", "zip:", "upload:/www/wwwroot/shop.example.com-release-rel-baota.zip"}
	for _, wantPrefix := range want {
		found := false
		for _, call := range calls {
			if strings.HasPrefix(call, wantPrefix) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("缺少面板调用 %q，实际 %v", wantPrefix, calls)
		}
	}
	// 必须解压 zip 到站点目录
	unzipOK := false
	for _, call := range calls {
		if strings.HasPrefix(call, "unzip:") && strings.Contains(call, "->/www/wwwroot/shop.example.com") &&
			!strings.Contains(call, ".backup-") {
			unzipOK = true
		}
	}
	if !unzipOK {
		t.Fatalf("未解压到站点目录：%v", calls)
	}
}

// TestBaotaDeployRollsBackOnUnzipFailure 校验解压失败时回滚到备份版本并标失败。
func TestBaotaDeployRollsBackOnUnzipFailure(t *testing.T) {
	f := newBaotaFixture(t, store.AppProviderBaota)
	f.panel.failUnzip = true
	ctx := context.Background()

	f.orch.runRelease(ctx, f.rel.ID)

	run, err := f.env.st.GetReleaseRun(ctx, f.rel.ID, f.server.ID)
	if err != nil {
		t.Fatalf("读取发布任务失败: %v", err)
	}
	if run.Status != store.ReleaseStatusFailed {
		t.Fatalf("应部署失败，实际 %s", run.Status)
	}
	if run.FailedStep == nil || *run.FailedStep != "unzip" {
		t.Fatalf("失败步骤应为 unzip，实际 %v", run.FailedStep)
	}
	// 应尝试用备份 zip 回滚
	rolledBack := false
	for _, call := range f.panel.snapshot() {
		if strings.HasPrefix(call, "unzip:") && strings.Contains(call, ".backup-") {
			rolledBack = true
		}
	}
	if !rolledBack {
		t.Fatalf("未尝试回滚：%v", f.panel.snapshot())
	}
}

// TestBuiltinProviderUsesShellPlan 校验 provider=builtin 时不走面板通道。
func TestBuiltinProviderUsesShellPlan(t *testing.T) {
	f := newBaotaFixture(t, store.AppProviderBuiltin)
	ctx := context.Background()
	f.orch.runRelease(ctx, f.rel.ID)
	for _, call := range f.panel.snapshot() {
		t.Fatalf("builtin 不应调用面板 API，实际 %v", call)
	}
}
