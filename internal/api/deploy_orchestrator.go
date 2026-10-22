package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/maliang/leadstack/internal/deploy"
	"github.com/maliang/leadstack/internal/store"

	"github.com/maliang/leadstack/internal/credential"
)

// 发布编排参数。
const (
	// deployRunTimeoutS 是单步命令的默认超时（模板可覆盖，见 deploy.Step.TimeoutS）。
	deployDefaultStepTimeoutS = 300
	// deployCancelGrace 是取消后等待执行协程收尾的时间。
	deployCancelGrace = 5 * time.Second
)

// deployOrchestrator 实现 DeployOrchestrator：异步按并发上限执行各 run，并汇总发布状态。
// planRunner 是「执行一个计划」的最小抽象（deploy.Executor 实现；测试可注入替身）。
type planRunner interface {
	RunPlan(ctx context.Context, serverID string, p deploy.Plan, hooks deploy.Hooks) (deploy.RunOutcome, error)
}

// 编译期确认 deploy.Executor 满足端口。
var _ planRunner = (*deploy.Executor)(nil)

type deployOrchestrator struct {
	cipher         *credential.Cipher
	artifactsDir   string
	panelFactory   PanelClientFactory
	st             *store.Store
	exec           planRunner
	logger         *slog.Logger
	maxConc        int
	public         string
	publicResolver func(ctx context.Context) (string, error)
	root           string

	buildPlanFn     func(tmpl deploy.Template, art deploy.Artifact, p deploy.BuildParams) (deploy.Plan, error)
	buildRollbackFn func(p deploy.Plan) (deploy.Plan, error)

	mu       sync.Mutex
	running  map[string]context.CancelFunc // releaseID → 取消函数
	rerun    map[string]bool               // releaseID → 收尾期间收到的新启动请求
	inflight sync.WaitGroup
}

// 编译期确认接口实现。
var _ DeployOrchestrator = (*deployOrchestrator)(nil)

// DeployOrchestratorOptions 是构造编排器的入参。
type DeployOrchestratorOptions struct {
	Store       *store.Store
	Runner      DeployRunner
	Concurrency int
	PublicURL   string
	DeployRoot  string
	Logger      *slog.Logger
	// PlanRunner 可注入替身（测试用）；为 nil 时用 deploy.NewExecutor 包装 Runner。
	PlanRunner planRunner
	// PublicURLResolver 实时解析控制台对外地址（settings > env > default）；
	// 为 nil 时使用 Options.PublicURL（静态值）。改设置后无需重启即生效。
	PublicURLResolver func(ctx context.Context) (string, error)
	// Cipher 用于解密宝塔面板凭据；provider=baota 的应用部署必需。
	Cipher *credential.Cipher
	// ArtifactsDir 是上传制品的落盘目录（provider=baota 时需要读取制品内容）。
	ArtifactsDir string
	// PanelFactory 构造宝塔面板客户端（测试可注入替身）；为 nil 时使用真实客户端。
	PanelFactory PanelClientFactory
	// PlanBuilder / RollbackBuilder 可注入替身（测试用）；默认为 deploy.Build / deploy.Rollback。
	PlanBuilder     func(tmpl deploy.Template, art deploy.Artifact, p deploy.BuildParams) (deploy.Plan, error)
	RollbackBuilder func(p deploy.Plan) (deploy.Plan, error)
}

// NewDeployOrchestrator 创建发布编排器；依赖不足时返回 nil（创建发布仍可落库）。
func NewDeployOrchestrator(opts DeployOrchestratorOptions) *DeployOrchestratorHandle {
	inner := newDeployOrchestrator(opts.Store, opts.Runner, opts)
	if inner == nil {
		return nil
	}
	return &DeployOrchestratorHandle{inner: inner}
}

// DeployOrchestratorHandle 是编排器的对外句柄（隐藏内部字段，同时提供 Close）。
type DeployOrchestratorHandle struct {
	inner *deployOrchestrator
}

// 编译期确认接口实现。
var _ DeployOrchestrator = (*DeployOrchestratorHandle)(nil)

// Start 见 DeployOrchestrator（nil 句柄安全：未装配编排器时静默跳过）。
func (h *DeployOrchestratorHandle) Start(releaseID string) {
	if h == nil || h.inner == nil {
		return
	}
	h.inner.Start(releaseID)
}

