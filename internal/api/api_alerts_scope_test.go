package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/maliang/leadstack/internal/store"
)

// seedAlertForScope 直接落库一条告警（started_at 显式指定）；serverID 传空串表示
// 规则级告警（alerts.server_id 为 NULL）。
func seedAlertForScope(t *testing.T, env *testEnv, id, serverID string, startedAt int64) {
	t.Helper()
	var server *string
	if serverID != "" {
		s := serverID
		server = &s
	}
	if err := env.st.CreateAlert(context.Background(), store.Alert{
		ID:        id,
		RuleID:    "rule-scope-f2",
		DedupKey:  "dedup-" + id,
		ServerID:  server,
		Status:    store.AlertStatusFiring,
		Severity:  "critical",
		Title:     "告警 " + id,
		StartedAt: startedAt,
		UpdatedAt: startedAt,
	}); err != nil {
		t.Fatalf("创建告警 %s 失败: %v", id, err)
	}
}

// alertListBody 是 GET /alerts 的响应形状（分页时带 page）。
type alertListBody struct {
	Data []struct {
		ID       string  `json:"id"`
		ServerID *string `json:"server_id"`
	} `json:"data"`
	Page struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} `json:"page"`
}

// TestListAlertsScopeFilterAppliesBeforeLimit 复现并锁定「告警列表先 LIMIT 后按范围过滤」：
// 范围过滤必须在 SQL 里、且排在 LIMIT 之前。
//
// 缺陷形态：组范围角色的告警只要不在全局最新 N 条里，接口返回空数组，前端显示
// 「暂无告警」——真有故障却告诉用户没事，是误报代价最高的一种。
func TestListAlertsScopeFilterAppliesBeforeLimit(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()
	now := store.NowMS()

	srvIn := env.createServerVia(admin, map[string]any{"name": "al-scope-in", "host": "10.32.0.1", "mode": "agent"})
	srvOut := env.createServerVia(admin, map[string]any{"name": "al-scope-out", "host": "10.32.0.2", "mode": "agent"})
	groupID := env.createGroupVia(admin, "告警范围组")
	env.setGroupMembers(admin, groupID, []string{srvIn.ID})

	roleKey := env.createRoleVia(admin, "范围内告警只读", []string{"alert.read"},
		map[string]any{"mode": "groups", "group_ids": []string{groupID}, "server_ids": []string{}})
	env.createUserVia(admin, "al-scope-viewer", "AlScope-123", roleKey)
	scoped := env.loginAs(t, "al-scope-viewer", "AlScope-123")

	// 排序与窗口：规则级告警最新；6 条范围外告警紧随；范围内那条最早（全局第 8）。
	seedAlertForScope(t, env, "al-rule-level", "", now)
	for i := 0; i < 6; i++ {
		seedAlertForScope(t, env, "al-out-"+string(rune('0'+i)), srvOut.ID, now-int64(1+i)*1000)
	}
	seedAlertForScope(t, env, "al-in-old", srvIn.ID, now-8*1000)

	// 管理员（全局范围）：仍是全局最新 5 条，且不含范围内那条旧告警。
	status, raw := env.request(http.MethodGet, "/api/v1/alerts?limit=5", admin, nil)
	if status != http.StatusOK {
		t.Fatalf("管理员查询告警列表失败: status=%d body=%s", status, raw)
	}
	var adminBody alertListBody
	if err := json.Unmarshal(raw, &adminBody); err != nil {
		t.Fatalf("解析管理员告警列表失败: %v body=%s", err, raw)
	}
	if len(adminBody.Data) != 5 {
		t.Fatalf("管理员应返回最新 5 条，实际 %d 条: %s", len(adminBody.Data), raw)
	}
	for _, item := range adminBody.Data {
		if item.ID == "al-in-old" {
			t.Fatalf("全局最新 5 条不应包含第 8 条旧告警，实际: %s", raw)
		}
	}

	// 组范围角色：范围内那条较旧告警必须出现在结果里（缺陷的复现点）。
	status, raw = env.request(http.MethodGet, "/api/v1/alerts?limit=5", scoped, nil)
	if status != http.StatusOK {
		t.Fatalf("范围角色查询告警列表失败: status=%d body=%s", status, raw)
	}
	var scopedBody alertListBody
	if err := json.Unmarshal(raw, &scopedBody); err != nil {
		t.Fatalf("解析范围角色告警列表失败: %v body=%s", err, raw)
	}
	if len(scopedBody.Data) != 1 || scopedBody.Data[0].ID != "al-in-old" {
		t.Fatalf("范围内应返回 al-in-old（不能被全局窗口截掉），实际: %s", raw)
	}
	for _, item := range scopedBody.Data {
		if item.ServerID == nil || *item.ServerID != srvIn.ID {
			t.Fatalf("范围角色拿到的告警必须属于范围内服务器，实际 %s: %s", item.ID, raw)
		}
	}

	// 契约 §53.3 不变：规则级告警（server_id 为 NULL）对非全局角色仍不可见，
	// 即使把 limit 放大到能覆盖全表也一样。
	status, raw = env.request(http.MethodGet, "/api/v1/alerts?limit=200", scoped, nil)
	if status != http.StatusOK {
		t.Fatalf("范围角色全量查询失败: status=%d body=%s", status, raw)
	}
	var scopedAll alertListBody
	if err := json.Unmarshal(raw, &scopedAll); err != nil {
		t.Fatalf("解析范围角色全量结果失败: %v body=%s", err, raw)
	}
	for _, item := range scopedAll.Data {
		if item.ID == "al-rule-level" {
			t.Fatalf("规则级告警不应出现在非全局范围结果里（契约 §53.3）: %s", raw)
		}
		if item.ServerID == nil {
			t.Fatalf("非全局范围结果不应包含 server_id 为 NULL 的告警: %s", raw)
		}
	}
	// 反证：管理员能看到规则级告警，说明它只是被范围条件挡住，不是数据缺失。
	status, raw = env.request(http.MethodGet, "/api/v1/alerts?limit=200", admin, nil)
	if status != http.StatusOK {
		t.Fatalf("管理员全量查询失败: status=%d body=%s", status, raw)
	}
	if !strings.Contains(string(raw), "al-rule-level") {
		t.Fatalf("管理员应能看到规则级告警，实际: %s", raw)
	}
}

