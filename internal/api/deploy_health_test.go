package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/maliang/leadstack/internal/deploy"
	"github.com/maliang/leadstack/internal/store"
)

// builtinFixture 准备一次 builtin 通道（release 模式）的发布，并注入计划执行替身。
type builtinFixture struct {
	env    *testEnv
	orch   *deployOrchestrator
	runner *fakePlanRunner
	app    *store.App
	server *store.Server
	rel    *store.Release
}

func newBuiltinFixture(t *testing.T, mut func(app *store.App)) *builtinFixture {
	t.Helper()
	env := newTestEnv(t)
	ctx := context.Background()

	srv := &store.Server{
		ID: "srv-hc", Name: "hc-host", Host: "127.0.0.1", Port: 22, Mode: store.ModeSSH,
		Status: "online", CreatedAt: store.NowMS(),
	}
	if err := env.st.CreateServer(ctx, srv); err != nil {
		t.Fatalf("创建服务器失败: %v", err)
	}

	app := &store.App{
		ID: "app-hc", Name: "健康检查", Slug: "hc-app", CreatedAt: store.NowMS(),
		Provider: store.AppProviderBuiltin, DeployMode: store.DeployModeRelease,
	}
	if mut != nil {
		mut(app)
	}
	if err := env.st.CreateApp(ctx, app); err != nil {
		t.Fatalf("创建应用失败: %v", err)
	}

	artID := "art-hc"
	sum := strings.Repeat("0", 64)
	art := &store.Artifact{ID: artID, Kind: store.ArtifactKindURL, Location: "https://example.com/a.tar.gz",
		SHA256: &sum, SizeBytes: 10, CreatedAt: store.NowMS()}
	if err := env.st.CreateArtifact(ctx, art); err != nil {
		t.Fatalf("创建制品失败: %v", err)
	}

	tmpl := &store.DeployTemplate{ID: "tmpl-hc", Name: "空", Kind: store.TemplateKindBinary,
		Content: "", CreatedAt: store.NowMS(), UpdatedAt: store.NowMS()}
	if err := env.st.CreateDeployTemplate(ctx, tmpl); err != nil {
		t.Fatalf("创建模板失败: %v", err)
	}

	rel := &store.Release{ID: "rel-hc", AppID: app.ID, TemplateID: tmpl.ID, Version: "1.0.0",
		ArtifactID: &artID, Status: store.ReleaseStatusPending, CreatedAt: store.NowMS(), Concurrency: 1}
	run := store.ReleaseRun{ID: "run-hc", ReleaseID: rel.ID, ServerID: srv.ID, Status: store.ReleaseStatusPending}
	if err := env.st.CreateRelease(ctx, rel, []store.ReleaseRun{run}); err != nil {
		t.Fatalf("创建发布失败: %v", err)
	}

	runner := &fakePlanRunner{}
	orch := newDeployOrchestrator(env.st, nil, DeployOrchestratorOptions{
		Store: env.st, PlanRunner: runner, Logger: env.api.logger,
	})
	if orch == nil {
		t.Fatal("构造编排器失败")
	}
	return &builtinFixture{env: env, orch: orch, runner: runner, app: app, server: srv, rel: rel}
}

func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("解析 URL 失败: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("解析端口失败: %v", err)
	}
	return port
}

// TestBuiltinHealthCheckPasses 校验 builtin 通道部署成功后会真的探一次 HTTP。
func TestBuiltinHealthCheckPasses(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	port := portOf(t, srv.URL)
	path := "/healthz"

	f := newBuiltinFixture(t, func(app *store.App) {
		app.HealthcheckPath = &path
		app.HealthcheckPort = &port
	})
	f.orch.runRelease(context.Background(), f.rel.ID)

	run, err := f.env.st.GetReleaseRun(context.Background(), f.rel.ID, f.server.ID)
	if err != nil {
		t.Fatalf("读取发布任务失败: %v", err)
	}
	if run.Status != store.ReleaseStatusSuccess {
		t.Fatalf("应成功，实际 %s（%s）", run.Status, derefStr(run.Message))
	}
	if hits == 0 {
		t.Fatal("健康检查没有真的发请求")
	}
	if len(f.runner.plans) != 1 {
		t.Fatalf("健康检查失败时不应额外执行计划，实际 %d 个", len(f.runner.plans))
	}
}

// TestBuiltinHealthCheckFailureRollsBack 校验健康检查失败 → 标记失败步骤 + 执行回滚计划。
func TestBuiltinHealthCheckFailureRollsBack(t *testing.T) {
	// 拿一个确定没有服务监听的端口
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("取端口失败: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	path := "/"

	f := newBuiltinFixture(t, func(app *store.App) {
		app.HealthcheckPath = &path
		app.HealthcheckPort = &port
	})
	f.orch.runRelease(context.Background(), f.rel.ID)

	run, err := f.env.st.GetReleaseRun(context.Background(), f.rel.ID, f.server.ID)
	if err != nil {
		t.Fatalf("读取发布任务失败: %v", err)
	}
	if run.Status != store.ReleaseStatusFailed {
		t.Fatalf("健康检查失败时应标记失败，实际 %s", run.Status)
	}
	if run.FailedStep == nil || *run.FailedStep != deploy.StepHealthCheck {
		t.Fatalf("失败步骤应为 %s，实际 %v", deploy.StepHealthCheck, run.FailedStep)
	}
	if run.Message == nil || !strings.Contains(*run.Message, "健康检查") {
		t.Fatalf("失败信息应说明健康检查: %v", run.Message)
	}
	if len(f.runner.plans) < 2 {
		t.Fatalf("应额外执行回滚计划，实际计划数 %d", len(f.runner.plans))
	}
	rb := f.runner.plans[len(f.runner.plans)-1]
	if len(rb.Steps) == 0 || !strings.HasPrefix(rb.Steps[0].Name, "rollback:") {
		t.Fatalf("回滚计划步骤未标记 rollback: %+v", rb.Steps)
	}
}

// TestBuiltinHealthCheckSkippedWithoutPort 校验未配置端口时跳过健康检查（不会误判失败）。
func TestBuiltinHealthCheckSkippedWithoutPort(t *testing.T) {
	path := "/healthz"
	f := newBuiltinFixture(t, func(app *store.App) {
		app.HealthcheckPath = &path
	})
	f.orch.runRelease(context.Background(), f.rel.ID)
	run, err := f.env.st.GetReleaseRun(context.Background(), f.rel.ID, f.server.ID)
	if err != nil {
		t.Fatalf("读取发布任务失败: %v", err)
	}
	if run.Status != store.ReleaseStatusSuccess {
		t.Fatalf("未配置端口应跳过检查并成功，实际 %s", run.Status)
	}
}