// Retry 见 DeployOrchestrator。
func (h *DeployOrchestratorHandle) Retry(releaseID string, serverIDs []string) (int, error) {
	if h == nil || h.inner == nil {
		return 0, errors.New("发布编排器未启用")
	}
	return h.inner.Retry(releaseID, serverIDs)
}

// Rollback 见 DeployOrchestrator。
func (h *DeployOrchestratorHandle) Rollback(releaseID string, serverIDs []string) (int, error) {
	if h == nil || h.inner == nil {
		return 0, errors.New("发布编排器未启用")
	}
	return h.inner.Rollback(releaseID, serverIDs)
}

// Cancel 见 DeployOrchestrator。
func (h *DeployOrchestratorHandle) Cancel(releaseID string, serverIDs []string) (int, error) {
	if h == nil || h.inner == nil {
		return 0, errors.New("发布编排器未启用")
	}
	return h.inner.Cancel(releaseID, serverIDs)
}

// Close 等待在途发布收尾。
func (h *DeployOrchestratorHandle) Close() {
	if h == nil || h.inner == nil {
		return
	}
	h.inner.Close()
}

// newDeployOrchestrator 创建编排器。
func newDeployOrchestrator(st *store.Store, runner DeployRunner, opts DeployOrchestratorOptions) *deployOrchestrator {
	// 允许只注入 PlanRunner（测试场景不需要真实 Runner）。
	if st == nil || (runner == nil && opts.PlanRunner == nil) {
		return nil
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 4
	}
	exec := opts.PlanRunner
	if exec == nil {
		exec = deploy.NewExecutor(runner, deploy.ExecutorOptions{Concurrency: concurrency, Logger: logger})
	}
	_ = runner
	buildFn := opts.PlanBuilder
	if buildFn == nil {
		buildFn = deploy.Build
	}
	rollbackFn := opts.RollbackBuilder
	if rollbackFn == nil {
		rollbackFn = deploy.Rollback
	}
	return &deployOrchestrator{
		st: st, exec: exec, logger: logger, maxConc: concurrency,
		public: strings.TrimSuffix(opts.PublicURL, "/"), publicResolver: opts.PublicURLResolver,
		root:         opts.DeployRoot,
		cipher:       opts.Cipher,
		artifactsDir: opts.ArtifactsDir,
		panelFactory: opts.PanelFactory,
		buildPlanFn:  buildFn, buildRollbackFn: rollbackFn,
		running: map[string]context.CancelFunc{},
		rerun:   map[string]bool{},
	}
}

