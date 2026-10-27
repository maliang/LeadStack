package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/maliang/leadstack/internal/auth"
)

// Router 组装全部路由。
//
// 设计要点：
//   - **全平坦注册**（不用 r.Route 子路由）——避免 chi 在「子树 + 同前缀平坦路由」混用时
//     把 /servers/{id} 之类的路径判成 405/404；
//   - 权限**逐条**声明（`r.With(a.RequirePerm(...))`），同一路径的 GET 与写方法可要求不同权限；
//   - 服务器子资源统一套 `a.requireServerInScope`：目标不在角色资源范围内按 404 处理（契约 §53.3）。
//
// 权限模型见 docs/ARCHITECTURE.md 第 52–56 节：
//   - 免鉴权：/healthz、/agent/ws、/api/v1/auth/login，以及终端 WebSocket（一次性 ticket）；
//   - 其余全部要求登录，并按「权限点 + 资源范围」判定。
func (a *API) Router() http.Handler {
	r := chi.NewRouter()

	r.Use(a.middlewareRequestID)
	r.Use(a.middlewareRecover)
	r.Use(a.middlewareLog)
	r.Use(a.auditMiddleware)

	r.Get("/healthz", a.handleHealthz)
	r.Get("/agent/ws", a.handleAgentWS)

	r.Route("/api/v1", func(r chi.Router) {
		// 登录：无鉴权（契约要求保持可访问）。
		r.Post("/auth/login", a.handleLogin)
		// 终端 WebSocket：浏览器无法自定义 Header，用一次性 ticket 鉴权（60s、单次）。
		r.Get("/servers/{serverID}/terminal", a.handleTerminalWS)
		// 制品下载：目标机没有 JWT，用短时效签名直连；浏览器带 JWT 也允许。
		// 必须放在 requireAuth 组**之外**，否则签名请求会先被 401 掉。
		r.With(a.artifactAccessMiddleware).Get("/artifacts/{artifactID}/download", a.handleDownloadArtifact)

		r.Group(func(r chi.Router) {
			r.Use(a.requireAuth)

			// ---------------- 任何登录用户 ----------------
			r.Get("/auth/me", a.handleMe)
			r.Post("/auth/logout", a.handleLogout)
			r.Post("/users/me/password", a.handleChangeOwnPassword)
			r.Get("/system/info", a.handleSystemInfo)
			r.Get("/system/onboarding", a.handleOnboarding)

			inScope := a.requireServerInScope

			// ---------------- 服务器（读） ----------------
			permServerRead := a.RequirePerm(auth.PermServerRead)
			r.With(permServerRead).Get("/groups", a.handleListGroups)
			r.With(permServerRead).Get("/servers", a.handleListServers)
			r.With(permServerRead, inScope).Get("/servers/{serverID}", a.handleGetServer)
			r.With(permServerRead, inScope).Get("/servers/{serverID}/metrics", a.handleServerMetrics)
			r.With(permServerRead, inScope).Get("/servers/{serverID}/checks", a.handleServerChecks)
			r.With(permServerRead).Get("/credentials", a.handleListCredentials)

			// ---------------- 分组（授权单元） ----------------
			permGroupWrite := a.RequirePerm(auth.PermGroupWrite)
			r.With(permServerRead).Get("/server-groups", a.handleListServerGroups)
			r.With(permServerRead).Get("/server-groups/{groupID}/servers", a.handleListServerGroupMembers)
			r.With(permGroupWrite, a.RequireGlobalScope).Post("/server-groups", a.handleCreateServerGroup)
			r.With(permGroupWrite, a.RequireGlobalScope).Patch("/server-groups/{groupID}", a.handlePatchServerGroup)
			r.With(permGroupWrite, a.RequireGlobalScope).Delete("/server-groups/{groupID}", a.handleDeleteServerGroup)
			r.With(permGroupWrite, a.RequireGlobalScope).Put("/server-groups/{groupID}/servers", a.handleSetServerGroupMembers)

			// ---------------- 检测（读） ----------------
			permCheckRead := a.RequirePerm(auth.PermCheckRead)
			r.With(permCheckRead).Get("/checks", a.handleListChecks)
			r.With(permCheckRead).Get("/checks/summary", a.handleCheckSummary)
			r.With(permCheckRead).Get("/checks/{checkID}", a.handleGetCheck)
			r.With(permCheckRead).Get("/checks/{checkID}/results", a.handleListCheckResults)

			// ---------------- 告警（读） ----------------
			permAlertRead := a.RequirePerm(auth.PermAlertRead)
			r.With(permAlertRead).Get("/alert-rules", a.handleListAlertRules)
			r.With(permAlertRead).Get("/alert-rules/{ruleID}", a.handleGetAlertRule)
			r.With(permAlertRead).Get("/alert-channels", a.handleListAlertChannels)
			r.With(permAlertRead).Get("/alerts", a.handleListAlerts)
			r.With(permAlertRead).Get("/alerts/summary", a.handleAlertSummary)
			r.With(permAlertRead).Get("/alerts/{alertID}", a.handleGetAlert)

			// ---------------- 发布（读） ----------------
			permDeployRead := a.RequirePerm(auth.PermDeployRead)
			r.With(permDeployRead).Get("/apps", a.handleListApps)
			r.With(permDeployRead).Get("/apps/{appID}", a.handleGetApp)
			r.With(permDeployRead).Get("/apps/{appID}/rollback-targets", a.handleRollbackTargets)
			r.With(permDeployRead).Get("/templates", a.handleListTemplates)
			r.With(permDeployRead).Get("/templates/{templateID}", a.handleGetTemplate)
			r.With(permDeployRead).Get("/releases", a.handleListReleases)
			r.With(permDeployRead).Get("/releases/{releaseID}", a.handleGetRelease)
			r.With(permDeployRead).Post("/releases/preview", a.handlePreviewRelease)
			r.With(permDeployRead).Get("/deploy/presets", a.handleListDeployPresets)
			r.With(permServerRead).Post("/servers/{serverID}/discover", a.handleDiscoverServer)
			r.With(permServerRead).Post("/servers/{serverID}/diagnose", a.handleDiagnoseServer)
			r.With(permDeployRead).Get("/releases/{releaseID}/runs", a.handleListReleaseRuns)
			r.With(permDeployRead).Get("/releases/{releaseID}/runs/{serverID}/logs", a.handleReleaseLogs)

			// ---------------- 备份（读；全局资源） ----------------
			permBackupRead := a.RequirePerm(auth.PermBackupRead)
			r.With(permBackupRead, a.RequireGlobalScope).Get("/backups", a.handleListBackups)
			r.With(permBackupRead, a.RequireGlobalScope).Get("/backups/{name}/download", a.handleDownloadBackup)
			r.With(permBackupRead, a.RequireGlobalScope).Get("/backup-settings", a.handleGetBackupSettings)

			// ---------------- 定时任务（读） ----------------
			permTaskRead := a.RequirePerm(auth.PermTaskRead)
			r.With(permTaskRead).Get("/scheduled-tasks", a.handleListScheduledTasks)
			r.With(permTaskRead).Get("/scheduled-tasks/{taskID}", a.handleGetScheduledTask)
			r.With(permTaskRead).Get("/scheduled-tasks/{taskID}/runs", a.handleListScheduledTaskRuns)

			// ---------------- 服务器（写 / 删） ----------------
			permServerWrite := a.RequirePerm(auth.PermServerWrite)
			r.With(permServerWrite).Post("/servers", a.handleCreateServer)
			r.With(permServerWrite).Post("/servers/batch", a.handleBatchServers)
			r.With(permServerWrite).Post("/servers/import", a.handleImportServers)
			r.With(permServerWrite, inScope).Patch("/servers/{serverID}", a.handlePatchServer)
			r.With(permServerWrite, inScope).Post("/servers/{serverID}/test-connection", a.handleTestConnection)
			r.With(permServerWrite, inScope).Post("/servers/{serverID}/refresh-host-info", a.handleRefreshHostInfo)
			r.With(a.RequirePerm(auth.PermServerDelete), inScope).Delete("/servers/{serverID}", a.handleDeleteServer)
			r.With(permServerWrite).Post("/credentials", a.handleCreateCredential)
			r.With(permServerWrite).Post("/credentials/{credentialID}/test", a.handleTestCredential)
			r.With(a.RequirePerm(auth.PermServerDelete)).Delete("/credentials/{credentialID}", a.handleDeleteCredential)
			r.With(permServerWrite).Post("/agent/install-script", a.handleInstallScript)

			// ---------------- 终端 / 文件 / 日志 / 进程 / 端口 ----------------
			permTerminal := a.RequirePerm(auth.PermTerminalUse)
			r.With(permTerminal, inScope).Post("/servers/{serverID}/terminal-ticket", a.handleTerminalTicket)
			r.With(permTerminal, inScope).Get("/servers/{serverID}/files", a.handleListFiles)
			r.With(permTerminal, inScope).Post("/servers/{serverID}/files/upload", a.handleUploadFile)
			r.With(permTerminal, inScope).Delete("/servers/{serverID}/files", a.handleDeleteFile)
			r.With(permTerminal, inScope).Get("/servers/{serverID}/files/download", a.handleDownloadFile)
			r.With(permTerminal, inScope).Get("/servers/{serverID}/logs", a.handleServerLogs)
			r.With(permTerminal, inScope).Get("/servers/{serverID}/processes", a.handleServerProcesses)
			r.With(permTerminal, inScope).Get("/servers/{serverID}/ports", a.handleServerPorts)

			// ---------------- 检测（写） ----------------
			permCheckWrite := a.RequirePerm(auth.PermCheckWrite)
			r.With(permCheckWrite).Post("/checks", a.handleCreateCheck)
			r.With(permCheckWrite).Patch("/checks/{checkID}", a.handlePatchCheck)
			r.With(permCheckWrite).Delete("/checks/{checkID}", a.handleDeleteCheck)
			r.With(permCheckWrite).Post("/checks/{checkID}/targets", a.handleSetCheckTargets)
			r.With(permCheckWrite).Post("/checks/{checkID}/run", a.handleRunCheck)

			// ---------------- 告警（写；规则与渠道是全局策略） ----------------
			permAlertWrite := a.RequirePerm(auth.PermAlertWrite)
			r.With(permAlertWrite, a.RequireGlobalScope).Post("/alert-rules", a.handleCreateAlertRule)
			r.With(permAlertWrite, a.RequireGlobalScope).Patch("/alert-rules/{ruleID}", a.handlePatchAlertRule)
			r.With(permAlertWrite, a.RequireGlobalScope).Delete("/alert-rules/{ruleID}", a.handleDeleteAlertRule)
			r.With(permAlertWrite, a.RequireGlobalScope).Post("/alert-rules/{ruleID}/silence", a.handleSilenceAlertRule)
			r.With(permAlertWrite, a.RequireGlobalScope).Post("/alert-channels", a.handleCreateAlertChannel)
			r.With(permAlertWrite, a.RequireGlobalScope).Patch("/alert-channels/{channelID}", a.handlePatchAlertChannel)
			r.With(permAlertWrite, a.RequireGlobalScope).Delete("/alert-channels/{channelID}", a.handleDeleteAlertChannel)
			r.With(permAlertWrite, a.RequireGlobalScope).Post("/alert-channels/{channelID}/test", a.handleTestAlertChannel)
			r.With(permAlertWrite).Post("/alerts/{alertID}/ack", a.handleAckAlert)

			// ---------------- 发布（写） ----------------
			permDeployWrite := a.RequirePerm(auth.PermDeployWrite)
			r.With(permDeployWrite, a.RequireGlobalScope).Post("/apps", a.handleCreateApp)
			r.With(permDeployWrite, a.RequireGlobalScope).Patch("/apps/{appID}", a.handlePatchApp)
			r.With(permDeployWrite, a.RequireGlobalScope).Delete("/apps/{appID}", a.handleDeleteApp)
			r.With(permDeployWrite, a.RequireGlobalScope).Post("/templates", a.handleCreateTemplate)
			r.With(permDeployWrite, a.RequireGlobalScope).Patch("/templates/{templateID}", a.handlePatchTemplate)
			r.With(permDeployWrite, a.RequireGlobalScope).Delete("/templates/{templateID}", a.handleDeleteTemplate)
			r.With(permDeployWrite, a.RequireGlobalScope).Post("/templates/{templateID}/render", a.handleRenderTemplate)
			r.With(permDeployWrite).Post("/artifacts", a.handleCreateArtifact)
			r.With(permDeployWrite).Post("/artifacts/upload", a.handleUploadArtifact)
			r.With(permDeployWrite).Post("/releases", a.handleCreateRelease)
			r.With(permDeployWrite).Post("/releases/{releaseID}/retry", a.handleRetryRelease)
			r.With(permDeployWrite).Post("/releases/{releaseID}/rollback", a.handleRollbackRelease)
			r.With(permDeployWrite).Post("/releases/{releaseID}/cancel", a.handleCancelRelease)

			// ---------------- 备份（写 / 删 / 恢复；全局资源） ----------------
			permBackupWrite := a.RequirePerm(auth.PermBackupWrite)
			r.With(permBackupWrite, a.RequireGlobalScope).Post("/backups", a.handleCreateBackup)
			r.With(permBackupWrite, a.RequireGlobalScope).Patch("/backup-settings", a.handlePatchBackupSettings)
			r.With(a.RequirePerm(auth.PermBackupDelete), a.RequireGlobalScope).Delete("/backups/{name}", a.handleDeleteBackup)
			r.With(a.RequirePerm(auth.PermBackupRestore), a.RequireGlobalScope).Post("/backups/{name}/verify", a.handleVerifyBackup)
			r.With(a.RequirePerm(auth.PermBackupRestore), a.RequireGlobalScope).Post("/backups/restore", a.handleRestoreUpload)
			// 恢复状态只读查询：前端轮询「重启后是否已应用」，无暂存时返回 200 + pending=false。
			r.With(a.RequirePerm(auth.PermBackupRestore), a.RequireGlobalScope).Get("/system/backup/restore-status", a.handleRestoreStatus)

			// ---------------- 定时任务（写） ----------------
			permTaskWrite := a.RequirePerm(auth.PermTaskWrite)
			r.With(permTaskWrite).Post("/scheduled-tasks", a.handleCreateScheduledTask)
			r.With(permTaskWrite).Patch("/scheduled-tasks/{taskID}", a.handlePatchScheduledTask)
			r.With(permTaskWrite).Delete("/scheduled-tasks/{taskID}", a.handleDeleteScheduledTask)
			r.With(permTaskWrite).Post("/scheduled-tasks/{taskID}/run", a.handleRunScheduledTask)

			// ---------------- agent 升级 ----------------
			permAgentUpgrade := a.RequirePerm(auth.PermAgentUpgrade)
			r.With(permAgentUpgrade, inScope).Post("/servers/{serverID}/agent-upgrade", a.handleAgentUpgrade)
			r.With(permAgentUpgrade).Post("/agents/upgrade", a.handleBatchAgentUpgrade)

			// ---------------- 用户 / 角色 / Token / 注册令牌 / 审计 / 系统 ----------------
			permUserManage := a.RequirePerm(auth.PermUserManage)
			r.With(permUserManage).Get("/users", a.handleListUsers)
			r.With(permUserManage).Post("/users", a.handleCreateUser)
			r.With(permUserManage).Patch("/users/{userID}", a.handlePatchUser)
			r.With(permUserManage).Delete("/users/{userID}", a.handleDeleteUser)
			r.With(permUserManage).Post("/users/{userID}/unlock", a.handleUnlockUser)

			permRoleManage := a.RequirePerm(auth.PermRoleManage)
			r.With(permRoleManage).Get("/permissions", a.handleListPermissions)
			r.With(permRoleManage).Get("/roles", a.handleListRoles)
			r.With(permRoleManage).Post("/roles", a.handleCreateRole)
			r.With(permRoleManage).Patch("/roles/{roleKey}", a.handlePatchRole)
			r.With(permRoleManage).Delete("/roles/{roleKey}", a.handleDeleteRole)

			permTokenManage := a.RequirePerm(auth.PermTokenManage)
			r.With(permTokenManage).Get("/api-tokens", a.handleListAPITokens)
			r.With(permTokenManage).Post("/api-tokens", a.handleCreateAPIToken)
			r.With(permTokenManage).Delete("/api-tokens/{tokenID}", a.handleDeleteAPIToken)

			permEnrollManage := a.RequirePerm(auth.PermEnrollManage)
			r.With(permEnrollManage).Get("/enroll-tokens", a.handleListEnrollTokens)
			r.With(permEnrollManage).Post("/enroll-tokens", a.handleCreateEnrollToken)
			// 本机接入：创建 mode=agent 的本机服务器 + 一次性令牌 + 一行安装命令。
			r.With(permEnrollManage).Post("/servers/local", a.handleCreateLocalServer)

			r.With(a.RequirePerm(auth.PermAuditRead), a.RequireGlobalScope).Get("/audit-logs", a.handleListAuditLogs)

			permSystem := a.RequirePerm(auth.PermSystemManage)
			r.With(permSystem, a.RequireGlobalScope).Get("/system/update", a.handleGetUpdate)
			r.With(permSystem, a.RequireGlobalScope).Post("/system/update/check", a.handleCheckUpdate)
			r.With(permSystem, a.RequireGlobalScope).Post("/system/update/apply", a.handleApplyUpdate)
			r.With(permSystem, a.RequireGlobalScope).Get("/system/update/status", a.handleUpdateStatus)
			r.With(permSystem, a.RequireGlobalScope).Get("/system/network", a.handleGetNetwork)
			r.With(permSystem, a.RequireGlobalScope).Put("/system/network", a.handlePutNetwork)
			r.With(permSystem, a.RequireGlobalScope).Post("/system/network/reset", a.handleResetNetwork)
		})
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		if isAPIPath(r.URL.Path) {
			writeError(w, http.StatusNotFound, codeNotFound, "接口不存在")
			return
		}
		a.serveSPA(w, r)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, codeInvalidArgument, "HTTP 方法不被支持")
	})
	return r
}

// isAPIPath 判断路径是否属于后端接口（而非前端 SPA 路由）。
func isAPIPath(p string) bool {
	return p == "/healthz" || strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/agent/")
}
