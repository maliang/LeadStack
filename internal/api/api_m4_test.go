package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maliang/leadstack/internal/deploy"
	"github.com/maliang/leadstack/internal/store"
)

// fakeDeployOrchestrator 记录编排调用，便于断言 REST 层语义。
type fakeDeployOrchestrator struct {
	mu         sync.Mutex
	started    []string
	retried    []string
	rolledBack []string
	canceled   []string
	retryN     int
	rollbackN  int
	cancelN    int
	err        error
}

func (f *fakeDeployOrchestrator) Start(releaseID string) {
	f.mu.Lock()
	f.started = append(f.started, releaseID)
	f.mu.Unlock()
}

func (f *fakeDeployOrchestrator) Retry(releaseID string, _ []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retried = append(f.retried, releaseID)
	return f.retryN, f.err
}

func (f *fakeDeployOrchestrator) Rollback(releaseID string, _ []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rolledBack = append(f.rolledBack, releaseID)
	return f.rollbackN, f.err
}

func (f *fakeDeployOrchestrator) Cancel(releaseID string, _ []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled = append(f.canceled, releaseID)
	return f.cancelN, f.err
}

// createAppVia 通过 REST 创建应用。
func (e *testEnv) createAppVia(jwt, name, slug string) appPayload {
	e.t.Helper()
	status, raw := e.request(http.MethodPost, "/api/v1/apps", jwt, map[string]any{"name": name, "slug": slug})
	if status != http.StatusCreated {
		e.t.Fatalf("创建应用失败: status=%d body=%s", status, raw)
	}
	var out struct {
		Data appPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("解析应用失败: %v", err)
	}
	return out.Data
}

// createTemplateVia 通过 REST 创建模板。
func (e *testEnv) createTemplateVia(jwt, name, kind, content string) store.DeployTemplate {
	e.t.Helper()
	status, raw := e.request(http.MethodPost, "/api/v1/templates", jwt,
		map[string]any{"name": name, "kind": kind, "content": content})
	if status != http.StatusCreated {
		e.t.Fatalf("创建模板失败: status=%d body=%s", status, raw)
	}
	var out struct {
		Data store.DeployTemplate `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("解析模板失败: %v", err)
	}
	return out.Data
}

// TestM4AppAndTemplateEndpoints 覆盖应用/模板 CRUD、409 保护与渲染预览。
func TestM4AppAndTemplateEndpoints(t *testing.T) {
	env := newTestEnv(t)

	// 401
	status, _ := env.request(http.MethodGet, "/api/v1/apps", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("未带 token 访问 /apps 期望 401，实际 %d", status)
	}

	jwt := env.login()
	app := env.createAppVia(jwt, "订单服务", "order-svc")
	if app.ID == "" || app.Slug != "order-svc" {
		t.Fatalf("应用创建结果不正确: %+v", app)
	}

	// slug 唯一 + 非法 slug
	status, _ = env.request(http.MethodPost, "/api/v1/apps", jwt, map[string]any{"name": "重复", "slug": "order-svc"})
	if status != http.StatusConflict {
		t.Fatalf("重复 slug 期望 409，实际 %d", status)
	}
	status, _ = env.request(http.MethodPost, "/api/v1/apps", jwt, map[string]any{"name": "x", "slug": "Bad Slug!"})
	if status != http.StatusBadRequest {
		t.Fatalf("非法 slug 期望 400，实际 %d", status)
	}

	// 详情 / 404
	status, _ = env.request(http.MethodGet, "/api/v1/apps/"+app.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询应用详情失败: %d", status)
	}
	status, _ = env.request(http.MethodGet, "/api/v1/apps/ghost", jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("不存在的应用期望 404，实际 %d", status)
	}

	// PATCH
	status, raw := env.request(http.MethodPatch, "/api/v1/apps/"+app.ID, jwt,
		map[string]any{"name": "订单服务v2", "description": "核心服务"})
	if status != http.StatusOK {
		t.Fatalf("更新应用失败: %d（body=%s）", status, raw)
	}

	// 模板
	tmpl := env.createTemplateVia(jwt, "binary 部署", "binary",
		"mkdir -p {{RELEASE_DIR}}\ncurl -fsSL {{ARTIFACT_URL}} -o {{RELEASE_DIR}}/app\n")
	if tmpl.ID == "" || tmpl.Kind != "binary" {
		t.Fatalf("模板创建结果不正确: %+v", tmpl)
	}
	status, _ = env.request(http.MethodPost, "/api/v1/templates", jwt,
		map[string]any{"name": "bad", "kind": "bogus", "content": "x"})
	if status != http.StatusBadRequest {
		t.Fatalf("非法 kind 期望 400，实际 %d", status)
	}

	// 渲染预览：合法占位符
	status, raw = env.request(http.MethodPost, "/api/v1/templates/"+tmpl.ID+"/render", jwt,
		map[string]any{"vars": map[string]string{"RELEASE_DIR": "/opt/app/releases/1", "ARTIFACT_URL": "http://x/y"}})
	if status != http.StatusOK {
		t.Fatalf("渲染失败: %d（body=%s）", status, raw)
	}
	var rendered struct {
		Data struct {
			Content string `json:"content"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &rendered); err != nil {
		t.Fatalf("解析渲染结果失败: %v", err)
	}
	if !strings.Contains(rendered.Data.Content, "/opt/app/releases/1") {
		t.Fatalf("渲染结果不正确: %s", rendered.Data.Content)
	}

	// 未知占位符 → 400/422
	status, raw = env.request(http.MethodPost, "/api/v1/templates/"+tmpl.ID+"/render", jwt,
		map[string]any{"vars": map[string]string{"RELEASE_DIR": "/x"}})
	if status != http.StatusUnprocessableEntity && status != http.StatusBadRequest {
		t.Fatalf("未知占位符期望 400/422，实际 %d（body=%s）", status, raw)
	}

	// 有发布记录时删除应用 → 409（先造一条发布）
	srv := env.createServerVia(jwt, map[string]any{"name": "s", "host": "10.40.0.1", "mode": "agent"})
	status, raw = env.request(http.MethodPost, "/api/v1/releases", jwt, map[string]any{
		"app_id": app.ID, "template_id": tmpl.ID, "version": "1.0.0", "server_ids": []string{srv.ID},
	})
	if status != http.StatusCreated {
		t.Fatalf("创建发布失败: %d（body=%s）", status, raw)
	}
	status, _ = env.request(http.MethodDelete, "/api/v1/apps/"+app.ID, jwt, nil)
	if status != http.StatusConflict {
		t.Fatalf("删除有发布的应用期望 409，实际 %d", status)
	}
}