// Start 异步执行一次发布（只处理 pending 的 run）。
//
// 并发语义：同一个 releaseID 任意时刻最多一个执行协程。
// 若已有执行在途（例如 retry 在上一轮收尾的瞬间到达），**不能静默忽略** ——
// retry 会把 failed 的 run 重置为 pending，一旦丢掉这次启动请求，那些 run 就永远停在 pending
// （此时 release 状态已是终态，没有任何人再推进它）。因此这里登记「收尾后重跑」，
// 由在途协程在释放占位后立即再次调度，保证**不丢唤醒、且不出现两个协程同时汇总同一发布**。
func (o *deployOrchestrator) Start(releaseID string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	if _, exists := o.running[releaseID]; exists {
		o.rerun[releaseID] = true
		o.mu.Unlock()
		o.logger.Debug("发布已在执行中，登记收尾后重跑", "release_id", releaseID)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	o.running[releaseID] = cancel
	o.mu.Unlock()

	o.inflight.Add(1)
	go func() {
		defer o.inflight.Done()
		// 收尾顺序：runRelease 内部会做最终状态汇总；随后释放占位并检查是否有新的启动请求。
		// 占位在汇总之后释放，保证同一发布不会有两个协程同时写状态（避免状态回退）。
		defer func() {
			o.mu.Lock()
			delete(o.running, releaseID)
			again := o.rerun[releaseID]
			delete(o.rerun, releaseID)
			o.mu.Unlock()
			cancel()
			if again {
				o.logger.Debug("执行期间收到新的启动请求，立即再次执行", "release_id", releaseID)
				o.Start(releaseID)
			}
		}()
		o.runRelease(ctx, releaseID)
	}()
}

// runRelease 执行一次发布的所有 pending run，并汇总状态。
func (o *deployOrchestrator) runRelease(ctx context.Context, releaseID string) {
	release, err := o.st.GetRelease(ctx, releaseID)
	if err != nil {
		o.logger.Warn("发布不存在，无法执行", "release_id", releaseID, "err", err)
		return
	}
	// 防御性重试：retry 可能在上一轮「写状态之后、释放占位之前」重置出新的 pending run。
	// 这里在收尾前再取一次，避免这类时序把 pending 留给没人推进的状态（最多补跑 2 轮）。
	for round := 0; round < 3; round++ {
		runs, err := o.st.ListReleaseRunsByStatus(ctx, releaseID, store.ReleaseStatusPending)
		if err != nil {
			o.logger.Warn("读取待执行发布任务失败", "release_id", releaseID, "err", err)
			return
		}
		if len(runs) == 0 {
			if round == 2 || o.noPendingAfterWait(ctx, releaseID) {
				o.refreshReleaseStatus(ctx, releaseID)
				return
			}
			continue
		}
		o.executeReleaseRuns(ctx, release, runs)
	}
	o.refreshReleaseStatus(ctx, releaseID)
}

// noPendingAfterWait 在收尾前再确认一次是否真的没有 pending run（抵消极短时序抖动）。
func (o *deployOrchestrator) noPendingAfterWait(ctx context.Context, releaseID string) bool {
	runs, err := o.st.ListReleaseRunsByStatus(ctx, releaseID, store.ReleaseStatusPending)
	if err != nil {
		return true
	}
	return len(runs) == 0
}

// executeReleaseRuns 执行一批 pending run（原有单轮执行逻辑）。
func (o *deployOrchestrator) executeReleaseRuns(ctx context.Context, release *store.Release, runs []store.ReleaseRun) {

	startedAt := store.NowMS()
	if err := o.st.UpdateReleaseStatus(ctx, release.ID, store.ReleaseStatusRunning, &startedAt, nil); err != nil {
		o.logger.Warn("更新发布状态失败", "release_id", release.ID, "err", err)
	}

	app, err := o.st.GetApp(ctx, release.AppID)
	if err != nil {
		o.failAll(ctx, release.ID, "应用不存在: "+err.Error())
		return
	}
	tmpl, err := o.st.GetDeployTemplate(ctx, release.TemplateID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		o.failAll(ctx, release.ID, "读取模板失败: "+err.Error())
		return
	}

	// 模板快照优先：即使模板已被删除或修改，本次发布仍按创建时的内容执行。
	content := release.TemplateSnapshot
	kind := store.TemplateKindBinary
	if tmpl != nil {
		kind = tmpl.Kind
	}
	var artifact *store.Artifact
	if release.ArtifactID != nil {
		artifact, err = o.st.GetArtifact(ctx, *release.ArtifactID)
		if err != nil {
			o.failAll(ctx, release.ID, "制品不存在: "+err.Error())
			return
		}
	}

	// 并发上限取「发布自己的 concurrency」与「全局上限」的较小值。
	limit := release.Concurrency
	if limit <= 0 || limit > o.maxConc {
		limit = o.maxConc
	}
	sem := make(chan struct{}, limit)

	var wg sync.WaitGroup
dispatch:
	for _, run := range runs {
		// 取消后不再为剩余 run 起协程（原来 select 里的 break 只跳出 select，语义是错的）。
		if ctx.Err() != nil {
			break dispatch
		}
		run := run
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			o.runOne(ctx, release, app, kind, content, artifact, run)
		}()
	}
	wg.Wait()
	o.refreshReleaseStatus(ctx, release.ID)
}

