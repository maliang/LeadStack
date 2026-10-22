package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/maliang/leadstack/internal/deploy"
	"github.com/maliang/leadstack/internal/store"
)

// PreviewStep 是预览里的一个执行步骤（Secret 步骤不回显命令内容）。
type PreviewStep struct {
	Name     string `json:"name"`
	Command  string `json:"command"`
	TimeoutS int    `json:"timeout_s"`
	Secret   bool   `json:"secret,omitempty"`
	Ignored  bool   `json:"ignore_failure,omitempty"`
}

// PreviewServer 是一台服务器上的执行预览。
type PreviewServer struct {
	ServerID   string        `json:"server_id"`
	ServerName string        `json:"server_name"`
	PlanRoot   string        `json:"plan_root"`
	Steps      []PreviewStep `json:"steps"`
	Rollback   []PreviewStep `json:"rollback_steps"`
	Notes      []string      `json:"notes"`
}

// PreviewResult 是一次「发布预览」的结果。
type PreviewResult struct {
	AppID      string          `json:"app_id"`
	AppSlug    string          `json:"app_slug"`
	Version    string          `json:"version"`
	Provider   string          `json:"provider"`
	DeployMode string          `json:"deploy_mode"`
	Servers    []PreviewServer `json:"servers"`
}

// Preview 生成一次发布将要执行的计划（**只读**：不落库、不执行）。
func (o *deployOrchestrator) Preview(ctx context.Context, app *store.App, tmpl *store.DeployTemplate,
	artifact *store.Artifact, version string, servers []*store.Server) (*PreviewResult, error) {

	if o == nil {
		return nil, errors.New("发布编排器未启用")
	}
	out := &PreviewResult{
		AppID: app.ID, AppSlug: app.Slug, Version: version,
		Provider: app.Provider, DeployMode: app.DeployMode,
		Servers: make([]PreviewServer, 0, len(servers)),
	}
	// 用一个临时 release 承载计划参数（不落库）。
	rel := &store.Release{ID: "preview", AppID: app.ID, TemplateID: tmpl.ID, Version: version,
		TemplateSnapshot: tmpl.Content}
	root := o.resolveRoot(app)
	noop := func(string, string) {}

	for _, srv := range servers {
		item := PreviewServer{ServerID: srv.ID, ServerName: srv.Name, Notes: []string{}}
		if app.Provider == store.AppProviderBaota {
			item.PlanRoot = "/www/wwwroot/" + previewSiteName(app)
			item.Steps = baotaPreviewSteps()
			item.Notes = append(item.Notes, baotaPreviewNotes(app, artifact)...)
			out.Servers = append(out.Servers, item)
			continue
		}
		plan, err := o.buildPlanFor(ctx, tmpl.Content, tmpl.Kind, artifact, rel, app, root, "", noop)
		if err != nil {
			item.Notes = append(item.Notes, "构建计划失败: "+err.Error())
			out.Servers = append(out.Servers, item)
			continue
		}
		item.PlanRoot = plan.Root
		item.Steps = toPreviewSteps(plan.Steps)
		item.Rollback = toPreviewSteps(plan.RollbackSteps)
		item.Notes = append(item.Notes, previewNotes(app, artifact, plan)...)
		out.Servers = append(out.Servers, item)
	}
	return out, nil
}

// previewSiteName 返回面板站点名（缺省 slug）。
func previewSiteName(app *store.App) string {
	if app.SiteName != nil && strings.TrimSpace(*app.SiteName) != "" {
		return strings.TrimSpace(*app.SiteName)
	}
	return app.Slug
}

// toPreviewSteps 把计划步骤转成预览结构（Secret 步骤脱敏）。
func toPreviewSteps(steps []deploy.Step) []PreviewStep {
	out := make([]PreviewStep, 0, len(steps))
	for _, s := range steps {
		ps := PreviewStep{Name: s.Name, TimeoutS: s.TimeoutS, Ignored: s.IgnoreFailure, Secret: s.Secret}
		if !s.Secret {
			ps.Command = s.Command
		}
		out = append(out, ps)
	}
	return out
}

// baotaPreviewSteps 描述面板通道的动作序列（不是 shell 计划）。
func baotaPreviewSteps() []PreviewStep {
	names := []string{"panel.ping", "panel.ensure-site", "artifact.read", "panel.backup",
		"panel.upload", "panel.unzip", "healthcheck"}
	out := make([]PreviewStep, 0, len(names))
	for _, n := range names {
		out = append(out, PreviewStep{Name: n})
	}
	return out
}

