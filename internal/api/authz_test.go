package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/maliang/leadstack/internal/auth"
	"github.com/maliang/leadstack/internal/store"
)

// decodeData 把 {"data":...} 解到 dst。
func decodeData(t *testing.T, raw []byte, dst any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, raw)
	}
	if err := json.Unmarshal(env.Data, dst); err != nil {
		t.Fatalf("解析 data 失败: %v body=%s", err, raw)
	}
}

// createRoleVia 通过 REST 创建一个自定义角色并返回其 key。
func (e *testEnv) createRoleVia(jwt string, name string, perms []string, scope map[string]any) string {
	e.t.Helper()
	status, raw := e.request(http.MethodPost, "/api/v1/roles", jwt, map[string]any{
		"name": name, "permissions": perms, "scope": scope,
	})
	if status != http.StatusCreated {
		e.t.Fatalf("创建角色失败: status=%d body=%s", status, raw)
	}
	var role rolePayload
	decodeData(e.t, raw, &role)
	return role.Key
}

// createGroupVia 创建分组并返回 ID。
func (e *testEnv) createGroupVia(jwt, name string) string {
	e.t.Helper()
	status, raw := e.request(http.MethodPost, "/api/v1/server-groups", jwt, map[string]any{"name": name})
	if status != http.StatusCreated {
		e.t.Fatalf("创建分组失败: status=%d body=%s", status, raw)
	}
	var group store.ServerGroup
	decodeData(e.t, raw, &group)
	return group.ID
}

// setGroupMembers 整包设置分组成员。
func (e *testEnv) setGroupMembers(jwt, groupID string, serverIDs []string) {
	e.t.Helper()
	status, raw := e.request(http.MethodPut, "/api/v1/server-groups/"+groupID+"/servers", jwt,
		map[string]any{"server_ids": serverIDs})
	if status != http.StatusOK {
		e.t.Fatalf("设置分组成员失败: status=%d body=%s", status, raw)
	}
}

// TestPermissionCatalogMatchesContract 校验权限目录与内置角色权限集合（契约 §52）。
func TestPermissionCatalogMatchesContract(t *testing.T) {
	perms := auth.Permissions()
	if len(perms) != 24 {
		t.Fatalf("权限点应为 24 个，实际 %d", len(perms))
	}
	adminOnly := 0
	for _, p := range perms {
		if p.AdminOnly {
			adminOnly++
		}
	}
	if adminOnly != 6 {
		t.Fatalf("仅管理员可持有的权限应为 6 个，实际 %d", adminOnly)
	}
	viewer, _ := auth.BuiltinRole("viewer")
	if len(viewer.Permissions) != 6 {
		t.Fatalf("viewer 权限应为 6 个，实际 %d", len(viewer.Permissions))
	}
	operator, _ := auth.BuiltinRole("operator")
	if len(operator.Permissions) != 14 {
		t.Fatalf("operator 权限应为 14 个，实际 %d", len(operator.Permissions))
	}
	admin, _ := auth.BuiltinRole("admin")
	if len(admin.Permissions) != 24 {
		t.Fatalf("admin 应拥有全部 24 个权限，实际 %d", len(admin.Permissions))
	}

	// 自定义角色不能持有管理权限，也不能是空集合/未知权限。
	if _, err := auth.NormalizePermissions([]string{auth.PermUserManage}); err == nil {
		t.Fatal("自定义角色不应能获得 user.manage")
	}
	if _, err := auth.NormalizePermissions([]string{"nope.read"}); err == nil {
		t.Fatal("未知权限点应报错")
	}
	if _, err := auth.NormalizePermissions(nil); err == nil {
		t.Fatal("空权限集合应报错")
	}
	normalized, err := auth.NormalizePermissions([]string{"check.write", "check.write", "check.read"})
	if err != nil {
		t.Fatalf("规整权限失败: %v", err)
	}
	if len(normalized) != 2 || normalized[0] != auth.PermCheckRead {
		t.Fatalf("权限应去重并按目录排序: %v", normalized)
	}
}

