package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/maliang/leadstack/internal/store"
)

// 分页参数（契约第 39 节）。
const (
	paginationMinLimit = 1
	paginationMaxLimit = 500
	paginationDefault  = 100
	// pageCursorPrefix 是 offset 游标的前缀（不透明字符串，客户端不应解析）。
	pageCursorPrefix = "off:"
	// auditBodyCaptureLimit 是审计记录请求体的最大抓取字节数。
	auditBodyCaptureLimit = 4096
)

// pageMeta 是分页元信息。
type pageMeta struct {
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

// pageParams 是解析后的分页参数。
type pageParams struct {
	Paged  bool
	Limit  int
	Offset int
}

// parsePage 解析 ?limit=&cursor=。
//
// **关键约定**：不传 limit 时 Paged=false，调用方必须保持原有 `{data:[...]}` 数组响应
// （既有 166 项验收依赖这个形状）。
func parsePage(r *http.Request) (pageParams, error) {
	rawLimit := strings.TrimSpace(r.URL.Query().Get("limit"))
	rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if rawLimit == "" {
		return pageParams{Paged: false}, nil
	}
	limit, err := strconv.Atoi(rawLimit)
	if err != nil || limit < paginationMinLimit {
		return pageParams{}, errPage("limit 必须是 1-500 的整数")
	}
	if limit > paginationMaxLimit {
		limit = paginationMaxLimit
	}
	offset := 0
	if rawCursor != "" {
		decoded, derr := base64.RawURLEncoding.DecodeString(rawCursor)
		if derr != nil || !strings.HasPrefix(string(decoded), pageCursorPrefix) {
			return pageParams{}, errPage("cursor 无效")
		}
		n, perr := strconv.Atoi(strings.TrimPrefix(string(decoded), pageCursorPrefix))
		if perr != nil || n < 0 {
			return pageParams{}, errPage("cursor 无效")
		}
		offset = n
	}
	return pageParams{Paged: true, Limit: limit, Offset: offset}, nil
}

// errPage 是分页参数错误（handler 统一映射成 400）。
type pageError struct {
	message string
}

func (e *pageError) Error() string { return e.message }

// errPage 构造分页参数错误。
func errPage(msg string) error { return &pageError{message: msg} }

// fetchLimit 返回应传给 store 的查询上限（多取 1 条用于判断 has_more）。
func (p pageParams) fetchLimit(defaultLimit int) int {
	if !p.Paged {
		return defaultLimit
	}
	return p.Offset + p.Limit + 1
}

// window 从「多取了 1 条」的结果里切出当前页，返回列表与 page 元信息。
func (p pageParams) window(total int) (start, end int, meta pageMeta) {
	if !p.Paged {
		return 0, total, pageMeta{}
	}
	start = p.Offset
	if start > total {
		start = total
	}
	end = start + p.Limit
	if end > total {
		end = total
	}
	hasMore := total > end
	if hasMore {
		meta.NextCursor = encodeCursor(end)
	}
	meta.HasMore = hasMore
	return start, end, meta
}

// encodeCursor 把偏移编码成不透明游标。
func encodeCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(pageCursorPrefix + strconv.Itoa(offset)))
}

// writePagedList 输出列表：未分页时保持 `{data:[...]}`；分页时输出 `{data:[...],page:{...}}`。
func writePagedList(w http.ResponseWriter, params pageParams, items any, meta pageMeta) {
	if !params.Paged {
		writeData(w, http.StatusOK, items)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "page": meta})
}

// ---------- 审计中间件 ----------

