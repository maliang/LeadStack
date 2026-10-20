package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/auth"
	"github.com/maliang/leadstack/internal/deploy"
	"github.com/maliang/leadstack/internal/store"
)

// 部署模块的字段约束。
const (
	maxAppNameRunes      = 128
	maxAppSlugLen        = 40
	maxVersionLen        = 64
	maxTemplateNameRunes = 128
	maxTemplateContent   = 256 << 10
	releaseListDefault   = 100
	releaseListMax       = 500
	releaseLogsDefault   = 1000
	releaseLogsMax       = 5000
	deployMaxConcurrency = 64
)

// appSlugPattern 是应用 slug 的白名单（契约第 26 节）。
var appSlugPattern = regexp.MustCompile(`^[a-z0-9-]{1,` + strconv.Itoa(maxAppSlugLen) + `}$`)

// versionPattern 是版本号白名单（契约第 26 节）：拒绝 `; rm -rf /` 之类的注入。
var versionPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,` + strconv.Itoa(maxVersionLen) + `}$`)

// DeployOrchestrator 是发布异步执行的端口（由 internal/deploy 的 Executor + Runner 组合实现）。
//
// 为 nil 时创建发布仍会落库，但不会执行（run 保持 pending），便于分阶段联调。
type DeployOrchestrator interface {
	// Start 异步执行一次发布（只处理 pending 的 run）。
	Start(releaseID string)
	// Retry 重跑 failed 的 run（serverIDs 为空表示全部 failed），返回重跑数量。
	Retry(releaseID string, serverIDs []string) (int, error)
	// Rollback 回滚指定服务器（为空表示全部成功的 run），返回处理数量。
	Rollback(releaseID string, serverIDs []string) (int, error)
	// Cancel 取消仍 pending/running 的 run，返回取消数量。
	Cancel(releaseID string, serverIDs []string) (int, error)
}

// appPayload 是应用的对外表示。
type appPayload struct {
	store.App
	ReleaseCount int `json:"release_count"`
}

// releaseDetailPayload 是发布详情的对外表示。
type releaseDetailPayload struct {
	Release store.Release      `json:"release"`
	Runs    []store.ReleaseRun `json:"runs"`
}

// createReleaseResponse 是创建发布的响应（立即返回，执行在后台）。
type createReleaseResponse struct {
	Release store.Release     `json:"release"`
	Runs    []releaseRunBrief `json:"runs"`
}

// releaseRunBrief 是创建响应里的精简 run。
type releaseRunBrief struct {
	ServerID string `json:"server_id"`
	Status   string `json:"status"`
}

// releaseLogsPayload 是日志增量响应。
type releaseLogsPayload struct {
	Logs []store.ReleaseLog `json:"logs"`
}

// artifactUploadResponse 是上传响应。
type artifactUploadResponse struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

// ------------------------------------------------ 应用 ------------------------------------------------

// handleListApps 处理 GET /api/v1/apps。
func (a *API) handleListApps(w http.ResponseWriter, r *http.Request) {
	apps, err := a.st.ListApps(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]appPayload, 0, len(apps))
	for _, app := range apps {
		count, err := a.st.CountAppReleases(r.Context(), app.ID)
		if err != nil {
			a.internalError(w, r, err)
			return
		}
		out = append(out, appPayload{App: app, ReleaseCount: count})
	}
	writeData(w, http.StatusOK, out)
}

// handleCreateApp 处理 POST /api/v1/apps。
func (a *API) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Root        string `json:"root"`
		Description string `json:"description"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := validateText("name", name, maxAppNameRunes, true); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	slug := strings.ToLower(strings.TrimSpace(req.Slug))
	if !appSlugPattern.MatchString(slug) {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			fmt.Sprintf("slug 必须是 %d 位以内的 [a-z0-9-]", maxAppSlugLen))
		return
	}
	app := &store.App{
		ID:        uuid.NewString(),
		Name:      name,
		Slug:      slug,
		CreatedAt: a.nowMS(),
	}
	if req.Root != "" {
		root := strings.TrimSpace(req.Root)
		if !strings.HasPrefix(root, "/") {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "root 必须是绝对路径")
			return
		}
		app.Root = &root
	}
	if req.Description != "" {
		desc := strings.TrimSpace(req.Description)
		app.Description = &desc
	}
	if err := a.st.CreateApp(r.Context(), app); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("创建应用", "app_id", app.ID, "slug", app.Slug)
	writeData(w, http.StatusCreated, appPayload{App: *app})
}

