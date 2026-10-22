package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maliang/leadstack/internal/deploy"
	"github.com/maliang/leadstack/internal/store"
)

// canaryFixture 准备 3 台服务器的发布环境（release 模式，binary 空模板 + url 制品）。
func canaryFixture(t *testing.T, serverCount int) (*testEnv, *deployOrchestrator, *fakePlanRunner, *store.App, []string) {
	t.Helper()
	env := newTestEnv(t)
	ctx := context.Background()

	servers := make([]string, 0, serverCount)
	for i := 0; i < serverCount; i++ {
		id := "srv-c" + string(rune('1'+i))
		srv := &store.Server{ID: id, Name: id, Host: "10.1.0." + string(rune('1'+i)), Port: 22,
			Mode: store.ModeSSH, Status: "online", CreatedAt: store.NowMS()}
		if err := env.st.CreateServer(ctx, srv); err != nil {
			t.Fatalf("创建服务器失败: %v", err)
		}
		servers = append(servers, id)
	}
	app := &store.App{ID: "app-canary", Name: "灰度", Slug: "canary", CreatedAt: store.NowMS(),
		Provider: store.AppProviderBuiltin, DeployMode: store.DeployModeRelease}
	if err := env.st.CreateApp(ctx, app); err != nil {
		t.Fatalf("创建应用失败: %v", err)
	}
	art := &store.Artifact{ID: "art-canary", Kind: store.ArtifactKindURL,
		Location: "https://example.com/a.tar.gz", SHA256: strPtrOrNil(strings.Repeat("a", 64)),
		SizeBytes: 10, CreatedAt: store.NowMS()}
	if err := env.st.CreateArtifact(ctx, art); err != nil {
		t.Fatalf("创建制品失败: %v", err)
	}
	tmpl := &store.DeployTemplate{ID: "tmpl-canary", Name: "空", Kind: store.TemplateKindBinary,
		Content: "", CreatedAt: store.NowMS(), UpdatedAt: store.NowMS()}
	if err := env.st.CreateDeployTemplate(ctx, tmpl); err != nil {
		t.Fatalf("创建模板失败: %v", err)
	}
	runner := &fakePlanRunner{}
	orch := newDeployOrchestrator(env.st, nil, DeployOrchestratorOptions{
		Store: env.st, PlanRunner: runner, Logger: env.api.logger,
	})
	return env, orch, runner, app, servers
}

// createTestRelease 直接落库一个发布 + runs。
func createTestRelease(t *testing.T, env *testEnv, appID string, servers []string, strategy string, canary int, hold int) *store.Release {
	return createTestReleaseVersion(t, env, appID, servers, strategy, canary, hold, "1.0.0")
}

func createTestReleaseVersion(t *testing.T, env *testEnv, appID string, servers []string, strategy string, canary int, hold int, version string) *store.Release {
	t.Helper()
	ctx := context.Background()
	rel := &store.Release{
		ID: "rel-" + strategy + "-" + time.Now().Format("150405.000000"), AppID: appID,
		TemplateID: "tmpl-canary", Version: version, ArtifactID: strPtrOrNil("art-canary"),
		Status: store.ReleaseStatusPending, CreatedAt: store.NowMS(), Concurrency: 2,
		Strategy: strategy, CanaryCount: canary, HoldSeconds: hold,
	}
	runs := make([]store.ReleaseRun, 0, len(servers))
	for _, id := range servers {
		runs = append(runs, store.ReleaseRun{ID: "run-" + rel.ID + "-" + id, ReleaseID: rel.ID, ServerID: id,
			Status: store.ReleaseStatusPending})
	}
	if err := env.st.CreateRelease(ctx, rel, runs); err != nil {
		t.Fatalf("创建发布失败: %v", err)
	}
	return rel
}