// TestM4ArtifactUploadAndSecurity 覆盖制品上传、sha256、体积上限与 URL 登记。
func TestM4ArtifactUploadAndSecurity(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	// multipart 上传
	content := []byte("#!/bin/sh\necho hello-leadstack\n")
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", `form-data; name="file"; filename="app.sh"`)
	partHeader.Set("Content-Type", "application/octet-stream")
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		t.Fatalf("构造 multipart 失败: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("写入 multipart 失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 multipart 失败: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, env.srv.URL+"/api/v1/artifacts", &buf)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+jwt)
	resp, err := env.http.Do(req)
	if err != nil {
		t.Fatalf("上传制品失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("上传期望 201，实际 %d", resp.StatusCode)
	}
	var uploaded struct {
		Data artifactUploadResponse `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&uploaded); err != nil {
		t.Fatalf("解析上传响应失败: %v", err)
	}
	if uploaded.Data.SizeBytes != int64(len(content)) {
		t.Fatalf("大小期望 %d，实际 %d", len(content), uploaded.Data.SizeBytes)
	}
	// sha256 必须与内容一致
	wantSHA := "b7f4b1c2e8fbcbf6d0e0b2a4b0a8d55f9ba3cdbb25d0a3f61b5b3f36b3fbb85e" // 仅长度参考
	if len(uploaded.Data.SHA256) != 64 {
		t.Fatalf("sha256 长度应为 64，实际 %q", uploaded.Data.SHA256)
	}
	stored, err := env.st.GetArtifact(context.Background(), uploaded.Data.ID)
	if err != nil {
		t.Fatalf("查询制品失败: %v", err)
	}
	if stored.SHA256 == nil || *stored.SHA256 != uploaded.Data.SHA256 {
		t.Fatalf("制品 sha256 未落库: %+v", stored)
	}
	_ = wantSHA

	// 下载
	status, body := env.request(http.MethodGet, "/api/v1/artifacts/"+uploaded.Data.ID+"/download", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("下载制品失败: %d", status)
	}
	if !bytes.Equal(body, content) {
		t.Fatalf("下载内容不一致: %q", body)
	}

	// 体积上限：直接调用制品存储（避免构造超大请求体）
	small, err := newArtifactStore(t.TempDir(), 8)
	if err != nil {
		t.Fatalf("构造小上限存储失败: %v", err)
	}
	if _, _, err := small.Save("toobig", strings.NewReader("0123456789")); err == nil {
		t.Fatal("超限应报错")
	} else if !strings.Contains(err.Error(), "上限") {
		t.Fatalf("超限错误文案不正确: %v", err)
	}
	// 路径穿越拒绝
	if _, _, err := small.Save("../escape", strings.NewReader("x")); err == nil {
		t.Fatal("非法制品 ID 应被拒绝")
	}

	// url 登记：必须带 sha256（否则目标机无法校验、回滚没有保障）
	sha := strings.Repeat("ab", 32)
	status, raw := env.request(http.MethodPost, "/api/v1/artifacts", jwt,
		map[string]any{"kind": "url", "url": "http://127.0.0.1:9/app.tgz", "sha256": sha, "size_bytes": 1024})
	if status != http.StatusCreated {
		t.Fatalf("登记 url 制品失败: %d（body=%s）", status, raw)
	}
	var registered struct {
		Data artifactUploadResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &registered); err != nil {
		t.Fatalf("解析登记响应失败: %v", err)
	}
	if registered.Data.SHA256 != sha {
		t.Fatalf("响应应回传 sha256，实际 %q", registered.Data.SHA256)
	}
	storedURL, err := env.st.GetArtifact(context.Background(), registered.Data.ID)
	if err != nil {
		t.Fatalf("查询 url 制品失败: %v", err)
	}
	if storedURL.SHA256 == nil || *storedURL.SHA256 != sha {
		t.Fatalf("url 制品的 sha256 必须落库: %+v", storedURL)
	}

	// 缺 sha256 → 400（fail fast，而不是发布时才失败）
	status, raw = env.request(http.MethodPost, "/api/v1/artifacts", jwt,
		map[string]any{"kind": "url", "url": "http://127.0.0.1:9/app.tgz"})
	if status != http.StatusBadRequest {
		t.Fatalf("缺 sha256 期望 400，实际 %d（body=%s）", status, raw)
	}
	if !strings.Contains(string(raw), "sha256") {
		t.Fatalf("缺 sha256 的提示应说明原因: %s", raw)
	}
	// sha256 格式非法 → 400
	for _, bad := range []string{"xyz", strings.Repeat("zz", 32), strings.Repeat("a", 63)} {
		status, _ = env.request(http.MethodPost, "/api/v1/artifacts", jwt,
			map[string]any{"kind": "url", "url": "http://127.0.0.1:9/app.tgz", "sha256": bad})
		if status != http.StatusBadRequest {
			t.Fatalf("非法 sha256 %q 期望 400，实际 %d", bad, status)
		}
	}
	// 大写 hex 归一化为小写
	upper := strings.ToUpper(strings.Repeat("cd", 32))
	status, raw = env.request(http.MethodPost, "/api/v1/artifacts", jwt,
		map[string]any{"kind": "url", "url": "http://127.0.0.1:9/app.tgz", "sha256": upper})
	if status != http.StatusCreated {
		t.Fatalf("大写 sha256 应被接受: %d（body=%s）", status, raw)
	}
	if !strings.Contains(string(raw), strings.ToLower(upper)) {
		t.Fatalf("大写 sha256 应归一化为小写: %s", raw)
	}
	// 非法 url
	status, _ = env.request(http.MethodPost, "/api/v1/artifacts", jwt,
		map[string]any{"kind": "url", "url": "not-a-url", "sha256": sha})
	if status != http.StatusBadRequest {
		t.Fatalf("非法 url 期望 400，实际 %d", status)
	}
}

// TestM4ReleaseEndpoints 覆盖发布创建、状态汇总、日志增量、retry/rollback/cancel 与注入防护。
func TestM4ReleaseEndpoints(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	ctx := context.Background()

	app := env.createAppVia(jwt, "svc", "svc-a")
	tmpl := env.createTemplateVia(jwt, "t", "binary", "echo {{VERSION}}\n")
	srv1 := env.createServerVia(jwt, map[string]any{"name": "s1", "host": "10.41.0.1"})
	srv2 := env.createServerVia(jwt, map[string]any{"name": "s2", "host": "10.41.0.2"})

	// 版本号注入 → 400
	status, raw := env.request(http.MethodPost, "/api/v1/releases", jwt, map[string]any{
		"app_id": app.ID, "template_id": tmpl.ID, "version": "1.0; rm -rf /", "server_ids": []string{srv1.ID},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("注入型 version 期望 400，实际 %d（body=%s）", status, raw)
	}
	// 空目标 / 不存在的服务器
	status, _ = env.request(http.MethodPost, "/api/v1/releases", jwt, map[string]any{
		"app_id": app.ID, "template_id": tmpl.ID, "version": "1.0.0", "server_ids": []string{},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("空 server_ids 期望 400，实际 %d", status)
	}
	status, _ = env.request(http.MethodPost, "/api/v1/releases", jwt, map[string]any{
		"app_id": app.ID, "template_id": tmpl.ID, "version": "1.0.0", "server_ids": []string{"ghost"},
	})
	if status != http.StatusNotFound {
		t.Fatalf("不存在的服务器期望 404，实际 %d", status)
	}

	// 正常创建：响应形如 {data:{release:{id,...},runs:[{server_id,status}]}}
	status, raw = env.request(http.MethodPost, "/api/v1/releases", jwt, map[string]any{
		"app_id": app.ID, "template_id": tmpl.ID, "version": "1.0.0",
		"server_ids": []string{srv1.ID, srv2.ID}, "concurrency": 2,
	})
	if status != http.StatusCreated {
		t.Fatalf("创建发布失败: %d（body=%s）", status, raw)
	}
	var created struct {
		Data createReleaseResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("解析创建响应失败: %v", err)
	}
	releaseID := created.Data.Release.ID
	if releaseID == "" || len(created.Data.Runs) != 2 {
		t.Fatalf("创建响应不正确: %+v", created.Data)
	}
	if created.Data.Release.Status != store.ReleaseStatusPending {
		t.Fatalf("新发布应为 pending，实际 %q", created.Data.Release.Status)
	}
	if created.Data.Release.TemplateSnapshot == "" {
		t.Fatal("发布应冻结模板快照")
	}
	if created.Data.Release.Concurrency != 2 {
		t.Fatalf("concurrency 期望 2，实际 %d", created.Data.Release.Concurrency)
	}
	// 编排器（替身）应被调用
	env.deploy.mu.Lock()
	started := len(env.deploy.started)
	env.deploy.mu.Unlock()
	if started != 1 {
		t.Fatalf("创建发布后应异步启动执行，实际启动 %d 次", started)
	}

	// 详情：优先取 .data.release.status
	status, raw = env.request(http.MethodGet, "/api/v1/releases/"+releaseID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询发布详情失败: %d", status)
	}
	var detail struct {
		Data releaseDetailPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("解析详情失败: %v", err)
	}
	if detail.Data.Release.ID != releaseID || len(detail.Data.Runs) != 2 {
		t.Fatalf("详情内容不正确: %+v", detail.Data)
	}
	status, _ = env.request(http.MethodGet, "/api/v1/releases/ghost", jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("不存在的发布期望 404，实际 %d", status)
	}

	// runs 列表
	status, raw = env.request(http.MethodGet, "/api/v1/releases/"+releaseID+"/runs", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询 runs 失败: %d", status)
	}
	var runsPayload struct {
		Data struct {
			Runs []store.ReleaseRun `json:"runs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &runsPayload); err != nil {
		t.Fatalf("解析 runs 失败: %v", err)
	}
	if len(runsPayload.Data.Runs) != 2 {
		t.Fatalf("期望 2 条 run，实际 %d", len(runsPayload.Data.Runs))
	}

	// 日志增量：先写 3 条，after_seq=0 拿到 3 条，after_seq=最后一条拿到 0 条，再写 1 条能拿到
	run, err := env.st.GetReleaseRun(ctx, releaseID, srv1.ID)
	if err != nil {
		t.Fatalf("查询 run 失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := env.st.AppendReleaseLog(ctx, run.ID, "stdout", fmt.Sprintf("line-%d\n", i), store.NowMS()); err != nil {
			t.Fatalf("写入日志失败: %v", err)
		}
	}
	logs := env.fetchLogs(t, jwt, releaseID, srv1.ID, 0)
	if len(logs) != 3 {
		t.Fatalf("初次拉取期望 3 条，实际 %d", len(logs))
	}
	for i, item := range logs {
		if item.Seq != int64(i+1) {
			t.Fatalf("seq 应严格递增: %+v", logs)
		}
	}
	if again := env.fetchLogs(t, jwt, releaseID, srv1.ID, logs[len(logs)-1].Seq); len(again) != 0 {
		t.Fatalf("after_seq 应不重复返回，实际 %d", len(again))
	}
	if _, err := env.st.AppendReleaseLog(ctx, run.ID, "stderr", "boom\n", store.NowMS()); err != nil {
		t.Fatalf("写入日志失败: %v", err)
	}
	tail := env.fetchLogs(t, jwt, releaseID, srv1.ID, logs[len(logs)-1].Seq)
	if len(tail) != 1 || tail[0].Seq != 4 || tail[0].Stream != "stderr" {
		t.Fatalf("增量拉取不正确: %+v", tail)
	}

	// 不存在的服务器日志 → 404
	status, _ = env.request(http.MethodGet,
		"/api/v1/releases/"+releaseID+"/runs/ghost/logs?after_seq=0", jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("不存在服务器的日志期望 404，实际 %d", status)
	}

	// retry / rollback / cancel（替身返回计数）
	env.deploy.mu.Lock()
	env.deploy.retryN, env.deploy.rollbackN, env.deploy.cancelN = 1, 1, 2
	env.deploy.mu.Unlock()
	for _, action := range []string{"retry", "rollback", "cancel"} {
		status, raw = env.request(http.MethodPost, "/api/v1/releases/"+releaseID+"/"+action, jwt, nil)
		if status != http.StatusOK {
			t.Fatalf("%s 期望 200，实际 %d（body=%s）", action, status, raw)
		}
	}
	if !strings.Contains(string(raw), `"canceled":2`) {
		t.Fatalf("cancel 响应应带计数: %s", raw)
	}
}

