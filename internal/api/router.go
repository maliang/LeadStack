package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Router 组装全部路由。
//
// 设计要点：
//   - **全平坦注册**（不用 r.Route 子路由）——避免 chi 在「子树 + 同前缀平坦路由」混用时
//     把 /servers/{id} 之类的路径判成 405/404；
//   - 角色分组用 r.Group，但同一路径的 GET 与写方法必须在同一组内，否则会互相抢占节点。
//
// 权限（契约第 37 节）：
//   - 免鉴权：/healthz、/agent/ws、/api/v1/auth/login；
//   - viewer+：业务只读；
//   - operator+：业务写 + 终端/文件/日志/进程/端口/定时任务/备份创建；
//   - admin：用户、审计、Token、删除服务器/凭证、备份恢复与校验、Agent 升级、enroll token。
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

		r.Group(func(r chi.Router) {
			r.Use(a.requireAuth)

			// ---------------- viewer 及以上：只读 ----------------
			r.Get("/auth/me", a.handleMe)
			r.Post("/auth/logout", a.handleLogout)
			r.Post("/users/me/password", a.handleChangeOwnPassword)
			r.Get("/system/info", a.handleSystemInfo)
			r.Get("/system/onboarding", a.handleOnboarding)
			r.Get("/groups", a.handleListGroups)

			r.Get("/servers", a.handleListServers)
			r.Get("/servers/{serverID}", a.handleGetServer)
			r.Get("/credentials", a.handleListCredentials)
			r.Get("/checks", a.handleListChecks)
			r.Get("/checks/summary", a.handleCheckSummary)
			r.Get("/checks/{checkID}", a.handleGetCheck)
			r.Get("/checks/{checkID}/results", a.handleListCheckResults)
			r.Get("/alert-rules", a.handleListAlertRules)
			r.Get("/alert-rules/{ruleID}", a.handleGetAlertRule)
			r.Get("/alert-channels", a.handleListAlertChannels)
			r.Get("/alerts", a.handleListAlerts)
			r.Get("/alerts/summary", a.handleAlertSummary)
			r.Get("/alerts/{alertID}", a.handleGetAlert)
			r.Get("/apps", a.handleListApps)
			r.Get("/apps/{appID}", a.handleGetApp)
			r.Get("/templates", a.handleListTemplates)
			r.Get("/templates/{templateID}", a.handleGetTemplate)
			r.Get("/releases", a.handleListReleases)
			r.Get("/releases/{releaseID}", a.handleGetRelease)
			r.Get("/releases/{releaseID}/runs", a.handleListReleaseRuns)
			r.Get("/releases/{releaseID}/runs/{serverID}/logs", a.handleReleaseLogs)
			r.Get("/backups", a.handleListBackups)
			r.Get("/backups/{name}/download", a.handleDownloadBackup)
			r.Get("/backup-settings", a.handleGetBackupSettings)
			r.Get("/scheduled-tasks", a.handleListScheduledTasks)
			r.Get("/scheduled-tasks/{taskID}", a.handleGetScheduledTask)
			r.Get("/scheduled-tasks/{taskID}/runs", a.handleListScheduledTaskRuns)

			// ---------------- operator 及以上 ----------------
			r.Group(func(r chi.Router) {
				r.Use(a.RequireRole(roleOperator))

				r.Post("/servers", a.handleCreateServer)
				r.Post("/servers/batch", a.handleBatchServers)
				r.Post("/servers/import", a.handleImportServers)
				r.Patch("/servers/{serverID}", a.handlePatchServer)
				r.Post("/servers/{serverID}/test-connection", a.handleTestConnection)
				r.Post("/servers/{serverID}/refresh-host-info", a.handleRefreshHostInfo)
				r.Get("/servers/{serverID}/metrics", a.handleServerMetrics)
				r.Get("/servers/{serverID}/checks", a.handleServerChecks)

				// 终端/文件/日志/进程/端口（运维动作，operator 起）。
				r.Post("/servers/{serverID}/terminal-ticket", a.handleTerminalTicket)

				r.Get("/servers/{serverID}/files", a.handleListFiles)
				r.Post("/servers/{serverID}/files/upload", a.handleUploadFile)
				r.Delete("/servers/{serverID}/files", a.handleDeleteFile)
				r.Get("/servers/{serverID}/files/download", a.handleDownloadFile)
				r.Get("/servers/{serverID}/logs", a.handleServerLogs)
				r.Get("/servers/{serverID}/processes", a.handleServerProcesses)
				r.Get("/servers/{serverID}/ports", a.handleServerPorts)

				r.Post("/credentials", a.handleCreateCredential)
				r.Post("/checks", a.handleCreateCheck)
				r.Patch("/checks/{checkID}", a.handlePatchCheck)
				r.Delete("/checks/{checkID}", a.handleDeleteCheck)
				r.Post("/checks/{checkID}/targets", a.handleSetCheckTargets)
				r.Post("/checks/{checkID}/run", a.handleRunCheck)
				r.Post("/alert-rules", a.handleCreateAlertRule)
				r.Patch("/alert-rules/{ruleID}", a.handlePatchAlertRule)
				r.Post("/alert-rules/{ruleID}/silence", a.handleSilenceAlertRule)
				r.Post("/alert-channels", a.handleCreateAlertChannel)
				r.Patch("/alert-channels/{channelID}", a.handlePatchAlertChannel)
				r.Post("/alert-channels/{channelID}/test", a.handleTestAlertChannel)
				r.Post("/alerts/{alertID}/ack", a.handleAckAlert)
				r.Post("/apps", a.handleCreateApp)
				r.Patch("/apps/{appID}", a.handlePatchApp)
				r.Post("/templates", a.handleCreateTemplate)
				r.Patch("/templates/{templateID}", a.handlePatchTemplate)
				r.Post("/templates/{templateID}/render", a.handleRenderTemplate)
				r.Post("/artifacts", a.handleCreateArtifact)
				r.Post("/artifacts/upload", a.handleUploadArtifact)
				r.Get("/artifacts/{artifactID}/download", a.handleDownloadArtifact)
				r.Post("/releases", a.handleCreateRelease)
				r.Post("/releases/{releaseID}/retry", a.handleRetryRelease)
				r.Post("/releases/{releaseID}/rollback", a.handleRollbackRelease)
				r.Post("/releases/{releaseID}/cancel", a.handleCancelRelease)
				r.Post("/backups", a.handleCreateBackup)
				r.Patch("/backup-settings", a.handlePatchBackupSettings)
				r.Post("/scheduled-tasks", a.handleCreateScheduledTask)
				r.Patch("/scheduled-tasks/{taskID}", a.handlePatchScheduledTask)
				r.Delete("/scheduled-tasks/{taskID}", a.handleDeleteScheduledTask)
				r.Post("/scheduled-tasks/{taskID}/run", a.handleRunScheduledTask)
				r.Post("/agent/install-script", a.handleInstallScript)
			})

			// ---------------- admin 专属 ----------------
			r.Group(func(r chi.Router) {
				r.Use(a.RequireRole(roleAdmin))

				r.Get("/system/network", a.handleGetNetwork)
				r.Put("/system/network", a.handlePutNetwork)
				r.Post("/system/network/reset", a.handleResetNetwork)

				r.Get("/audit-logs", a.handleListAuditLogs)
				r.Get("/enroll-tokens", a.handleListEnrollTokens)
				r.Post("/enroll-tokens", a.handleCreateEnrollToken)
				r.Get("/api-tokens", a.handleListAPITokens)
				r.Post("/api-tokens", a.handleCreateAPIToken)
				r.Delete("/api-tokens/{tokenID}", a.handleDeleteAPIToken)
				r.Get("/users", a.handleListUsers)
				r.Post("/users", a.handleCreateUser)
				r.Patch("/users/{userID}", a.handlePatchUser)
				r.Delete("/users/{userID}", a.handleDeleteUser)

				r.Delete("/servers/{serverID}", a.handleDeleteServer)
				r.Delete("/credentials/{credentialID}", a.handleDeleteCredential)
				r.Delete("/alert-rules/{ruleID}", a.handleDeleteAlertRule)
				r.Delete("/alert-channels/{channelID}", a.handleDeleteAlertChannel)
				r.Delete("/apps/{appID}", a.handleDeleteApp)
				r.Delete("/templates/{templateID}", a.handleDeleteTemplate)

				r.Delete("/backups/{name}", a.handleDeleteBackup)
				r.Post("/backups/{name}/verify", a.handleVerifyBackup)
				r.Post("/backups/restore", a.handleRestoreUpload)

				r.Post("/servers/{serverID}/agent-upgrade", a.handleAgentUpgrade)
				r.Post("/agents/upgrade", a.handleBatchAgentUpgrade)
			})
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
