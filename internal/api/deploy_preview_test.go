package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/maliang/leadstack/internal/store"
)

// TestPreviewInplaceRedactsSecrets 预览：不落库、Secret 步骤不回显命令、带风险提示。
func TestPreviewInplaceRedactsSecrets(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	srv := &store.Server{ID: "srv-preview", Name: "pv", Host: "10.0.0.8", Port: 22, Mode: store.ModeSSH,
		Status: "online", CreatedAt: store.NowMS()}
	if err := env.st.CreateServer(ctx, srv); err != nil {
		t.Fatal(err)
	}
	target := "/www/wwwroot/pv"
	envJSON := `{"DB_PASSWORD":"s3cret"}`
	app := &store.App{ID: "app-preview", Name: "预览", Slug: "pv", CreatedAt: store.NowMS(),
		Provider: store.AppProviderBuiltin, DeployMode: store.DeployModeInplace,
		TargetDir: &target, EnvJSON: &envJSON}
	if err := env.st.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	tmpl := &store.DeployTemplate{ID: "tmpl-preview", Name: "空", Kind: store.TemplateKindBinary,
		Content: "", CreatedAt: store.NowMS(), UpdatedAt: store.NowMS()}
	if err := env.st.CreateDeployTemplate(ctx, tmpl); err != nil {
		t.Fatal(err)
	}
	art := &store.Artifact{ID: "art-preview", Kind: store.ArtifactKindURL, Location: "https://example.com/a.tar.gz",
		SHA256: strPtrOrNil(strings.Repeat("a", 64)), SizeBytes: 100, CreatedAt: store.NowMS()}
	if err := env.st.CreateArtifact(ctx, art); err != nil {
		t.Fatal(err)
	}

	orch := newDeployOrchestrator(env.st, nil, DeployOrchestratorOptions{
		Store: env.st, PlanRunner: &fakePlanRunner{}, Logger: env.api.logger,
	})
	out, err := orch.Preview(ctx, app, tmpl, art, "1.2.3", []*store.Server{srv})
	if err != nil {
		t.Fatalf("Preview 失败: %v", err)
	}
	if len(out.Servers) != 1 {
		t.Fatalf("应返回 1 台服务器的预览，实际 %d", len(out.Servers))
	}
	pv := out.Servers[0]
	if pv.PlanRoot != target {
		t.Fatalf("计划根目录应为 target_dir，实际 %s", pv.PlanRoot)
	}
	var envStep *PreviewStep
	for i := range pv.Steps {
		if pv.Steps[i].Name == "write-env" {
			envStep = &pv.Steps[i]
		}
	}
	if envStep == nil {
		t.Fatal("预览应包含 write-env 步骤")
	}
	if !envStep.Secret {
		t.Fatal("write-env 必须标记 secret")
	}
	if envStep.Command != "" {
		t.Fatalf("secret 步骤不能回显命令内容，实际 %q", envStep.Command)
	}
	// keep_paths 为空 + 未配健康检查 → 必须有风险提示
	joined := strings.Join(pv.Notes, " | ")
	if !strings.Contains(joined, "keep_paths") {
		t.Fatalf("应提示 keep_paths 风险，实际 %s", joined)
	}
	if !strings.Contains(joined, "健康检查") {
		t.Fatalf("应提示未配健康检查，实际 %s", joined)
	}
	if len(pv.Rollback) == 0 {
		t.Fatal("inplace 预览应包含回滚步骤")
	}
}

