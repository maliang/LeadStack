package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Router 组装全部路由。
//
// 顺序很关键：具体路由优先注册，未匹配的路径再由 NotFound 分流
// （/api 与 /agent 返回 JSON 404，其余交给 SPA）。
func (a *API) Router() http.Handler {
	r := chi.NewRouter()

	r.Use(a.middlewareRequestID)
	r.Use(a.middlewareRecover)
	r.Use(a.middlewareLog)

	// 免鉴权探针。
	r.Get("/healthz", a.handleHealthz)

	// agent WebSocket：使用 enroll-token 握手，不走 JWT 中间件。
	r.Get("/agent/ws", a.handleAgentWS)

	r.Route("/api/v1", func(r chi.Router) {
		// 登录免鉴权。
		r.Post("/auth/login", a.handleLogin)

		r.Group(func(r chi.Router) {
			r.Use(a.authMgr.RequireAuth)

			r.Get("/auth/me", a.handleMe)
			r.Post("/auth/logout", a.handleLogout)
			r.Get("/system/info", a.handleSystemInfo)

			r.Route("/servers", func(r chi.Router) {
				r.Get("/", a.handleListServers)
				r.Post("/", a.handleCreateServer)
				// 批量操作必须先于 /{serverID} 注册，避免 "batch" 被当成服务器 ID。
				r.Post("/batch", a.handleBatchServers)
				r.Route("/{serverID}", func(r chi.Router) {
					r.Get("/", a.handleGetServer)
					r.Patch("/", a.handlePatchServer)
					r.Delete("/", a.handleDeleteServer)
					r.Post("/test-connection", a.handleTestConnection)
					r.Post("/refresh-host-info", a.handleRefreshHostInfo)
					r.Get("/metrics", a.handleServerMetrics)
					r.Get("/checks", a.handleServerChecks)
				})
			})

			r.Get("/groups", a.handleListGroups)

			// ---- M3 告警 ----
			r.Route("/alert-rules", func(r chi.Router) {
				r.Get("/", a.handleListAlertRules)
				r.Post("/", a.handleCreateAlertRule)
				r.Route("/{ruleID}", func(r chi.Router) {
					r.Get("/", a.handleGetAlertRule)
					r.Patch("/", a.handlePatchAlertRule)
					r.Delete("/", a.handleDeleteAlertRule)
					r.Post("/silence", a.handleSilenceAlertRule)
				})
			})

			r.Route("/alert-channels", func(r chi.Router) {
				r.Get("/", a.handleListAlertChannels)
				r.Post("/", a.handleCreateAlertChannel)
				r.Route("/{channelID}", func(r chi.Router) {
					r.Patch("/", a.handlePatchAlertChannel)
					r.Delete("/", a.handleDeleteAlertChannel)
					r.Post("/test", a.handleTestAlertChannel)
				})
			})

			// ---- M4 批量部署 ----
			r.Route("/apps", func(r chi.Router) {
				r.Get("/", a.handleListApps)
				r.Post("/", a.handleCreateApp)
				r.Route("/{appID}", func(r chi.Router) {
					r.Get("/", a.handleGetApp)
					r.Patch("/", a.handlePatchApp)
					r.Delete("/", a.handleDeleteApp)
				})
			})

			r.Route("/templates", func(r chi.Router) {
				r.Get("/", a.handleListTemplates)
				r.Post("/", a.handleCreateTemplate)
				r.Route("/{templateID}", func(r chi.Router) {
					r.Get("/", a.handleGetTemplate)
					r.Patch("/", a.handlePatchTemplate)
					r.Delete("/", a.handleDeleteTemplate)
					r.Post("/render", a.handleRenderTemplate)
				})
			})

			r.Route("/artifacts", func(r chi.Router) {
				r.Post("/", a.handleCreateArtifact)
				r.Get("/{artifactID}/download", a.handleDownloadArtifact)
			})

			r.Route("/releases", func(r chi.Router) {
				r.Get("/", a.handleListReleases)
				r.Post("/", a.handleCreateRelease)
				r.Route("/{releaseID}", func(r chi.Router) {
					r.Get("/", a.handleGetRelease)
					r.Get("/runs", a.handleListReleaseRuns)
					r.Get("/runs/{serverID}/logs", a.handleReleaseLogs)
					r.Post("/retry", a.handleRetryRelease)
					r.Post("/rollback", a.handleRollbackRelease)
					r.Post("/cancel", a.handleCancelRelease)
				})
			})

			r.Route("/alerts", func(r chi.Router) {
				// /alerts/summary 必须先于 /alerts/{alertID} 注册。
				r.Get("/summary", a.handleAlertSummary)
				r.Get("/", a.handleListAlerts)
				r.Route("/{alertID}", func(r chi.Router) {
					r.Get("/", a.handleGetAlert)
					r.Post("/ack", a.handleAckAlert)
				})
			})

			r.Route("/checks", func(r chi.Router) {
				// /checks/summary 与 /checks/{checkID} 同为静态优先，显式先注册避免歧义。
				r.Get("/summary", a.handleCheckSummary)
				r.Get("/", a.handleListChecks)
				r.Post("/", a.handleCreateCheck)
				r.Route("/{checkID}", func(r chi.Router) {
					r.Get("/", a.handleGetCheck)
					r.Patch("/", a.handlePatchCheck)
					r.Delete("/", a.handleDeleteCheck)
					r.Post("/targets", a.handleSetCheckTargets)
					r.Post("/run", a.handleRunCheck)
					r.Get("/results", a.handleListCheckResults)
				})
			})

			r.Route("/credentials", func(r chi.Router) {
				r.Get("/", a.handleListCredentials)
				r.Post("/", a.handleCreateCredential)
				r.Delete("/{credentialID}", a.handleDeleteCredential)
			})

			r.Route("/enroll-tokens", func(r chi.Router) {
				r.Get("/", a.handleListEnrollTokens)
				r.Post("/", a.handleCreateEnrollToken)
			})

			r.Post("/agent/install-script", a.handleInstallScript)
		})
	})

	r.NotFound(a.handleNotFound)
	r.MethodNotAllowed(a.handleMethodNotAllowed)
	return r
}

// handleNotFound 对 API/agent 路径返回 JSON 404，其余交给 SPA 回退。
func (a *API) handleNotFound(w http.ResponseWriter, r *http.Request) {
	if isAPIPath(r.URL.Path) {
		writeError(w, http.StatusNotFound, codeNotFound, "接口不存在")
		return
	}
	a.serveSPA(w, r)
}

// handleMethodNotAllowed 统一方法不允许的响应。
func (a *API) handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusMethodNotAllowed, codeInvalidArgument, "HTTP 方法不被支持")
}

// isAPIPath 判断路径是否属于后端接口（而非前端 SPA 路由）。
func isAPIPath(p string) bool {
	return p == "/healthz" || strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/agent/")
}
