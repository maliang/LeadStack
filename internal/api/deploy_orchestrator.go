package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
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

	if release.Strategy == store.ReleaseStrategyCanary && release.CanaryCount > 0 && len(runs) > release.CanaryCount {
		o.executeCanary(ctx, release, app, kind, content, artifact, runs, limit)
		o.refreshReleaseStatus(ctx, release.ID)
		return
	}

	o.runBatch(ctx, release, app, kind, content, artifact, runs, limit)
	o.refreshReleaseStatus(ctx, release.ID)
}

// executeCanary 执行灰度发布：先发第一批 → 观察 hold_seconds → 再发剩余批次。
//
// 停止条件（任一命中就不再发后续批次，剩余 run 标记为 canceled）：
//   - 第一批有失败/取消；
//   - 观察期内被取消（用户点了取消）；
//   - 观察结束后重查第一批，仍有非成功状态。
func (o *deployOrchestrator) executeCanary(ctx context.Context, release *store.Release, app *store.App,
	kind, content string, artifact *store.Artifact, runs []store.ReleaseRun, limit int) {

	count := release.CanaryCount
	if count > len(runs)-1 {
		count = len(runs) - 1
	}
	if count < 1 {
		count = 1
	}
	first := runs[:count]
	rest := runs[count:]

	o.logAppend(ctx, release, first, "system",
		fmt.Sprintf("=== 灰度发布：先执行 %d 台，观察 %d 秒后再发剩余 %d 台 ===\n", len(first), release.HoldSeconds, len(rest)))
	o.runBatch(ctx, release, app, kind, content, artifact, first, limit)

	if failed := o.failedRuns(ctx, first); len(failed) > 0 {
		o.cancelRuns(ctx, rest, "灰度批次失败，已停止后续批次")
		return
	}

	if release.HoldSeconds > 0 {
		o.logAppend(ctx, release, first, "system",
			fmt.Sprintf("=== 第一批成功，观察 %d 秒（可在观察期内取消）===\n", release.HoldSeconds))
		select {
		case <-time.After(time.Duration(release.HoldSeconds) * time.Second):
		case <-ctx.Done():
			o.cancelRuns(ctx, rest, "灰度观察期内被取消")
			return
		}
	}

	// 观察期结束再确认一次（期间可能被手动回滚/取消）。
	if failed := o.failedRuns(ctx, first); len(failed) > 0 {
		o.cancelRuns(ctx, rest, "灰度批次在观察期内转为失败，已停止后续批次")
		return
	}
	o.logAppend(ctx, release, first, "system", "=== 观察期结束，继续发布剩余批次 ===\n")
	o.runBatch(ctx, release, app, kind, content, artifact, rest, limit)
}