// TestViewerCannotWrite 校验只读角色只能读。
func TestViewerCannotWrite(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	env.createUserVia(jwt, "viewer1", "ViewerPass-123", "viewer")
	viewer := env.loginAs(t, "viewer1", "ViewerPass-123")

	if status, raw := env.request(http.MethodGet, "/api/v1/servers", viewer, nil); status != http.StatusOK {
		t.Fatalf("viewer 应能读服务器: status=%d body=%s", status, raw)
	}
	if status, _ := env.request(http.MethodPost, "/api/v1/servers", viewer,
		map[string]any{"name": "x", "host": "127.0.0.1"}); status != http.StatusForbidden {
		t.Fatalf("viewer 创建服务器应 403，实际 %d", status)
	}
	if status, _ := env.request(http.MethodPost, "/api/v1/checks", viewer,
		map[string]any{"name": "c", "kind": "tcp", "target": "127.0.0.1:80", "interval_s": 60}); status != http.StatusForbidden {
		t.Fatalf("viewer 创建检测应 403，实际 %d", status)
	}
	if status, _ := env.request(http.MethodGet, "/api/v1/users", viewer, nil); status != http.StatusForbidden {
		t.Fatalf("viewer 读用户列表应 403，实际 %d", status)
	}
}

// TestCustomRoleGrantsOnlyItsPermissions 校验自定义角色的权限边界。
func TestCustomRoleGrantsOnlyItsPermissions(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()

	roleKey := env.createRoleVia(admin, "发布员", []string{"deploy.read", "deploy.write"}, map[string]any{"mode": "all"})
	env.createUserVia(admin, "deployer", "DeployPass-123", roleKey)
	deployer := env.loginAs(t, "deployer", "DeployPass-123")

	if status, raw := env.request(http.MethodGet, "/api/v1/releases", deployer, nil); status != http.StatusOK {
		t.Fatalf("发布员应能读发布: status=%d body=%s", status, raw)
	}
	if status, _ := env.request(http.MethodPost, "/api/v1/servers", deployer,
		map[string]any{"name": "x", "host": "127.0.0.1"}); status != http.StatusForbidden {
		t.Fatalf("发布员无 server.write，创建服务器应 403，实际 %d", status)
	}
	if status, _ := env.request(http.MethodGet, "/api/v1/checks", deployer, nil); status != http.StatusForbidden {
		t.Fatalf("发布员无 check.read，读检测应 403，实际 %d", status)
	}
	// /auth/me 应带回权限与范围，供前端门控。
	status, raw := env.request(http.MethodGet, "/api/v1/auth/me", deployer, nil)
	if status != http.StatusOK {
		t.Fatalf("读取当前用户失败: status=%d body=%s", status, raw)
	}
	var me userPayload
	decodeData(t, raw, &me)
	if me.Role != roleKey || me.RoleName != "发布员" {
		t.Fatalf("/auth/me 角色信息不符: %+v", me)
	}
	if len(me.Permissions) != 2 || me.Scope == nil || me.Scope.Mode != "all" {
		t.Fatalf("/auth/me 权限/范围不符: %+v", me)
	}
}

// TestCustomRoleRejectsAdminOnlyPermission 校验防提权。
func TestCustomRoleRejectsAdminOnlyPermission(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()
	for _, perms := range [][]string{
		{"user.manage"},
		{"server.read", "role.manage"},
		{"system.manage", "audit.read"},
	} {
		status, raw := env.request(http.MethodPost, "/api/v1/roles", admin, map[string]any{
			"name": "提权尝试", "permissions": perms, "scope": map[string]any{"mode": "all"},
		})
		if status != http.StatusBadRequest {
			t.Fatalf("带管理权限的自定义角色应 400，实际 %d body=%s", status, raw)
		}
	}
}

