package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/maliang/leadstack/internal/store"
)

type serverAccountEnvelope struct {
	Data serverAccountPayload `json:"data"`
}

// TestServerAccounts 覆盖访问账号的增删改查、密码按需读取、脚本执行与审计脱敏。
func TestServerAccounts(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	srv := env.createServerVia(jwt, map[string]any{"name": "acct-srv", "host": "10.20.30.40", "mode": "agent"})

	const password = "{{SECRET_kr3iznga}}"
	const script = "#!/bin/sh\nprintf '{\"url\":\"https://panel.example.com/abc\"}'\n"
	status, raw := env.request(http.MethodPost, "/api/v1/servers/"+srv.ID+"/accounts", jwt, map[string]any{
		"name": "宝塔面板", "address": "https://10.20.30.40:8888", "username": "admin",
		"password": password, "note": "生产面板",
		"custom_fields": map[string]string{"环境": "生产"},
		"login_script":  script,
	})
	if status != http.StatusCreated {
		t.Fatalf("创建账号期望 201，实际 %d（body=%s）", status, raw)
	}
	var created serverAccountEnvelope
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("解析创建响应失败: %v", err)
	}
	acc := created.Data
	if acc.ID == "" || acc.ServerID != srv.ID || acc.Name != "宝塔面板" ||
		!acc.HasPassword || !acc.HasLoginScript || acc.LoginScript != strings.TrimSpace(script) {
		t.Fatalf("创建返回字段不符: %+v", acc)
	}
	if strings.Contains(string(raw), password) {
		t.Fatalf("创建响应不得回显密码: %s", raw)
	}

	// 列表：返回元数据，不回显密码；自定义字段按对象返回。
	status, raw = env.request(http.MethodGet, "/api/v1/servers/"+srv.ID+"/accounts", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("列表期望 200，实际 %d（body=%s）", status, raw)
	}
	if strings.Contains(string(raw), password) {
		t.Fatalf("列表不得包含明文密码: %s", raw)
	}
	var list struct {
		Data []serverAccountPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("解析列表失败: %v", err)
	}
	if len(list.Data) != 1 || list.Data[0].Note != "生产面板" {
		t.Fatalf("列表内容不符: %+v", list.Data)
	}
	var fields map[string]string
	if err := json.Unmarshal(list.Data[0].CustomFields, &fields); err != nil || fields["环境"] != "生产" {
		t.Fatalf("自定义字段不符: %v / %s", err, string(list.Data[0].CustomFields))
	}

	// 密码单独读取，并写审计。
	status, raw = env.request(http.MethodGet, "/api/v1/servers/"+srv.ID+"/accounts/"+acc.ID+"/secret", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("读取密码期望 200，实际 %d（body=%s）", status, raw)
	}
	var secret struct {
		Data serverAccountSecretPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &secret); err != nil || secret.Data.Password != password {
		t.Fatalf("密码读取不符: %v / %+v", err, secret.Data)
	}

	// 更新：改名/备注/清空密码；不传的字段保持不变。
	status, raw = env.request(http.MethodPatch, "/api/v1/servers/"+srv.ID+"/accounts/"+acc.ID, jwt, map[string]any{
		"name": "主面板", "note": "已迁移", "password": "",
	})
	if status != http.StatusOK {
		t.Fatalf("更新期望 200，实际 %d（body=%s）", status, raw)
	}
	var updated serverAccountEnvelope
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatalf("解析更新响应失败: %v", err)
	}
	if updated.Data.Name != "主面板" || updated.Data.Note != "已迁移" || updated.Data.HasPassword {
		t.Fatalf("更新未生效: %+v", updated.Data)
	}
	if status, _ := env.request(http.MethodGet, "/api/v1/servers/"+srv.ID+"/accounts/"+acc.ID+"/secret", jwt, nil); status != http.StatusConflict {
		t.Fatalf("密码清空后期望 409，实际 %d", status)
	}

	// 脚本执行：按 url/terminal/output 解析，并在审计里只记长度/是否出 url。
	runner := &loginDeployStub{fakeDeployOrchestrator: &fakeDeployOrchestrator{}, output: `{"url":"https://panel.example.com/abc"}`}
	env.api.deploy = runner
	status, raw = env.request(http.MethodPost, "/api/v1/servers/"+srv.ID+"/accounts/"+acc.ID+"/login", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("执行脚本期望 200，实际 %d（body=%s）", status, raw)
	}
	var login struct {
		Data serverAccountLoginPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &login); err != nil {
		t.Fatalf("解析执行结果失败: %v", err)
	}
	if login.Data.URL != "https://panel.example.com/abc" || login.Data.AccountID != acc.ID || login.Data.ServerID != srv.ID {
		t.Fatalf("执行结果不符: %+v", login.Data)
	}
	if len(runner.calls) != 1 || !strings.Contains(runner.calls[0], strings.TrimSpace(script)) {
		t.Fatalf("执行器收到的脚本不符: %+v", runner.calls)
	}

	// 审计：不得出现明文密码或 panel URL 明文；关键动作齐全。
	logs, err := env.st.ListAuditLogs(context.Background(), store.AuditFilter{Limit: 200})
	if err != nil {
		t.Fatalf("读取审计失败: %v", err)
	}
	actions := map[string]bool{}
	for _, entry := range logs {
		actions[entry.Action] = true
		if entry.Detail != nil && strings.Contains(*entry.Detail, password) {
			t.Fatalf("审计 detail 不得含明文密码: %s", *entry.Detail)
		}
	}
	for _, want := range []string{"server.account.create", "server.account.update", "server.account.login", "server.account.access"} {
		if !actions[want] {
			t.Fatalf("缺少审计 action %s（已有 %v）", want, actions)
		}
	}

	// 跨服务器访问按 404 处理。
	other := env.createServerVia(jwt, map[string]any{"name": "other", "host": "10.20.30.41"})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/servers/" + other.ID + "/accounts/" + acc.ID + "/secret"},
		{http.MethodPost, "/api/v1/servers/" + other.ID + "/accounts/" + acc.ID + "/login"},
	} {
		if status, raw := env.request(tc.method, tc.path, jwt, nil); status != http.StatusNotFound {
			t.Fatalf("跨服务器访问 %s %s 期望 404，实际 %d（body=%s）", tc.method, tc.path, status, raw)
		}
	}

	// 删除后列表为空。
	if status, raw := env.request(http.MethodDelete, "/api/v1/servers/"+srv.ID+"/accounts/"+acc.ID, jwt, nil); status != http.StatusOK {
		t.Fatalf("删除期望 200，实际 %d（body=%s）", status, raw)
	}
	status, raw = env.request(http.MethodGet, "/api/v1/servers/"+srv.ID+"/accounts", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("删除后列表期望 200，实际 %d（body=%s）", status, raw)
	}
	if err := json.Unmarshal(raw, &list); err != nil || len(list.Data) != 0 {
		t.Fatalf("删除后列表应为空: %v / %+v", err, list.Data)
	}
}