// runOne 在单台服务器上执行一次发布。
func (o *deployOrchestrator) runOne(ctx context.Context, release *store.Release, app *store.App,
	kind, templateContent string, artifact *store.Artifact, run store.ReleaseRun) {

	nowMS := store.NowMS()
	run.Status = store.ReleaseStatusRunning
	run.StartedAt = &nowMS
	run.Message = nil
	run.FailedStep = nil
	if err := o.st.UpdateReleaseRun(ctx, run); err != nil {
		o.logger.Warn("更新发布任务状态失败", "release_run_id", run.ID, "err", err)
	}

	logf := func(stream, chunk string) {
		if _, err := o.st.AppendReleaseLog(context.WithoutCancel(ctx), run.ID, stream, chunk, store.NowMS()); err != nil {
			o.logger.Warn("写入发布日志失败", "release_run_id", run.ID, "err", err)
		}
	}
	logf("system", fmt.Sprintf("=== 开始在服务器 %s 上部署应用 %s v%s ===\n", run.ServerID, app.Slug, release.Version))

	// 宝塔面板通道：与 SSH/agent 的 shell 计划并列，走面板 API 完成建站与解压。
	if app.Provider == store.AppProviderBaota {
		o.runOneBaota(ctx, release, app, artifact, run, logf)
		return
	}

	// 上一成功版本：用于回滚与占位符。
	prev, err := o.previousSuccessRelease(ctx, release, run.ServerID)
	if err != nil {
		o.logger.Warn("查询上一成功发布失败", "release_id", release.ID, "err", err)
	}
	if prev != "" {
		run.PrevReleaseID = &prev
	}

	root := o.resolveRoot(app)
	plan, err := o.buildPlanFor(ctx, templateContent, kind, artifact, release, app, root, prev, logf)
	if err != nil {
		o.finishRun(ctx, run, store.ReleaseStatusFailed, -1, "构建执行计划失败: "+err.Error(), nil, logf)
		return
	}

	// 步骤与日志回调：日志**边执行边落库**。
	hooks := deploy.Hooks{
		OnStep: func(index int, name string) {
			logf("system", fmt.Sprintf("--- [%d/%d] %s ---\n", index+1, len(plan.Steps), name))
		},
		OnLog: logf,
		OnStatus: func(status, message string) {
			o.logger.Debug("发布任务状态回调", "release_run_id", run.ID, "status", status, "message", message)
		},
	}

	runCtx := withDeployRun(ctx, run.ID)
	outcome, execErr := o.exec.RunPlan(runCtx, run.ServerID, plan, hooks)
	if execErr != nil && outcome.Status == "" {
		o.finishRun(ctx, run, store.ReleaseStatusFailed, -1, "执行失败: "+execErr.Error(), nil, logf)
		return
	}

	status := store.ReleaseStatusSuccess
	if outcome.Status != "" {
		status = outcome.Status
	}
	exitCode := outcome.ExitCode
	var failedStep *string
	if outcome.FailedStep != "" {
		failedStep = &outcome.FailedStep
	}
	message := outcome.Message
	if message == "" && status == store.ReleaseStatusSuccess {
		message = "部署成功"
	}
	if status != store.ReleaseStatusSuccess && message == "" && execErr != nil {
		message = execErr.Error()
	}
	// 部署后健康检查（builtin 通道；baota 通道在 runOneBaota 内部完成）。
	if status == store.ReleaseStatusSuccess {
		if port, hpath := healthTarget(app); hpath != "" && port > 0 {
			logf("system", fmt.Sprintf("--- 健康检查 http://<host>:%d%s ---\n", port, hpath))
			srv, serr := o.st.GetServer(ctx, run.ServerID)
			if serr != nil {
				o.finishRun(ctx, run, store.ReleaseStatusFailed, -1, "读取服务器失败: "+serr.Error(), strPtrOrNil(deploy.StepHealthCheck), logf)
				return
			}
			if herr := waitHTTPHealthy(ctx, fmt.Sprintf("http://%s:%d%s", srv.Host, port, hpath)); herr != nil {
				logf("system", "健康检查失败，执行回滚: "+herr.Error()+"\n")
				o.rollbackAfterHealthFailure(ctx, run, plan, logf)
				o.finishRun(ctx, run, store.ReleaseStatusFailed, -1, "部署后健康检查失败: "+herr.Error(), strPtrOrNil(deploy.StepHealthCheck), logf)
				return
			}
			logf("system", "健康检查通过\n")
		}
	}

	o.finishRun(ctx, run, status, exitCode, message, failedStep, logf)
}

// finishRun 落库 run 的最终结果。
func (o *deployOrchestrator) finishRun(ctx context.Context, run store.ReleaseRun,
	status string, exitCode int, message string, failedStep *string, logf func(stream, chunk string)) {

	finishedAt := store.NowMS()
	run.Status = status
	run.ExitCode = &exitCode
	run.FinishedAt = &finishedAt
	if strings.TrimSpace(message) != "" {
		run.Message = &message
	}
	run.FailedStep = failedStep

	if status == store.ReleaseStatusSuccess {
		logf("system", "=== 部署成功 ===\n")
	} else {
		logf("system", fmt.Sprintf("=== 部署失败：%s ===\n", message))
	}
	if err := o.st.UpdateReleaseRun(context.WithoutCancel(ctx), run); err != nil {
		o.logger.Warn("写入发布任务结果失败", "release_run_id", run.ID, "err", err)
	}
}