// handleGetApp 处理 GET /api/v1/apps/{id}。
func (a *API) handleGetApp(w http.ResponseWriter, r *http.Request) {
	app, err := a.loadApp(w, r)
	if err != nil {
		return
	}
	count, err := a.st.CountAppReleases(r.Context(), app.ID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, appPayload{App: *app, ReleaseCount: count})
}

// handlePatchApp 处理 PATCH /api/v1/apps/{id}。
func (a *API) handlePatchApp(w http.ResponseWriter, r *http.Request) {
	app, err := a.loadApp(w, r)
	if err != nil {
		return
	}
	var req struct {
		Name        *string `json:"name"`
		Root        *string `json:"root"`
		Description *string `json:"description"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	if req.Name == nil && req.Root == nil && req.Description == nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "至少需要提供 name/root/description 之一")
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if err := validateText("name", name, maxAppNameRunes, true); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		req.Name = &name
	}
	if req.Root != nil {
		root := strings.TrimSpace(*req.Root)
		if root != "" && !strings.HasPrefix(root, "/") {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "root 必须是绝对路径")
			return
		}
		req.Root = &root
	}
	updated, err := a.st.UpdateApp(r.Context(), app.ID, req.Name, req.Root, req.Description)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	count, _ := a.st.CountAppReleases(r.Context(), updated.ID)
	writeData(w, http.StatusOK, appPayload{App: *updated, ReleaseCount: count})
}

// handleDeleteApp 处理 DELETE /api/v1/apps/{id}（有发布记录时 409）。
func (a *API) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	app, err := a.loadApp(w, r)
	if err != nil {
		return
	}
	if err := a.st.DeleteApp(r.Context(), app.ID); err != nil {
		if errors.Is(err, store.ErrConflict) {
			a.logger.Warn("删除应用被拒绝：仍有发布记录", "app_id", app.ID)
		}
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("删除应用", "app_id", app.ID, "slug", app.Slug)
	writeData(w, http.StatusOK, map[string]any{"id": app.ID})
}

// loadApp 读取路径上的应用。
func (a *API) loadApp(w http.ResponseWriter, r *http.Request) (*store.App, error) {
	id := chi.URLParam(r, "appID")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少应用 ID")
		return nil, errors.New("缺少应用 ID")
	}
	app, err := a.st.GetApp(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return nil, err
	}
	return app, nil
}

// ------------------------------------------------ 模板 ------------------------------------------------

// handleListTemplates 处理 GET /api/v1/templates。
func (a *API) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	templates, err := a.st.ListDeployTemplates(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if templates == nil {
		templates = []store.DeployTemplate{}
	}
	writeData(w, http.StatusOK, templates)
}

// handleCreateTemplate 处理 POST /api/v1/templates。
func (a *API) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Kind    string `json:"kind"`
		Content string `json:"content"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := validateText("name", name, maxTemplateNameRunes, true); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if !isValidTemplateKind(kind) {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "kind 只能是 compose|systemd|binary")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "content 不能为空")
		return
	}
	if len(req.Content) > maxTemplateContent {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "content 过大")
		return
	}
	nowMS := a.nowMS()
	tmpl := &store.DeployTemplate{
		ID: uuid.NewString(), Name: name, Kind: kind, Content: req.Content,
		CreatedAt: nowMS, UpdatedAt: nowMS,
	}
	if err := a.st.CreateDeployTemplate(r.Context(), tmpl); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("创建部署模板", "template_id", tmpl.ID, "kind", tmpl.Kind)
	writeData(w, http.StatusCreated, tmpl)
}

