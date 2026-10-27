package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/maliang/leadstack/internal/deploy"
	"github.com/maliang/leadstack/internal/store"
)

// RollbackTarget 是一个可回滚到的历史版本。
type RollbackTarget struct {
	ReleaseID  string   `json:"release_id"`
	Version    string   `json:"version"`
	CreatedAt  int64    `json:"created_at"`
	FinishedAt *int64   `json:"finished_at"`
	ServerIDs  []string `json:"server_ids"`
}

// handleRollbackTargets 处理 GET /api/v1/apps/{appID}/rollback-targets?server_id=：
// 返回该应用**发布成功**的历史版本（可按服务器过滤，只有该服务器成功过的才算可选）。
func (a *API) handleRollbackTargets(w http.ResponseWriter, r *http.Request) {
	app, err := a.loadApp(w, r)
	if err != nil {
		return
	}
	ctx := r.Context()
	serverFilter := strings.TrimSpace(r.URL.Query().Get("server_id"))
	if serverFilter != "" && !a.requireServersInScope(w, r, []string{serverFilter}) {
		return
	}

	releases, err := a.st.ListReleases(ctx, store.ReleaseFilter{AppID: app.ID, Status: store.ReleaseStatusSuccess, Limit: 50})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]RollbackTarget, 0, len(releases))
	for _, rel := range releases {
		runs, rerr := a.st.ListReleaseRuns(ctx, rel.ID)
		if rerr != nil {
			continue
		}
		servers := make([]string, 0, len(runs))
		for _, run := range runs {
			if run.Status != store.ReleaseStatusSuccess {
				continue
			}
			if serverFilter != "" && run.ServerID != serverFilter {
				continue
			}
			servers = append(servers, run.ServerID)
		}
		if len(servers) == 0 {
			continue
		}
		out = append(out, RollbackTarget{
			ReleaseID: rel.ID, Version: rel.Version, CreatedAt: rel.CreatedAt,
			FinishedAt: rel.FinishedAt, ServerIDs: servers,
		})
	}
	writeData(w, http.StatusOK, out)
}

// DiagnoseCheck 是体检的一项结果。
type DiagnoseCheck struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"` // pass | warn | fail
	Detail string `json:"detail"`
}

// DiagnoseRecommendation 是体检给出的接管建议。
type DiagnoseRecommendation struct {
	DeployMode      string   `json:"deploy_mode"`
	TargetDir       string   `json:"target_dir"`
	RestartCmd      string   `json:"restart_cmd,omitempty"`
	KeepPaths       []string `json:"keep_paths,omitempty"`
	HealthcheckPort int      `json:"healthcheck_port,omitempty"`
	Script          string   `json:"script,omitempty"`
}

// DiagnoseReport 是一次接管体检的结果。
type DiagnoseReport struct {
	ServerID       string                 `json:"server_id"`
	Name           string                 `json:"name"`
	Kind           string                 `json:"kind"`
	TargetDir      string                 `json:"target_dir"`
	Checks         []DiagnoseCheck        `json:"checks"`
	Recommendation DiagnoseRecommendation `json:"recommendation"`
}

// diagnoseScript 是只读体检脚本：目录权限/磁盘/git 状态/systemd 单元/服务状态/端口。
func diagnoseScript(dir, unit string) string {
	return strings.Join([]string{
		"set -u",
		"DIR=" + shQuoteShell(dir),
		"UNIT=" + shQuoteShell(unit),
		`echo "###DIR"`,
		`[ -d "$DIR" ] && echo "exists" || echo "missing"`,
		`[ -w "$DIR" ] && echo "writable" || echo "readonly"`,
		`df -Pk "$DIR" 2>/dev/null | tail -1`,
		`echo "###GIT"`,
		`git -C "$DIR" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "-"`,
		`git -C "$DIR" status --porcelain 2>/dev/null | wc -l | tr -d ' '`,
		`git -C "$DIR" remote get-url origin 2>/dev/null || echo "-"`,
		`echo "###UNIT"`,
		`systemctl show -p ExecStart -p WorkingDirectory -p User -p FragmentPath --value "$UNIT" 2>/dev/null || true`,
		`systemctl is-active "$UNIT" 2>/dev/null || true`,
		`echo "###PORTS"`,
		`(ss -lntp 2>/dev/null || netstat -lntp 2>/dev/null) | grep -F "127.0.0.1" | head -20 || true`,
		`echo "###END"`,
	}, "\n")
}