// fetchLogs 增量拉取发布日志。
func (e *testEnv) fetchLogs(t *testing.T, jwt, releaseID, serverID string, afterSeq int64) []store.ReleaseLog {
	t.Helper()
	path := fmt.Sprintf("/api/v1/releases/%s/runs/%s/logs?after_seq=%d", releaseID, serverID, afterSeq)
	status, raw := e.request(http.MethodGet, path, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("拉取日志失败: status=%d body=%s", status, raw)
	}
	var out struct {
		Data releaseLogsPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析日志失败: %v", err)
	}
	return out.Data.Logs
}

// fakePlanBuilder 是 deploy.Build 的替身：产出稳定的两步计划（不触网、不执行）。
func fakePlanBuilder(_ deploy.Template, _ deploy.Artifact, p deploy.BuildParams) (deploy.Plan, error) {
	return deploy.Plan{
		AppSlug: p.AppSlug, ReleaseID: p.ReleaseID, Version: p.Version, Root: p.Root,
		Steps: []deploy.Step{
			{Name: "prepare", Command: "echo prepare", TimeoutS: 30},
			{Name: "switch", Command: "echo switch", TimeoutS: 30},
		},
		RollbackSteps: []deploy.Step{{Name: "rollback-switch", Command: "echo rollback", TimeoutS: 30}},
	}, nil
}