// handleGetTemplate 处理 GET /api/v1/templates/{id}。
func (a *API) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	tmpl, err := a.loadTemplate(w, r)
	if err != nil {
		return
	}
	writeData(w, http.StatusOK, tmpl)
}

// handlePatchTemplate 处理 PATCH /api/v1/templates/{id}（已发布快照不受影响）。
func (a *API) handlePatchTemplate(w http.ResponseWriter, r *http.Request) {
	tmpl, err := a.loadTemplate(w, r)
	if err != nil {
		return
	}
	var req struct {
		Name    *string `json:"name"`
		Kind    *string `json:"kind"`
		Content *string `json:"content"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	if req.Name == nil && req.Kind == nil && req.Content == nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "至少需要提供 name/kind/content 之一")
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if err := validateText("name", name, maxTemplateNameRunes, true); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		req.Name = &name
	}
	if req.Kind != nil {
		k := strings.TrimSpace(*req.Kind)
		if !isValidTemplateKind(k) {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "kind 只能是 compose|systemd|binary")
			return
		}
		req.Kind = &k
	}
	if req.Content != nil && len(*req.Content) > maxTemplateContent {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "content 过大")
		return
	}
	updated, err := a.st.UpdateDeployTemplate(r.Context(), tmpl.ID, req.Name, req.Kind, req.Content, a.nowMS())
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, updated)
}

// handleDeleteTemplate 处理 DELETE /api/v1/templates/{id}。
func (a *API) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	tmpl, err := a.loadTemplate(w, r)
	if err != nil {
		return
	}
	if err := a.st.DeleteDeployTemplate(r.Context(), tmpl.ID); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"id": tmpl.ID})
}

// handleRenderTemplate 处理 POST /api/v1/templates/{id}/render：**只渲染，不执行任何命令**。
func (a *API) handleRenderTemplate(w http.ResponseWriter, r *http.Request) {
	tmpl, err := a.loadTemplate(w, r)
	if err != nil {
		return
	}
	var req struct {
		Vars map[string]string `json:"vars"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	if req.Vars == nil {
		req.Vars = map[string]string{}
	}
	rendered, err := deployRender(tmpl.Content, req.Vars)
	if err != nil {
		// 未知占位符 / 变量非法：按契约返回 422（脚本接受 400 或 422）
		writeError(w, http.StatusUnprocessableEntity, codeInvalidArgument, "模板渲染失败: "+err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"content": rendered})
}

// loadTemplate 读取路径上的模板。
func (a *API) loadTemplate(w http.ResponseWriter, r *http.Request) (*store.DeployTemplate, error) {
	id := chi.URLParam(r, "templateID")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少模板 ID")
		return nil, errors.New("缺少模板 ID")
	}
	tmpl, err := a.st.GetDeployTemplate(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return nil, err
	}
	return tmpl, nil
}

// normalizeSHA256 校验并归一化 url 制品的 sha256（64 位 hex，允许大写并转小写）。
//
// 缺失时返回明确的产品提示：url 制品必须提供 sha256，否则部署无法校验、回滚没有保障。
func normalizeSHA256(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" {
		return "", errors.New("url 制品必须提供 sha256（可用 shasum -a 256 <文件> 计算）")
	}
	if len(v) != 64 {
		return "", fmt.Errorf("sha256 必须是 64 位十六进制字符串，实际长度 %d", len(v))
	}
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return "", errors.New("sha256 只能包含十六进制字符 0-9a-f")
		}
	}
	return v, nil
}

// isValidTemplateKind 校验模板类型。
func isValidTemplateKind(kind string) bool {
	switch kind {
	case store.TemplateKindCompose, store.TemplateKindSystemd, store.TemplateKindBinary:
		return true
	default:
		return false
	}
}

// ------------------------------------------------ 制品 ------------------------------------------------