// TestCanaryHoldsFirstBatch 灰度：第一批先执行，观察期内不会派发第二批。
func TestCanaryHoldsFirstBatch(t *testing.T) {
	env, orch, runner, app, servers := canaryFixture(t, 3)
	rel := createTestRelease(t, env, app.ID, servers, store.ReleaseStrategyCanary, 1, 1)
	done := make(chan struct{})
	go func() { orch.runRelease(context.Background(), rel.ID); close(done) }()

	// 观察期内应只执行了 1 个计划
	time.Sleep(300 * time.Millisecond)
	runner.mu.Lock()
	inflightPlans := len(runner.plans)
	runner.mu.Unlock()
	if inflightPlans != 1 {
		t.Fatalf("观察期内应只执行第一批 1 个计划，实际 %d", inflightPlans)
	}
	<-done
	runner.mu.Lock()
	total := len(runner.plans)
	runner.mu.Unlock()
	if total != 3 {
		t.Fatalf("观察结束后应执行完 3 台，实际 %d", total)
	}
	for _, id := range servers {
		run, err := env.st.GetReleaseRun(context.Background(), rel.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != store.ReleaseStatusSuccess {
			t.Fatalf("服务器 %s 应成功，实际 %s", id, run.Status)
		}
	}
}

// TestCanaryStopsOnBatchFailure 灰度：第一批失败 → 剩余批次取消，发布失败。
func TestCanaryStopsOnBatchFailure(t *testing.T) {
	env, orch, runner, app, servers := canaryFixture(t, 3)
	runner.failOn = map[string]bool{servers[0]: true}
	rel := createTestRelease(t, env, app.ID, servers, store.ReleaseStrategyCanary, 1, 0)
	orch.runRelease(context.Background(), rel.ID)

	firstRun, _ := env.st.GetReleaseRun(context.Background(), rel.ID, servers[0])
	if firstRun.Status != store.ReleaseStatusFailed {
		t.Fatalf("第一批应失败，实际 %s", firstRun.Status)
	}
	for _, id := range servers[1:] {
		run, err := env.st.GetReleaseRun(context.Background(), rel.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != store.ReleaseStatusCanceled {
			t.Fatalf("后续批次应被取消，%s 实际 %s", id, run.Status)
		}
	}
	rel2, _ := env.st.GetRelease(context.Background(), rel.ID)
	if rel2.Status != store.ReleaseStatusFailed {
		t.Fatalf("发布整体应失败，实际 %s", rel2.Status)
	}
}

// TestRollbackToSpecificRelease release 模式：切 current 到指定历史版本。
func TestRollbackToSpecificRelease(t *testing.T) {
	env, orch, runner, app, servers := canaryFixture(t, 1)
	ctx := context.Background()
	// 先造一个「已成功」的历史版本
	old := createTestReleaseVersion(t, env, app.ID, servers, store.ReleaseStrategyAll, 0, 0, "0.9.0")
	if err := env.st.UpdateReleaseStatus(ctx, old.ID, store.ReleaseStatusSuccess, &old.CreatedAt, &old.CreatedAt); err != nil {
		t.Fatal(err)
	}
	oldRun, _ := env.st.GetReleaseRun(ctx, old.ID, servers[0])
	oldRun.Status = store.ReleaseStatusSuccess
	if err := env.st.UpdateReleaseRun(ctx, *oldRun); err != nil {
		t.Fatal(err)
	}
	cur := createTestRelease(t, env, app.ID, servers, store.ReleaseStrategyAll, 0, 0)
	curRun, _ := env.st.GetReleaseRun(ctx, cur.ID, servers[0])
	curRun.Status = store.ReleaseStatusSuccess
	if err := env.st.UpdateReleaseRun(ctx, *curRun); err != nil {
		t.Fatal(err)
	}

	n, err := orch.RollbackTo(cur.ID, old.ID, nil)
	if err != nil || n != 1 {
		t.Fatalf("RollbackTo 失败: n=%d err=%v", n, err)
	}
	// 计划里应包含切 current
	if len(runner.plans) == 0 {
		t.Fatal("回滚没有执行计划")
	}
	hasSwitch := false
	for _, step := range runner.plans[len(runner.plans)-1].Steps {
		if step.Name == deploy.StepSwitchCurrent {
			hasSwitch = true
		}
	}
	if !hasSwitch {
		t.Fatalf("回滚计划应包含切 current：%+v", runner.plans[len(runner.plans)-1].Steps)
	}
	run, _ := env.st.GetReleaseRun(ctx, cur.ID, servers[0])
	if run.Message == nil || !strings.Contains(*run.Message, "0.9.0") {
		t.Fatalf("回滚信息应包含目标版本: %v", run.Message)
	}
}

// TestRollbackToRejectsScriptMode 脚本模式不支持回滚到指定版本（明确拒绝）。
func TestRollbackToRejectsScriptMode(t *testing.T) {
	env, orch, _, app, servers := canaryFixture(t, 1)
	ctx := context.Background()
	app.DeployMode = store.DeployModeScript
	if err := env.st.UpdateAppDeploy(ctx, app.ID, strPtrOrNil(store.DeployModeScript), nil, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	old := createTestRelease(t, env, app.ID, servers, store.ReleaseStrategyAll, 0, 0)
	if err := env.st.UpdateReleaseStatus(ctx, old.ID, store.ReleaseStatusSuccess, &old.CreatedAt, &old.CreatedAt); err != nil {
		t.Fatal(err)
	}
	cur := createTestRelease(t, env, app.ID, servers, store.ReleaseStrategyAll, 0, 0)
	if _, err := orch.RollbackTo(cur.ID, old.ID, nil); err == nil || !strings.Contains(err.Error(), "脚本模式") {
		t.Fatalf("应明确拒绝脚本模式回滚，实际 %v", err)
	}
}

// TestDiagnoseReportGitRepo 体检：git 仓库 → 建议脚本模式，并给出通过项。
func TestDiagnoseReportGitRepo(t *testing.T) {
	raw := strings.Join([]string{
		"###DIR", "exists", "writable", "Filesystem 1024-blocks Used Available Capacity Mounted",
		"###GIT", "master", "0", "ssh://git@192.168.1.6:2222/oa/oa.git",
		"###UNIT", "", "", "", "", "",
		"###PORTS", "",
		"###END",
	}, "\n")
	report := buildDiagnoseReport("srv-1", "site", "tuoluojiang", "/www/wwwroot/tuoluojiang", raw)
	if report.Recommendation.DeployMode != store.DeployModeScript {
		t.Fatalf("git 仓库应建议 script 模式，实际 %s", report.Recommendation.DeployMode)
	}
	if !strings.Contains(report.Recommendation.Script, "git pull") {
		t.Fatalf("应给出 git pull 脚本: %q", report.Recommendation.Script)
	}
	statuses := map[string]string{}
	for _, c := range report.Checks {
		statuses[c.ID] = c.Status
	}
	if statuses["dir"] != "pass" || statuses["writable"] != "pass" || statuses["git"] != "pass" {
		t.Fatalf("体检项状态不符: %+v", report.Checks)
	}
}

// TestDiagnoseReportDirtyRepo 体检：有未提交改动 → warn，且建议里仍带 pull 前提示。
func TestDiagnoseReportDirtyRepo(t *testing.T) {
	raw := strings.Join([]string{
		"###DIR", "exists", "writable", "",
		"###GIT", "master", "3", "ssh://git@example.com/x.git",
		"###UNIT", "/usr/bin/app --serve", "/opt/app", "nobody", "/etc/systemd/system/app.service", "active",
		"###PORTS", "",
		"###END",
	}, "\n")
	report := buildDiagnoseReport("srv-2", "systemd", "app.service", "/opt/app", raw)
	var gitCheck *DiagnoseCheck
	for i := range report.Checks {
		if report.Checks[i].ID == "git" {
			gitCheck = &report.Checks[i]
		}
	}
	if gitCheck == nil || gitCheck.Status != "warn" || !strings.Contains(gitCheck.Detail, "3") {
		t.Fatalf("未提交改动应给 warn: %+v", report.Checks)
	}
	if report.Recommendation.RestartCmd != "systemctl restart app.service" {
		t.Fatalf("systemd 建议重启命令不符: %s", report.Recommendation.RestartCmd)
	}
}

// TestRollbackToInplaceUsesSuccessorBackup inplace 回滚到历史版本时，
// 应恢复「目标版本之后替换它的那次发布」的备份（备份存的是发布前状态）。
func TestRollbackToInplaceUsesSuccessorBackup(t *testing.T) {
	env, orch, runner, app, servers := canaryFixture(t, 1)
	ctx := context.Background()
	target := "/srv/inplace-app"
	if err := env.st.UpdateAppDeploy(ctx, app.ID, strPtrOrNil(store.DeployModeInplace), &target, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	old := createTestReleaseVersion(t, env, app.ID, servers, store.ReleaseStrategyAll, 0, 0, "1.0.0")
	newer := createTestReleaseVersion(t, env, app.ID, servers, store.ReleaseStrategyAll, 0, 0, "1.1.0")
	for _, rel := range []*store.Release{old, newer} {
		finished := rel.CreatedAt
		if err := env.st.UpdateReleaseStatus(ctx, rel.ID, store.ReleaseStatusSuccess, &rel.CreatedAt, &finished); err != nil {
			t.Fatal(err)
		}
		run, _ := env.st.GetReleaseRun(ctx, rel.ID, servers[0])
		run.Status = store.ReleaseStatusSuccess
		if err := env.st.UpdateReleaseRun(ctx, *run); err != nil {
			t.Fatal(err)
		}
	}
	// 时间顺序：old 先、newer 后（ListReleases 倒序时 newer 在前）
	cur := newer
	n, err := orch.RollbackTo(cur.ID, old.ID, nil)
	if err != nil || n != 1 {
		t.Fatalf("RollbackTo 失败: n=%d err=%v", n, err)
	}
	if len(runner.plans) == 0 {
		t.Fatal("没有执行回滚计划")
	}
	// 恢复的应是 newer（即替换 old 的那次发布）的备份
	found := false
	for _, step := range runner.plans[len(runner.plans)-1].Steps {
		if strings.Contains(step.Command, "backup-"+newer.ID+".tar.gz") {
			found = true
		}
		if strings.Contains(step.Command, "backup-"+old.ID+".tar.gz") {
			t.Fatalf("不应恢复目标版本自身的备份（那是发布前状态）: %s", step.Command)
		}
	}
	if !found {
		t.Fatalf("回滚计划未使用后继版本的备份: %+v", runner.plans[len(runner.plans)-1].Steps)
	}
}