// refreshReleaseStatus 依据各 run 状态汇总并更新发布整体状态。
func (o *deployOrchestrator) refreshReleaseStatus(ctx context.Context, releaseID string) {
	status, err := o.st.SummarizeReleaseStatus(ctx, releaseID)
	if err != nil {
		o.logger.Warn("汇总发布状态失败", "release_id", releaseID, "err", err)
		return
	}
	var finishedAt *int64
	if status == store.ReleaseStatusSuccess || status == store.ReleaseStatusFailed || status == store.ReleaseStatusCanceled {
		ts := store.NowMS()
		finishedAt = &ts
	}
	if err := o.st.UpdateReleaseStatus(context.WithoutCancel(ctx), releaseID, status, nil, finishedAt); err != nil {
		o.logger.Warn("更新发布状态失败", "release_id", releaseID, "err", err)
		return
	}
	o.logger.Info("发布状态已更新", "release_id", releaseID, "status", status)
}

// failAll 在编排前置条件不满足时把该发布所有未完成 run 置为 failed。
func (o *deployOrchestrator) failAll(ctx context.Context, releaseID, reason string) {
	runs, err := o.st.ListReleaseRuns(ctx, releaseID)
	if err != nil {
		o.logger.Warn("读取发布任务失败", "release_id", releaseID, "err", err)
		return
	}
	for _, run := range runs {
		if run.Status != store.ReleaseStatusPending && run.Status != store.ReleaseStatusRunning {
			continue
		}
		missing := -1
		message := reason
		finishedAt := store.NowMS()
		run.Status = store.ReleaseStatusFailed
		run.ExitCode = &missing
		run.Message = &message
		run.FinishedAt = &finishedAt
		if err := o.st.UpdateReleaseRun(ctx, run); err != nil {
			o.logger.Warn("标记发布任务失败出错", "release_run_id", run.ID, "err", err)
		}
		if _, err := o.st.AppendReleaseLog(ctx, run.ID, "system", "=== 编排失败："+reason+" ===\n", store.NowMS()); err != nil {
			o.logger.Debug("写入编排失败日志出错", "err", err)
		}
	}
	o.refreshReleaseStatus(ctx, releaseID)
}

// Retry 重跑 failed 的 run。
func (o *deployOrchestrator) Retry(releaseID string, serverIDs []string) (int, error) {
	if o == nil {
		return 0, errors.New("发布编排器未启用")
	}
	ctx := context.Background()
	if len(serverIDs) == 0 {
		failed, err := o.st.ListReleaseRunsByStatus(ctx, releaseID, store.ReleaseStatusFailed)
		if err != nil {
			return 0, err
		}
		for _, run := range failed {
			serverIDs = append(serverIDs, run.ServerID)
		}
	}
	n, err := o.st.ResetReleaseRunsForRetry(ctx, releaseID, serverIDs)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		o.Start(releaseID)
	}
	return n, nil
}

// Rollback 回滚指定服务器（为空表示全部成功的 run）。
func (o *deployOrchestrator) Rollback(releaseID string, serverIDs []string) (int, error) {
	if o == nil {
		return 0, errors.New("发布编排器未启用")
	}
	ctx := context.Background()
	release, err := o.st.GetRelease(ctx, releaseID)
	if err != nil {
		return 0, err
	}
	app, err := o.st.GetApp(ctx, release.AppID)
	if err != nil {
		return 0, err
	}
	runs, err := o.st.ListReleaseRuns(ctx, releaseID)
	if err != nil {
		return 0, err
	}

	// 回滚虽然不下载制品，但 deploy.Build 会校验 Kind/Artifact，必须传真实值。
	kind := store.TemplateKindBinary
	if tmpl, terr := o.st.GetDeployTemplate(ctx, release.TemplateID); terr == nil {
		kind = tmpl.Kind
	}
	var artifact *store.Artifact
	if release.ArtifactID != nil {
		if art, aerr := o.st.GetArtifact(ctx, *release.ArtifactID); aerr == nil {
			artifact = art
		}
	}

	selected := map[string]bool{}
	for _, id := range serverIDs {
		selected[id] = true
	}

	rolled := 0
	for _, run := range runs {
		if run.Status != store.ReleaseStatusSuccess {
			continue
		}
		if len(selected) > 0 && !selected[run.ServerID] {
			continue
		}
		if run.PrevReleaseID == nil || *run.PrevReleaseID == "" {
			o.logger.Warn("该发布没有上一成功版本，无法回滚", "release_run_id", run.ID, "server_id", run.ServerID)
			continue
		}
		if err := o.rollbackOne(ctx, release, app, kind, artifact, run); err != nil {
			o.logger.Warn("回滚失败", "release_run_id", run.ID, "err", err)
			continue
		}
		rolled++
	}
	if rolled > 0 {
		o.refreshReleaseStatus(ctx, releaseID)
	}
	return rolled, nil
}