// handleCreateArtifact 处理 POST /api/v1/artifacts。
//
// 两种形态：
//   - multipart 上传（`file` 字段）：服务端落盘并**自行计算 sha256**；
//   - JSON `{kind:"url",url,sha256,size_bytes?}`：只登记元数据（发布时才下载）。
//
// 产品口径（M4 裁决）：**url 制品必须提供 sha256**。原因是目标机下载后必须校验，
// 校验通过才切换 current —— 这是「失败不破坏上一版本、回滚有意义」的前提；
// 缺 sha256 一律在登记阶段 400（fail fast），而不是等到发布执行时才失败。
func (a *API) handleCreateArtifact(w http.ResponseWriter, r *http.Request) {
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		a.handleUploadArtifact(w, r)
		return
	}

	var req struct {
		Kind      string `json:"kind"`
		URL       string `json:"url"`
		SHA256    string `json:"sha256"`
		SizeBytes int64  `json:"size_bytes"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			"请求体格式错误（上传请用 multipart，url 登记请传 {kind,url,sha256}）: "+err.Error())
		return
	}
	kind := strings.TrimSpace(req.Kind)
	rawURL := strings.TrimSpace(req.URL)
	if kind == "" {
		kind = store.ArtifactKindURL
	}
	if kind != store.ArtifactKindURL {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			"JSON 登记只支持 kind=url；上传文件请用 multipart/form-data")
		return
	}
	normalized, err := normalizeServerURL(rawURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "url 不合法: "+err.Error())
		return
	}

	// url 制品必须带 sha256：目标机靠它校验下载内容（校验通过才切 current）。
	sha, err := normalizeSHA256(req.SHA256)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	if req.SizeBytes < 0 {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "size_bytes 不能为负数")
		return
	}

	// url 制品此时只登记元数据，**发布时才下载**（契约第 23 节）。
	artifact := &store.Artifact{
		ID: uuid.NewString(), Kind: store.ArtifactKindURL,
		Location: normalized, SHA256: &sha, SizeBytes: req.SizeBytes, CreatedAt: a.nowMS(),
	}
	if err := a.st.CreateArtifact(r.Context(), artifact); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("登记 url 制品", "artifact_id", artifact.ID, "url", normalized, "sha256", sha)
	writeData(w, http.StatusCreated, artifactUploadResponse{
		ID: artifact.ID, Kind: artifact.Kind, SHA256: sha, SizeBytes: artifact.SizeBytes,
	})
}

// handleUploadArtifact 处理 multipart 上传。
func (a *API) handleUploadArtifact(w http.ResponseWriter, r *http.Request) {
	if a.artifacts == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "制品存储未启用")
		return
	}
	maxBytes := a.artifacts.MaxBytes()
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, codeInvalidArgument,
				fmt.Sprintf("制品超过 %d 字节上限", maxBytes))
			return
		}
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "解析 multipart 失败: "+err.Error())
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			if err := r.MultipartForm.RemoveAll(); err != nil {
				a.logger.Debug("清理上传临时文件失败", "err", err)
			}
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少 file 字段")
		return
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			a.logger.Debug("关闭上传文件失败", "err", cerr)
		}
	}()

	artifactID := uuid.NewString()
	sha, size, err := a.artifacts.Save(artifactID, file)
	if err != nil {
		if errors.Is(err, errArtifactTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, codeInvalidArgument,
				fmt.Sprintf("制品超过 %d 字节上限", maxBytes))
			return
		}
		a.internalError(w, r, err)
		return
	}

	artifact := &store.Artifact{
		ID: artifactID, Kind: store.ArtifactKindUpload, Location: artifactID,
		SHA256: &sha, SizeBytes: size, CreatedAt: a.nowMS(),
	}
	if err := a.st.CreateArtifact(r.Context(), artifact); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("上传制品完成",
		"artifact_id", artifact.ID, "filename", header.Filename, "size_bytes", size, "sha256", sha)
	writeData(w, http.StatusCreated, artifactUploadResponse{
		ID: artifact.ID, Kind: artifact.Kind, SHA256: sha, SizeBytes: size,
	})
}

// handleDownloadArtifact 处理 GET /api/v1/artifacts/{id}/download（供目标机拉取 upload 制品）。
func (a *API) handleDownloadArtifact(w http.ResponseWriter, r *http.Request) {
	if a.artifacts == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "制品存储未启用")
		return
	}
	id := chi.URLParam(r, "artifactID")
	artifact, err := a.st.GetArtifact(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if artifact.Kind != store.ArtifactKindUpload {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "该制品不是上传类型")
		return
	}
	file, err := a.artifacts.Open(artifact.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "制品文件不存在")
		return
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			a.logger.Debug("关闭制品文件失败", "err", cerr)
		}
	}()
	info, err := file.Stat()
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	if artifact.SHA256 != nil {
		w.Header().Set("X-Checksum-Sha256", *artifact.SHA256)
	}
	http.ServeContent(w, r, artifact.ID, info.ModTime(), file)
}

// isBodyTooLarge 判断是否为 MaxBytesReader 触发的超限错误。
func isBodyTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr) || strings.Contains(err.Error(), "request body too large")
}

// ------------------------------------------------ 发布 ------------------------------------------------

// handleListReleases 处理 GET /api/v1/releases。
func (a *API) handleListReleases(w http.ResponseWriter, r *http.Request) {
	limit := releaseListDefault
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "limit 必须是正整数")
			return
		}
		if v > releaseListMax {
			v = releaseListMax
		}
		limit = v
	}
	params, perr := parsePage(r)
	if perr != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, perr.Error())
		return
	}
	if params.Paged {
		limit = params.fetchLimit(releaseListDefault)
		if limit > releaseListMax {
			limit = releaseListMax
		}
	}
	releases, err := a.st.ListReleases(r.Context(), store.ReleaseFilter{
		AppID:  strings.TrimSpace(r.URL.Query().Get("app_id")),
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Limit:  limit,
	})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if releases == nil {
		releases = []store.Release{}
	}
	start, end, meta := params.window(len(releases))
	if !params.Paged {
		start, end = 0, len(releases)
	}
	writePagedList(w, params, releases[start:end], meta)
}

// handleCreateRelease 处理 POST /api/v1/releases：落库后立即返回，执行在后台。
func (a *API) handleCreateRelease(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AppID       string   `json:"app_id"`
		TemplateID  string   `json:"template_id"`
		Version     string   `json:"version"`
		ArtifactID  string   `json:"artifact_id"`
		ServerIDs   []string `json:"server_ids"`
		Concurrency *int     `json:"concurrency"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}

	version := strings.TrimSpace(req.Version)
	if !versionPattern.MatchString(version) {
		// 版本号白名单校验：拒绝 `; rm -rf /` 之类的注入（契约第 26 节安全红线）。
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			fmt.Sprintf("version 必须是 %d 位以内的 [A-Za-z0-9._-]", maxVersionLen))
		return
	}
	serverIDs := normalizeIDList(req.ServerIDs)
	if len(serverIDs) == 0 {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "server_ids 不能为空")
		return
	}
	if len(serverIDs) > maxBatchIDs {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			fmt.Sprintf("一次发布最多 %d 台服务器", maxBatchIDs))
		return
	}
	concurrency := a.cfg.DeployConcurrency
	if req.Concurrency != nil {
		concurrency = *req.Concurrency
		if concurrency < 1 || concurrency > deployMaxConcurrency {
			writeError(w, http.StatusBadRequest, codeInvalidArgument,
				fmt.Sprintf("concurrency 必须在 1-%d 之间", deployMaxConcurrency))
			return
		}
	}

	ctx := r.Context()
	app, err := a.st.GetApp(ctx, strings.TrimSpace(req.AppID))
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	tmpl, err := a.st.GetDeployTemplate(ctx, strings.TrimSpace(req.TemplateID))
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	var artifact *store.Artifact
	if strings.TrimSpace(req.ArtifactID) != "" {
		artifact, err = a.st.GetArtifact(ctx, strings.TrimSpace(req.ArtifactID))
		if err != nil {
			a.writeStoreError(w, r, err)
			return
		}
		if artifact.Kind == store.ArtifactKindUpload && a.cfg.PublicURL == "" {
			writeError(w, http.StatusBadRequest, codeInvalidArgument,
				"upload 制品需要配置 LEADSTACK_PUBLIC_URL（目标机需能访问控制端下载制品）")
			return
		}
	}
	for _, serverID := range serverIDs {
		if _, err := a.st.GetServer(ctx, serverID); err != nil {
			a.writeStoreError(w, r, err)
			return
		}
	}

	createdBy := ""
	if claims, ok := auth.ClaimsFromRequest(r); ok {
		createdBy = claims.Username
	}
	releaseID := uuid.NewString()
	release := &store.Release{
		ID:               releaseID,
		AppID:            app.ID,
		TemplateID:       tmpl.ID,
		Version:          version,
		TemplateSnapshot: tmpl.Content, // 冻结快照：模板后续修改不影响本次发布
		Status:           store.ReleaseStatusPending,
		CreatedAt:        a.nowMS(),
		Concurrency:      concurrency,
	}
	if artifact != nil {
		release.ArtifactID = &artifact.ID
	}
	if createdBy != "" {
		release.CreatedBy = &createdBy
	}

	runs := make([]store.ReleaseRun, 0, len(serverIDs))
	briefs := make([]releaseRunBrief, 0, len(serverIDs))
	for _, serverID := range serverIDs {
		runs = append(runs, store.ReleaseRun{
			ID: uuid.NewString(), ReleaseID: releaseID, ServerID: serverID,
			Status: store.ReleaseStatusPending,
		})
		briefs = append(briefs, releaseRunBrief{ServerID: serverID, Status: store.ReleaseStatusPending})
	}
	if err := a.st.CreateRelease(ctx, release, runs); err != nil {
		a.writeStoreError(w, r, err)
		return
	}

	a.logger.Info("创建发布",
		"release_id", releaseID, "app", app.Slug, "version", version,
		"servers", len(serverIDs), "concurrency", concurrency)

	// 异步执行：立即返回，前端轮询进度。
	if a.deploy != nil {
		go a.deploy.Start(releaseID)
	} else {
		a.logger.Warn("发布编排器未装配，发布将保持 pending", "release_id", releaseID)
	}

	writeData(w, http.StatusCreated, createReleaseResponse{Release: *release, Runs: briefs})
}