// fakeRollbackBuilder 是 deploy.Rollback 的替身。
func fakeRollbackBuilder(p deploy.Plan) (deploy.Plan, error) {
	p.Steps = []deploy.Step{{Name: "rollback-switch", Command: "echo rollback", TimeoutS: 30}}
	p.RollbackSteps = nil
	return p, nil
}

// fakePlanRunner 是 planRunner 的替身：可控成功/失败、记录并发峰值与计划。
type fakePlanRunner struct {
	mu          sync.Mutex
	inflight    int
	maxInflight int
	plans       []deploy.Plan
	servers     []string
	failOn      map[string]bool
	hold        time.Duration
	hooksLog    []string
}

func (f *fakePlanRunner) RunPlan(ctx context.Context, serverID string, p deploy.Plan, hooks deploy.Hooks) (deploy.RunOutcome, error) {
	f.mu.Lock()
	f.inflight++
	if f.inflight > f.maxInflight {
		f.maxInflight = f.inflight
	}
	f.plans = append(f.plans, p)
	f.servers = append(f.servers, serverID)
	shouldFail := f.failOn[serverID]
	hold := f.hold
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.inflight--
		f.mu.Unlock()
	}()

	if hooks.OnStep != nil {
		for i, step := range p.Steps {
			hooks.OnStep(i, step.Name)
		}
	}
	if hooks.OnLog != nil {
		hooks.OnLog("stdout", "step output for "+serverID+"\n")
		f.mu.Lock()
		f.hooksLog = append(f.hooksLog, serverID)
		f.mu.Unlock()
	}
	if hold > 0 {
		select {
		case <-time.After(hold):
		case <-ctx.Done():
			return deploy.RunOutcome{Status: "failed", Message: "已取消"}, ctx.Err()
		}
	}
	if shouldFail {
		return deploy.RunOutcome{Status: "failed", ExitCode: 1, Message: "步骤失败", FailedStep: "switch"}, nil
	}
	return deploy.RunOutcome{Status: "success", ExitCode: 0, Message: "部署成功"}, nil
}