// shQuoteShell 把值安全地放进 shell（单引号转义）。
func shQuoteShell(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// handleDiagnoseServer 处理 POST /api/v1/servers/{serverID}/diagnose：
// 对「要接管的候选」做一次只读体检，并给出接管建议。
func (a *API) handleDiagnoseServer(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	var req struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		TargetDir string `json:"target_dir"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	switch kind {
	case "systemd", "docker", "site":
	default:
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "kind 只能是 systemd/docker/site")
		return
	}
	name := strings.TrimSpace(req.Name)
	dir := strings.TrimSpace(req.TargetDir)
	if name == "" && dir == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "name 或 target_dir 至少提供一个")
		return
	}
	if dir != "" && !strings.HasPrefix(dir, "/") {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "target_dir 必须是绝对路径")
		return
	}
	if !a.requireServersInScope(w, r, []string{serverID}) {
		return
	}
	if _, err := a.st.GetServer(r.Context(), serverID); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	diag, ok := a.deploy.(interface {
		RunDiagnose(ctx context.Context, serverID, script string) (string, error)
	})
	if !ok {
		// 控制端本地没装配执行器：503 unavailable，不是目标机的 502。
		writeUnavailable(w, "发布执行器未启用，无法体检")
		return
	}
	unit := name
	if kind == "systemd" && unit != "" && !strings.HasSuffix(unit, ".service") {
		unit += ".service"
	}
	out, err := diag.RunDiagnose(r.Context(), serverID, diagnoseScript(dir, unit))
	if err != nil {
		if errors.Is(err, errOrchestratorDisabled) {
			// 本地装配缺陷（执行器存在但执行端为空），同样不能算远端失败。
			writeUnavailable(w, "发布执行器未启用，无法体检")
			return
		}
		// 目标机不可达/脚本执行失败属于远端失败，统一 502 + remote_failure（A19）。
		a.writeRemoteError(w, r, err, "体检")
		return
	}
	writeData(w, http.StatusOK, buildDiagnoseReport(serverID, kind, name, dir, out))
}

// RunDiagnose 执行只读体检脚本并返回原始输出。
func (o *deployOrchestrator) RunDiagnose(ctx context.Context, serverID, script string) (string, error) {
	if o == nil || o.exec == nil {
		return "", errOrchestratorDisabled
	}
	plan := deploy.Plan{
		AppSlug: "diagnose", ReleaseID: "diagnose", Version: "0", Root: "/tmp",
		Steps: []deploy.Step{{Name: "diagnose", Command: script, TimeoutS: 60}},
	}
	var sb strings.Builder
	outcome, err := o.exec.RunPlan(ctx, serverID, plan, deploy.Hooks{OnLog: func(stream, chunk string) {
		if stream == "stdout" {
			sb.WriteString(chunk)
		}
	}})
	if err != nil && outcome.Status == "" {
		return "", fmt.Errorf("执行体检脚本失败: %w", err)
	}
	if outcome.Status != "" && outcome.Status != deploy.StatusSuccess {
		return "", fmt.Errorf("执行体检脚本失败: %s", strings.TrimSpace(outcome.Message))
	}
	return sb.String(), nil
}

// buildDiagnoseReport 把体检脚本输出转成结构化报告 + 接管建议。
func buildDiagnoseReport(serverID, kind, name, dir, raw string) *DiagnoseReport {
	sections := map[string]string{}
	cur := ""
	var buf strings.Builder
	flush := func() {
		if cur != "" {
			sections[cur] = buf.String()
			buf.Reset()
		}
	}
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "###") {
			flush()
			cur = strings.TrimPrefix(line, "###")
			continue
		}
		buf.WriteString(line)
		buf.WriteString("\n")
	}
	flush()

	checks := make([]DiagnoseCheck, 0, 8)
	add := func(id, title, status, detail string) {
		checks = append(checks, DiagnoseCheck{ID: id, Title: title, Status: status, Detail: detail})
	}

	// 目录
	dirLines := strings.Fields(strings.TrimSpace(sections["DIR"]))
	exists := len(dirLines) > 0 && dirLines[0] == "exists"
	writable := len(dirLines) > 1 && dirLines[1] == "writable"
	if dir == "" {
		add("dir", "目标目录", "warn", "未提供目录（systemd/docker 候选可留空）")
	} else if exists {
		add("dir", "目标目录存在", "pass", dir)
	} else {
		add("dir", "目标目录存在", "fail", dir+" 不存在")
	}
	if dir != "" && exists && writable {
		add("writable", "目录可写", "pass", "LeadStack 可以在其中备份/落盘")
	} else if dir != "" && exists {
		add("writable", "目录可写", "fail", "目录不可写：部署会失败，请检查属主/权限")
	}
	if len(dirLines) > 2 {
		add("disk", "磁盘可用", "pass", dirLines[2])
	}

	// git
	gitLines := strings.Split(sections["GIT"], "\n")
	branch := strings.TrimSpace(firstOr(gitLines, 0))
	dirty := strings.TrimSpace(firstOr(gitLines, 1))
	remote := strings.TrimSpace(firstOr(gitLines, 2))
	isGit := branch != "" && branch != "-"
	if isGit {
		status := "pass"
		if dirty != "" && dirty != "0" {
			status = "warn"
		}
		add("git", "git 仓库", status, fmt.Sprintf("分支 %s，未提交改动 %s 个，remote %s", branch, dirty, remote))
	} else if dir != "" && exists {
		add("git", "git 仓库", "warn", "不是 git 仓库：建议用 inplace 模式（制品覆盖）")
	}

	// systemd
	unitLines := strings.Split(strings.TrimSpace(sections["UNIT"]), "\n")
	execStart := strings.TrimSpace(firstOr(unitLines, 0))
	workDir := strings.TrimSpace(firstOr(unitLines, 1))
	unitUser := strings.TrimSpace(firstOr(unitLines, 2))
	fragment := strings.TrimSpace(firstOr(unitLines, 3))
	active := strings.TrimSpace(firstOr(unitLines, 4))
	if kind == "systemd" || name != "" {
		if execStart != "" {
			add("unit", "systemd 单元", "pass", fmt.Sprintf("%s · ExecStart=%s", name, execStart))
			if dir != "" && strings.Contains(execStart, dir) {
				add("unit-path", "单元路径与目标目录一致", "pass", "unit 的 ExecStart 落在目标目录内（可 inplace 接管）")
			} else if dir != "" {
				add("unit-path", "单元路径与目标目录一致", "warn", "ExecStart 不在目标目录内：接管前请确认替换路径")
			}
			if workDir != "" {
				add("unit-workdir", "WorkingDirectory", "pass", workDir)
			}
			if unitUser != "" && unitUser != "-" && unitUser != "root" {
				add("unit-user", "运行用户", "warn", "服务以 "+unitUser+" 运行：部署目录需要该用户可读，或部署后修正属主")
			}
			if fragment != "" {
				add("unit-file", "单元文件", "pass", fragment)
			}
			if active != "" && active != "-" {
				add("unit-active", "服务状态", "pass", active)
			}
		} else if kind == "systemd" {
			add("unit", "systemd 单元", "warn", "未找到单元 "+name+"（可能不是 systemd 管理）")
		}
	}

	// 建议
	rec := DiagnoseRecommendation{TargetDir: dir, KeepPaths: []string{"storage/", "public/uploads/", ".env", "vendor/"}}
	if isGit {
		rec.DeployMode = store.DeployModeScript
		rec.Script = "#!/bin/bash\nset -eu\ngit pull --ff-only\nbash start.sh restart"
	} else {
		rec.DeployMode = store.DeployModeInplace
	}
	if execStart != "" && kind == "systemd" {
		rec.RestartCmd = "systemctl restart " + name
	}
	if !isGit && rec.Script != "" {
		rec.KeepPaths = nil
	}

	return &DiagnoseReport{
		ServerID: serverID, Name: name, Kind: kind, TargetDir: dir,
		Checks: checks, Recommendation: rec,
	}
}

// firstOr 返回切片第 i 个元素（越界返回空串）。
func firstOr(items []string, i int) string {
	if i >= 0 && i < len(items) {
		return items[i]
	}
	return ""
}
