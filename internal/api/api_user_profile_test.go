package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestUserProfileFieldsAndSuperAdminProtection 覆盖用户资料字段、/users/me 自助修改
// 与内置 admin 不可删除的边界。
func TestUserProfileFieldsAndSuperAdminProtection(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	status, raw := env.request(http.MethodPost, "/api/v1/users", jwt, map[string]any{
		"username": "alice", "password": "alice-pass-123", "role": "viewer",
		"display_name": "Alice 运维", "phone": "13800000000", "notes": "夜间值班",
	})
	if status != http.StatusCreated {
		t.Fatalf("创建用户期望 201，实际 %d（body=%s）", status, raw)
	}
	var created struct {
		Data userPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("解析创建响应失败: %v", err)
	}
	if created.Data.DisplayName != "Alice 运维" || created.Data.Phone != "13800000000" || created.Data.Notes != "夜间值班" {
		t.Fatalf("创建响应资料字段不符: %+v", created.Data)
	}

	status, raw = env.request(http.MethodPatch, "/api/v1/users/"+created.Data.ID, jwt, map[string]any{
		"display_name": "Alice", "phone": "13900000000", "notes": "白班", "role": "operator",
	})
	if status != http.StatusOK {
		t.Fatalf("更新用户期望 200，实际 %d（body=%s）", status, raw)
	}
	var updated struct {
		Data userPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatalf("解析更新响应失败: %v", err)
	}
	if updated.Data.DisplayName != "Alice" || updated.Data.Phone != "13900000000" || updated.Data.Notes != "白班" || updated.Data.Role != "operator" {
		t.Fatalf("更新用户资料失败: %+v", updated.Data)
	}

	// /users/me：只改自己的资料字段，并能在 /auth/me 读回。
	status, raw = env.request(http.MethodPatch, "/api/v1/users/me", jwt, map[string]any{
		"display_name": "总管理员", "phone": "010-12345678", "notes": "内置账号",
	})
	if status != http.StatusOK {
		t.Fatalf("修改个人资料期望 200，实际 %d（body=%s）", status, raw)
	}
	status, raw = env.request(http.MethodGet, "/api/v1/auth/me", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("读取 /auth/me 期望 200，实际 %d", status)
	}
	var me struct {
		Data userPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &me); err != nil {
		t.Fatalf("解析 /auth/me 失败: %v", err)
	}
	if me.Data.DisplayName != "总管理员" || me.Data.Phone != "010-12345678" {
		t.Fatalf("/auth/me 资料字段未更新: %+v", me.Data)
	}

	// 另一个管理员也不能删除内置 admin。
	env.createUserVia(jwt, "admin2", "admin2-pass-123", "admin")
	admin2 := env.loginAs(t, "admin2", "admin2-pass-123")
	var adminMe struct {
		Data userPayload `json:"data"`
	}
	_, raw = env.request(http.MethodGet, "/api/v1/auth/me", jwt, nil)
	if err := json.Unmarshal(raw, &adminMe); err != nil {
		t.Fatalf("解析 admin 身份失败: %v", err)
	}
	status, raw = env.request(http.MethodDelete, "/api/v1/users/"+adminMe.Data.ID, admin2, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("删除内置 admin 期望 400，实际 %d（body=%s）", status, raw)
	}
}