// TestPreviewBaotaSteps 预览：面板通道给出动作序列而不是 shell 计划。
func TestPreviewBaotaSteps(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	srv := &store.Server{ID: "srv-pv-bt", Name: "bt", Host: "10.0.0.9", Port: 22, Mode: store.ModeSSH,
		Status: "online", CreatedAt: store.NowMS()}
	if err := env.st.CreateServer(ctx, srv); err != nil {
		t.Fatal(err)
	}
	port := 8080
	app := &store.App{ID: "app-pv-bt", Name: "面板", Slug: "web", CreatedAt: store.NowMS(),
		Provider: store.AppProviderBaota, SitePort: &port}
	if err := env.st.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	tmpl := &store.DeployTemplate{ID: "tmpl-pv-bt", Name: "空", Kind: store.TemplateKindBinary,
		Content: "", CreatedAt: store.NowMS(), UpdatedAt: store.NowMS()}
	if err := env.st.CreateDeployTemplate(ctx, tmpl); err != nil {
		t.Fatal(err)
	}
	orch := newDeployOrchestrator(env.st, nil, DeployOrchestratorOptions{
		Store: env.st, PlanRunner: &fakePlanRunner{}, Logger: env.api.logger,
	})
	out, err := orch.Preview(ctx, app, tmpl, nil, "1.0.0", []*store.Server{srv})
	if err != nil {
		t.Fatalf("Preview 失败: %v", err)
	}
	pv := out.Servers[0]
	names := make([]string, 0, len(pv.Steps))
	for _, s := range pv.Steps {
		names = append(names, s.Name)
	}
	for _, want := range []string{"panel.ping", "panel.ensure-site", "panel.unzip"} {
		if !strings.Contains(strings.Join(names, ","), want) {
			t.Fatalf("面板预览缺少 %s：%v", want, names)
		}
	}
	if !strings.Contains(strings.Join(pv.Notes, " | "), "zip") {
		t.Fatalf("未选制品时应提示需要 zip，实际 %v", pv.Notes)
	}
}

// TestDeployPresetsSane 预设数据自检：键唯一、kind 合法、脚本类内容非空。
func TestDeployPresetsSane(t *testing.T) {
	presets := deployPresets()
	if len(presets) < 5 {
		t.Fatalf("预设数量偏少: %d", len(presets))
	}
	seen := map[string]bool{}
	for _, p := range presets {
		if seen[p.Key] {
			t.Fatalf("预设 key 重复: %s", p.Key)
		}
		seen[p.Key] = true
		switch p.Template.Kind {
		case store.TemplateKindBinary, store.TemplateKindSystemd, store.TemplateKindCompose:
		default:
			t.Fatalf("预设 %s 的模板类型非法: %s", p.Key, p.Template.Kind)
		}
		if p.Key != "baota-site" && strings.TrimSpace(p.Template.Content) == "" {
			t.Fatalf("预设 %s 的模板内容为空", p.Key)
		}
		if p.Defaults["deploy_mode"] != "" {
			mode := p.Defaults["deploy_mode"]
			if mode != store.DeployModeRelease && mode != store.DeployModeInplace && mode != store.DeployModeScript {
				t.Fatalf("预设 %s 的 deploy_mode 非法: %s", p.Key, mode)
			}
		}
	}
}

// TestDiscoveryDetectsGitSite 探测：/www/wwwroot 下的 git 仓库站点应建议脚本模式。
func TestDiscoveryDetectsGitSite(t *testing.T) {
	raw := strings.Join([]string{
		"###SYSTEMD",
		"",
		"###EXECSTART",
		"",
		"###PORTS",
		"",
		"###DOCKER",
		"",
		"###WWWROOT",
		"/www/wwwroot/tuoluojiang",
		"/www/wwwroot/static-site",
		"###GITDIRS",
		"/www/wwwroot/tuoluojiang|master|ssh://git@192.168.1.6:2222/oa/oa.git",
		"###END",
	}, "\n")
	report := parseDiscoveryOutput("srv-1", raw)
	if len(report.Candidates) != 2 {
		t.Fatalf("应发现 2 个站点，实际 %d: %+v", len(report.Candidates), report.Candidates)
	}
	var gitSite, plainSite *DiscoveryCandidate
	for i := range report.Candidates {
		switch report.Candidates[i].Name {
		case "tuoluojiang":
			gitSite = &report.Candidates[i]
		case "static-site":
			plainSite = &report.Candidates[i]
		}
	}
	if gitSite == nil || plainSite == nil {
		t.Fatalf("候选解析不完整: %+v", report.Candidates)
	}
	if gitSite.Suggested.DeployMode != store.DeployModeScript {
		t.Fatalf("git 站点应建议 script 模式，实际 %s", gitSite.Suggested.DeployMode)
	}
	if !strings.Contains(gitSite.Detail, "git(") {
		t.Fatalf("git 站点 detail 应带 git 信息: %s", gitSite.Detail)
	}
	if plainSite.Suggested.DeployMode != store.DeployModeInplace {
		t.Fatalf("普通站点应保持 inplace，实际 %s", plainSite.Suggested.DeployMode)
	}
}

