package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/maliang/leadstack/internal/store"
)

// handleListDeployPresets 处理 GET /api/v1/deploy/presets（内置部署预设）。
func (a *API) handleListDeployPresets(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, deployPresets())
}

// previewRequest 是发布预览的请求体（与创建发布一致，但只读）。
type previewRequest struct {
	AppID      string   `json:"app_id"`
	TemplateID string   `json:"template_id"`
	Version    string   `json:"version"`
	ArtifactID string   `json:"artifact_id"`
	ServerIDs  []string `json:"server_ids"`
}

// handlePreviewRelease 处理 POST /api/v1/releases/preview：返回将要执行的计划，不落库、不执行。
func (a *API) handlePreviewRelease(w http.ResponseWriter, r *http.Request) {
	var req previewRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	version := strings.TrimSpace(req.Version)
	if !versionPattern.MatchString(version) {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			fmt.Sprintf("version 必须是 %d 位以内的 [A-Za-z0-9._-]", maxVersionLen))
		return
	}
	serverIDs := normalizeIDList(req.ServerIDs)
	if len(serverIDs) == 0 {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "server_ids 不能为空")
		return
	}
	if !a.requireServersInScope(w, r, serverIDs) {
		return
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
	if app.DeployMode == store.DeployModeScript {
		if tmpl.Kind != store.TemplateKindBinary || strings.TrimSpace(tmpl.Content) == "" {
			writeError(w, http.StatusBadRequest, codeInvalidArgument,
				"脚本模式需要 binary 模板且内容非空（模板内容即更新脚本）")
			return
		}
	}
	var artifact *store.Artifact
	if strings.TrimSpace(req.ArtifactID) != "" {
		artifact, err = a.st.GetArtifact(ctx, strings.TrimSpace(req.ArtifactID))
		if err != nil {
			a.writeStoreError(w, r, err)
			return
		}
	}

	servers := make([]*store.Server, 0, len(serverIDs))
	for _, id := range serverIDs {
		srv, serr := a.st.GetServer(ctx, id)
		if serr != nil {
			a.writeStoreError(w, r, serr)
			return
		}
		servers = append(servers, srv)
	}

	previewer, ok := a.deploy.(interface {
		Preview(context.Context, *store.App, *store.DeployTemplate, *store.Artifact, string, []*store.Server) (*PreviewResult, error)
	})
	if !ok {
		writeError(w, http.StatusNotImplemented, codeInternal, "发布编排器未启用，无法预览")
		return
	}
	out, err := previewer.Preview(ctx, app, tmpl, artifact, version, servers)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "生成预览失败: "+err.Error())
		return
	}
	writeData(w, http.StatusOK, out)
}