// auditActionTable 是「路由模式 + 方法 → action 名」的映射（契约第 36 节命名表）。
//
// 未命中的写操作不会记录（例如内部维护接口），GET/HEAD 一律不记录。
var auditActionTable = map[string]string{
	"POST /api/v1/servers":                              "server.create",
	"PATCH /api/v1/servers/{serverID}":                  "server.update",
	"DELETE /api/v1/servers/{serverID}":                 "server.delete",
	"POST /api/v1/servers/batch":                        "server.batch",
	"POST /api/v1/servers/import":                       "import.servers",
	"POST /api/v1/servers/{serverID}/test-connection":   "server.test_connection",
	"POST /api/v1/servers/{serverID}/refresh-host-info": "server.refresh_host_info",
	"POST /api/v1/servers/{serverID}/agent-upgrade":     "agent.upgrade",
	"POST /api/v1/agents/upgrade":                       "agent.upgrade",
	"POST /api/v1/servers/{serverID}/terminal-ticket":   "terminal.open",
	"POST /api/v1/servers/{serverID}/files/upload":      "file.upload",
	"DELETE /api/v1/servers/{serverID}/files":           "file.delete",
	"POST /api/v1/credentials":                          "credential.create",
	"DELETE /api/v1/credentials/{credentialID}":         "credential.delete",
	"POST /api/v1/checks":                               "check.create",
	"PATCH /api/v1/checks/{checkID}":                    "check.update",
	"DELETE /api/v1/checks/{checkID}":                   "check.delete",
	"POST /api/v1/checks/{checkID}/run":                 "check.run",
	"POST /api/v1/alert-rules":                          "alert_rule.create",
	"PATCH /api/v1/alert-rules/{ruleID}":                "alert_rule.update",
	"DELETE /api/v1/alert-rules/{ruleID}":               "alert_rule.delete",
	"POST /api/v1/alert-rules/{ruleID}/silence":         "alert_rule.silence",
	"POST /api/v1/alert-channels":                       "alert_channel.create",
	"PATCH /api/v1/alert-channels/{channelID}":          "alert_channel.update",
	"DELETE /api/v1/alert-channels/{channelID}":         "alert_channel.delete",
	"POST /api/v1/alert-channels/{channelID}/test":      "alert_channel.test",
	"POST /api/v1/alerts/{alertID}/ack":                 "alert.ack",
	"POST /api/v1/apps":                                 "app.create",
	"PATCH /api/v1/apps/{appID}":                        "app.update",
	"DELETE /api/v1/apps/{appID}":                       "app.delete",
	"POST /api/v1/templates":                            "template.create",
	"PATCH /api/v1/templates/{templateID}":              "template.update",
	"DELETE /api/v1/templates/{templateID}":             "template.delete",
	"POST /api/v1/artifacts":                            "artifact.create",
	"POST /api/v1/releases":                             "release.create",
	"POST /api/v1/releases/{releaseID}/retry":           "release.retry",
	"POST /api/v1/releases/{releaseID}/rollback":        "release.rollback",
	"POST /api/v1/releases/{releaseID}/cancel":          "release.cancel",
	"POST /api/v1/backups":                              "backup.create",
	"DELETE /api/v1/backups/{name}":                     "backup.delete",
	"POST /api/v1/backups/restore":                      "backup.restore",
	"PATCH /api/v1/backup-settings":                     "backup.settings_update",
	"POST /api/v1/users":                                "user.create",
	"PATCH /api/v1/users/{userID}":                      "user.update",
	"DELETE /api/v1/users/{userID}":                     "user.delete",
	"POST /api/v1/users/me/password":                    "user.password_change",
	"POST /api/v1/api-tokens":                           "token.create",
	"DELETE /api/v1/api-tokens/{tokenID}":               "token.delete",
	"POST /api/v1/scheduled-tasks":                      "scheduled_task.create",
	"PATCH /api/v1/scheduled-tasks/{taskID}":            "scheduled_task.update",
	"DELETE /api/v1/scheduled-tasks/{taskID}":           "scheduled_task.delete",
	"POST /api/v1/scheduled-tasks/{taskID}/run":         "scheduled_task.run",
}

// auditResourceIDParams 是可能承载资源 ID 的 URL 参数名（按优先级）。
var auditResourceIDParams = []string{
	"serverID", "credentialID", "checkID", "ruleID", "channelID", "alertID",
	"appID", "templateID", "releaseID", "userID", "tokenID", "taskID", "artifactID", "name",
}

// auditOverride 让 handler 覆盖自动审计的 action/detail（例如记录改端口的旧值→新值）。
type auditOverride struct {
	mu     sync.Mutex
	action string
	detail string
	set    bool
}

// set 记录覆盖值。
func (o *auditOverride) setValues(action, detail string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.action = action
	o.detail = detail
	o.set = true
	o.mu.Unlock()
}