// TestBuiltinRoleImmutableAndInUseRoleProtection 校验内置角色不可改删、被引用角色不可删。
func TestBuiltinRoleImmutableAndInUseRoleProtection(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()

	if status, _ := env.request(http.MethodPatch, "/api/v1/roles/admin", admin,
		map[string]any{"name": "x"}); status != http.StatusForbidden {
		t.Fatalf("内置角色改权限/改名应 403，实际 %d", status)
	}
	if status, _ := env.request(http.MethodDelete, "/api/v1/roles/operator", admin, nil); status != http.StatusForbidden {
		t.Fatalf("内置角色删除应 403，实际 %d", status)
	}

	roleKey := env.createRoleVia(admin, "临时角色", []string{"server.read"}, map[string]any{"mode": "all"})
	env.createUserVia(admin, "tempuser", "TempPass-123", roleKey)
	if status, raw := env.request(http.MethodDelete, "/api/v1/roles/"+roleKey, admin, nil); status != http.StatusConflict {
		t.Fatalf("被用户引用的角色删除应 409，实际 %d body=%s", status, raw)
	}

	// 改派给内置角色后即可删除。
	var users []userPayload
	_, raw := env.request(http.MethodGet, "/api/v1/users", admin, nil)
	decodeData(t, raw, &users)
	var targetID string
	for _, u := range users {
		if u.Username == "tempuser" {
			targetID = u.ID
		}
	}
	if targetID == "" {
		t.Fatal("未找到 tempuser")
	}
	if status, raw := env.request(http.MethodPatch, "/api/v1/users/"+targetID, admin,
		map[string]any{"role": "viewer"}); status != http.StatusOK {
		t.Fatalf("改派角色失败: status=%d body=%s", status, raw)
	}
	if status, raw := env.request(http.MethodDelete, "/api/v1/roles/"+roleKey, admin, nil); status != http.StatusOK {
		t.Fatalf("无人引用后删除角色应成功，实际 %d body=%s", status, raw)
	}
}

// TestScopedRoleOnlySeesGroupServers 校验资源级授权（按分组）。
func TestScopedRoleOnlySeesGroupServers(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()

	inGroup := env.createServerVia(admin, map[string]any{"name": "in-group", "host": "10.0.0.1"})
	outGroup := env.createServerVia(admin, map[string]any{"name": "out-group", "host": "10.0.0.2"})

	groupID := env.createGroupVia(admin, "生产 A")
	env.setGroupMembers(admin, groupID, []string{inGroup.ID})

	roleKey := env.createRoleVia(admin, "生产运维", []string{"server.read", "check.write", "check.read"},
		map[string]any{"mode": "groups", "group_ids": []string{groupID}, "server_ids": []string{}})
	env.createUserVia(admin, "scoped", "ScopedPass-123", roleKey)
	scoped := env.loginAs(t, "scoped", "ScopedPass-123")

	// 列表只返回范围内服务器。
	var servers []serverPayload
	_, raw := env.request(http.MethodGet, "/api/v1/servers", scoped, nil)
	decodeData(t, raw, &servers)
	if len(servers) != 1 || servers[0].ID != inGroup.ID {
		t.Fatalf("受限用户应只看到 1 台范围内服务器，实际 %+v", servers)
	}
	// 越界读 → 404（不泄露存在性）。
	if status, _ := env.request(http.MethodGet, "/api/v1/servers/"+outGroup.ID, scoped, nil); status != http.StatusNotFound {
		t.Fatalf("越界读服务器应 404，实际 %d", status)
	}
	// 越界写检测目标 → 403。
	if status, raw := env.request(http.MethodPost, "/api/v1/checks", scoped, map[string]any{
		"name": "越界检测", "kind": "tcp", "target": "127.0.0.1:80", "interval_s": 60,
		"server_ids": []string{outGroup.ID},
	}); status != http.StatusForbidden {
		t.Fatalf("越界绑定检测目标应 403，实际 %d body=%s", status, raw)
	}
	// 范围内写检测 → 201，且可见。
	status, raw := env.request(http.MethodPost, "/api/v1/checks", scoped, map[string]any{
		"name": "范围内检测", "kind": "tcp", "target": "127.0.0.1:80", "interval_s": 60,
		"server_ids": []string{inGroup.ID},
	})
	if status != http.StatusCreated {
		t.Fatalf("范围内创建检测应 201，实际 %d body=%s", status, raw)
	}
	// 分组列表也按范围收敛。
	var groups []store.ServerGroup
	_, raw = env.request(http.MethodGet, "/api/v1/server-groups", scoped, nil)
	decodeData(t, raw, &groups)
	if len(groups) != 1 || groups[0].ID != groupID {
		t.Fatalf("受限用户应只看到范围内分组，实际 %+v", groups)
	}
}

