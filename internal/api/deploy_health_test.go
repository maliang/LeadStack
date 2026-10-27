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
	"time"

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
	// runID 是单台服务器的发布任务 id（日志按 run 落库，而不是按 release）。
	runID string
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
	return &builtinFixture{env: env, orch: orch, runner: runner, app: app, server: srv, rel: rel, runID: run.ID}
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

// runLogText 拼出某次 run 的全部日志文本，供「跳过健康检查必须有痕」这类断言使用。
func runLogText(t *testing.T, env *testEnv, runID string) string {
	t.Helper()
	logs, err := env.st.ListReleaseLogs(context.Background(), runID, 0, 5000)
	if err != nil {
		t.Fatalf("读取发布日志失败: %v", err)
	}
	var b strings.Builder
	for _, l := range logs {
		b.WriteString(l.Chunk)
	}
	return b.String()
}

// TestHealthCheckTreats3xxAsFailure 覆盖 A3/R4：只有 2xx 才算健康，302 必须判失败并走回滚。
func TestHealthCheckTreats3xxAsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 不带 Location：Go 不会自动跟随，客户端拿到 302 本身。
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	port := portOf(t, srv.URL)
	path := "/healthz"

	f := newBuiltinFixture(t, func(app *store.App) {
		app.HealthcheckPath = &path
		app.HealthcheckPort = &port
	})
	start := time.Now()
	f.orch.runRelease(context.Background(), f.rel.ID)
	elapsed := time.Since(start)

	run, err := f.env.st.GetReleaseRun(context.Background(), f.rel.ID, f.server.ID)
	if err != nil {
		t.Fatalf("读取发布任务失败: %v", err)
	}
	if run.Status != store.ReleaseStatusFailed {
		t.Fatalf("302 应判为不健康（发布失败），实际 %s", run.Status)
	}
	if run.FailedStep == nil || *run.FailedStep != deploy.StepHealthCheck {
		t.Fatalf("失败步骤应为 %s，实际 %v", deploy.StepHealthCheck, run.FailedStep)
	}
	if run.Message == nil || !strings.Contains(*run.Message, "302") {
		t.Fatalf("失败文案应带上实际状态码 302: %v", run.Message)
	}
	if run.Message == nil || !strings.Contains(*run.Message, "期望 2xx") {
		t.Fatalf("失败文案应说明期望 2xx: %v", run.Message)
	}
	// 3xx 是确定性结果，不该等满 30s 预算。
	if elapsed > 10*time.Second {
		t.Fatalf("3xx 应立即判失败（实测耗时 %s）", elapsed)
	}
	logs := runLogText(t, f.env, f.runID)
	if !strings.Contains(logs, "健康检查失败") || !strings.Contains(logs, "302") {
		t.Fatalf("日志应记录健康检查失败与状态码: %q", logs)
	}
	if len(f.runner.plans) < 2 {
		t.Fatalf("健康检查失败应执行回滚计划，实际计划数 %d", len(f.runner.plans))
	}
}

// TestHealthCheckTreats4xxAsImmediateFailure 覆盖 R4：4xx 立即判不健康（不重试到超时）。
func TestHealthCheckTreats4xxAsImmediateFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	port := portOf(t, srv.URL)
	path := "/absent"

	f := newBuiltinFixture(t, func(app *store.App) {
		app.HealthcheckPath = &path
		app.HealthcheckPort = &port
	})
	start := time.Now()
	f.orch.runRelease(context.Background(), f.rel.ID)

	run, err := f.env.st.GetReleaseRun(context.Background(), f.rel.ID, f.server.ID)
	if err != nil {
		t.Fatalf("读取发布任务失败: %v", err)
	}
	if run.Status != store.ReleaseStatusFailed {
		t.Fatalf("404 应判为不健康，实际 %s", run.Status)
	}
	if run.Message == nil || !strings.Contains(*run.Message, "404") {
		t.Fatalf("失败文案应带 404: %v", run.Message)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("4xx 不应重试到 30s 预算（实测 %s）", elapsed)
	}
}

// TestHealthCheckSkipIsLogged 覆盖 A4：未配置 path/port 时必须写「跳过健康检查」日志，不能静默。
func TestHealthCheckSkipIsLogged(t *testing.T) {
	// ① 完全没配 path。
	f := newBuiltinFixture(t, nil)
	f.orch.runRelease(context.Background(), f.rel.ID)
	run, err := f.env.st.GetReleaseRun(context.Background(), f.rel.ID, f.server.ID)
	if err != nil {
		t.Fatalf("读取发布任务失败: %v", err)
	}
	if run.Status != store.ReleaseStatusSuccess {
		t.Fatalf("未配置健康检查应成功，实际 %s（%s）", run.Status, derefStr(run.Message))
	}
	logs := runLogText(t, f.env, f.runID)
	if !strings.Contains(logs, "跳过健康检查：未配置 healthcheck_path") {
		t.Fatalf("日志应写明跳过原因（未配置 healthcheck_path）: %q", logs)
	}

	// ② 配了 path 但没有端口（healthcheck_port 与 site_port 都缺）。
	path := "/healthz"
	f2 := newBuiltinFixture(t, func(app *store.App) {
		app.HealthcheckPath = &path
	})
	f2.orch.runRelease(context.Background(), f2.rel.ID)
	run2, err := f2.env.st.GetReleaseRun(context.Background(), f2.rel.ID, f2.server.ID)
	if err != nil {
		t.Fatalf("读取发布任务失败: %v", err)
	}
	if run2.Status != store.ReleaseStatusSuccess {
		t.Fatalf("缺端口时应跳过而不是失败，实际 %s", run2.Status)
	}
	logs2 := runLogText(t, f2.env, f2.runID)
	if !strings.Contains(logs2, "跳过健康检查：healthcheck_port 与 site_port 均未配置") {
		t.Fatalf("缺端口时应写明命中哪一条跳过原因: %q", logs2)
	}
}

// TestHealthCheckLogsRealURL 覆盖 R4：健康检查日志必须打印真实的探测地址，而不是占位符。
func TestHealthCheckLogsRealURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	logs := runLogText(t, f.env, f.runID)
	wantURL := "http://127.0.0.1:" + strconv.Itoa(port) + "/healthz"
	if !strings.Contains(logs, wantURL) {
		t.Fatalf("日志应打印真实探测地址 %s: %q", wantURL, logs)
	}
	if strings.Contains(logs, "<host>") {
		t.Fatalf("日志不应出现占位符 <host>: %q", logs)
	}
	if !strings.Contains(logs, "健康检查通过") {
		t.Fatalf("通过时应写「健康检查通过」: %q", logs)
	}
}