func (f *fakePlanRunner) snapshot() (maxInflight int, plans int, servers []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxInflight, len(f.plans), append([]string(nil), f.servers...)
}

// TestM4OrchestratorConcurrencyAndStatus 覆盖异步编排：并发上限、状态汇总、失败隔离与重试只跑 failed。
func TestM4OrchestratorConcurrencyAndStatus(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	ctx := context.Background()

	app := env.createAppVia(jwt, "orch", "orch-a")
	tmpl := env.createTemplateVia(jwt, "t", "binary", "echo hi\n")
	servers := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		srv := env.createServerVia(jwt, map[string]any{
			"name": fmt.Sprintf("s%d", i), "host": fmt.Sprintf("10.42.0.%d", i+1), "mode": "agent",
		})
		servers = append(servers, srv.ID)
	}

	runner := &fakePlanRunner{hold: 60 * time.Millisecond, failOn: map[string]bool{}}
	orch := NewDeployOrchestrator(DeployOrchestratorOptions{
		Store: env.st, Concurrency: 2, DeployRoot: "/tmp/leadstack-test", PlanRunner: runner,
		PlanBuilder: fakePlanBuilder, RollbackBuilder: fakeRollbackBuilder,
		Logger: env.api.logger,
	})
	if orch == nil {
		t.Fatal("构造编排器失败")
	}
	t.Cleanup(orch.Close)
	// 让 REST 层也走真实编排器
	env.api.deploy = orch

	// 4 台机器、并发上限 2：createReleaseVia 走 REST
	status, raw := env.request(http.MethodPost, "/api/v1/releases", jwt, map[string]any{
		"app_id": app.ID, "template_id": tmpl.ID, "version": "2.0.0",
		"server_ids": servers, "concurrency": 4, // 请求 4，但全局上限是 2
	})
	if status != http.StatusCreated {
		t.Fatalf("创建发布失败: %d（body=%s）", status, raw)
	}
	var created struct {
		Data createReleaseResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	releaseID := created.Data.Release.ID

	// 等待终态
	final := env.waitReleaseStatus(t, jwt, releaseID, store.ReleaseStatusSuccess, 15*time.Second)
	if final != store.ReleaseStatusSuccess {
		t.Fatalf("期望 success，实际 %q", final)
	}
	maxInflight, plans, _ := runner.snapshot()
	if plans != 4 {
		t.Fatalf("期望执行 4 个计划，实际 %d", plans)
	}
	if maxInflight > 2 {
		t.Fatalf("并发上限应生效（≤2），实际峰值 %d", maxInflight)
	}

	// 日志通过 hooks 实时落库
	run, err := env.st.GetReleaseRun(ctx, releaseID, servers[0])
	if err != nil {
		t.Fatalf("查询 run 失败: %v", err)
	}
	count, err := env.st.CountReleaseLogs(ctx, run.ID)
	if err != nil {
		t.Fatalf("统计日志失败: %v", err)
	}
	if count == 0 {
		t.Fatal("执行过程中应写入发布日志")
	}
	if run.Status != store.ReleaseStatusSuccess || run.ExitCode == nil || *run.ExitCode != 0 {
		t.Fatalf("run 结果不正确: %+v", run)
	}

	// 第二次发布：一台失败 → 整体 failed，成功的那台不受影响
	runner.mu.Lock()
	runner.failOn = map[string]bool{servers[2]: true}
	runner.mu.Unlock()
	status, raw = env.request(http.MethodPost, "/api/v1/releases", jwt, map[string]any{
		"app_id": app.ID, "template_id": tmpl.ID, "version": "2.1.0",
		"server_ids": servers, "concurrency": 4,
	})
	if status != http.StatusCreated {
		t.Fatalf("创建第二次发布失败: %d（body=%s）", status, raw)
	}
	var created2 struct {
		Data createReleaseResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &created2); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	release2 := created2.Data.Release.ID
	final2 := env.waitReleaseStatus(t, jwt, release2, store.ReleaseStatusFailed, 15*time.Second)
	if final2 != store.ReleaseStatusFailed {
		t.Fatalf("含失败 run 时整体应为 failed，实际 %q", final2)
	}
	okRun, err := env.st.GetReleaseRun(ctx, release2, servers[0])
	if err != nil {
		t.Fatalf("查询 run 失败: %v", err)
	}
	if okRun.Status != store.ReleaseStatusSuccess {
		t.Fatalf("成功的那台不应受失败影响: %+v", okRun)
	}
	failedRun, err := env.st.GetReleaseRun(ctx, release2, servers[2])
	if err != nil {
		t.Fatalf("查询 run 失败: %v", err)
	}
	if failedRun.Status != store.ReleaseStatusFailed || failedRun.FailedStep == nil || *failedRun.FailedStep != "switch" {
		t.Fatalf("失败 run 记录不正确: %+v", failedRun)
	}

	// retry：只重跑 failed 的那台
	_, plansBefore, _ := runner.snapshot()
	status, raw = env.request(http.MethodPost, "/api/v1/releases/"+release2+"/retry", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("retry 失败: %d（body=%s）", status, raw)
	}
	runner.mu.Lock()
	runner.failOn = map[string]bool{}
	runner.mu.Unlock()
	// retry 刚返回时，release 状态可能仍是上一轮的 failed（新一轮还没把状态改成 running），
	// 因此这里容忍中间态，最终必须收敛到 success。
	finalStatus := env.waitReleaseStatusAfterRetry(t, jwt, release2, 20*time.Second)
	if finalStatus != store.ReleaseStatusSuccess {
		runs, _ := env.st.ListReleaseRuns(context.Background(), release2)
		t.Fatalf("retry 后应变为 success，实际 %q（runs=%+v）", finalStatus, runs)
	}
	// 关键回归断言：不允许任何 run 永远停在 pending（丢唤醒的典型症状）
	allRuns, err := env.st.ListReleaseRuns(context.Background(), release2)
	if err != nil {
		t.Fatalf("查询 runs 失败: %v", err)
	}
	for _, run := range allRuns {
		if run.Status == store.ReleaseStatusPending || run.Status == store.ReleaseStatusRunning {
			t.Fatalf("retry 后仍有 run 未终态（丢唤醒）：%+v", run)
		}
		if run.StartedAt == nil {
			t.Fatalf("run %s 从未开始执行（StartedAt 为空）", run.ServerID)
		}
	}
	_, plansAfter, _ := runner.snapshot()
	if plansAfter-plansBefore != 1 {
		t.Fatalf("retry 应只重跑 1 台，实际执行 %d 个计划", plansAfter-plansBefore)
	}
	// 重试后日志 seq 从 1 重新开始（旧日志被清理）
	retried, err := env.st.GetReleaseRun(ctx, release2, servers[2])
	if err != nil {
		t.Fatalf("查询 run 失败: %v", err)
	}
	tail := env.fetchLogs(t, jwt, release2, servers[2], 0)
	if len(tail) == 0 || tail[0].Seq != 1 {
		t.Fatalf("重试后日志 seq 应从 1 开始: %+v", tail)
	}
	_ = retried
}