// handleGetRelease 处理 GET /api/v1/releases/{id}。
func (a *API) handleGetRelease(w http.ResponseWriter, r *http.Request) {
	release, err := a.loadRelease(w, r)
	if err != nil {
		return
	}
	runs, err := a.st.ListReleaseRuns(r.Context(), release.ID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if runs == nil {
		runs = []store.ReleaseRun{}
	}
	writeData(w, http.StatusOK, releaseDetailPayload{Release: *release, Runs: runs})
}

// handleListReleaseRuns 处理 GET /api/v1/releases/{id}/runs。
func (a *API) handleListReleaseRuns(w http.ResponseWriter, r *http.Request) {
	release, err := a.loadRelease(w, r)
	if err != nil {
		return
	}
	runs, err := a.st.ListReleaseRuns(r.Context(), release.ID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if runs == nil {
		runs = []store.ReleaseRun{}
	}
	writeData(w, http.StatusOK, map[string]any{"runs": runs})
}

// handleReleaseLogs 处理 GET /api/v1/releases/{id}/runs/{serverID}/logs?after_seq=。
func (a *API) handleReleaseLogs(w http.ResponseWriter, r *http.Request) {
	release, err := a.loadRelease(w, r)
	if err != nil {
		return
	}
	serverID := chi.URLParam(r, "serverID")
	if serverID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}
	afterSeq, err := parseQueryInt(r, "after_seq", 0)
	if err != nil || afterSeq < 0 {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "after_seq 必须是非负整数")
		return
	}
	limit := releaseLogsDefault
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		v, perr := strconv.Atoi(raw)
		if perr != nil || v <= 0 {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "limit 必须是正整数")
			return
		}
		if v > releaseLogsMax {
			v = releaseLogsMax
		}
		limit = v
	}

	run, err := a.st.GetReleaseRun(r.Context(), release.ID, serverID)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	logs, err := a.st.ListReleaseLogs(r.Context(), run.ID, afterSeq, limit)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if logs == nil {
		logs = []store.ReleaseLog{}
	}
	writeData(w, http.StatusOK, releaseLogsPayload{Logs: logs})
}