// TestListAlertsEmptyScopeReturnsEmptyPage 确认范围为空时直接返回 200 + 空列表 + 分页
// meta：不查库（SQL 不会出现 IN ()）、不 403、不 500。
func TestListAlertsEmptyScopeReturnsEmptyPage(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()

	srv := env.createServerVia(admin, map[string]any{"name": "al-empty-out", "host": "10.32.1.1", "mode": "agent"})
	seedAlertForScope(t, env, "al-empty-1", srv.ID, store.NowMS())

	emptyGroup := env.createGroupVia(admin, "空告警组")
	roleKey := env.createRoleVia(admin, "空范围告警只读", []string{"alert.read"},
		map[string]any{"mode": "groups", "group_ids": []string{emptyGroup}, "server_ids": []string{}})
	env.createUserVia(admin, "al-empty-viewer", "AlEmpty-123", roleKey)
	scoped := env.loginAs(t, "al-empty-viewer", "AlEmpty-123")

	status, raw := env.request(http.MethodGet, "/api/v1/alerts?limit=5", scoped, nil)
	if status != http.StatusOK {
		t.Fatalf("空范围应返回 200，实际 status=%d body=%s", status, raw)
	}
	var body alertListBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("解析空范围结果失败: %v body=%s", err, raw)
	}
	if len(body.Data) != 0 {
		t.Fatalf("空范围应返回空列表，实际: %s", raw)
	}
	if !strings.Contains(string(raw), `"page"`) {
		t.Fatalf("分页请求应带 page meta，实际: %s", raw)
	}
	if body.Page.HasMore || body.Page.NextCursor != "" {
		t.Fatalf("空范围不应有下一页，实际: %s", raw)
	}
}