// runBatch 用给定并发上限执行一批 run（派发前检查 ctx 取消）。
func (o *deployOrchestrator) runBatch(ctx context.Context, release *store.Release, app *store.App,
	kind, content string, artifact *store.Artifact, runs []store.ReleaseRun, limit int) {

	if limit <= 0 {
		limit = deploy.DefaultConcurrency
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
dispatch:
	for _, run := range runs {
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
}

// failedRuns 返回给定 run 中已进入终态且非成功的那些。
func (o *deployOrchestrator) failedRuns(ctx context.Context, runs []store.ReleaseRun) []store.ReleaseRun {
	out := make([]store.ReleaseRun, 0, len(runs))
	for _, run := range runs {
		cur, err := o.st.GetReleaseRun(context.WithoutCancel(ctx), run.ReleaseID, run.ServerID)
		if err != nil {
			continue
		}
		if cur.Status == store.ReleaseStatusFailed || cur.Status == store.ReleaseStatusCanceled {
			out = append(out, *cur)
		}
	}
	return out
}

// cancelRuns 把一批仍 pending/running 的 run 标记为 canceled（灰度停止时用）。
func (o *deployOrchestrator) cancelRuns(ctx context.Context, runs []store.ReleaseRun, reason string) {
	cctx := context.WithoutCancel(ctx)
	for _, run := range runs {
		cur, err := o.st.GetReleaseRun(cctx, run.ReleaseID, run.ServerID)
		if err != nil {
			continue
		}
		if cur.Status != store.ReleaseStatusPending && cur.Status != store.ReleaseStatusRunning {
			continue
		}
		finishedAt := store.NowMS()
		msg := reason
		cur.Status = store.ReleaseStatusCanceled
		cur.Message = &msg
		cur.FinishedAt = &finishedAt
		if err := o.st.UpdateReleaseRun(cctx, *cur); err != nil {
			o.logger.Warn("标记灰度剩余批次取消失败", "release_run_id", cur.ID, "err", err)
		}
	}
}

// logAppend 往一批 run 的日志里追加一条系统日志（灰度阶段提示用）。
func (o *deployOrchestrator) logAppend(ctx context.Context, release *store.Release, runs []store.ReleaseRun, stream, chunk string) {
	cctx := context.WithoutCancel(ctx)
	for _, run := range runs {
		if _, err := o.st.AppendReleaseLog(cctx, run.ID, stream, chunk, store.NowMS()); err != nil {
			o.logger.Debug("写灰度日志失败", "release_run_id", run.ID, "err", err)
		}
	}
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
		port, hpath := healthTarget(app)
		// B-04：跳过健康检查不能静默，否则用户会误以为「配了端口就有保护」（A4）。
		// healthTarget 在没有端口时会连 path 一起返回空，因此这里用配置本身判定「缺哪一项」。
		if strings.TrimSpace(derefStr(app.HealthcheckPath)) == "" {
			logf("system", "跳过健康检查：未配置 healthcheck_path\n")
		} else if port <= 0 {
			logf("system", "跳过健康检查：healthcheck_port 与 site_port 均未配置\n")
		}
		if hpath != "" && port > 0 {
			srv, serr := o.st.GetServer(ctx, run.ServerID)
			if serr != nil {
				o.finishRun(ctx, run, store.ReleaseStatusFailed, -1, "读取服务器失败: "+serr.Error(), strPtrOrNil(deploy.StepHealthCheck), logf)
				return
			}
			healthURL := fmt.Sprintf("http://%s:%d%s", srv.Host, port, hpath)
			logf("system", "--- 健康检查 "+healthURL+" ---\n")
			if herr := waitHTTPHealthy(ctx, healthURL); herr != nil {
				reason := healthCheckReason(herr)
				logf("system", "健康检查失败: "+reason+"\n")
				o.rollbackAfterHealthFailure(ctx, run, plan, logf)
				var hce *healthCheckError
				if errors.As(herr, &hce) && hce.isConnectionFailure() {
					reason += healthCheckHint
				}
				o.finishRun(ctx, run, store.ReleaseStatusFailed, -1, "部署后健康检查失败: "+reason, strPtrOrNil(deploy.StepHealthCheck), logf)
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

// 回滚判定词表（唯一，英文机器码；PLAN「回滚判定词表」）。
//
// 前端只按码映射文案，禁止把中文当 reason 传输。
const (
	// 逐台 skipped：不需要或无法回滚，但都不算失败。
	rollbackSkipNoSuccessfulRun = "no-successful-run" // 目标版本在这台服务器上没有成功记录
	rollbackSkipAlreadyCurrent  = "already-current"   // 目标版本已是这台服务器的当前版本
	rollbackSkipMissingBackup   = "missing-backup"    // inplace：后继发布的备份文件缺失
	// 逐台 failed：真的执行过回滚但失败了。
	rollbackFailReason = "rollback-error"
)

// rollbackSkip 是「该服务器无需/无法回滚」的逐台原因（进 skipped，不算失败）。
type rollbackSkip struct {
	ServerID string `json:"server_id"`
	Reason   string `json:"reason"`
}

// rollbackFailure 是逐台回滚失败（进 failed）。
type rollbackFailure struct {
	ServerID string `json:"server_id"`
	Reason   string `json:"reason"`
	Message  string `json:"message"`
}

// rollbackToResult 是「回滚到指定版本」的执行结果。
type rollbackToResult struct {
	RolledBack      int               `json:"rolled_back"`
	TargetReleaseID string            `json:"target_release_id"`
	TargetVersion   string            `json:"-"`
	Skipped         []rollbackSkip    `json:"skipped"`
	Failed          []rollbackFailure `json:"failed"`
}

// skippedText 把 skipped 清单渲染成 `server_id:reason；…`（用于「全跳过」的 400 文案）。
func (r rollbackToResult) skippedText() string {
	parts := make([]string, 0, len(r.Skipped))
	for _, s := range r.Skipped {
		parts = append(parts, s.ServerID+":"+s.Reason)
	}
	return strings.Join(parts, "；")
}

// rollbackToError 是「回滚到指定版本」的**请求级**错误：整请求失败，不进 skipped（R6）。
type rollbackToError struct {
	// Status 是建议的 HTTP 状态码。
	Status int
	// Code 是错误体里的 code（invalid_argument / not_found）。
	Code    string
	Message string
}

func (e *rollbackToError) Error() string { return e.Message }

// newRollbackRequestError 构造请求级回滚错误。
func newRollbackRequestError(status int, code, message string) *rollbackToError {
	return &rollbackToError{Status: status, Code: code, Message: message}
}

// RollbackTo 回滚到**指定历史版本**（release 模式切 current，inplace 模式解回该版本的备份）。
//
// 与 Rollback（回上一版）的区别：目标版本由调用方指定，且必须在该服务器上有过成功发布。
// 脚本模式没有可回放的历史态（git 状态由脚本管理），直接拒绝并说明原因。
//
// 判定口径（R6/R7）：每台服务器要么进 rolled_back，要么进 skipped（无需/无法回滚，带唯一 reason），
// 要么进 failed（真的执行过但失败，带底层原因）。全部被跳过时返回请求级错误，message 逐台列原因。
func (o *deployOrchestrator) RollbackTo(releaseID, targetReleaseID string, serverIDs []string) (rollbackToResult, error) {
	res := rollbackToResult{
		TargetReleaseID: targetReleaseID,
		Skipped:         []rollbackSkip{},
		Failed:          []rollbackFailure{},
	}
	if o == nil {
		return res, errors.New("发布编排器未启用")
	}
	ctx := context.Background()
	release, err := o.st.GetRelease(ctx, releaseID)
	if err != nil {
		return res, err
	}
	app, err := o.st.GetApp(ctx, release.AppID)
	if err != nil {
		return res, err
	}
	target, err := o.st.GetRelease(ctx, targetReleaseID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return res, newRollbackRequestError(http.StatusNotFound, codeNotFound,
				"目标版本不存在（target-not-found）")
		}
		return res, err
	}
	res.TargetVersion = target.Version
	if target.AppID != app.ID {
		return res, newRollbackRequestError(http.StatusBadRequest, codeInvalidArgument,
			"目标版本不属于同一个应用（target-not-in-app）")
	}
	if target.Status != store.ReleaseStatusSuccess {
		return res, newRollbackRequestError(http.StatusBadRequest, codeInvalidArgument,
			"只能回滚到发布成功的版本（target-not-success）")
	}
	if app.DeployMode == store.DeployModeScript {
		return res, newRollbackRequestError(http.StatusBadRequest, codeInvalidArgument,
			"script 模式不支持回滚到指定版本，请在 rollback_script 中实现")
	}

	runs, err := o.st.ListReleaseRuns(ctx, releaseID)
	if err != nil {
		return res, err
	}
	targetRuns, err := o.st.ListReleaseRuns(ctx, targetReleaseID)
	if err != nil {
		return res, err
	}
	targetOK := map[string]bool{}
	for _, r := range targetRuns {
		if r.Status == store.ReleaseStatusSuccess {
			targetOK[r.ServerID] = true
		}
	}
	selected := map[string]bool{}
	for _, id := range serverIDs {
		selected[id] = true
	}
	candidates := make([]store.ReleaseRun, 0, len(runs))
	for _, run := range runs {
		if len(selected) > 0 && !selected[run.ServerID] {
			continue
		}
		candidates = append(candidates, run)
	}

	// inplace 模式：备份名是 backup-<releaseID>，存的是「该次发布之前」的状态。
	// 要回滚到版本 X，应恢复「X 之后、替换了 X 的那次发布」的备份；逐台判定（灰度会让服务器进度不同）。
	successor := map[string]string{}
	if app.DeployMode == store.DeployModeInplace {
		servers := make([]string, 0, len(candidates))
		for _, run := range candidates {
			servers = append(servers, run.ServerID)
		}
		successor = o.successorReleases(ctx, app.ID, targetReleaseID, servers)
	}

	for _, run := range candidates {
		if !targetOK[run.ServerID] {
			// 目标版本在这台服务器上没有成功记录：无从回滚（R6）。
			res.Skipped = append(res.Skipped, rollbackSkip{ServerID: run.ServerID, Reason: rollbackSkipNoSuccessfulRun})
			continue
		}
		backupReleaseID := ""
		if app.DeployMode == store.DeployModeInplace {
			backupReleaseID = successor[run.ServerID]
			if backupReleaseID == "" {
				// 目标就是该服务器上的最新成功版本：无需回滚。
				res.Skipped = append(res.Skipped, rollbackSkip{ServerID: run.ServerID, Reason: rollbackSkipAlreadyCurrent})
				continue
			}
			// 备份文件缺失时不能悄悄执行一条注定失败的恢复命令：直接给出 missing-backup（R6）。
			// 探测本身失败（如目标机离线）不在此判定，交给回滚执行阶段归类为 rollback-error。
			if exists, perr := o.inplaceBackupExists(ctx, app, run.ServerID, backupReleaseID); perr == nil && !exists {
				res.Skipped = append(res.Skipped, rollbackSkip{ServerID: run.ServerID, Reason: rollbackSkipMissingBackup})
				continue
			}
		}
		if rerr := o.rollbackToOne(ctx, release, app, target, run, backupReleaseID); rerr != nil {
			o.logger.Warn("回滚到指定版本失败", "release_run_id", run.ID, "target", targetReleaseID, "err", rerr)
			res.Failed = append(res.Failed, rollbackFailure{
				ServerID: run.ServerID, Reason: rollbackFailReason, Message: rerr.Error(),
			})
			continue
		}
		res.RolledBack++
	}
	if res.RolledBack > 0 {
		o.refreshReleaseStatus(ctx, releaseID)
	}
	if res.RolledBack == 0 && len(res.Skipped) > 0 {
		// 全跳过：请求级 400，message 必须逐台列出 server_id:reason（A10/R7）。
		version := res.TargetVersion
		if version == "" {
			version = targetReleaseID
		}
		return res, newRollbackRequestError(http.StatusBadRequest, codeInvalidArgument,
			fmt.Sprintf("没有任何服务器可回滚到 %s：%s", version, res.skippedText()))
	}
	return res, nil
}

// successorReleases 为每台服务器找出「目标版本之后**最早**的一次成功发布」的 id（inplace 备份口径）。
//
// 备份 backup-<releaseID> 存的是那次发布之前的目录状态，所以要恢复「替换了目标版本的那次发布」。
// 逐台判定：同一次发布可能只覆盖了部分服务器（灰度、重试），不能拿全局「下一次发布」一概而论。
// store 层没有按 (app_id, server_id) 的查询（本轮不改 store），这里用 ListReleases + ListReleaseRuns 组合，
// 服务端过滤到「目标版本之后」的成功发布，再逐台取第一个命中的那次。
func (o *deployOrchestrator) successorReleases(ctx context.Context, appID, targetReleaseID string, servers []string) map[string]string {
	out := map[string]string{}
	pending := make(map[string]bool, len(servers))
	for _, id := range servers {
		pending[id] = true
	}
	if len(pending) == 0 {
		return out
	}
	all, err := o.st.ListReleases(ctx, store.ReleaseFilter{
		AppID: appID, Status: store.ReleaseStatusSuccess, Limit: releaseListMax,
	})
	if err != nil {
		o.logger.Warn("读取发布历史失败，按无备份处理", "app_id", appID, "err", err)
		return out
	}
	// ListReleases 按时间倒序：这里从最早的一端往前扫，先跳过目标版本及其之前的发布，
	// 之后遇到的每一次发布都是「晚于目标版本」的，按由早到晚的顺序取每台服务器第一个命中的那次
	// （最早替换掉目标版本的那次，它的备份里才是目标版本的内容）。
	passedTarget := false
	for i := len(all) - 1; i >= 0; i-- {
		if !passedTarget {
			if all[i].ID == targetReleaseID {
				passedTarget = true
			}
			continue
		}
		if len(pending) == 0 {
			return out
		}
		rel := all[i]
		runs, rerr := o.st.ListReleaseRuns(ctx, rel.ID)
		if rerr != nil {
			o.logger.Warn("读取发布 run 失败，跳过该次发布", "release_id", rel.ID, "err", rerr)
			continue
		}
		for _, run := range runs {
			if !pending[run.ServerID] || run.Status != store.ReleaseStatusSuccess {
				continue
			}
			out[run.ServerID] = rel.ID
			delete(pending, run.ServerID)
		}
	}
	if !passedTarget {
		// 目标版本不在最近的成功发布窗口里：按「没有后继发布」处理（视为已是当前版本）。
		o.logger.Warn("目标版本不在成功发布窗口内，按无后继发布处理",
			"app_id", appID, "target_release_id", targetReleaseID)
	}
	return out
}

// inplaceBackupDirName / inplaceBackupExt 复刻 internal/deploy 的备份路径口径。
//
// internal/deploy 里的 inplaceBackupDir/inplaceBackupSuffix 未导出，且 deploy 不在本任务范围内，
// 因此这里镜像同样的常量；deploy_canary_rollback_test.go 用真实的恢复计划做交叉校验，防止两处漂移。
const (
	inplaceBackupDirName = ".leadstack-backups"
	inplaceBackupExt     = ".tar.gz"
)

// inplaceBackupPath 返回某次 inplace 发布在目标机上的备份文件绝对路径。
func inplaceBackupPath(workDir, releaseID string) string {
	return path.Join(workDir, inplaceBackupDirName, "backup-"+releaseID+inplaceBackupExt)
}

// inplaceBackupExists 在目标机上**只读**探测 inplace 备份是否存在。
//
// 返回 (false, nil) 表示明确「不存在」；(false, err) 表示探测没跑成（离线/取消等），
// 调用方应把它当作「情况未知」，交给回滚执行阶段给出 rollback-error。
func (o *deployOrchestrator) inplaceBackupExists(ctx context.Context, app *store.App, serverID, backupReleaseID string) (bool, error) {
	workDir := strings.TrimSpace(derefStr(app.TargetDir))
	if workDir == "" {
		workDir = strings.TrimSpace(derefStr(app.Root))
	}
	if workDir == "" {
		return false, errors.New("inplace 模式缺少 target_dir")
	}
	plan := deploy.Plan{
		ReleaseID: backupReleaseID,
		Root:      workDir,
		Steps: []deploy.Step{{
			Name:     "check-backup",
			Command:  "test -f " + shellQuote(inplaceBackupPath(workDir, backupReleaseID)),
			TimeoutS: 30,
		}},
	}
	outcome, err := o.exec.RunPlan(ctx, serverID, plan, deploy.Hooks{})
	if err != nil && outcome.Status == "" {
		return false, err
	}
	return outcome.Status == deploy.StatusSuccess, nil
}

// rollbackToOne 在单台服务器上回滚到指定版本。
func (o *deployOrchestrator) rollbackToOne(ctx context.Context, release *store.Release, app *store.App,
	target *store.Release, run store.ReleaseRun, backupReleaseID string) error {

	logf := func(stream, chunk string) {
		if _, err := o.st.AppendReleaseLog(context.WithoutCancel(ctx), run.ID, stream, chunk, store.NowMS()); err != nil {
			o.logger.Warn("写入回滚日志失败", "release_run_id", run.ID, "err", err)
		}
	}
	logf("system", fmt.Sprintf("=== 回滚到指定版本 %s（release %s）===\n", target.Version, target.ID))

	var plan deploy.Plan
	var err error
	switch app.DeployMode {
	case store.DeployModeInplace:
		workDir := strings.TrimSpace(derefStr(app.TargetDir))
		if workDir == "" {
			workDir = strings.TrimSpace(derefStr(app.Root))
		}
		restoreID := backupReleaseID
		if restoreID == "" {
			restoreID = target.ID
		}
		plan, err = deploy.BuildRestoreInplace(workDir, restoreID, derefStr(app.RestartCmd))
	default:
		// release 模式：用「目标版本 + 上一版本 = 目标」的方式生成回滚步骤，
		// 得到「切 current 到目标目录 + 跑目标版本的启动脚本」。
		kind := store.TemplateKindBinary
		if tmpl, terr := o.st.GetDeployTemplate(ctx, target.TemplateID); terr == nil {
			kind = tmpl.Kind
		}
		var artifact *store.Artifact
		if target.ArtifactID != nil {
			if art, aerr := o.st.GetArtifact(ctx, *target.ArtifactID); aerr == nil {
				artifact = art
			}
		}
		base, berr := o.buildPlanFn(
			deploy.Template{ID: target.TemplateID, Kind: deploy.Kind(kind), Content: target.TemplateSnapshot},
			o.toDeployArtifact(ctx, artifact),
			// Build 不允许 PrevReleaseID == ReleaseID，所以用「当前发布 ID」做名义发布、
			// 把 PrevReleaseID 指向目标版本；随后 buildRollbackFn 生成「切到目标目录 + 跑目标脚本」。
			deploy.BuildParams{
				AppSlug: app.Slug, ReleaseID: release.ID, Version: target.Version,
				Root: o.resolveRoot(app), PrevReleaseID: target.ID, Now: store.NowMS(),
			})
		if berr != nil {
			return fmt.Errorf("构建目标版本计划失败: %w", berr)
		}
		plan, err = o.buildRollbackFn(base)
	}
	if err != nil {
		return fmt.Errorf("构建回滚计划失败: %w", err)
	}

	// 与执行器一致：RollbackSteps 逆序执行；这里拿到的已是「正向」计划，直接按序执行。
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
	finishedAt := store.NowMS()
	status := store.ReleaseStatusSuccess
	if outcome.Status != "" {
		status = outcome.Status
	}
	message := fmt.Sprintf("已回滚到版本 %s", target.Version)
	if status != store.ReleaseStatusSuccess {
		message = fmt.Sprintf("回滚到 %s 失败：%s", target.Version, outcome.Message)
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

// RollbackTo 见 DeployOrchestratorHandle。
func (h *DeployOrchestratorHandle) RollbackTo(releaseID, targetReleaseID string, serverIDs []string) (rollbackToResult, error) {
	if h == nil || h.inner == nil {
		return rollbackToResult{}, errors.New("发布编排器未启用")
	}
	return h.inner.RollbackTo(releaseID, targetReleaseID, serverIDs)
}

// RunDiagnose 见 handler（只读体检）。
func (h *DeployOrchestratorHandle) RunDiagnose(ctx context.Context, serverID, script string) (string, error) {
	if h == nil || h.inner == nil {
		return "", fmt.Errorf("发布编排器未启用")
	}
	return h.inner.RunDiagnose(ctx, serverID, script)
}
