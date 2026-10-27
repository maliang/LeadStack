package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/maliang/leadstack/internal/store"
)

// ---------- 审计脱敏：辅助 ----------

// redactAuditBody 走真实的脱敏入口（不经 HTTP），返回脱敏后的 JSON 文本。
func redactAuditBody(t *testing.T, body string) string {
	t.Helper()
	return string(redactJSONBody([]byte(body)))
}

// decodeAuditDetail 把脱敏结果解析成 map，顺带断言它是合法 JSON。
func decodeAuditDetail(t *testing.T, detail string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(detail), &out); err != nil {
		t.Fatalf("脱敏结果不是合法 JSON: %v（%s）", err, detail)
	}
	return out
}

// auditMaskedKeys 取 detail 里的 __masked 键名清单。
func auditMaskedKeys(t *testing.T, detail map[string]any) []string {
	t.Helper()
	raw, ok := detail[auditMaskedListKey]
	if !ok {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s 应是数组，实际 %T", auditMaskedListKey, raw)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("%s 里应只有字符串，实际 %T", auditMaskedListKey, item)
		}
		out = append(out, s)
	}
	return out
}

// containsString 判断切片是否包含指定元素。
func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// latestAuditDetail 取指定 action 的最新一条审计 detail（列表按 id 倒序，第一条即最新）。
func latestAuditDetail(t *testing.T, env *testEnv, jwt, action string) string {
	t.Helper()
	status, raw := env.request(http.MethodGet, "/api/v1/audit-logs?action="+action+"&limit=20", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询审计失败: status=%d body=%s", status, raw)
	}
	var out struct {
		Data struct {
			Logs []struct {
				Action string  `json:"action"`
				Detail *string `json:"detail"`
			} `json:"logs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析审计列表失败: %v（%s）", err, raw)
	}
	if len(out.Data.Logs) == 0 {
		t.Fatalf("审计里没有 %s 条目: %s", action, raw)
	}
	if out.Data.Logs[0].Detail == nil || *out.Data.Logs[0].Detail == "" {
		t.Fatalf("审计 %s 条目没有任何 detail: %s", action, raw)
	}
	return *out.Data.Logs[0].Detail
}

// ---------- A15：env_json 入敏感键 ----------

// TestAuditRedactEnvJSONMasksValues 覆盖 env_json 在两种提交形态下的脱敏结果。
//
// 字符串形态（API 契约形态，env_json 是一段 JSON 文本）整段替换为 __masked；
// 对象形态（有的客户端直接把 env 当对象提交）掩码到值一级、键名保留。
func TestAuditRedactEnvJSONMasksValues(t *testing.T) {
	// 1) 字符串形态：明文绝不出现，键名登记进 __masked
	detail := redactAuditBody(t, `{"name":"order-svc","env_json":"{\"DB_PASS\":\"hunter2-secret\"}"}`)
	if strings.Contains(detail, "hunter2-secret") {
		t.Fatalf("env_json 明文落进了审计 detail: %s", detail)
	}
	parsed := decodeAuditDetail(t, detail)
	if got, _ := parsed["env_json"].(string); got != auditMaskedPlaceholder {
		t.Fatalf("env_json 的值应整体替换为 %s，实际 %v", auditMaskedPlaceholder, parsed["env_json"])
	}
	if keys := auditMaskedKeys(t, parsed); !containsString(keys, "env_json") {
		t.Fatalf("__masked 里应登记 env_json，实际 %v", keys)
	}

	// 2) 对象形态：键名保留、值一级掩码（含嵌套对象）
	nested := redactAuditBody(t, `{"env":{"DB_PASS":"hunter2-secret","MODE":"prod"},"config":{"env_json":{"API_KEY":"sk-live-xyz"}}}`)
	for _, plain := range []string{"hunter2-secret", "sk-live-xyz", "prod"} {
		if strings.Contains(nested, plain) {
			t.Fatalf("嵌套环境变量明文落进了审计 detail: %s", nested)
		}
	}
	parsed = decodeAuditDetail(t, nested)
	env, ok := parsed["env"].(map[string]any)
	if !ok {
		t.Fatalf("对象形态的 env 应保留键名，实际 %v", parsed["env"])
	}
	if _, ok := env["DB_PASS"]; !ok {
		t.Fatalf("对象形态的 env 应保留键名 DB_PASS，实际 %v", env)
	}
	if env["DB_PASS"] != auditMaskedPlaceholder || env["MODE"] != auditMaskedPlaceholder {
		t.Fatalf("对象形态的 env 应把每个值替换为 %s，实际 %v", auditMaskedPlaceholder, env)
	}
	cfg, ok := parsed["config"].(map[string]any)
	if !ok {
		t.Fatalf("config 应保留为对象，实际 %v", parsed["config"])
	}
	inner, ok := cfg["env_json"].(map[string]any)
	if !ok {
		t.Fatalf("嵌套的 env_json 应保留键名，实际 %v", cfg["env_json"])
	}
	if inner["API_KEY"] != auditMaskedPlaceholder {
		t.Fatalf("嵌套的 env_json 应掩码到值一级，实际 %v", inner)
	}
}

// TestAuditRedactSecretKeyCoverage 覆盖口令/密钥类字段全部脱敏（A15 同批要求）。
func TestAuditRedactSecretKeyCoverage(t *testing.T) {
	keys := []string{
		"password", "old_password", "new_password", "api_key", "apikey",
		"access_token", "secret", "passphrase",
	}
	for _, key := range keys {
		raw := `{"` + key + `":"plain-` + key + `-value","name":"svc"}`
		detail := redactAuditBody(t, raw)
		if strings.Contains(detail, "plain-"+key+"-value") {
			t.Fatalf("%s 明文落进了审计 detail: %s", key, detail)
		}
		parsed := decodeAuditDetail(t, detail)
		if masked := auditMaskedKeys(t, parsed); !containsString(masked, key) {
			t.Fatalf("%s 应登记进 __masked，实际 %v（detail=%s）", key, masked, detail)
		}
	}
}

// ---------- A16：content 出敏感键，原样记录 + 超长截断 ----------

// TestAuditRedactContentKeepsBody 覆盖 content 不再被脱敏：短正文原样保留，超长按字节截断。
func TestAuditRedactContentKeepsBody(t *testing.T) {
	// 1) 短正文原样保留
	short := "#!/bin/sh\necho hello-a16\n"
	detail := redactAuditBody(t, `{"content":`+mustJSON(t, short)+`}`)
	parsed := decodeAuditDetail(t, detail)
	if got, _ := parsed["content"].(string); got != short {
		t.Fatalf("短正文应原样保留，实际 %q（detail=%s）", got, detail)
	}
	if strings.Contains(detail, auditMaskedListKey) {
		t.Fatalf("content 不应再被登记为敏感键: %s", detail)
	}

	// 2) 超长 ASCII 正文：截断到 2048 字节并补标记，尾部内容不可见
	long := strings.Repeat("A", auditContentMaxBytes+500) + "TAIL-A16"
	detail = redactAuditBody(t, `{"content":`+mustJSON(t, long)+`}`)
	parsed = decodeAuditDetail(t, detail)
	got, _ := parsed["content"].(string)
	if !strings.HasSuffix(got, auditContentTruncatedSuffix) {
		t.Fatalf("超长正文应补截断标记，实际结尾 %q", got[len(got)-16:])
	}
	if want := long[:auditContentMaxBytes] + auditContentTruncatedSuffix; got != want {
		t.Fatalf("截断结果不符合预期：len=%d", len(got))
	}
	if strings.Contains(detail, "TAIL-A16") {
		t.Fatalf("截断后不应保留尾部内容: %s", detail)
	}

	// 3) 多字节字符不被劈成半个字符（否则 json.Marshal 会写出 U+FFFD 乱码）
	multi := strings.Repeat("a", auditContentMaxBytes-1) + "中文正文"
	detail = redactAuditBody(t, `{"content":`+mustJSON(t, multi)+`}`)
	parsed = decodeAuditDetail(t, detail)
	got, _ = parsed["content"].(string)
	if !strings.HasSuffix(got, auditContentTruncatedSuffix) {
		t.Fatalf("多字节正文超长也应截断，实际 %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("截断后不是合法 UTF-8: %q", got)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("截断把多字节字符劈开了（出现 U+FFFD）: %q", got)
	}
}

// mustJSON 把字符串编码成 JSON 字面量（用于拼请求体）。
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return string(raw)
}

// ---------- A15/A16：走真实 HTTP，断言审计条目 ----------

// TestAuditAppPatchMasksEnvJSON 覆盖 A15：PATCH /apps 带 env_json 后，detail 不含明文。
func TestAuditAppPatchMasksEnvJSON(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	app := env.createAppVia(jwt, "审计环境变量", "audit-env-json")

	// 正常形态：env_json 是一段 JSON 文本
	status, raw := env.request(http.MethodPatch, "/api/v1/apps/"+app.ID, jwt, map[string]any{
		"env_json": `{"DB_PASS":"hunter2-secret"}`,
	})
	if status != http.StatusOK {
		t.Fatalf("更新应用失败: status=%d body=%s", status, raw)
	}
	detail := latestAuditDetail(t, env, jwt, "app.update")
	if strings.Contains(detail, "hunter2-secret") {
		t.Fatalf("A15 失败：env 明文落进了审计 detail: %s", detail)
	}
	parsed := decodeAuditDetail(t, detail)
	if got, _ := parsed["env_json"].(string); got != auditMaskedPlaceholder {
		t.Fatalf("A15 失败：detail 里 env_json 的值应为 %s，实际 %v（detail=%s）",
			auditMaskedPlaceholder, parsed["env_json"], detail)
	}
	if keys := auditMaskedKeys(t, parsed); !containsString(keys, "env_json") {
		t.Fatalf("A15 失败：__masked 里应登记 env_json，实际 %v（detail=%s）", keys, detail)
	}

	// 异常形态：客户端把 env 当对象提交（handler 400），审计同样不能落明文
	status, _ = env.request(http.MethodPatch, "/api/v1/apps/"+app.ID, jwt, map[string]any{
		"env_json": map[string]any{"DB_PASS": "hunter2-nested"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("对象形态的 env_json 期望 400，实际 %d", status)
	}
	detail = latestAuditDetail(t, env, jwt, "app.update")
	if strings.Contains(detail, "hunter2-nested") {
		t.Fatalf("A15 失败：对象形态的 env 明文落进了审计 detail: %s", detail)
	}
	if !strings.Contains(detail, "DB_PASS") || !strings.Contains(detail, auditMaskedPlaceholder) {
		t.Fatalf("A15 失败：对象形态应保留键名并掩码值，实际 %s", detail)
	}
}

// TestAuditTemplatePatchKeepsContent 覆盖 A16：PATCH /templates 后 detail 保留正文。
func TestAuditTemplatePatchKeepsContent(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	tmpl := env.createTemplateVia(jwt, "审计模板正文", "binary", "#!/bin/sh\necho origin\n")

	content := "#!/bin/sh\necho template-content-a16\n"
	status, raw := env.request(http.MethodPatch, "/api/v1/templates/"+tmpl.ID, jwt,
		map[string]any{"content": content})
	if status != http.StatusOK {
		t.Fatalf("更新模板失败: status=%d body=%s", status, raw)
	}
	detail := latestAuditDetail(t, env, jwt, "template.update")
	if !strings.Contains(detail, "echo template-content-a16") {
		t.Fatalf("A16 失败：detail 应保留模板正文: %s", detail)
	}
	if strings.Contains(detail, `"content":"`+auditMaskedPlaceholder+`"`) {
		t.Fatalf("A16 失败：detail 里的 content 仍被脱敏: %s", detail)
	}

	// 超长正文（>2048 字节，且整包超过旧的 4KB 抓取上限）：截断可追溯，尾部不外泄
	long := strings.Repeat("# pad line for audit truncation\n", 400) + "TAIL-A16-MARKER\n"
	status, raw = env.request(http.MethodPatch, "/api/v1/templates/"+tmpl.ID, jwt,
		map[string]any{"content": long})
	if status != http.StatusOK {
		t.Fatalf("更新超长模板失败: status=%d body=%s", status, raw)
	}
	detail = latestAuditDetail(t, env, jwt, "template.update")
	if !strings.Contains(detail, auditContentTruncatedSuffix) {
		t.Fatalf("A16 失败：超长正文应补截断标记，detail 长度 %d", len(detail))
	}
	if strings.Contains(detail, "TAIL-A16-MARKER") {
		t.Fatalf("A16 失败：截断后不应保留尾部内容（detail 长度 %d）", len(detail))
	}

	// 审计抓取不能影响 handler 落库：模板正文必须完整保存
	status, raw = env.request(http.MethodGet, "/api/v1/templates/"+tmpl.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询模板失败: %d", status)
	}
	var got struct {
		Data store.DeployTemplate `json:"data"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("解析模板失败: %v", err)
	}
	if got.Data.Content != long {
		t.Fatalf("模板正文未完整落库：期望 %d 字节，实际 %d 字节", len(long), len(got.Data.Content))
	}
}

// TestAuditHistoricalDetailNotRewritten 覆盖「历史条目形态不回写」：
// 老库里 content 已被写成 __masked 的行，读取时原样返回，不回溯改写成新形态。
func TestAuditHistoricalDetailNotRewritten(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	legacy := `{"content":"__masked","__masked":["content"]}`
	if err := env.st.InsertAuditLog(context.Background(), &store.AuditLog{
		TS:     store.NowMS(),
		Actor:  "admin",
		Action: "legacy.audit_snapshot",
		Detail: &legacy,
	}); err != nil {
		t.Fatalf("写入历史审计条目失败: %v", err)
	}

	detail := latestAuditDetail(t, env, jwt, "legacy.audit_snapshot")
	if detail != legacy {
		t.Fatalf("历史条目被改写：期望 %s，实际 %s", legacy, detail)
	}
}
