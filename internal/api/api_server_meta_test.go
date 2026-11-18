package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestServerMeta 覆盖服务器档案（notes / custom_fields）的读写边界：
// 详情返回、列表不返回；非法输入先拒后写；重放幂等；审计 detail 只记键名不记值。
func TestServerMeta(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	srv := env.createServerVia(jwt, map[string]any{"name": "meta-target", "host": "10.30.40.50", "mode": "agent"})

	// 新建服务器：详情里 notes/custom_fields 稳定给出默认值。
	status, raw := env.request(http.MethodGet, "/api/v1/servers/"+srv.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("读取服务器详情失败: %d（body=%s）", status, raw)
	}
	var detail struct {
		Data serverDetailPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("解析详情失败: %v", err)
	}
	if detail.Data.Notes != "" {
		t.Fatalf("新建服务器 notes 应为空串，实际 %q", detail.Data.Notes)
	}
	if string(detail.Data.CustomFields) != "{}" {
		t.Fatalf("新建服务器 custom_fields 应为 {}，实际 %s", detail.Data.CustomFields)
	}

	// 合法写入：多行备注 + 字符串键值对象。
	notes := "第一行备注\n第二行：机架 A-03"
	fields := map[string]string{"env": "prod", "owner": "ops-team"}
	status, raw = env.request(http.MethodPatch, "/api/v1/servers/"+srv.ID, jwt, map[string]any{
		"notes":         notes,
		"custom_fields": fields,
	})
	if status != http.StatusOK {
		t.Fatalf("写入档案失败: %d（body=%s）", status, raw)
	}
	status, raw = env.request(http.MethodGet, "/api/v1/servers/"+srv.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("读取服务器详情失败: %d", status)
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("解析详情失败: %v", err)
	}
	if detail.Data.Notes != notes {
		t.Fatalf("详情 notes 不一致: %q", detail.Data.Notes)
	}
	var gotFields map[string]string
	if err := json.Unmarshal(detail.Data.CustomFields, &gotFields); err != nil {
		t.Fatalf("详情 custom_fields 不是对象: %v（%s）", err, detail.Data.CustomFields)
	}
	if gotFields["env"] != "prod" || gotFields["owner"] != "ops-team" {
		t.Fatalf("详情 custom_fields 内容不一致: %+v", gotFields)
	}
	t.Logf("详情报文（notes/custom_fields 与列表字段同级）: %s", raw)

	// 列表接口不返回这两项。
	status, raw = env.request(http.MethodGet, "/api/v1/servers", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("读取服务器列表失败: %d", status)
	}
	if strings.Contains(string(raw), `"notes"`) || strings.Contains(string(raw), `"custom_fields"`) {
		t.Fatalf("列表不应返回 notes/custom_fields: %s", raw)
	}

	// 重放同一请求：结果一致，不产生第二条记录/副作用。
	status, raw = env.request(http.MethodPatch, "/api/v1/servers/"+srv.ID, jwt, map[string]any{
		"notes":         notes,
		"custom_fields": fields,
	})
	if status != http.StatusOK {
		t.Fatalf("重放档案更新失败: %d（body=%s）", status, raw)
	}

	// 非法输入一律 400，且不得写库。
	tooLongNotes := strings.Repeat("备", maxServerNotesRunes+1)
	tooManyKeys := map[string]string{}
	for i := 0; i <= maxCustomFieldCount; i++ {
		tooManyKeys[fmt.Sprintf("key%d", i)] = "v"
	}
	longKey := map[string]string{strings.Repeat("k", maxCustomFieldKeyRunes+1): "v"}
	controlKey := map[string]string{"bad\x07key": "v"}
	longValue := map[string]string{"k": strings.Repeat("v", maxCustomFieldValueRunes+1)}
	hugeValues := map[string]string{}
	for i := 0; i < 20; i++ {
		hugeValues["k"+string(rune('a'+i))] = strings.Repeat("备", maxCustomFieldValueRunes)
	}
	cases := []struct {
		name string
		body map[string]any
	}{
		{"notes 超长", map[string]any{"notes": tooLongNotes}},
		{"notes 含控制字符", map[string]any{"notes": "bad\x00note"}},
		{"custom_fields 键过多", map[string]any{"custom_fields": tooManyKeys}},
		{"custom_fields 键超长", map[string]any{"custom_fields": longKey}},
		{"custom_fields 键含控制字符", map[string]any{"custom_fields": controlKey}},
		{"custom_fields 值超长", map[string]any{"custom_fields": longValue}},
		{"custom_fields 序列化超 8KB", map[string]any{"custom_fields": hugeValues}},
	}
	for _, tc := range cases {
		if status, raw := env.request(http.MethodPatch, "/api/v1/servers/"+srv.ID, jwt, tc.body); status != http.StatusBadRequest {
			t.Fatalf("%s 期望 400，实际 %d（body=%s）", tc.name, status, raw)
		}
	}
	// 被拒请求同样不得把备注正文带进审计 detail。
	if status, raw := env.request(http.MethodPatch, "/api/v1/servers/"+srv.ID, jwt,
		map[string]any{"notes": "绝密非法备注-keep-out\x00"}); status != http.StatusBadRequest {
		t.Fatalf("含 NUL 备注期望 400，实际 %d（body=%s）", status, raw)
	}
	stored, err := env.st.GetServer(context.Background(), srv.ID)
	if err != nil {
		t.Fatalf("读取服务器失败: %v", err)
	}
	if stored.Notes != notes {
		t.Fatalf("非法输入不得改写 notes，实际 %q", stored.Notes)
	}
	if stored.CustomFieldsJSON != `{"env":"prod","owner":"ops-team"}` {
		t.Fatalf("非法输入不得改写 custom_fields，实际 %s", stored.CustomFieldsJSON)
	}

	// 审计继续用 server.update，但 detail 只记键名不记值。
	status, raw = env.request(http.MethodPatch, "/api/v1/servers/"+srv.ID, jwt, map[string]any{
		"notes":         "绝密备注-abc123",
		"custom_fields": map[string]string{"db_password_ref": "prod-secret-xyz"},
	})
	if status != http.StatusOK {
		t.Fatalf("写入敏感档案失败: %d（body=%s）", status, raw)
	}
	status, raw = env.request(http.MethodGet, "/api/v1/audit-logs?action=server.update&limit=20", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询审计失败: %d", status)
	}
	audit := string(raw)
	if strings.Contains(audit, "绝密备注-abc123") || strings.Contains(audit, "prod-secret-xyz") ||
		strings.Contains(audit, "绝密非法备注-keep-out") {
		t.Fatalf("审计 detail 不得记录 notes/custom_fields 的值: %s", audit)
	}
	if !strings.Contains(audit, "custom_field_keys") || !strings.Contains(audit, "db_password_ref") {
		t.Fatalf("审计 detail 应记录 custom_fields 的键名: %s", audit)
	}

	// 清空：notes 空串、custom_fields 空对象。
	if status, raw := env.request(http.MethodPatch, "/api/v1/servers/"+srv.ID, jwt, map[string]any{
		"notes": "", "custom_fields": map[string]string{},
	}); status != http.StatusOK {
		t.Fatalf("清空档案失败: %d（body=%s）", status, raw)
	}
	status, raw = env.request(http.MethodGet, "/api/v1/servers/"+srv.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("读取详情失败: %d", status)
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("解析详情失败: %v", err)
	}
	if detail.Data.Notes != "" || string(detail.Data.CustomFields) != "{}" {
		t.Fatalf("清空后详情不一致: notes=%q custom_fields=%s", detail.Data.Notes, detail.Data.CustomFields)
	}
}
