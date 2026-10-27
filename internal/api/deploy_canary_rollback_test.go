package api

import (
	"context"
	"errors"
	"net/http"
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

	res, err := orch.RollbackTo(cur.ID, old.ID, nil)
	if err != nil || res.RolledBack != 1 {
		t.Fatalf("RollbackTo 失败: res=%+v err=%v", res, err)
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
	_, rerr := orch.RollbackTo(cur.ID, old.ID, nil)
	if rerr == nil || !strings.Contains(rerr.Error(), "不支持回滚到指定版本") {
		t.Fatalf("应明确拒绝脚本模式回滚，实际 %v", rerr)
	}
	if !strings.Contains(rerr.Error(), "rollback_script") {
		t.Fatalf("拒绝文案应指向 rollback_script，实际 %q", rerr.Error())
	}
	var typed *rollbackToError
	if !errors.As(rerr, &typed) || typed.Status != http.StatusBadRequest || typed.Code != codeInvalidArgument {
		t.Fatalf("script 模式应为 400 invalid_argument，实际 %+v", typed)
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
	res, err := orch.RollbackTo(cur.ID, old.ID, nil)
	if err != nil || res.RolledBack != 1 {
		t.Fatalf("RollbackTo 失败: res=%+v err=%v", res, err)
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

// markReleaseSuccess 把发布整体与该次发布在指定服务器上的 run 置为成功。
func markReleaseSuccess(t *testing.T, env *testEnv, rel *store.Release, servers []string) {
	t.Helper()
	ctx := context.Background()
	if err := env.st.UpdateReleaseStatus(ctx, rel.ID, store.ReleaseStatusSuccess, &rel.CreatedAt, &rel.CreatedAt); err != nil {
		t.Fatalf("更新发布状态失败: %v", err)
	}
	for _, id := range servers {
		run, err := env.st.GetReleaseRun(ctx, rel.ID, id)
		if err != nil {
			t.Fatalf("读取发布任务失败: %v", err)
		}
		run.Status = store.ReleaseStatusSuccess
		if err := env.st.UpdateReleaseRun(ctx, *run); err != nil {
			t.Fatalf("更新发布任务失败: %v", err)
		}
	}
}

// TestRollbackToAllSkippedIsDiagnosable 覆盖 A10/R7：全部跳过 → 请求级 400，message 逐台列出原因。
func TestRollbackToAllSkippedIsDiagnosable(t *testing.T) {
	env, orch, _, app, servers := canaryFixture(t, 1)
	ctx := context.Background()
	target := "/srv/inplace-app"
	if err := env.st.UpdateAppDeploy(ctx, app.ID, strPtrOrNil(store.DeployModeInplace), &target, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	old := createTestReleaseVersion(t, env, app.ID, servers, store.ReleaseStrategyAll, 0, 0, "1.0.0")
	markReleaseSuccess(t, env, old, servers)
	newer := createTestReleaseVersion(t, env, app.ID, servers, store.ReleaseStrategyAll, 0, 0, "1.1.0")
	markReleaseSuccess(t, env, newer, servers)

	// 目标版本已是该服务器上的最新成功版本 ⇒ 全部 already-current。
	res, err := orch.RollbackTo(old.ID, newer.ID, nil)
	if err == nil {
		t.Fatalf("全部跳过应返回请求级错误，实际 res=%+v", res)
	}
	var typed *rollbackToError
	if !errors.As(err, &typed) || typed.Status != http.StatusBadRequest || typed.Code != codeInvalidArgument {
		t.Fatalf("全跳过应为 400 invalid_argument，实际 %+v（err=%v）", typed, err)
	}
	if !strings.Contains(err.Error(), "没有任何服务器可回滚到 1.1.0") {
		t.Fatalf("message 应说明「没有任何服务器可回滚到 <version>」: %q", err.Error())
	}
	if !strings.Contains(err.Error(), servers[0]+":"+rollbackSkipAlreadyCurrent) {
		t.Fatalf("message 应逐台列出 server_id:reason: %q", err.Error())
	}
	if len(res.Skipped) != 1 || res.Skipped[0].ServerID != servers[0] ||
		res.Skipped[0].Reason != rollbackSkipAlreadyCurrent {
		t.Fatalf("skipped 应带逐台原因: %+v", res.Skipped)
	}
}

// TestRollbackToPartialSkippedReportsPerServer 覆盖 A10：部分跳过 → 200 且 skipped 非空（不静默）。
func TestRollbackToPartialSkippedReportsPerServer(t *testing.T) {
	env, orch, runner, app, servers := canaryFixture(t, 2)

	// 目标版本只在第一台上有成功记录。
	target := createTestReleaseVersion(t, env, app.ID, servers, store.ReleaseStrategyAll, 0, 0, "0.9.0")
	markReleaseSuccess(t, env, target, servers[:1])
	cur := createTestRelease(t, env, app.ID, servers, store.ReleaseStrategyAll, 0, 0)
	markReleaseSuccess(t, env, cur, servers)

	res, err := orch.RollbackTo(cur.ID, target.ID, nil)
	if err != nil {
		t.Fatalf("部分跳过应返回 200（err=nil），实际 %v", err)
	}
	if res.RolledBack != 1 {
		t.Fatalf("应回滚 1 台，实际 %d", res.RolledBack)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].ServerID != servers[1] ||
		res.Skipped[0].Reason != rollbackSkipNoSuccessfulRun {
		t.Fatalf("第二台应跳过并给出 no-successful-run: %+v", res.Skipped)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("跳过的服务器不应记为失败: %+v", res.Failed)
	}
	if len(runner.plans) == 0 {
		t.Fatal("应真的执行了第一台的切换计划")
	}
	if res.TargetReleaseID != target.ID {
		t.Fatalf("结果应带目标发布 id: %+v", res)
	}
}

// TestRollbackToMissingBackupIsSkipped 覆盖 R6：inplace 备份缺失应归为 missing-backup，而不是静默 continue。
func TestRollbackToMissingBackupIsSkipped(t *testing.T) {
	env, orch, runner, app, servers := canaryFixture(t, 1)
	ctx := context.Background()
	target := "/srv/inplace-app"
	if err := env.st.UpdateAppDeploy(ctx, app.ID, strPtrOrNil(store.DeployModeInplace), &target, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	old := createTestReleaseVersion(t, env, app.ID, servers, store.ReleaseStrategyAll, 0, 0, "1.0.0")
	markReleaseSuccess(t, env, old, servers)
	newer := createTestReleaseVersion(t, env, app.ID, servers, store.ReleaseStrategyAll, 0, 0, "1.1.0")
	markReleaseSuccess(t, env, newer, servers)

	// 只让「探测备份是否存在」的那条单步计划失败，模拟备份文件被清理。
	runner.failPlan = func(p deploy.Plan) bool {
		return len(p.Steps) == 1 && p.Steps[0].Name == "check-backup"
	}

	res, err := orch.RollbackTo(newer.ID, old.ID, nil)
	if err == nil {
		t.Fatalf("备份缺失且无其它可回滚服务器时应为请求级错误，实际 res=%+v", res)
	}
	var typed *rollbackToError
	if !errors.As(err, &typed) || typed.Status != http.StatusBadRequest || typed.Code != codeInvalidArgument {
		t.Fatalf("应为 400 invalid_argument，实际 %+v（err=%v）", typed, err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != rollbackSkipMissingBackup {
		t.Fatalf("应归为 missing-backup: %+v", res.Skipped)
	}
	if !strings.Contains(err.Error(), servers[0]+":"+rollbackSkipMissingBackup) {
		t.Fatalf("message 应带 missing-backup 原因: %q", err.Error())
	}
	// 备份缺失时不应真的执行恢复计划（那是注定失败的破坏性操作）。
	for _, plan := range runner.plans {
		for _, step := range plan.Steps {
			if step.Name == deploy.StepRollbackRestore {
				t.Fatalf("备份缺失时不应执行恢复步骤: %+v", runner.plans)
			}
		}
	}
}

// TestRollbackToMissingTargetReturns404 覆盖 R6：目标版本不存在 → 404 not_found（不是 400/500）。
func TestRollbackToMissingTargetReturns404(t *testing.T) {
	env, orch, _, app, servers := canaryFixture(t, 1)
	cur := createTestRelease(t, env, app.ID, servers, store.ReleaseStrategyAll, 0, 0)
	markReleaseSuccess(t, env, cur, servers)

	_, err := orch.RollbackTo(cur.ID, "rel-does-not-exist", nil)
	var typed *rollbackToError
	if !errors.As(err, &typed) || typed.Status != http.StatusNotFound || typed.Code != codeNotFound {
		t.Fatalf("目标版本不存在应为 404 not_found，实际 %+v（err=%v）", typed, err)
	}
	if !strings.Contains(err.Error(), "target-not-found") {
		t.Fatalf("message 应带唯一机器码 target-not-found: %q", err.Error())
	}
}

// fakeRollbackToOrchestrator 在 fakeDeployOrchestrator 之上补 RollbackTo，用于断言 REST 层语义。
type fakeRollbackToOrchestrator struct {
	*fakeDeployOrchestrator
	res   rollbackToResult
	err   error
	calls []string
}

func (f *fakeRollbackToOrchestrator) RollbackTo(releaseID, targetReleaseID string, serverIDs []string) (rollbackToResult, error) {
	f.calls = append(f.calls, releaseID+">"+targetReleaseID)
	return f.res, f.err
}

// TestRollbackReleaseHTTPReportsSkipped 覆盖 A10 的 HTTP 语义：200 带 skipped[]，全跳过 400，目标不存在 404。
func TestRollbackReleaseHTTPReportsSkipped(t *testing.T) {
	env, _, _, app, servers := canaryFixture(t, 2)
	jwt := env.login()

	target := createTestReleaseVersion(t, env, app.ID, servers, store.ReleaseStrategyAll, 0, 0, "0.9.0")
	markReleaseSuccess(t, env, target, servers[:1])
	cur := createTestRelease(t, env, app.ID, servers, store.ReleaseStrategyAll, 0, 0)
	markReleaseSuccess(t, env, cur, servers)

	stub := &fakeRollbackToOrchestrator{
		fakeDeployOrchestrator: env.deploy,
		res: rollbackToResult{
			RolledBack: 1, TargetReleaseID: target.ID, TargetVersion: "0.9.0",
			Skipped: []rollbackSkip{{ServerID: servers[1], Reason: rollbackSkipNoSuccessfulRun}},
			Failed:  []rollbackFailure{},
		},
	}
	env.api.deploy = stub

	// ① 部分跳过：200 + skipped 非空。
	status, raw := env.request(http.MethodPost, "/api/v1/releases/"+cur.ID+"/rollback", jwt,
		map[string]any{"target_release_id": target.ID})
	if status != http.StatusOK {
		t.Fatalf("部分跳过应 200，实际 %d body=%s", status, raw)
	}
	var got struct {
		RolledBack int               `json:"rolled_back"`
		Target     string            `json:"target_release_id"`
		Skipped    []rollbackSkip    `json:"skipped"`
		Failed     []rollbackFailure `json:"failed"`
	}
	decodeData(t, raw, &got)
	if got.RolledBack != 1 || got.Target != target.ID {
		t.Fatalf("响应应带 rolled_back/target_release_id: %+v body=%s", got, raw)
	}
	if len(got.Skipped) != 1 || got.Skipped[0].ServerID != servers[1] ||
		got.Skipped[0].Reason != rollbackSkipNoSuccessfulRun {
		t.Fatalf("响应应带逐台 skipped: %+v body=%s", got.Skipped, raw)
	}
	if got.Failed == nil {
		t.Fatalf("failed 应始终出现（可为空数组），body=%s", raw)
	}

	// ② 全部跳过：400 + invalid_argument，message 逐台列原因。
	stub.res = rollbackToResult{TargetReleaseID: target.ID, TargetVersion: "0.9.0",
		Skipped: []rollbackSkip{{ServerID: servers[0], Reason: rollbackSkipAlreadyCurrent}},
		Failed:  []rollbackFailure{}}
	stub.err = newRollbackRequestError(http.StatusBadRequest, codeInvalidArgument,
		"没有任何服务器可回滚到 0.9.0："+servers[0]+":"+rollbackSkipAlreadyCurrent)
	status, raw = env.request(http.MethodPost, "/api/v1/releases/"+cur.ID+"/rollback", jwt,
		map[string]any{"target_release_id": target.ID})
	if status != http.StatusBadRequest {
		t.Fatalf("全跳过应 400，实际 %d body=%s", status, raw)
	}
	code, message, _ := decodeErrorBody(t, raw)
	if code != codeInvalidArgument {
		t.Fatalf("全跳过 code 应为 %s，实际 %s", codeInvalidArgument, code)
	}
	if !strings.Contains(message, servers[0]+":"+rollbackSkipAlreadyCurrent) {
		t.Fatalf("400 文案应逐台列原因: %q", message)
	}

	// ③ 目标版本不存在：404 not_found。
	stub.err = newRollbackRequestError(http.StatusNotFound, codeNotFound, "目标版本不存在（target-not-found）")
	status, raw = env.request(http.MethodPost, "/api/v1/releases/"+cur.ID+"/rollback", jwt,
		map[string]any{"target_release_id": target.ID})
	if status != http.StatusNotFound {
		t.Fatalf("目标版本不存在应 404，实际 %d body=%s", status, raw)
	}
	if code, message, _ := decodeErrorBody(t, raw); code != codeNotFound || !strings.Contains(message, "target-not-found") {
		t.Fatalf("404 应带 not_found + target-not-found: code=%s message=%s", code, message)
	}
}