// TestGroupWriteRequiresGlobalScope 校验 group.write 必须配合全局范围。
func TestGroupWriteRequiresGlobalScope(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()
	srv := env.createServerVia(admin, map[string]any{"name": "s1", "host": "10.0.0.9"})
	groupID := env.createGroupVia(admin, "G1")
	env.setGroupMembers(admin, groupID, []string{srv.ID})

	roleKey := env.createRoleVia(admin, "分组长", []string{"server.read", "group.write"},
		map[string]any{"mode": "groups", "group_ids": []string{groupID}})
	env.createUserVia(admin, "grouper", "GroupPass-123", roleKey)
	grouper := env.loginAs(t, "grouper", "GroupPass-123")

	status, raw := env.request(http.MethodPost, "/api/v1/server-groups", grouper,
		map[string]any{"name": "越权分组"})
	if status != http.StatusForbidden {
		t.Fatalf("受限范围写分组应 403，实际 %d body=%s", status, raw)
	}
}

// TestLoginLockoutAndUnlock 校验失败锁定 → 锁定期拒绝 → 管理员解锁（契约 §54）。
func TestLoginLockoutAndUnlock(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()
	env.createUserVia(admin, "lockme", "LockPass-123", "viewer")

	var users []userPayload
	_, raw := env.request(http.MethodGet, "/api/v1/users", admin, nil)
	decodeData(t, raw, &users)
	var userID string
	for _, u := range users {
		if u.Username == "lockme" {
			userID = u.ID
		}
	}
	if userID == "" {
		t.Fatal("未找到 lockme 用户")
	}

	wrong := map[string]string{"username": "lockme", "password": "WrongPass-000"}
	for i := 1; i <= 4; i++ {
		status, body := env.request(http.MethodPost, "/api/v1/auth/login", "", wrong)
		if status != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误密码应 401，实际 %d body=%s", i, status, body)
		}
		var envBody struct {
			Error struct {
				Details struct {
					RemainingAttempts int `json:"remaining_attempts"`
				} `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &envBody); err != nil {
			t.Fatalf("解析失败响应失败: %v", err)
		}
		if envBody.Error.Details.RemainingAttempts != 5-i {
			t.Fatalf("第 %d 次失败后剩余次数应为 %d，实际 %d", i, 5-i, envBody.Error.Details.RemainingAttempts)
		}
	}

	// 第 5 次失败触发锁定：返回 429 login_locked。
	status, body := env.request(http.MethodPost, "/api/v1/auth/login", "", wrong)
	if status != http.StatusTooManyRequests {
		t.Fatalf("达到阈值应 429，实际 %d body=%s", status, body)
	}
	var locked struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				RetryAfterS int `json:"retry_after_s"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &locked); err != nil {
		t.Fatalf("解析锁定响应失败: %v", err)
	}
	if locked.Error.Code != "login_locked" || locked.Error.Details.RetryAfterS <= 0 {
		t.Fatalf("锁定响应不符: %+v", locked)
	}

	// 锁定期内即使密码正确也必须拒绝。
	if status, body := env.request(http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"username": "lockme", "password": "LockPass-123"}); status != http.StatusTooManyRequests {
		t.Fatalf("锁定期内正确密码也应 429，实际 %d body=%s", status, body)
	}

	// 用户列表应体现锁定状态。
	_, raw = env.request(http.MethodGet, "/api/v1/users", admin, nil)
	decodeData(t, raw, &users)
	lockedFound := false
	for _, u := range users {
		if u.Username == "lockme" {
			lockedFound = u.Locked && u.FailedAttempts >= 5
		}
	}
	if !lockedFound {
		t.Fatalf("用户列表应显示锁定与失败次数: %+v", users)
	}

	// 管理员解锁后可正常登录。
	if status, raw := env.request(http.MethodPost, "/api/v1/users/"+userID+"/unlock", admin, nil); status != http.StatusOK {
		t.Fatalf("解锁失败: status=%d body=%s", status, raw)
	}
	if got := env.loginAs(t, "lockme", "LockPass-123"); got == "" {
		t.Fatal("解锁后应能登录")
	}

	// 审计里应留下锁定与解锁事件。
	logs, err := env.st.ListAuditLogs(context.Background(), store.AuditFilter{Limit: 200})
	if err != nil {
		t.Fatalf("读取审计日志失败: %v", err)
	}
	var hasLocked, hasUnlock bool
	for _, entry := range logs {
		if entry.Action == "auth.login_locked" {
			hasLocked = true
		}
		if entry.Action == "user.unlock" {
			hasUnlock = true
		}
	}
	if !hasLocked || !hasUnlock {
		t.Fatalf("审计应包含锁定与解锁事件（locked=%v unlock=%v）", hasLocked, hasUnlock)
	}
}