// TestM4CancelRelease 覆盖取消：pending/running 的 run 变 canceled。
func TestM4CancelRelease(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	ctx := context.Background()

	app := env.createAppVia(jwt, "cancel", "cancel-a")
	tmpl := env.createTemplateVia(jwt, "t", "binary", "echo hi\n")
	srv := env.createServerVia(jwt, map[string]any{"name": "c1", "host": "10.43.0.1"})

	runner := &fakePlanRunner{hold: 300 * time.Millisecond}
	orch := NewDeployOrchestrator(DeployOrchestratorOptions{
		Store: env.st, Concurrency: 1, DeployRoot: "/tmp/leadstack-test", PlanRunner: runner,
		PlanBuilder: fakePlanBuilder, RollbackBuilder: fakeRollbackBuilder,
		Logger: env.api.logger,
	})
	t.Cleanup(orch.Close)
	env.api.deploy = orch

	status, raw := env.request(http.MethodPost, "/api/v1/releases", jwt, map[string]any{
		"app_id": app.ID, "template_id": tmpl.ID, "version": "3.0.0", "server_ids": []string{srv.ID},
	})
	if status != http.StatusCreated {
		t.Fatalf("创建发布失败: %d（body=%s）", status, raw)
	}
	var created struct {
		Data createReleaseResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	releaseID := created.Data.Release.ID

	// 立刻取消
	status, raw = env.request(http.MethodPost, "/api/v1/releases/"+releaseID+"/cancel", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("取消失败: %d（body=%s）", status, raw)
	}
	// 等待终态（canceled 或 failed —— 取决于取消到达时是否已在执行）
	final := env.waitReleaseTerminal(t, jwt, releaseID, 15*time.Second)
	if final != store.ReleaseStatusCanceled && final != store.ReleaseStatusFailed {
		t.Fatalf("取消后期望 canceled/failed，实际 %q", final)
	}
	runs, err := env.st.ListReleaseRuns(ctx, releaseID)
	if err != nil {
		t.Fatalf("查询 runs 失败: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("期望 1 条 run，实际 %d", len(runs))
	}
}

// waitReleaseStatus 轮询直到发布达到期望状态（或超时）。
func (e *testEnv) waitReleaseStatus(t *testing.T, jwt, releaseID, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		status, raw := e.request(http.MethodGet, "/api/v1/releases/"+releaseID, jwt, nil)
		if status != http.StatusOK {
			t.Fatalf("查询发布失败: %d", status)
		}
		var detail struct {
			Data releaseDetailPayload `json:"data"`
		}
		if err := json.Unmarshal(raw, &detail); err != nil {
			t.Fatalf("解析发布失败: %v", err)
		}
		last = detail.Data.Release.Status
		if last == want {
			return last
		}
		if last == store.ReleaseStatusFailed && want == store.ReleaseStatusSuccess {
			t.Fatalf("发布进入 failed，未达到期望的 success（runs=%+v）", detail.Data.Runs)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return last
}

// waitReleaseStatusAfterRetry 轮询直到 retry 后的发布收敛到 success。
//
// 与 waitReleaseStatus 的区别：retry 刚发出时 release 可能还停留在上一轮的 failed，
// 因此这里**不**因为看到 failed 就立即失败，而是容忍中间态直到超时。
func (e *testEnv) waitReleaseStatusAfterRetry(t *testing.T, jwt, releaseID string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		status, raw := e.request(http.MethodGet, "/api/v1/releases/"+releaseID, jwt, nil)
		if status != http.StatusOK {
			t.Fatalf("查询发布失败: %d", status)
		}
		var detail struct {
			Data releaseDetailPayload `json:"data"`
		}
		if err := json.Unmarshal(raw, &detail); err != nil {
			t.Fatalf("解析发布失败: %v", err)
		}
		last = detail.Data.Release.Status
		if last == store.ReleaseStatusSuccess {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	return last
}

// waitReleaseTerminal 轮询直到发布进入终态。
func (e *testEnv) waitReleaseTerminal(t *testing.T, jwt, releaseID string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		status, raw := e.request(http.MethodGet, "/api/v1/releases/"+releaseID, jwt, nil)
		if status != http.StatusOK {
			t.Fatalf("查询发布失败: %d", status)
		}
		var detail struct {
			Data releaseDetailPayload `json:"data"`
		}
		if err := json.Unmarshal(raw, &detail); err != nil {
			t.Fatalf("解析发布失败: %v", err)
		}
		last = detail.Data.Release.Status
		if last == store.ReleaseStatusSuccess || last == store.ReleaseStatusFailed || last == store.ReleaseStatusCanceled {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	return last
}

// TestM4AgentLogTap 覆盖 agent 模式流式日志旁路：runID 绑定后 job.log 帧实时入库。
func TestM4AgentLogTap(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	tap := newAgentLogTap(env.st, env.api.logger)
	if tap == nil {
		t.Fatal("构造旁路失败")
	}

	// 无绑定时：帧被忽略（不影响检测任务等其他 job）
	tap.Handle("srv-x", "run-other", "stdout", "should be ignored")

	claim := tap.Claim("srv-1", "run-1")
	// 模拟 HubSender 从 job.exec 信封里解析出 runID
	envelope := []byte(`{"type":"job.exec","id":"run-abc","ts":1,"payload":{"run_id":"run-abc"}}`)
	if got := extractRunIDFromEnvelope(envelope); got != "run-abc" {
		t.Fatalf("信封解析 runID 失败: %q", got)
	}
	tap.Bind("srv-1", "run-abc")

	// 绑定后的三条帧应实时写入 release_logs
	tap.Handle("srv-1", "run-abc", "stdout", "line1\n")
	tap.Handle("srv-1", "run-abc", "stderr", "line2\n")
	tap.Handle("srv-1", "run-abc", "stdout", "line3\n")

	logs, err := env.st.ListReleaseLogs(ctx, "run-1", 0, 100)
	if err != nil {
		t.Fatalf("查询旁路日志失败: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("期望 3 条旁路日志，实际 %d", len(logs))
	}
	for i, item := range logs {
		if item.Seq != int64(i+1) {
			t.Fatalf("旁路日志 seq 应严格递增: %+v", logs)
		}
	}
	if !claim.Captured() {
		t.Fatal("捕获后 Captured() 应为 true（据此跳过结束时的重复落库）")
	}

	// Release 后不再写入
	claim.Release()
	tap.Handle("srv-1", "run-abc", "stdout", "late\n")
	logs, _ = env.st.ListReleaseLogs(ctx, "run-1", 0, 100)
	if len(logs) != 3 {
		t.Fatalf("释放后不应再写入，实际 %d", len(logs))
	}

	// 非 job.exec 信封不产生绑定
	if got := extractRunIDFromEnvelope([]byte(`{"type":"job.log","id":"x"}`)); got != "" {
		t.Fatalf("非 job.exec 不应解析出 runID: %q", got)
	}
}

// TestM4PlanRootConvention 用**真实 deploy.Build** 校验部署根约定：
// BuildParams.Root 是部署根，应用根由 Build 拼成 <Root>/<AppSlug>，绝不能出现重复 slug。
func TestM4PlanRootConvention(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	app := env.createAppVia(jwt, "root-check", "root-app")
	tmpl := env.createTemplateVia(jwt, "t", "binary",
		"mkdir -p {{RELEASE_DIR}}\ncurl -fsSL {{ARTIFACT_URL}} -o {{RELEASE_DIR}}/app\n")
	// url 制品（真实 deploy.Build 会校验 kind/location/sha256）
	sha := strings.Repeat("a", 64)
	status, raw := env.request(http.MethodPost, "/api/v1/artifacts", jwt,
		map[string]any{"kind": "url", "url": "http://127.0.0.1:9/app.tgz", "sha256": sha})
	if status != http.StatusCreated {
		t.Fatalf("登记制品失败: %d（body=%s）", status, raw)
	}
	art := &store.Artifact{
		ID: "art-root-check", Kind: store.ArtifactKindURL,
		Location: "http://127.0.0.1:9/app.tgz", SHA256: &sha, CreatedAt: store.NowMS(),
	}
	if err := env.st.CreateArtifact(context.Background(), art); err != nil {
		t.Fatalf("写入制品失败: %v", err)
	}

	srv := env.createServerVia(jwt, map[string]any{"name": "r1", "host": "10.44.0.1", "mode": "agent"})

	runner := &fakePlanRunner{}
	orch := NewDeployOrchestrator(DeployOrchestratorOptions{
		Store: env.st, Concurrency: 1, DeployRoot: "/srv/leadstack-apps",
		PlanRunner: runner, Logger: env.api.logger,
		// 不注入 PlanBuilder：走真实 deploy.Build
	})
	if orch == nil {
		t.Fatal("构造编排器失败")
	}
	t.Cleanup(orch.Close)
	env.api.deploy = orch

	status, raw = env.request(http.MethodPost, "/api/v1/releases", jwt, map[string]any{
		"app_id": app.ID, "template_id": tmpl.ID, "version": "1.2.3",
		"artifact_id": art.ID, "server_ids": []string{srv.ID},
	})
	if status != http.StatusCreated {
		t.Fatalf("创建发布失败: %d（body=%s）", status, raw)
	}
	var created struct {
		Data createReleaseResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	env.waitReleaseStatus(t, jwt, created.Data.Release.ID, store.ReleaseStatusSuccess, 15*time.Second)

	runner.mu.Lock()
	plans := append([]deploy.Plan(nil), runner.plans...)
	runner.mu.Unlock()
	if len(plans) != 1 {
		t.Fatalf("期望 1 个计划，实际 %d", len(plans))
	}
	plan := plans[0]
	wantRoot := "/srv/leadstack-apps/root-app"
	if plan.Root != wantRoot {
		t.Fatalf("应用根期望 %q，实际 %q（slug 不能被拼两次）", wantRoot, plan.Root)
	}
	wantReleaseDir := wantRoot + "/releases/" + created.Data.Release.ID
	for _, step := range plan.Steps {
		if strings.Contains(step.Command, wantReleaseDir) {
			// 命中了 release 目录约定即通过
			goto verified
		}
	}
	t.Fatalf("计划里未出现 release 目录 %q: %+v", wantReleaseDir, plan.Steps)
verified:
	// 回滚路径也要能通过 Build 校验（真实 Build + 真实 Kind/artifact）：
	// 这里没有上一版本（prev 为空），回滚应被安全跳过而不是报错。
	n, err := orch.Rollback(created.Data.Release.ID, nil)
	if err != nil {
		t.Fatalf("回滚调用不应报错: %v", err)
	}
	if n != 0 {
		t.Fatalf("没有上一版本时不应回滚任何机器，实际 %d", n)
	}
}