// get 读取覆盖值。
func (o *auditOverride) get() (string, string, bool) {
	if o == nil {
		return "", "", false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.action, o.detail, o.set
}

// auditOverrideCtxKey 是 auditOverride 的上下文键。
type auditOverrideCtxKey struct{}

// setAuditDetail 由 handler 调用，覆盖本次写操作的审计 action 与 detail。
func (a *API) setAuditDetail(r *http.Request, action, detail string) {
	if r == nil {
		return
	}
	if override, ok := r.Context().Value(auditOverrideCtxKey{}).(*auditOverride); ok {
		override.setValues(action, detail)
	}
}

// auditMiddleware 自动记录写操作的审计日志（GET/HEAD 不记录）。
//
// 记录时机在 handler 返回之后：此时能拿到最终状态码、路由模式与调用者身份。
func (a *API) auditMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.Method
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		// 在 handler 之前抓一份脱敏后的请求体摘要（multipart 跳过，避免占用文件流）。
		detail := a.captureAuditDetail(r)

		// 注入 holder：内层认证成功后会把调用者身份回填，供这里记录。
		holder := &actorHolder{}
		override := &auditOverride{}
		ctx := context.WithValue(r.Context(), actorHolderCtxKey{}, holder)
		ctx = context.WithValue(ctx, auditOverrideCtxKey{}, override)
		r = r.WithContext(ctx)

		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		// 路由模式在 handler 执行后由 chi 填充，因此这里读取。
		pattern := ""
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			pattern = rctx.RoutePattern()
		}
		action := auditActionTable[method+" "+pattern]
		if action == "" {
			action = fallbackAuditAction(method, pattern)
		}
		// handler 可覆盖 action/detail（例如记录旧值→新值）。
		if overrideAction, overrideDetail, ok := override.get(); ok {
			if overrideAction != "" {
				action = overrideAction
			}
			if overrideDetail != "" {
				detail = &overrideDetail
			}
		}
		if action == "" {
			return
		}

		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		claims := holder.get()
		actor := "system"
		role := ""
		if claims != nil && claims.Username != "" {
			actor = claims.Username
			role = claims.Role
		}
		entry := &store.AuditLog{
			TS:         a.nowMS(),
			Actor:      actor,
			Action:     action,
			Method:     &method,
			StatusCode: &status,
			Detail:     detail,
		}
		if role != "" {
			entry.ActorRole = &role
		}
		if path := r.URL.Path; path != "" {
			entry.Path = &path
		}
		if ip := remoteHost(r); ip != "" {
			entry.IP = &ip
		}
		if ua := r.UserAgent(); ua != "" {
			entry.UserAgent = &ua
		}
		if rtype, rid := auditResourceFromRequest(r, pattern); rtype != "" {
			entry.ResourceType = &rtype
			if rid != "" {
				entry.ResourceID = &rid
			}
		}
		a.writeAudit(entry)
	})
}

// fallbackAuditAction 为未显式登记的写操作推导一个 action：<资源>.<动词>。
func fallbackAuditAction(method, pattern string) string {
	if pattern == "" || !strings.HasPrefix(pattern, "/api/v1/") {
		return ""
	}
	rest := strings.TrimPrefix(pattern, "/api/v1/")
	resource := strings.Split(rest, "/")[0]
	resource = strings.ReplaceAll(resource, "-", "_")
	// 去掉复数与常见后缀，得到动作名里的资源单数形式。
	resource = strings.TrimSuffix(resource, "s")
	verb := map[string]string{
		http.MethodPost: "create", http.MethodPatch: "update",
		http.MethodPut: "update", http.MethodDelete: "delete",
	}[method]
	if resource == "" || verb == "" {
		return ""
	}
	return resource + "." + verb
}

// auditResourceFromRequest 从路由参数推导资源类型与 ID。
func auditResourceFromRequest(r *http.Request, pattern string) (resourceType, resourceID string) {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		for _, key := range auditResourceIDParams {
			if v := strings.TrimSpace(rctx.URLParam(key)); v != "" {
				resourceID = v
				break
			}
		}
	}
	if pattern != "" && strings.HasPrefix(pattern, "/api/v1/") {
		rest := strings.TrimPrefix(pattern, "/api/v1/")
		resourceType = strings.Trim(strings.Split(rest, "/")[0], "/")
	}
	return resourceType, resourceID
}

// captureAuditDetail 抓取请求体并脱敏（只保留 JSON 摘要）。
func (a *API) captureAuditDetail(r *http.Request) *string {
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "json") || r.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, auditBodyCaptureLimit))
	if err != nil {
		return nil
	}
	// 把请求体还给 handler。
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if len(raw) == 0 {
		return nil
	}
	redacted := redactJSONBody(raw)
	detail := string(redacted)
	return &detail
}

// auditSecretKeys 是必须在审计 detail 中脱敏的字段（小写匹配，包含即视为敏感）。
var auditSecretKeys = []string{
	"password", "passphrase", "private_key", "secret", "token", "bot_token",
	"api_key", "apikey", "access_token", "old_password", "new_password", "content",
}