// TestLoginIPRateLimit 校验同一 IP 的登录限流。
func TestLoginIPRateLimit(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()

	env.api.login.setPolicy(loginPolicy{
		MaxAttempts:  5,
		LockDuration: 15 * time.Minute,
		IPLimit:      3,
		IPWindow:     time.Minute,
	})
	// 前 3 次（含 admin 已用掉的 1 次前）允许，之后 429。
	var got429 bool
	for i := 0; i < 5; i++ {
		status, raw := env.request(http.MethodPost, "/api/v1/auth/login", "",
			map[string]string{"username": "admin", "password": "WrongPass-000"})
		if status == http.StatusTooManyRequests {
			got429 = true
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("解析限流响应失败: %v", err)
			}
			if body.Error.Code != "rate_limited" {
				t.Fatalf("IP 限流的 code 应为 rate_limited，实际 %q", body.Error.Code)
			}
			break
		}
	}
	if !got429 {
		t.Fatal("超过 IP 窗口上限后应返回 429 rate_limited")
	}
	_ = admin
}

// TestServerDetailCarriesGroups 校验单台服务器响应也带分组（历史 bug：切片拷贝导致丢字段）。
func TestServerDetailCarriesGroups(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()
	srv := env.createServerVia(admin, map[string]any{"name": "grouped", "host": "10.0.0.7"})
	groupID := env.createGroupVia(admin, "详情分组")
	env.setGroupMembers(admin, groupID, []string{srv.ID})

	var detail serverPayload
	_, raw := env.request(http.MethodGet, "/api/v1/servers/"+srv.ID, admin, nil)
	decodeData(t, raw, &detail)
	if len(detail.GroupIDs) != 1 || detail.GroupIDs[0] != groupID {
		t.Fatalf("详情应携带 group_ids，实际 %+v", detail.GroupIDs)
	}
	if len(detail.GroupNames) != 1 || detail.GroupNames[0] != "详情分组" {
		t.Fatalf("详情应携带 group_names，实际 %+v", detail.GroupNames)
	}
	// PATCH 响应同样要带（前端保存后即时渲染）。
	var patched serverPayload
	_, raw = env.request(http.MethodPatch, "/api/v1/servers/"+srv.ID, admin,
		map[string]any{"name": "grouped-2"})
	decodeData(t, raw, &patched)
	if len(patched.GroupNames) != 1 {
		t.Fatalf("PATCH 响应应携带 group_names，实际 %+v", patched.GroupNames)
	}
}
