package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/maliang/leadstack/internal/store"
)

// TestServerAccess 覆盖 GET /api/v1/servers/{id}/access：
// 正常回显登录信息（含明文密码）、未绑定凭证/宝塔凭证返回 409、不存在返回 404、
// 详情接口不外泄密码、以及成功读取会写 server.access 审计且 detail 不含密码。
func TestServerAccess(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	const password = "S3cret-Pass!"
	cred := env.createCredentialVia(jwt, map[string]any{
		"name": "access-pw", "kind": "password", "username": "root", "password": password,
	})
	credID, _ := cred["id"].(string)
	if credID == "" {
		t.Fatalf("创建凭证未返回 id: %+v", cred)
	}

	srv := env.createServerVia(jwt, map[string]any{
		"name": "access-srv", "host": "10.9.9.9", "port": 2222,
		"mode": "ssh", "credential_id": credID,
	})

	// 未认证 → 401
	if status, _ := env.request(http.MethodGet, "/api/v1/servers/"+srv.ID+"/access", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("未带 token 期望 401，实际 %d", status)
	}

	// 正常：返回名称/地址/端口/用户名/密码
	status, raw := env.request(http.MethodGet, "/api/v1/servers/"+srv.ID+"/access", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("读取登录信息期望 200，实际 %d（body=%s）", status, raw)
	}
	var out struct {
		Data serverAccessPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析响应失败: %v（body=%s）", err, raw)
	}
	got := out.Data
	if got.ServerID != srv.ID || got.Name != "access-srv" || got.Host != "10.9.9.9" ||
		got.Port != 2222 || got.Username != "root" || got.Password != password {
		t.Fatalf("登录信息不符: %+v", got)
	}
	if got.AuthType != "password" {
		t.Fatalf("auth_type 期望 password，实际 %q", got.AuthType)
	}

	// 服务器详情接口不得回显密码
	status, detail := env.request(http.MethodGet, "/api/v1/servers/"+srv.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("读取服务器详情期望 200，实际 %d（body=%s）", status, detail)
	}
	if strings.Contains(string(detail), password) {
		t.Fatalf("服务器详情不应包含明文密码: %s", detail)
	}

	// 未绑定 SSH 凭证 → 409
	noCred := env.createServerVia(jwt, map[string]any{"name": "no-cred", "host": "10.9.9.10"})
	if status, raw := env.request(http.MethodGet, "/api/v1/servers/"+noCred.ID+"/access", jwt, nil); status != http.StatusConflict {
		t.Fatalf("未绑定凭证期望 409，实际 %d（body=%s）", status, raw)
	}

	// 绑定的是宝塔面板凭据 → 409（不能把 api_key 当密码回显）
	baota := env.createCredentialVia(jwt, map[string]any{
		"name": "access-baota", "kind": "baota", "panel_url": "http://127.0.0.1:8888", "api_key": "test-api-key",
	})
	baotaID, _ := baota["id"].(string)
	if baotaID == "" {
		t.Fatalf("创建宝塔凭证未返回 id: %+v", baota)
	}
	withBaota := env.createServerVia(jwt, map[string]any{
		"name": "baota-srv", "host": "10.9.9.11", "mode": "ssh", "credential_id": baotaID,
	})
	if status, raw := env.request(http.MethodGet, "/api/v1/servers/"+withBaota.ID+"/access", jwt, nil); status != http.StatusConflict {
		t.Fatalf("宝塔凭证期望 409，实际 %d（body=%s）", status, raw)
	}

	// 不存在的服务器 → 404
	if status, _ := env.request(http.MethodGet, "/api/v1/servers/not-exist/access", jwt, nil); status != http.StatusNotFound {
		t.Fatalf("不存在的服务器期望 404，实际 %d", status)
	}

	// 审计：成功读取必须留痕，且 detail 不含明文密码
	logs, err := env.st.ListAuditLogs(context.Background(), store.AuditFilter{Limit: 200})
	if err != nil {
		t.Fatalf("读取审计日志失败: %v", err)
	}
	found := false
	for _, entry := range logs {
		if entry.Action != auditActionServerAccess {
			continue
		}
		found = true
		if entry.ResourceType == nil || *entry.ResourceType != "server" {
			t.Fatalf("审计 resource_type 期望 server，实际 %+v", entry.ResourceType)
		}
		if entry.ResourceID == nil || *entry.ResourceID != srv.ID {
			t.Fatalf("审计 resource_id 期望 %s，实际 %+v", srv.ID, entry.ResourceID)
		}
		if entry.Detail != nil && (strings.Contains(*entry.Detail, password) || strings.Contains(*entry.Detail, "test-api-key")) {
			t.Fatalf("审计 detail 不应含明文机密: %s", *entry.Detail)
		}
	}
	if !found {
		t.Fatalf("审计应包含 %s 记录", auditActionServerAccess)
	}
}