// isAuditSecretKey 判断字段名是否属于敏感字段。
func isAuditSecretKey(key string) bool {
	lower := strings.ToLower(key)
	for _, part := range auditSecretKeys {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

// redactJSONBody 对 JSON 请求体做递归脱敏；解析失败时直接返回占位符（绝不落原文）。
func redactJSONBody(raw []byte) []byte {
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return []byte(`{"_redacted":"非 JSON 或无法解析的请求体"}`)
	}
	redacted := redactValue(parsed)
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return []byte(`{"_redacted":"序列化失败"}`)
	}
	return encoded
}

// redactValue 递归脱敏。
func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t)+1)
		masked := make([]string, 0, 2)
		for key, value := range t {
			if isAuditSecretKey(key) {
				// 只登记被脱敏的键名，不保留任何可能被误判为明文的内容。
				masked = append(masked, key)
				continue
			}
			out[key] = redactValue(value)
		}
		if len(masked) > 0 {
			out["__masked"] = masked
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, item := range t {
			out = append(out, redactValue(item))
		}
		return out
	default:
		return v
	}
}

// writeAudit 写入一条审计记录（失败只记日志，绝不影响业务响应）。
func (a *API) writeAudit(entry *store.AuditLog) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.st.InsertAuditLog(ctx, entry); err != nil {
		a.logger.Warn("写入审计日志失败", "action", entry.Action, "err", err)
	}
}

// auditManual 供 handler 手动补记一条审计（登录、终端关闭等无路由模式的动作）。
func (a *API) auditManual(r *http.Request, action, resourceType, resourceID, detail string) {
	entry := &store.AuditLog{
		TS:     a.nowMS(),
		Actor:  actorName(r),
		Action: action,
	}
	if role := actorRole(r); role != "" {
		entry.ActorRole = &role
	}
	if method := r.Method; method != "" {
		entry.Method = &method
	}
	if path := r.URL.Path; path != "" {
		entry.Path = &path
	}
	if ip := remoteHost(r); ip != "" {
		entry.IP = &ip
	}
	if ua := r.UserAgent(); ua != "" {
		entry.UserAgent = &ua
	}
	if resourceType != "" {
		entry.ResourceType = &resourceType
	}
	if resourceID != "" {
		entry.ResourceID = &resourceID
	}
	if detail != "" {
		entry.Detail = &detail
	}
	a.writeAudit(entry)
}

// auditLogin 记录登录成功/失败（此时还没有认证身份，actor 用尝试登录的用户名）。
func (a *API) auditLogin(r *http.Request, username string, ok bool, reason string) {
	action := "auth.login"
	detail := fmt.Sprintf(`{"success":%t}`, ok)
	if !ok {
		detail = fmt.Sprintf(`{"success":false,"reason":%q}`, reason)
	}
	entry := &store.AuditLog{TS: a.nowMS(), Actor: username, Action: action}
	if username == "" {
		entry.Actor = "anonymous"
	}
	if method := r.Method; method != "" {
		entry.Method = &method
	}
	if path := r.URL.Path; path != "" {
		entry.Path = &path
	}
	if ip := remoteHost(r); ip != "" {
		entry.IP = &ip
	}
	if ua := r.UserAgent(); ua != "" {
		entry.UserAgent = &ua
	}
	entry.Detail = &detail
	status := http.StatusOK
	if !ok {
		status = http.StatusUnauthorized
	}
	entry.StatusCode = &status
	a.writeAudit(entry)
}

// handleListAuditLogs 处理 GET /api/v1/audit-logs（仅 admin）。
func (a *API) handleListAuditLogs(w http.ResponseWriter, r *http.Request) {
	params, err := parsePage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	from, err := parseQueryInt(r, "from", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "from 必须是 Unix 毫秒整数")
		return
	}
	to, err := parseQueryInt(r, "to", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "to 必须是 Unix 毫秒整数")
		return
	}

	limit := paginationDefault
	if params.Paged {
		limit = params.fetchLimit(paginationDefault)
	}
	rows, err := a.st.ListAuditLogs(r.Context(), store.AuditFilter{
		Actor:        strings.TrimSpace(r.URL.Query().Get("actor")),
		Action:       strings.TrimSpace(r.URL.Query().Get("action")),
		ResourceType: strings.TrimSpace(r.URL.Query().Get("resource_type")),
		From:         from,
		To:           to,
		Limit:        limit,
	})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if rows == nil {
		rows = []store.AuditLog{}
	}
	start, end, meta := params.window(len(rows))
	if !params.Paged {
		start, end = 0, len(rows)
		writeData(w, http.StatusOK, rows[start:end])
		return
	}
	// 分页时把列表放进 logs 字段，page 放在顶层（验收脚本与前端都兼容这种形状）。
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"logs": rows[start:end]}, "page": meta})
}