// rollbackOne 在单台服务器上执行回滚（current 指回上一版本）。
func (o *deployOrchestrator) rollbackOne(ctx context.Context, release *store.Release, app *store.App,
	kind string, artifact *store.Artifact, run store.ReleaseRun) error {
	tmplContent := release.TemplateSnapshot
	prev := *run.PrevReleaseID
	root := o.resolveRoot(app)

	// 用真实 Kind 与制品生成基础计划（通过 Build 的校验），再取回滚步骤把 current 指回 prev。
	params := deploy.BuildParams{
		AppSlug: app.Slug, ReleaseID: release.ID, Version: release.Version,
		Root: root, PrevReleaseID: prev, Now: store.NowMS(),
	}
	basePlan, err := o.buildPlanFn(
		deploy.Template{ID: release.TemplateID, Kind: deploy.Kind(kind), Content: tmplContent},
		o.toDeployArtifact(ctx, artifact), params)
	if err != nil {
		return fmt.Errorf("构建回滚计划失败: %w", err)
	}
	plan, err := o.buildRollbackFn(basePlan)
	if err != nil {
		return fmt.Errorf("生成回滚计划失败: %w", err)
	}

	logf := func(stream, chunk string) {
		if _, err := o.st.AppendReleaseLog(context.WithoutCancel(ctx), run.ID, stream, chunk, store.NowMS()); err != nil {
			o.logger.Warn("写入回滚日志失败", "release_run_id", run.ID, "err", err)
		}
	}
	logf("system", fmt.Sprintf("=== 开始回滚到上一版本 %s ===\n", prev))

	hooks := deploy.Hooks{
		OnStep: func(index int, name string) {
			logf("system", fmt.Sprintf("--- [回滚 %d/%d] %s ---\n", index+1, len(plan.Steps), name))
		},
		OnLog: logf,
	}
	outcome, execErr := o.exec.RunPlan(withDeployRun(ctx, run.ID), run.ServerID, plan, hooks)
	if execErr != nil && outcome.Status == "" {
		logf("system", "=== 回滚失败："+execErr.Error()+" ===\n")
		return execErr
	}

	// 回滚结果写回该 run：状态按回滚结果、message 说明已回滚到哪个版本。
	finishedAt := store.NowMS()
	status := store.ReleaseStatusSuccess
	if outcome.Status != "" {
		status = outcome.Status
	}
	message := fmt.Sprintf("已回滚到上一版本 %s", prev)
	if status != store.ReleaseStatusSuccess {
		message = fmt.Sprintf("回滚失败：%s", outcome.Message)
	}
	exitCode := outcome.ExitCode
	run.Status = status
	run.ExitCode = &exitCode
	run.Message = &message
	run.FinishedAt = &finishedAt
	if err := o.st.UpdateReleaseRun(context.WithoutCancel(ctx), run); err != nil {
		return err
	}
	logf("system", "=== "+message+" ===\n")
	return nil
}

// Cancel 取消仍 pending/running 的 run。
func (o *deployOrchestrator) Cancel(releaseID string, serverIDs []string) (int, error) {
	if o == nil {
		return 0, errors.New("发布编排器未启用")
	}
	o.mu.Lock()
	cancel := o.running[releaseID]
	o.mu.Unlock()
	if cancel != nil && len(serverIDs) == 0 {
		cancel() // 整发布取消
	}
	n, err := o.st.CancelPendingRuns(context.Background(), releaseID, serverIDs, store.NowMS())
	if err != nil {
		return 0, err
	}
	o.refreshReleaseStatus(context.Background(), releaseID)
	return n, nil
}

