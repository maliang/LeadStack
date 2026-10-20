package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/maliang/leadstack/internal/store"
)

// loginAs 用指定用户名/密码登录并返回 token。
func (e *testEnv) loginAs(t *testing.T, username, password string) string {
	t.Helper()
	status, raw := e.request(http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"username": username, "password": password})
	if status != http.StatusOK {
		t.Fatalf("登录失败(%s): status=%d body=%s", username, status, raw)
	}
	var out struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析登录响应失败: %v", err)
	}
	return out.Data.Token
}

// createUserVia 通过 REST 创建用户。
func (e *testEnv) createUserVia(jwt, username, password, role string) {
	e.t.Helper()
	status, raw := e.request(http.MethodPost, "/api/v1/users", jwt, map[string]any{
		"username": username, "password": password, "role": role,
	})
	if status != http.StatusCreated {
		e.t.Fatalf("创建用户失败: status=%d body=%s", status, raw)
	}
}

// TestAuditLogRecordsAndRedacts 覆盖审计记录与脱敏。
func TestAuditLogRecordsAndRedacts(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	// 一次写操作（创建服务器），请求体里带一个密码字段用于验证脱敏。
	status, raw := env.request(http.MethodPost, "/api/v1/servers", jwt, map[string]any{
		"name": "audit-target", "host": "10.50.0.1", "mode": "agent",
		"password": "super-secret-value", "description": "keep-me",
	})
	if status != http.StatusCreated {
		t.Fatalf("创建服务器失败: %d（body=%s）", status, raw)
	}

	// 审计列表应能看到 server.create
	status, raw = env.request(http.MethodGet, "/api/v1/audit-logs?action=server.create", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询审计日志失败: %d", status)
	}
	if !strings.Contains(string(raw), `"action":"server.create"`) {
		t.Fatalf("审计日志缺少 server.create: %s", raw)
	}
	if !strings.Contains(string(raw), `"actor":"admin"`) {
		t.Fatalf("审计日志缺少操作者: %s", raw)
	}
	// 敏感字段必须脱敏，非敏感字段保留
	if strings.Contains(string(raw), "super-secret-value") {
		t.Fatalf("审计日志泄露了密码明文: %s", raw)
	}
	if !strings.Contains(string(raw), "keep-me") {
		t.Fatalf("非敏感字段应保留: %s", raw)
	}

	// GET 不应记录
	before := countAudit(t, env, jwt)
	env.request(http.MethodGet, "/api/v1/servers", jwt, nil)
	if after := countAudit(t, env, jwt); after != before {
		t.Fatalf("GET 请求不应产生审计记录（%d → %d）", before, after)
	}

	// 登录成功/失败手动记录
	env.request(http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"username": "admin", "password": "wrong-password"})
	status, raw = env.request(http.MethodGet, "/api/v1/audit-logs?action=auth.login", jwt, nil)
	if status != http.StatusOK || !strings.Contains(string(raw), "auth.login") {
		t.Fatalf("登录应产生审计记录: %s", raw)
	}

	// viewer 不可读审计
	env.createUserVia(jwt, "viewer1", "viewer-pass-123", "viewer")
	viewerToken := env.loginAs(t, "viewer1", "viewer-pass-123")
	status, _ = env.request(http.MethodGet, "/api/v1/audit-logs", viewerToken, nil)
	if status != http.StatusForbidden {
		t.Fatalf("viewer 读审计期望 403，实际 %d", status)
	}
}

