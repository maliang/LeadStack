package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/maliang/leadstack/internal/store"
)

// loginDeployStub 在默认发布替身上补齐「执行登录脚本」能力，记录收到的脚本便于断言。
type loginDeployStub struct {
	*fakeDeployOrchestrator
	mu     sync.Mutex
	calls  []string
	output string
	err    error
}

func (f *loginDeployStub) RunLoginScript(_ context.Context, serverID, script string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, serverID+"\x00"+script)
	return f.output, f.err
}

// TestServerLogin 覆盖登录脚本的配置、执行与失败边界：
// 合法脚本可读回且执行结果按 url/terminal/output 返回；非法输入在执行/落盘前被拒；
// 未配置脚本 409、执行器未装配 503、远端失败 502；同一请求重放结果一致；审计不含敏感输出。
func TestServerLogin(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	// 未配置脚本的服务器：POST 应 409，且不得触发执行器。
	srv := env.createServerVia(jwt, map[string]any{"name": "login-target", "host": "10.20.30.40", "mode": "agent"})
	runner := &loginDeployStub{fakeDeployOrchestrator: &fakeDeployOrchestrator{}}
	env.api.deploy = runner
	status, raw := env.request(http.MethodPost, "/api/v1/servers/"+srv.ID+"/login", jwt, nil)
	if status != http.StatusConflict {
		t.Fatalf("未配置脚本期望 409，实际 %d（body=%s）", status, raw)
	}
	if code, message, _ := decodeErrorBody(t, raw); code != codeConflict || !strings.Contains(message, srv.ID) {
		t.Fatalf("未配置脚本错误体应含 server id（code=%q message=%q）", code, message)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("未配置脚本不应执行，实际调用 %d 次", len(runner.calls))
	}

	// 非法脚本（超长 / 含 NUL）必须在落盘前被拒。
	tooLong := strings.Repeat("a", maxLoginScriptRunes+1)
	if status, raw := env.request(http.MethodPatch, "/api/v1/servers/"+srv.ID, jwt,
		map[string]any{"login_script": tooLong}); status != http.StatusBadRequest {
		t.Fatalf("超长脚本期望 400，实际 %d（body=%s）", status, raw)
	}
	if status, raw := env.request(http.MethodPatch, "/api/v1/servers/"+srv.ID, jwt,
		map[string]any{"login_script": "echo hi\x00rm -rf /"}); status != http.StatusBadRequest {
		t.Fatalf("含 NUL 脚本期望 400，实际 %d（body=%s）", status, raw)
	}
	stored, err := env.st.GetServer(context.Background(), srv.ID)
	if err != nil {
		t.Fatalf("读取服务器失败: %v", err)
	}
	if stored.LoginScript != "" {
		t.Fatalf("非法脚本不得落盘，实际 %q", stored.LoginScript)
	}

	// 合法脚本（多行、含引号）可写入并原样读回。
	script := "#!/bin/sh\necho '{\"url\":\"https://example.com/s/abc\"}'\n"
	status, raw = env.request(http.MethodPatch, "/api/v1/servers/"+srv.ID, jwt,
		map[string]any{"login_script": script})
	if status != http.StatusOK {
		t.Fatalf("写入登录脚本失败: %d（body=%s）", status, raw)
	}
	var patched struct {
		Data serverPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &patched); err != nil {
		t.Fatalf("解析 PATCH 响应失败: %v", err)
	}
	if patched.Data.LoginScript != strings.TrimSpace(script) {
		t.Fatalf("PATCH 回显脚本不一致: %q", patched.Data.LoginScript)
	}

	// 读接口同样带出脚本。
	status, raw = env.request(http.MethodGet, "/api/v1/servers/"+srv.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("读取服务器失败: %d（body=%s）", status, raw)
	}
	var got struct {
		Data serverPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("解析 GET 响应失败: %v", err)
	}
	if got.Data.LoginScript != strings.TrimSpace(script) {
		t.Fatalf("GET 未返回登录脚本: %q", got.Data.LoginScript)
	}

	// 正常执行：脚本 stdout 是 JSON，按 url/terminal/output 返回。
	runner.output = `{"url":"https://panel.example.com/login?t=1","terminal":"ssh root@10.20.30.40","output":"ready"}`
	status, raw = env.request(http.MethodPost, "/api/v1/servers/"+srv.ID+"/login", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("执行登录脚本失败: %d（body=%s）", status, raw)
	}
	var loginOut struct {
		Data serverLoginPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &loginOut); err != nil {
		t.Fatalf("解析登录响应失败: %v", err)
	}
	want := serverLoginPayload{
		ServerID: srv.ID,
		URL:      "https://panel.example.com/login?t=1",
		Terminal: "ssh root@10.20.30.40",
		Output:   "ready",
	}
	if loginOut.Data != want {
		t.Fatalf("登录响应不一致: got %+v want %+v", loginOut.Data, want)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("执行器应被调用 1 次，实际 %d", len(runner.calls))
	}
	if runner.calls[0] != srv.ID+"\x00"+strings.TrimSpace(script) {
		t.Fatalf("执行器收到的 serverID/script 不符: %q", runner.calls[0])
	}

	// 重放同一请求：结果必须一致（无时间戳/随机量）。
	status2, raw2 := env.request(http.MethodPost, "/api/v1/servers/"+srv.ID+"/login", jwt, nil)
	if status2 != http.StatusOK || string(raw2) != string(raw) {
		t.Fatalf("重放结果不一致: status=%d body=%s", status2, raw2)
	}

	// 非 JSON 输出：整段落到 output，url/terminal 为空。
	runner.output = "plain text\nsecond line\n"
	status, raw = env.request(http.MethodPost, "/api/v1/servers/"+srv.ID+"/login", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("非 JSON 输出期望 200，实际 %d（body=%s）", status, raw)
	}
	if err := json.Unmarshal(raw, &loginOut); err != nil {
		t.Fatalf("解析登录响应失败: %v", err)
	}
	if loginOut.Data != (serverLoginPayload{ServerID: srv.ID, Output: "plain text\nsecond line"}) {
		t.Fatalf("非 JSON 输出应整体落到 output: %+v", loginOut.Data)
	}

	// JSON 缺字段：缺的字段为空串。
	runner.output = `{"output":"only"}`
	if status, raw = env.request(http.MethodPost, "/api/v1/servers/"+srv.ID+"/login", jwt, nil); status != http.StatusOK {
		t.Fatalf("缺字段输出期望 200，实际 %d（body=%s）", status, raw)
	}
	if err := json.Unmarshal(raw, &loginOut); err != nil {
		t.Fatalf("解析登录响应失败: %v", err)
	}
	if loginOut.Data.URL != "" || loginOut.Data.Terminal != "" || loginOut.Data.Output != "only" {
		t.Fatalf("缺字段输出解析不符: %+v", loginOut.Data)
	}

	// 远端失败 → 502 + remote_failure。
	runner.err = errors.New("dial tcp 10.20.30.40:22: connect: connection refused")
	status, raw = env.request(http.MethodPost, "/api/v1/servers/"+srv.ID+"/login", jwt, nil)
	if status != http.StatusBadGateway {
		t.Fatalf("远端失败期望 502，实际 %d（body=%s）", status, raw)
	}
	if code, message, _ := decodeErrorBody(t, raw); code != codeRemoteFailure || !strings.Contains(message, "登录脚本") {
		t.Fatalf("远端失败错误体不符: code=%q message=%q", code, message)
	}

	// 执行器未启用 → 503 + unavailable。
	runner.err = errOrchestratorDisabled
	status, raw = env.request(http.MethodPost, "/api/v1/servers/"+srv.ID+"/login", jwt, nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("执行器未启用期望 503，实际 %d（body=%s）", status, raw)
	}
	if code, _, _ := decodeErrorBody(t, raw); code != codeUnavailable {
		t.Fatalf("执行器未启用 code 期望 %q，实际 %q", codeUnavailable, code)
	}

	// 不存在的服务器 → 404。
	if status, raw = env.request(http.MethodPost, "/api/v1/servers/not-exist/login", jwt, nil); status != http.StatusNotFound {
		t.Fatalf("不存在的服务器期望 404，实际 %d（body=%s）", status, raw)
	}

	// 审计：成功执行必须留痕为 server.login，detail 不含脚本输出里的登录 URL。
	logs, err := env.st.ListAuditLogs(context.Background(), store.AuditFilter{Action: "server.login", Limit: 100})
	if err != nil {
		t.Fatalf("读取审计日志失败: %v", err)
	}
	if len(logs) == 0 {
		t.Fatalf("审计应包含 server.login 记录")
	}
	for _, entry := range logs {
		if entry.ResourceID == nil || *entry.ResourceID != srv.ID {
			t.Fatalf("审计 resource_id 期望 %s，实际 %+v", srv.ID, entry.ResourceID)
		}
		if entry.Detail != nil && strings.Contains(*entry.Detail, "panel.example.com") {
			t.Fatalf("审计 detail 不应包含脚本输出中的登录 URL: %s", *entry.Detail)
		}
	}
}
