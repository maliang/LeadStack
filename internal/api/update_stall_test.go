package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestUpgradePanic 验证升级协程 panic 被 recover：任务转失败，但进程与并发锁都安全。
//
// 变异检验：去掉 runUpgradeGuarded 里的 recover，本用例会因 panic 逃逸直接失败；
// 去掉 failUpgrade，isRunning 会保持 true，用例同样变红。
func TestUpgradePanic(t *testing.T) {
	env := newTestEnv(t)
	a := env.api

	job := &upgradeJob{
		JobID: "panic-job", Phase: "validating", TargetVersion: "v0.9.9",
		FromVersion: a.version, StartedAt: a.nowMS(), PhaseStartedAt: a.nowMS(),
	}
	if !a.upgrader.begin() {
		t.Fatal("测试前置失败：upgrader 已被占用")
	}
	a.upgrader.setJob(job)

	// 同步执行（不是 go）：recover 只对同一 goroutine 生效，正好直接验证兜底逻辑。
	a.runUpgradeGuarded(job, func() { panic("boom") })

	snap := a.upgrader.getJob()
	if snap == nil || snap.JobID != job.JobID {
		t.Fatalf("panic 后任务快照丢失: %+v", snap)
	}
	if !snap.Done {
		t.Fatal("panic 后任务必须置为 Done，否则前端会永远轮询")
	}
	if snap.Phase != "internal" {
		t.Fatalf("panic 应记录为 internal 阶段，实际 %q", snap.Phase)
	}
	if !strings.Contains(snap.Error, "异常退出") {
		t.Fatalf("错误文案应说明协程异常退出，实际 %q", snap.Error)
	}
	if a.upgrader.isRunning() {
		t.Fatal("panic 后必须释放并发锁，否则后续升级永远 409")
	}
}

// TestUpgradeStall 验证阶段停滞看门狗：超阈值即判失败、释放锁，状态接口暴露停滞信息。
//
// 变异检验：把 UpgradeStallTimeout 调大（或删掉 elapsed 判断），本用例会在 2s 内
// 等不到任务结束而变红；这就是「看门狗真的在按阈值判定」的证据。
func TestUpgradeStall(t *testing.T) {
	env := newTestEnv(t)
	a := env.api
	a.cfg.UpgradeStallTimeout = 200 * time.Millisecond

	now := a.nowMS()
	job := &upgradeJob{
		JobID: "stall-job", Phase: "backup", TargetVersion: "v0.9.9",
		FromVersion: a.version, StartedAt: now - 1000, PhaseStartedAt: now - 1000,
	}
	if !a.upgrader.begin() {
		t.Fatal("测试前置失败：upgrader 已被占用")
	}
	a.upgrader.setJob(job)

	done := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		a.watchUpgradeStall(job, stop)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(stop)
		t.Fatal("看门狗未在 2s 内判定停滞（阈值未生效）")
	}

	snap := a.upgrader.getJob()
	if snap == nil || !snap.Done {
		t.Fatalf("停滞必须把任务置为 Done: %+v", snap)
	}
	if snap.Phase != "stalled" {
		t.Fatalf("停滞阶段应为 stalled，实际 %q", snap.Phase)
	}
	if !strings.Contains(snap.Error, "停滞") || !strings.Contains(snap.Error, "backup") {
		t.Fatalf("停滞错误应同时给出原因与卡住的阶段，实际 %q", snap.Error)
	}
	if !snap.Stalled {
		t.Fatal("状态快照必须标记 stalled=true")
	}
	if a.upgrader.isRunning() {
		t.Fatal("判定停滞必须释放并发锁，否则下次升级永远 409")
	}

	// 状态接口必须真的暴露停滞信息（而不是只存在内部结构体里）。
	jwt := env.login()
	status, raw := env.request(http.MethodGet, "/api/v1/system/update/status", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("状态接口期望 200，实际 %d（body=%s）", status, raw)
	}
	var envelope struct {
		Data struct {
			StallTimeoutS *int64 `json:"stall_timeout_s"`
			Job           struct {
				Phase         string `json:"phase"`
				Stalled       bool   `json:"stalled"`
				PhaseElapsedS int64  `json:"phase_elapsed_s"`
			} `json:"job"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("解析状态响应失败: %v（body=%s）", err, raw)
	}
	if envelope.Data.StallTimeoutS == nil {
		t.Fatal("状态响应缺少 stall_timeout_s")
	}
	if envelope.Data.Job.Phase != "stalled" || !envelope.Data.Job.Stalled {
		t.Fatalf("状态响应未暴露停滞信息: %+v", envelope.Data.Job)
	}
	if envelope.Data.Job.PhaseElapsedS < 0 {
		t.Fatalf("phase_elapsed_s 不能为负: %d", envelope.Data.Job.PhaseElapsedS)
	}
}

// TestUpgradeSetPhaseAfterDoneKeepsTerminalEvidence 覆盖 M2：任务 Done 之后，
// 任何迟到的 setPhase（如备份返回后的 "prepare"）都不得改写 phase/stalled/error。
//
// 变异检验：去掉 setPhase 里的 !u.job.Done 守卫，phase 会从 stalled 变成 swap，
// 用户看到的失败证据与真实结局不一致。
func TestUpgradeSetPhaseAfterDoneKeepsTerminalEvidence(t *testing.T) {
	env := newTestEnv(t)
	a := env.api

	now := a.nowMS()
	job := &upgradeJob{
		JobID: "done-job", Phase: "backup", TargetVersion: "v0.9.9",
		FromVersion: a.version, StartedAt: now, PhaseStartedAt: now,
	}
	if !a.upgrader.begin() {
		t.Fatal("测试前置失败：upgrader 已被占用")
	}
	a.upgrader.setJob(job)

	// 模拟停滞看门狗：failUpgrade 先 setPhase 再 finish，产物是 Done 的终态证据。
	a.failUpgrade(job, "stalled", errors.New("升级在阶段 backup 停滞超过 100ms，已判定失败"))
	snap := a.upgrader.getJob()
	if snap == nil || !snap.Done {
		t.Fatalf("前置失败：任务未被置为终态 %+v", snap)
	}
	wantPhase := snap.Phase
	wantError := snap.Error
	wantStartedAt := snap.PhaseStartedAt
	wantFinishedAt := snap.FinishedAt
	if wantPhase != "stalled" || wantFinishedAt == nil || !snap.Stalled {
		t.Fatalf("前置失败：终态证据不完整 %+v", snap)
	}

	// 迟到调用：备份返回后收尾阶段还会多次 setPhase。
	a.upgrader.setPhase(job, "prepare")
	a.upgrader.setPhase(job, "swap")

	got := a.upgrader.getJob()
	if got.Phase != wantPhase {
		t.Fatalf("Done 后 setPhase 改写了 phase：%q → %q", wantPhase, got.Phase)
	}
	if got.Error != wantError {
		t.Fatalf("Done 后 error 被改写：%q → %q", wantError, got.Error)
	}
	if !got.Stalled {
		t.Fatal("Done 后 stalled 派生标记必须保持 true")
	}
	if got.PhaseStartedAt != wantStartedAt {
		t.Fatalf("Done 后 PhaseStartedAt 被改写：%d → %d", wantStartedAt, got.PhaseStartedAt)
	}
	if got.FinishedAt == nil || *got.FinishedAt != *wantFinishedAt {
		t.Fatalf("Done 后 FinishedAt 被改写：%v → %v", wantFinishedAt, got.FinishedAt)
	}
}