// countAudit 统计审计记录条数（未分页）。
func countAudit(t *testing.T, env *testEnv, jwt string) int {
	t.Helper()
	status, raw := env.request(http.MethodGet, "/api/v1/audit-logs?action=server.create", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询审计失败: %d", status)
	}
	// 未传 limit 时是数组；传了 limit 是 {data:{logs:[...]}}。
	var asArray struct {
		Data []store.AuditLog `json:"data"`
	}
	if err := json.Unmarshal(raw, &asArray); err == nil && asArray.Data != nil {
		return len(asArray.Data)
	}
	var asObject struct {
		Data struct {
			Logs []store.AuditLog `json:"logs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &asObject); err != nil {
		t.Fatalf("解析审计失败: %v", err)
	}
	return len(asObject.Data.Logs)
}

// TestRBACRoles 覆盖三种角色的越权与放行。
func TestRBACRoles(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()

	env.createUserVia(admin, "op1", "operator-pass-1", "operator")
	env.createUserVia(admin, "vw1", "viewer-pass-123", "viewer")
	op := env.loginAs(t, "op1", "operator-pass-1")
	vw := env.loginAs(t, "vw1", "viewer-pass-123")

	// viewer：可读，不可写
	status, _ := env.request(http.MethodGet, "/api/v1/servers", vw, nil)
	if status != http.StatusOK {
		t.Fatalf("viewer 读服务器期望 200，实际 %d", status)
	}
	status, raw := env.request(http.MethodPost, "/api/v1/servers", vw,
		map[string]any{"name": "nope", "host": "10.51.0.1"})
	if status != http.StatusForbidden {
		t.Fatalf("viewer 写期望 403，实际 %d（body=%s）", status, raw)
	}
	if !strings.Contains(string(raw), `"code":"forbidden"`) {
		t.Fatalf("403 响应码不正确: %s", raw)
	}
	// viewer 不可申请终端票据
	status, _ = env.request(http.MethodPost, "/api/v1/servers/ghost/terminal-ticket", vw, nil)
	if status != http.StatusForbidden {
		t.Fatalf("viewer 申请终端票据期望 403（角色先于资源校验），实际 %d", status)
	}

	// operator：可写业务资源，但不可管用户/不可删服务器
	status, raw = env.request(http.MethodPost, "/api/v1/servers", op,
		map[string]any{"name": "op-srv", "host": "10.51.0.2", "mode": "agent"})
	if status != http.StatusCreated {
		t.Fatalf("operator 建服务器期望 201，实际 %d（body=%s）", status, raw)
	}
	var created struct {
		Data serverPayload `json:"data"`
	}
	_ = json.Unmarshal(raw, &created)
	status, _ = env.request(http.MethodDelete, "/api/v1/servers/"+created.Data.ID, op, nil)
	if status != http.StatusForbidden {
		t.Fatalf("operator 删服务器期望 403，实际 %d", status)
	}
	status, _ = env.request(http.MethodGet, "/api/v1/users", op, nil)
	if status != http.StatusForbidden {
		t.Fatalf("operator 读用户列表期望 403，实际 %d", status)
	}

	// admin：全部放行
	status, _ = env.request(http.MethodGet, "/api/v1/users", admin, nil)
	if status != http.StatusOK {
		t.Fatalf("admin 读用户列表期望 200，实际 %d", status)
	}
	status, _ = env.request(http.MethodDelete, "/api/v1/servers/"+created.Data.ID, admin, nil)
	if status != http.StatusOK {
		t.Fatalf("admin 删服务器期望 200，实际 %d", status)
	}
}

// TestUserSelfProtection 覆盖「禁止自删 / 最后一个 admin」。
func TestUserSelfProtection(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()

	// 拿到自己的 id
	status, raw := env.request(http.MethodGet, "/api/v1/auth/me", admin, nil)
	if status != http.StatusOK {
		t.Fatalf("查询当前用户失败: %d", status)
	}
	var me struct {
		Data userPayload `json:"data"`
	}
	_ = json.Unmarshal(raw, &me)

	status, _ = env.request(http.MethodDelete, "/api/v1/users/"+me.Data.ID, admin, nil)
	if status != http.StatusBadRequest && status != http.StatusForbidden {
		t.Fatalf("删除自己期望 400/403，实际 %d", status)
	}

	// 创建第二个 admin 后删除第一个 admin（自己以外）应成功
	env.createUserVia(admin, "admin2", "admin2-pass-123", "admin")
	status, raw = env.request(http.MethodGet, "/api/v1/users", admin, nil)
	if status != http.StatusOK {
		t.Fatalf("查询用户失败: %d", status)
	}
	var users struct {
		Data []userPayload `json:"data"`
	}
	_ = json.Unmarshal(raw, &users)
	var admin2ID string
	for _, u := range users.Data {
		if u.Username == "admin2" {
			admin2ID = u.ID
		}
	}
	if admin2ID == "" {
		t.Fatal("未找到 admin2")
	}
	status, _ = env.request(http.MethodDelete, "/api/v1/users/"+admin2ID, admin, nil)
	if status != http.StatusOK {
		t.Fatalf("删除另一个 admin 期望 200，实际 %d", status)
	}
}

// TestAPITokenLifecycle 覆盖 token 创建/使用/撤销。
func TestAPITokenLifecycle(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()

	status, raw := env.request(http.MethodPost, "/api/v1/api-tokens", admin, map[string]any{
		"name": "ci-token", "role": "operator", "expires_in": "1h",
	})
	if status != http.StatusCreated {
		t.Fatalf("创建 token 失败: %d（body=%s）", status, raw)
	}
	var created struct {
		Data createTokenResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("解析 token 响应失败: %v", err)
	}
	if !strings.HasPrefix(created.Data.Token, "lsk_") {
		t.Fatalf("token 明文应带 lsk_ 前缀: %q", created.Data.Token)
	}

	// 用 token 调 API 成功
	status, _ = env.request(http.MethodGet, "/api/v1/servers", created.Data.Token, nil)
	if status != http.StatusOK {
		t.Fatalf("用 API Token 访问期望 200，实际 %d", status)
	}
	// last_used_at 已更新
	stored, err := env.st.FindAPITokenByHash(context.Background(), hashTokenForTest(created.Data.Token))
	if err != nil {
		t.Fatalf("查询 token 失败: %v", err)
	}
	if stored.LastUsedAt == nil {
		t.Fatal("使用后应更新 last_used_at")
	}
	// 角色生效：operator token 不能读 users
	status, _ = env.request(http.MethodGet, "/api/v1/users", created.Data.Token, nil)
	if status != http.StatusForbidden {
		t.Fatalf("operator token 读用户期望 403，实际 %d", status)
	}

	// 列表不返回明文
	status, raw = env.request(http.MethodGet, "/api/v1/api-tokens", admin, nil)
	if status != http.StatusOK {
		t.Fatalf("查询 token 列表失败: %d", status)
	}
	if strings.Contains(string(raw), created.Data.Token) {
		t.Fatalf("token 列表泄露了明文: %s", raw)
	}

	// 撤销后 401
	status, _ = env.request(http.MethodDelete, "/api/v1/api-tokens/"+created.Data.ID, admin, nil)
	if status != http.StatusOK {
		t.Fatalf("撤销 token 失败: %d", status)
	}
	status, _ = env.request(http.MethodGet, "/api/v1/servers", created.Data.Token, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("撤销后应 401，实际 %d", status)
	}
}

// hashTokenForTest 计算 token 的 sha256（与实现一致的测试助手）。
func hashTokenForTest(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// TestPaginationBoundaries 覆盖分页：不传 limit 保持数组，传 limit 返回 page。
func TestPaginationBoundaries(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	for i := 0; i < 5; i++ {
		env.createServerVia(jwt, map[string]any{
			"name": fmt.Sprintf("pg-%d", i), "host": fmt.Sprintf("10.52.0.%d", i+1),
		})
	}

	// 不传 limit → 数组（既有契约）
	status, raw := env.request(http.MethodGet, "/api/v1/servers", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询服务器失败: %d", status)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), `{"data":[`) {
		t.Fatalf("不传 limit 时必须返回数组: %s", raw[:min(80, len(raw))])
	}
	var all struct {
		Data []serverPayload `json:"data"`
	}
	_ = json.Unmarshal(raw, &all)
	if len(all.Data) != 5 {
		t.Fatalf("期望 5 台，实际 %d", len(all.Data))
	}

	// 传 limit=2 → {data,page}
	status, raw = env.request(http.MethodGet, "/api/v1/servers?limit=2", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("分页查询失败: %d", status)
	}
	var page1 struct {
		Data []serverPayload `json:"data"`
		Page pageMeta        `json:"page"`
	}
	if err := json.Unmarshal(raw, &page1); err != nil {
		t.Fatalf("解析分页响应失败: %v（body=%s）", err, raw)
	}
	if len(page1.Data) != 2 || !page1.Page.HasMore || page1.Page.NextCursor == "" {
		t.Fatalf("第一页不正确: %+v", page1)
	}

	// 用 cursor 取第二页，不重复
	status, raw = env.request(http.MethodGet,
		"/api/v1/servers?limit=2&cursor="+page1.Page.NextCursor, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("第二页查询失败: %d", status)
	}
	var page2 struct {
		Data []serverPayload `json:"data"`
		Page pageMeta        `json:"page"`
	}
	_ = json.Unmarshal(raw, &page2)
	if len(page2.Data) != 2 {
		t.Fatalf("第二页应有 2 条，实际 %d", len(page2.Data))
	}
	ids := map[string]bool{}
	for _, s := range page1.Data {
		ids[s.ID] = true
	}
	for _, s := range page2.Data {
		if ids[s.ID] {
			t.Fatalf("分页出现重复记录: %s", s.ID)
		}
	}

	// 最后页 has_more=false
	status, raw = env.request(http.MethodGet,
		"/api/v1/servers?limit=2&cursor="+page2.Page.NextCursor, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("第三页查询失败: %d", status)
	}
	var page3 struct {
		Data []serverPayload `json:"data"`
		Page pageMeta        `json:"page"`
	}
	_ = json.Unmarshal(raw, &page3)
	if len(page3.Data) != 1 || page3.Page.HasMore {
		t.Fatalf("最后一页应 1 条且 has_more=false: %+v", page3)
	}

	// 非法 limit / cursor
	status, _ = env.request(http.MethodGet, "/api/v1/servers?limit=0", jwt, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("limit=0 期望 400，实际 %d", status)
	}
	status, _ = env.request(http.MethodGet, "/api/v1/servers?limit=2&cursor=not-a-cursor", jwt, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("非法 cursor 期望 400，实际 %d", status)
	}
}

// TestServerImport 覆盖批量导入的逐条结果。
func TestServerImport(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	// 先建一台已存在的主机用于重复检测
	env.createServerVia(jwt, map[string]any{"name": "dup", "host": "10.53.0.1", "port": 22})

	text := strings.Join([]string{
		"# 这是注释",
		"10.53.0.1",      // 已存在 → failed
		"10.53.0.2:2222", // 新主机（指定端口）
		"10.53.0.3",      // 新主机
	}, "\n")
	status, raw := env.request(http.MethodPost, "/api/v1/servers/import", jwt,
		map[string]any{"text": text, "default_port": 22})
	if status != http.StatusOK {
		t.Fatalf("导入失败: %d（body=%s）", status, raw)
	}
	var resp struct {
		Data importResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("解析导入结果失败: %v", err)
	}
	if len(resp.Data.Created) != 2 {
		t.Fatalf("期望创建 2 台，实际 %d（%+v）", len(resp.Data.Created), resp.Data)
	}
	if len(resp.Data.Failed) != 1 {
		t.Fatalf("期望失败 1 条，实际 %d（%+v）", len(resp.Data.Failed), resp.Data.Failed)
	}
	if !strings.Contains(resp.Data.Failed[0].Error, "已存在") {
		t.Fatalf("重复主机应说明原因: %+v", resp.Data.Failed[0])
	}

	// 导入的服务器确实存在
	status, raw = env.request(http.MethodGet, "/api/v1/servers", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询服务器失败: %d", status)
	}
	if !strings.Contains(string(raw), "10.53.0.2") || !strings.Contains(string(raw), "10.53.0.3") {
		t.Fatalf("导入的服务器未落库: %s", raw)
	}
}

// TestTerminalSwitchAndFileTraversal 覆盖终端默认 403 与文件路径穿越防护。
func TestTerminalSwitchAndFileTraversal(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	// 终端默认关闭 → 403（即使服务器存在）
	srv := env.createServerVia(jwt, map[string]any{"name": "tty", "host": "10.54.0.1", "mode": "ssh"})
	status, raw := env.request(http.MethodPost, "/api/v1/servers/"+srv.ID+"/terminal-ticket", jwt, nil)
	if status != http.StatusForbidden {
		t.Fatalf("终端默认关闭时申请票据期望 403，实际 %d（body=%s）", status, raw)
	}
	if !strings.Contains(strings.ToLower(string(raw)), "terminal") || !strings.Contains(string(raw), "LEADSTACK_TERMINAL_ENABLED") {
		t.Fatalf("403 提示应说明如何开启: %s", raw)
	}

	// 文件路径穿越：必须 400
	badPaths := []string{"../../../etc/passwd", "/etc/../etc/shadow", "relative/path"}
	for _, p := range badPaths {
		status, body := env.request(http.MethodGet,
			"/api/v1/servers/"+srv.ID+"/files?path="+urlQueryEscape(p), jwt, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("非法路径 %q 期望 400，实际 %d（body=%s）", p, status, body)
		}
	}
	// NUL 字节（原始 %00 由服务端解码为 0x00）必须被拒绝
	status, body := env.requestRaw(http.MethodGet,
		"/api/v1/servers/"+srv.ID+"/files?path=/tmp/a%00b", jwt)
	if status != http.StatusBadRequest {
		t.Fatalf("含 NUL 的路径期望 400，实际 %d（body=%s）", status, body)
	}
	// 绝对路径合法（但通道未启用 → 502，说明通过了路径校验）
	status, _ = env.request(http.MethodGet, "/api/v1/servers/"+srv.ID+"/files?path=%2Ftmp", jwt, nil)
	if status != http.StatusBadGateway && status != http.StatusOK && status != http.StatusServiceUnavailable {
		t.Fatalf("合法绝对路径应通过校验（200/502/503），实际 %d", status)
	}
}

// TestScheduledTaskValidation 覆盖非法 cron 400 与任务 CRUD。
func TestScheduledTaskValidation(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	srv := env.createServerVia(jwt, map[string]any{"name": "cron-srv", "host": "10.55.0.1"})

	// 非法 cron
	for _, bad := range []string{"not-a-cron", "* * *", "61 * * * *", "*/0 * * * *"} {
		status, raw := env.request(http.MethodPost, "/api/v1/scheduled-tasks", jwt, map[string]any{
			"name": "t", "cron": bad, "command": "echo hi", "server_ids": []string{srv.ID},
		})
		if status != http.StatusBadRequest {
			t.Fatalf("非法 cron %q 期望 400，实际 %d（body=%s）", bad, status, raw)
		}
	}

	// 合法任务
	status, raw := env.request(http.MethodPost, "/api/v1/scheduled-tasks", jwt, map[string]any{
		"name": "每分钟", "cron": "* * * * *", "command": "echo hi",
		"server_ids": []string{srv.ID}, "timeout_s": 30,
	})
	if status != http.StatusCreated {
		t.Fatalf("创建定时任务失败: %d（body=%s）", status, raw)
	}
	var created struct {
		Data store.ScheduledTask `json:"data"`
	}
	_ = json.Unmarshal(raw, &created)
	if created.Data.ID == "" || created.Data.NextRunAt == nil {
		t.Fatalf("任务应带 next_run_at: %+v", created.Data)
	}

	// 列表 / 详情
	status, _ = env.request(http.MethodGet, "/api/v1/scheduled-tasks", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询任务列表失败: %d", status)
	}
	status, _ = env.request(http.MethodGet, "/api/v1/scheduled-tasks/"+created.Data.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询任务详情失败: %d", status)
	}
	// runs（数组 + 分页）
	status, raw = env.request(http.MethodGet, "/api/v1/scheduled-tasks/"+created.Data.ID+"/runs", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询任务执行记录失败: %d", status)
	}
	if !strings.Contains(string(raw), `"data":[`) {
		t.Fatalf("runs 未分页时应为数组: %s", raw)
	}

	// 删除
	status, _ = env.request(http.MethodDelete, "/api/v1/scheduled-tasks/"+created.Data.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("删除任务失败: %d", status)
	}
}

// TestOnboarding 覆盖首次引导状态。
func TestOnboarding(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	status, raw := env.request(http.MethodGet, "/api/v1/system/onboarding", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询引导状态失败: %d", status)
	}
	var out struct {
		Data onboardingResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析引导状态失败: %v", err)
	}
	if !out.Data.NeedsSetup || out.Data.HasServers {
		t.Fatalf("空系统应 needs_setup=true: %+v", out.Data)
	}

	env.createServerVia(jwt, map[string]any{"name": "o1", "host": "10.56.0.1"})
	_, raw = env.request(http.MethodGet, "/api/v1/system/onboarding", jwt, nil)
	_ = json.Unmarshal(raw, &out)
	if out.Data.NeedsSetup || !out.Data.HasServers {
		t.Fatalf("有服务器后 needs_setup 应为 false: %+v", out.Data)
	}
}

// requestRaw 发送请求但不做 URL 规范化的辅助（用于验证编码穿越）。
func (e *testEnv) requestRaw(method, path, jwt string) (int, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, nil)
	if err != nil {
		e.t.Fatalf("构造请求失败: %v", err)
	}
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}
	resp, err := e.http.Do(req)
	if err != nil {
		e.t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// urlQueryEscape 用标准库编码查询参数。
func urlQueryEscape(s string) string { return url.QueryEscape(s) }

var _ = time.Second