// baotaPreviewNotes 提示面板通道的硬约束。
func baotaPreviewNotes(app *store.App, artifact *store.Artifact) []string {
	notes := []string{}
	if artifact == nil {
		notes = append(notes, "未选择制品：面板通道需要 zip 制品")
	} else if artifact.SizeBytes > baotaMaxArtifactBytes {
		notes = append(notes, fmt.Sprintf("制品 %d 字节超过面板 API 上限 %d 字节", artifact.SizeBytes, baotaMaxArtifactBytes))
	}
	if hp := strings.TrimSpace(derefStr(app.HealthcheckPath)); hp == "" {
		notes = append(notes, "未配置健康检查路径：部署后不会探测")
	}
	return notes
}

// previewNotes 提示部署配置里的风险点（只提示，不阻断）。
func previewNotes(app *store.App, artifact *store.Artifact, plan deploy.Plan) []string {
	notes := []string{}
	mode := app.DeployMode
	if mode == store.DeployModeInplace && strings.TrimSpace(derefStr(app.KeepPaths)) == "" {
		notes = append(notes, "inplace 模式未配置 keep_paths：目标目录中不在制品里的文件会被删除")
	}
	if mode == store.DeployModeScript && strings.TrimSpace(derefStr(app.RollbackScript)) == "" {
		notes = append(notes, "脚本模式未配置回滚脚本：失败后不会自动回滚")
	}
	if mode != store.DeployModeScript {
		if artifact == nil {
			notes = append(notes, "未选择制品：当前模式需要制品才能发布")
		}
	}
	if strings.TrimSpace(derefStr(app.HealthcheckPath)) == "" {
		notes = append(notes, "未配置健康检查：部署成功与否只看脚本退出码")
	} else if port, _ := healthTarget(app); port == 0 {
		notes = append(notes, "配置了健康检查路径但没有端口：不会执行探测")
	}
	if len(plan.RollbackSteps) == 0 && mode != store.DeployModeInplace {
		notes = append(notes, "本次计划没有回滚步骤")
	}
	return notes
}

