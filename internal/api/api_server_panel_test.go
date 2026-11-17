package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/maliang/leadstack/internal/store"
)

// TestServerPanel 覆盖 GET /api/v1/servers/{id}/panel：
// 成功时 panel_url 逐字相等且不泄露 api_key、未绑定凭证/非宝塔凭证返回 409、
// 不存在与组外服务器的返回 404、以及成功读取写 server.panel 审计但 detail 不含 panel_url。
func TestServerPanel(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()

	const panelURL = "https://panel.example.com:8888"
	const apiKey = "S3cret-Pass!"
	cred := env.createCredentialVia(admin, map[string]any{
		"name": "panel-baota", "kind": "baota", "panel_url": panelURL, "api_key": apiKey,
	})
	credID, _ := cred["id"].(string)
	if credID == "" {
		t.Fatalf("创建宝塔凭证未返回 id: %+v", cred)
	}
	// 面板凭据走独立字段 panel_credential_id；credential_id 是 SSH 凭据，不能混用。
	srv := env.createServerVia(admin, map[string]any{
		"name": "panel-srv", "host": "10.9.9.50", "mode": "ssh", "panel_credential_id": credID,
	})

	// 未认证 → 401
	if status, _ := env.request(http.MethodGet, "/api/v1/servers/"+srv.ID+"/panel", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("未带 token 期望 401，实际 %d", status)
	}

	// 正常：panel_url 与创建时逐字相等，auth_type=baota
	status, raw := env.request(http.MethodGet, "/api/v1/servers/"+srv.ID+"/panel", admin, nil)
	if status != http.StatusOK {
		t.Fatalf("读取面板地址期望 200，实际 %d（body=%s）", status, raw)
	}
	var out struct {
		Data serverPanelPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析响应失败: %v（body=%s）", err, raw)
	}
	got := out.Data
	if got.ServerID != srv.ID || got.PanelURL != panelURL {
		t.Fatalf("面板信息不符: %+v", got)
	}
	if got.AuthType != "baota" {
		t.Fatalf("auth_type 期望 baota，实际 %q", got.AuthType)
	}
	// 响应体绝不能出现 api_key（密钥不是给前端看的）
	if strings.Contains(string(raw), apiKey) {
		t.Fatalf("响应体不应包含 api_key: %s", raw)
	}

	// 只传 panel_credential_id（credential_id 缺省为空）→ 200：这是前端 AddServerWizard 的真实路径
	panelOnly := env.createServerVia(admin, map[string]any{
		"name": "panel-only", "host": "10.9.9.53", "mode": "ssh", "panel_credential_id": credID,
	})
	if status, raw := env.request(http.MethodGet, "/api/v1/servers/"+panelOnly.ID+"/panel", admin, nil); status != http.StatusOK {
		t.Fatalf("只绑面板凭据期望 200，实际 %d（body=%s）", status, raw)
	}

	// 同时绑 SSH 凭据与面板凭据 → 200 且返回面板凭据的 panel_url（不能读成 SSH 凭据）
	sshCred := env.createCredentialVia(admin, map[string]any{
		"name": "panel-ssh", "kind": "key", "username": "root",
		"private_key": generateTestPrivateKey(t),
	})
	sshID, _ := sshCred["id"].(string)
	if sshID == "" {
		t.Fatalf("创建 SSH 凭据未返回 id: %+v", sshCred)
	}
	bothSrv := env.createServerVia(admin, map[string]any{
		"name": "panel-both", "host": "10.9.9.54", "mode": "ssh",
		"credential_id": sshID, "panel_credential_id": credID,
	})
	if status, raw := env.request(http.MethodGet, "/api/v1/servers/"+bothSrv.ID+"/panel", admin, nil); status != http.StatusOK {
		t.Fatalf("同时绑 SSH 与面板凭据期望 200，实际 %d（body=%s）", status, raw)
	} else {
		var bothOut struct {
			Data serverPanelPayload `json:"data"`
		}
		if err := json.Unmarshal(raw, &bothOut); err != nil {
			t.Fatalf("解析响应失败: %v（body=%s）", err, raw)
		}
		if bothOut.Data.PanelURL != panelURL {
			t.Fatalf("应返回面板凭据的 panel_url=%s，实际 %q", panelURL, bothOut.Data.PanelURL)
		}
	}

	// 未绑定凭证 → 409
	noCred := env.createServerVia(admin, map[string]any{"name": "panel-no-cred", "host": "10.9.9.51"})
	if status, raw := env.request(http.MethodGet, "/api/v1/servers/"+noCred.ID+"/panel", admin, nil); status != http.StatusConflict {
		t.Fatalf("未绑定凭证期望 409，实际 %d（body=%s）", status, raw)
	} else if !strings.Contains(string(raw), "服务器未绑定面板凭证") {
		t.Fatalf("未绑定凭证的错误信息不符: %s", raw)
	}

	// 只绑了 SSH 私钥凭据、没绑面板凭据 → 409（SSH 凭据不会被当成面板凭据）
	keyCred := env.createCredentialVia(admin, map[string]any{
		"name": "panel-key", "kind": "key", "username": "root",
		"private_key": generateTestPrivateKey(t),
	})
	keyID, _ := keyCred["id"].(string)
	if keyID == "" {
		t.Fatalf("创建私钥凭证未返回 id: %+v", keyCred)
	}
	withKey := env.createServerVia(admin, map[string]any{
		"name": "panel-key-srv", "host": "10.9.9.52", "mode": "ssh", "credential_id": keyID,
	})
	status, raw = env.request(http.MethodGet, "/api/v1/servers/"+withKey.ID+"/panel", admin, nil)
	if status != http.StatusConflict {
		t.Fatalf("只有 SSH 凭据期望 409，实际 %d（body=%s）", status, raw)
	}
	if !strings.Contains(string(raw), "服务器未绑定面板凭证") {
		t.Fatalf("只有 SSH 凭据的错误信息不符: %s", raw)
	}

	// 不存在的服务器 → 404
	if status, _ := env.request(http.MethodGet, "/api/v1/servers/not-exist/panel", admin, nil); status != http.StatusNotFound {
		t.Fatalf("不存在的服务器期望 404，实际 %d", status)
	}

	// 组范围角色：组内可读，组外一律 404（不泄露存在性）
	groupID := env.createGroupVia(admin, "面板范围组")
	env.setGroupMembers(admin, groupID, []string{srv.ID})
	roleKey := env.createRoleVia(admin, "面板范围只读", []string{"server.read"},
		map[string]any{"mode": "groups", "group_ids": []string{groupID}, "server_ids": []string{}})
	env.createUserVia(admin, "panel-scope-viewer", "PanelScope-123", roleKey)
	scoped := env.loginAs(t, "panel-scope-viewer", "PanelScope-123")

	if status, raw := env.request(http.MethodGet, "/api/v1/servers/"+srv.ID+"/panel", scoped, nil); status != http.StatusOK {
		t.Fatalf("组内服务器期望 200，实际 %d（body=%s）", status, raw)
	}
	if status, raw := env.request(http.MethodGet, "/api/v1/servers/"+withKey.ID+"/panel", scoped, nil); status != http.StatusNotFound {
		t.Fatalf("组外服务器期望 404，实际 %d（body=%s）", status, raw)
	}

	// 审计：成功读取必须留痕，detail 只含 credential_id，不含 panel_url/api_key
	logs, err := env.st.ListAuditLogs(context.Background(), store.AuditFilter{Limit: 200})
	if err != nil {
		t.Fatalf("读取审计日志失败: %v", err)
	}
	found := false
	for _, entry := range logs {
		if entry.Action != auditActionServerPanel {
			continue
		}
		// 本用例会多次成功读取不同服务器，只断言目标服务器 srv 的审计记录。
		if entry.ResourceID == nil || *entry.ResourceID != srv.ID {
			continue
		}
		found = true
		if entry.ResourceType == nil || *entry.ResourceType != "server" {
			t.Fatalf("审计 resource_type 期望 server，实际 %+v", entry.ResourceType)
		}
		if entry.Detail == nil || !strings.Contains(*entry.Detail, credID) {
			t.Fatalf("审计 detail 应含 credential_id=%s，实际 %+v", credID, entry.Detail)
		}
		if strings.Contains(*entry.Detail, panelURL) || strings.Contains(*entry.Detail, apiKey) {
			t.Fatalf("审计 detail 不应含 panel_url/api_key: %s", *entry.Detail)
		}
	}
	if !found {
		t.Fatalf("审计应包含 %s 记录", auditActionServerPanel)
	}
}