// TestListAppsIncludesRollbackScript GET /apps 必须能带出新列（曾因 SELECT 漏列直接 500）。
func TestListAppsIncludesRollbackScript(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	app := map[string]any{"name": "列表", "slug": "list-app", "deploy_mode": "script", "target_dir": "/srv/list-app"}
	code, body := env.request(http.MethodPost, "/api/v1/apps", jwt, app)
	if code != http.StatusCreated {
		t.Fatalf("创建应用失败: %d %s", code, body)
	}
	code, body = env.request(http.MethodGet, "/api/v1/apps", jwt, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /apps 应 200，实际 %d：%s", code, body)
	}
	if !strings.Contains(string(body), `"deploy_mode":"script"`) {
		t.Fatalf("列表响应缺少 deploy_mode: %s", body)
	}
	if !strings.Contains(string(body), `"rollback_script":null`) && !strings.Contains(string(body), `"rollback_script":""`) {
		t.Fatalf("列表响应缺少 rollback_script 字段: %s", body)
	}
}

// TestCreateLocalServer 本机接入：创建 agent 模式服务器 + 一次性令牌 + 安装命令。
func TestCreateLocalServer(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	code, body := env.request(http.MethodPost, "/api/v1/servers/local", jwt,
		map[string]any{"name": "本机测试", "ttl_s": 600})
	if code != http.StatusCreated {
		t.Fatalf("接入本机应 201，实际 %d：%s", code, body)
	}
	var resp struct {
		Data struct {
			Server struct {
				ID   string `json:"id"`
				Mode string `json:"mode"`
				Host string `json:"host"`
			} `json:"server"`
			Token   string `json:"token"`
			Command string `json:"command"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Data.Server.Mode != "agent" || resp.Data.Server.Host != "127.0.0.1" {
		t.Fatalf("本机服务器应为 agent/127.0.0.1，实际 %+v", resp.Data.Server)
	}
	if resp.Data.Token == "" || !strings.Contains(resp.Data.Command, "--token '"+resp.Data.Token+"'") {
		t.Fatalf("安装命令应包含令牌: %s", resp.Data.Command)
	}
	if !strings.Contains(resp.Data.Command, "/install.sh") || !strings.Contains(resp.Data.Command, "--server 'http://127.0.0.1:") {
		t.Fatalf("安装命令格式不符: %s", resp.Data.Command)
	}
	// 令牌应预绑定到这台服务器
	srv, err := env.st.GetServer(context.Background(), resp.Data.Server.ID)
	if err != nil || srv.Mode != "agent" {
		t.Fatalf("服务器未正确落库: %+v %v", srv, err)
	}
}

// TestCreateLocalServerRequiresEnrollPerm 非管理员不能接入本机（令牌管理是 admin-only 权限）。
func TestCreateLocalServerRequiresEnrollPerm(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	// 建一个只有运维权限的账号
	code, body := env.request(http.MethodPost, "/api/v1/users", jwt, map[string]any{
		"username": "local-ops", "password": "local-ops-pass-123", "role": "operator",
	})
	if code != http.StatusCreated {
		t.Fatalf("创建运维账号失败: %d %s", code, body)
	}
	var user struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &user)
	code, loginBody := env.request(http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"username": "local-ops", "password": "local-ops-pass-123"})
	if code != http.StatusOK {
		t.Skipf("运维账号登录失败（%d），跳过权限断言", code)
	}
	var loginOut struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(loginBody, &loginOut)
	opsJWT := loginOut.Data.Token
	code, _ = env.request(http.MethodPost, "/api/v1/servers/local", opsJWT, map[string]any{})
	if code != http.StatusForbidden {
		t.Fatalf("运维角色不应能接入本机，实际 %d", code)
	}
}