// handleRetryRelease 处理 POST /api/v1/releases/{id}/retry：只重跑 failed 的服务器。
func (a *API) handleRetryRelease(w http.ResponseWriter, r *http.Request) {
	release, err := a.loadRelease(w, r)
	if err != nil {
		return
	}
	var req struct {
		ServerIDs []string `json:"server_ids"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
			return
		}
	}
	if a.deploy == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "发布编排器未启用")
		return
	}
	serverIDs := normalizeIDList(req.ServerIDs)
	n, err := a.deploy.Retry(release.ID, serverIDs)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("重试发布", "release_id", release.ID, "retried", n)
	a.writeReleaseActionResult(w, r, release.ID, map[string]any{"retried": n})
}

// handleRollbackRelease 处理 POST /api/v1/releases/{id}/rollback。
func (a *API) handleRollbackRelease(w http.ResponseWriter, r *http.Request) {
	release, err := a.loadRelease(w, r)
	if err != nil {
		return
	}
	var req struct {
		ServerIDs []string `json:"server_ids"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
			return
		}
	}
	if a.deploy == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "发布编排器未启用")
		return
	}
	n, err := a.deploy.Rollback(release.ID, normalizeIDList(req.ServerIDs))
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("回滚发布", "release_id", release.ID, "rolled_back", n)
	a.writeReleaseActionResult(w, r, release.ID, map[string]any{"rolled_back": n})
}