// Close 等待在途发布收尾（优雅关闭；不做强制中断，避免部署到一半被打断）。
func (o *deployOrchestrator) Close() {
	if o == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		o.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(deployCancelGrace):
		o.logger.Warn("有发布仍在执行，优雅关闭不再等待")
	}
}

// previousSuccessRelease 返回「同一应用、同一服务器上最近一次成功的发布 ID」。
func (o *deployOrchestrator) previousSuccessRelease(ctx context.Context, release *store.Release, serverID string) (string, error) {
	const query = `SELECT r.id FROM releases r
	                 JOIN release_runs rr ON rr.release_id = r.id
	                WHERE r.app_id = ? AND rr.server_id = ? AND rr.status = ?
	                  AND r.created_at < ?
	                ORDER BY r.created_at DESC LIMIT 1`
	var id string
	err := o.st.DB().QueryRowContext(ctx, query, release.AppID, serverID, store.ReleaseStatusSuccess, release.CreatedAt).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return id, nil
}

// resolveRoot 返回传给 deploy.Build 的**部署根**。
//
// 注意：应用根由 deploy.Build 自己拼成 <Root>/<AppSlug>（见 internal/deploy 的说明），
// 这里**不能再拼一次 slug**，否则会变成 <root>/<slug>/<slug>。
// 应用自带 root 时以它作为该应用的部署根。
func (o *deployOrchestrator) resolveRoot(app *store.App) string {
	if app.Root != nil && strings.TrimSpace(*app.Root) != "" {
		return strings.TrimSuffix(strings.TrimSpace(*app.Root), "/")
	}
	base := strings.TrimSuffix(o.root, "/")
	if base == "" {
		base = "/opt/leadstack/apps"
	}
	return base
}

// resolvePublic 返回当前生效的控制台对外地址（实时读设置，改完立即生效）。
func (o *deployOrchestrator) resolvePublic(ctx context.Context) string {
	if o.publicResolver != nil {
		if value, err := o.publicResolver(ctx); err == nil && strings.TrimSpace(value) != "" {
			return strings.TrimSuffix(strings.TrimSpace(value), "/")
		}
	}
	return o.public
}

// toDeployArtifact 把存储模型转成 deploy.Artifact。
//
// upload 制品的 Location 必须是**绝对 http(s) URL**（契约要求），由 LEADSTACK_PUBLIC_URL 拼出。
func (o *deployOrchestrator) toDeployArtifact(ctx context.Context, artifact *store.Artifact) deploy.Artifact {
	if artifact == nil {
		return deploy.Artifact{}
	}
	art := deploy.Artifact{Kind: artifact.Kind, SizeBytes: artifact.SizeBytes}
	if artifact.SHA256 != nil {
		art.SHA256 = *artifact.SHA256
	}
	switch artifact.Kind {
	case store.ArtifactKindUpload:
		// 目标机没有 JWT，下载地址必须带签名（短时效），否则 curl 会被 401。
		if signed, err := SignedArtifactURL(ctx, o.st, artifact.ID); err == nil {
			art.Location = signed
		} else {
			art.Location = fmt.Sprintf("%s/api/v1/artifacts/%s/download", o.resolvePublic(ctx), artifact.ID)
		}
	default:
		art.Location = artifact.Location
	}
	return art
}

// buildPlan 组装 deploy.Build 的入参并生成计划。
func (o *deployOrchestrator) buildPlan(ctx context.Context, templateContent, kind string,
	artifact *store.Artifact, release *store.Release, app *store.App, root, prevReleaseID string) (deploy.Plan, error) {

	art := o.toDeployArtifact(ctx, artifact)
	params := deploy.BuildParams{
		AppSlug:       app.Slug,
		ReleaseID:     release.ID,
		Version:       release.Version,
		Root:          root,
		PrevReleaseID: prevReleaseID,
		Now:           store.NowMS(),
	}
	return o.buildPlanFn(deploy.Template{ID: release.TemplateID, Kind: deploy.Kind(kind), Content: templateContent}, art, params)
}

// Preview 见 previewer（预览只读，不落库、不执行）。
func (h *DeployOrchestratorHandle) Preview(ctx context.Context, app *store.App, tmpl *store.DeployTemplate,
	artifact *store.Artifact, version string, servers []*store.Server) (*PreviewResult, error) {
	if h == nil || h.inner == nil {
		return nil, fmt.Errorf("发布编排器未启用")
	}
	return h.inner.Preview(ctx, app, tmpl, artifact, version, servers)
}