// DeployPreset 是内置部署预设（模板 + 推荐的部署参数）。
type DeployPreset struct {
	Key         string            `json:"key"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Template    DeployPresetTmpl  `json:"template"`
	Hints       []string          `json:"hints"`
	Defaults    map[string]string `json:"defaults"`
}

// DeployPresetTmpl 是预设的模板定义。
type DeployPresetTmpl struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

// deployPresets 是内置预设集合（纯静态数据，方便前端「从预设创建」）。
func deployPresets() []DeployPreset {
	return []DeployPreset{
		{
			Key:         "git-pull",
			Name:        "Git 拉取更新（脚本模式）",
			Description: "服务器上已有 git 仓库；发布 = git pull + 重启脚本。适合宝塔直装的 Laravel/LaravelS 项目。",
			Template: DeployPresetTmpl{
				Name: "git-pull-update",
				Kind: store.TemplateKindBinary,
				Content: strings.Join([]string{
					"#!/bin/bash",
					"set -eu",
					"# 工作目录就是应用配置里的 target_dir，脚本已在该目录下执行",
					"git pull --ff-only",
					"# 如需装依赖再取消注释",
					"# composer install --no-dev --no-interaction",
					"bash start.sh restart",
				}, "\n"),
			},
			Hints: []string{
				"部署模式选「自定义脚本」，工作目录填仓库根（如 /www/wwwroot/tuoluojiang）",
				"回滚脚本建议：git reset --hard HEAD@{1} && bash start.sh restart",
				"服务器需要能访问 git 远端（部署密钥/凭据请配在服务器侧）",
			},
			Defaults: map[string]string{"deploy_mode": "script", "healthcheck_path": "/up"},
		},
		{
			Key:         "static-takeover",
			Name:        "接管静态站点（多版本目录）",
			Description: "已有目录里放前端产物；每次发布备份 → rsync 覆盖 → 可选重启。",
			Template: DeployPresetTmpl{
				Name: "static-takeover",
				Kind: store.TemplateKindBinary,
				Content: strings.Join([]string{
					"#!/bin/sh",
					"# inplace 模式会自动把制品解包并 rsync 到 target_dir；",
					"# 这里只在需要额外动作时写（例如清理缓存）。",
					"set -eu",
					"echo \"deployed {{VERSION}} to {{ROOT}}\"",
				}, "\n"),
			},
			Hints: []string{
				"部署模式选「接管已有目录」，工作目录填站点目录",
				"把 data/、uploads/ 等数据目录写进 keep_paths，避免被 rsync 删除",
				"制品建议用 tar.gz 或 zip",
			},
			Defaults: map[string]string{"deploy_mode": "inplace", "keep_paths": "uploads/,storage/"},
		},
		{
			Key:         "baota-site",
			Name:        "宝塔面板站点（面板 API 部署）",
			Description: "由控制端调用宝塔 API：复用/新建站点 → 上传 zip → 解压，可选健康检查。",
			Template: DeployPresetTmpl{
				Name:    "baota-empty",
				Kind:    store.TemplateKindBinary,
				Content: "",
			},
			Hints: []string{
				"先在「凭证」里建宝塔面板凭据并测试连接，再到服务器上绑定",
				"制品必须是 zip 且 ≤16MB",
				"已有同名站点只复用，不会改它的目录/端口配置",
			},
			Defaults: map[string]string{"provider": "baota", "site_type": "static", "site_port": "80"},
		},
		{
			Key:         "systemd-binary",
			Name:        "Systemd 二进制服务（新项目）",
			Description: "release 模式：每次发布到新版本目录，写 unit、切 current、重启、校验服务状态。",
			Template: DeployPresetTmpl{
				Name: "systemd-unit",
				Kind: store.TemplateKindSystemd,
				Content: strings.Join([]string{
					"[Unit]",
					"Description={{APP_SLUG}} {{VERSION}}",
					"After=network.target",
					"",
					"[Service]",
					"Type=simple",
					"WorkingDirectory={{RELEASE_DIR}}",
					"EnvironmentFile=-{{ROOT}}/current/.leadstack.env",
					"ExecStart={{RELEASE_DIR}}/{{APP_SLUG}}",
					"Restart=always",
					"RestartSec=5",
					"",
					"[Install]",
					"WantedBy=multi-user.target",
				}, "\n"),
			},
			Hints: []string{
				"ExecStart 指向制品解包后的可执行文件名（约定与 slug 同名，可自行改）",
				"环境变量会写成 <应用根>/current/.leadstack.env，unit 里用 EnvironmentFile 引用",
				"健康检查建议配 systemd 状态之外的 HTTP 路径（如 /healthz）",
			},
			Defaults: map[string]string{"deploy_mode": "release", "healthcheck_path": "/healthz"},
		},
		{
			Key:         "compose-stack",
			Name:        "Docker Compose（保留数据卷）",
			Description: "release 模式：写 docker-compose.yml、pull、up -d；数据卷路径要在 keep_paths 之外自行挂载。",
			Template: DeployPresetTmpl{
				Name: "compose-stack",
				Kind: store.TemplateKindCompose,
				Content: strings.Join([]string{
					"services:",
					"  app:",
					"    image: {{ARTIFACT_URL}}",
					"    restart: always",
					"    ports:",
					"      - \"8080:8080\"",
					"    volumes:",
					"      - ./data:/app/data",
				}, "\n"),
			},
			Hints: []string{
				"compose 模式用 image 制品：制品类型选「镜像」，填镜像引用",
				"数据目录建议用宿主机绝对路径，避免随 release 目录被清理",
				"健康检查配容器端口对应的 HTTP 路径",
			},
			Defaults: map[string]string{"deploy_mode": "release", "healthcheck_path": "/"},
		},
		{
			Key:         "laravel-swoole",
			Name:        "LaravelS 宝塔直装（git pull + 重启）",
			Description: "拓罗匠这类项目：git pull → composer → start.sh restart，保留 storage 与上传目录。",
			Template: DeployPresetTmpl{
				Name: "laravel-swoole-update",
				Kind: store.TemplateKindBinary,
				Content: strings.Join([]string{
					"#!/bin/bash",
					"set -eu",
					"git pull --ff-only",
					"composer install --no-dev --no-interaction --prefer-dist",
					"php artisan migrate --force || true",
					"php artisan config:clear || true",
					"bash start.sh restart",
				}, "\n"),
			},
			Hints: []string{
				"部署模式选「自定义脚本」，工作目录填项目根",
				"keep_paths 建议：storage/、public/uploads/、.env、vendor/",
				"回滚脚本：git reset --hard HEAD@{1} && bash start.sh restart",
				"健康检查路径填 Laravel 的存活探针（如 /up 或 /healthz）",
			},
			Defaults: map[string]string{
				"deploy_mode": "script", "keep_paths": "storage/,public/uploads/,.env,vendor/",
				"healthcheck_path": "/up",
			},
		},
	}
}