// handleCancelRelease 处理 POST /api/v1/releases/{id}/cancel。
func (a *API) handleCancelRelease(w http.ResponseWriter, r *http.Request) {
	release, err := a.loadRelease(w, r)
	if err != nil {
		return
	}
	var req struct {
		ServerIDs []string `json:"server_ids"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
			return
		}
	}
	serverIDs := normalizeIDList(req.ServerIDs)
	canceled := 0
	if a.deploy != nil {
		canceled, err = a.deploy.Cancel(release.ID, serverIDs)
		if err != nil {
			a.writeStoreError(w, r, err)
			return
		}
	} else {
		n, cerr := a.st.CancelPendingRuns(r.Context(), release.ID, serverIDs, a.nowMS())
		if cerr != nil {
			a.internalError(w, r, cerr)
			return
		}
		canceled = n
	}
	a.logger.Info("取消发布", "release_id", release.ID, "canceled", canceled)
	a.writeReleaseActionResult(w, r, release.ID, map[string]any{"canceled": canceled})
}

// writeReleaseActionResult 统一返回「动作结果 + 最新发布与 run 状态」。
func (a *API) writeReleaseActionResult(w http.ResponseWriter, r *http.Request, releaseID string, extra map[string]any) {
	release, err := a.st.GetRelease(r.Context(), releaseID)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	runs, err := a.st.ListReleaseRuns(r.Context(), releaseID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if runs == nil {
		runs = []store.ReleaseRun{}
	}
	payload := map[string]any{"release": release, "runs": runs}
	for key, value := range extra {
		payload[key] = value
	}
	writeData(w, http.StatusOK, payload)
}

// loadRelease 读取路径上的发布。
func (a *API) loadRelease(w http.ResponseWriter, r *http.Request) (*store.Release, error) {
	id := chi.URLParam(r, "releaseID")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少发布 ID")
		return nil, errors.New("缺少发布 ID")
	}
	release, err := a.st.GetRelease(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return nil, err
	}
	return release, nil
}

// deployRender 是 deploy.Render 的间接调用点（便于测试替换；渲染**不执行**任何命令）。
var deployRender = deploy.Render
