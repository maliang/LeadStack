package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/maliang/leadstack/internal/store"
)

// checkListBody 是 GET /checks 与 GET /checks/summary 的响应形状。
type checkListBody struct {
	Data []checkPayload `json:"data"`
}

// checkSummaryBody 是 GET /checks/summary 的 data 形状。
type checkSummaryBody struct {
	Data struct {
		Total   int            `json:"total"`
		Up      int            `json:"up"`
		Down    int            `json:"down"`
		Unknown int            `json:"unknown"`
		Checks  []checkPayload `json:"checks"`
	} `json:"data"`
}

// seedScopedCheckFixture 构造 A12 的双目标夹具：
//
//	检测项目标 = 1 台组内 + 1 台组外；组外 6 条成功（更新）+ 组内 1 条失败（更早）。
//
// 组范围用户看到的必须是「组内 1 台、最近一条是组内失败、可用率 0%」，管理员看到的
// 仍是全局值（2 台、最近一条是组外的成功、可用率 85.71%）。返回值依次是
// admin token、scoped token、组内服务器、组外服务器、检测项。
func seedScopedCheckFixture(t *testing.T, env *testEnv) (string, string, serverPayload, serverPayload, checkPayload) {
	t.Helper()
	admin := env.login()
	base := store.NowMS()

	srvIn := env.createServerVia(admin, map[string]any{"name": "chk-in", "host": "10.34.0.1", "mode": "agent"})
	srvOut := env.createServerVia(admin, map[string]any{"name": "chk-out", "host": "10.34.0.2", "mode": "agent"})
	check := env.createCheckVia(admin, map[string]any{
		"name": "双目标检测", "kind": "tcp", "target": "127.0.0.1:22", "interval_s": 10,
		"server_ids": []string{srvIn.ID, srvOut.ID},
	})

	// 组外 6 条成功且更新：全局最新一条是组外那条（修复前列表的 last_result 就是它）。
	for i := 0; i < 6; i++ {
		seedCheckResult(t, env, check.ID, srvOut.ID, base-int64(i+1)*1000, true, "")
	}
	seedCheckResult(t, env, check.ID, srvIn.ID, base-7*1000, false, "组内端口不可达")

	groupID := env.createGroupVia(admin, "检测payload范围组")
	env.setGroupMembers(admin, groupID, []string{srvIn.ID})
	roleKey := env.createRoleVia(admin, "检测payload范围只读", []string{"check.read"},
		map[string]any{"mode": "groups", "group_ids": []string{groupID}, "server_ids": []string{}})
	env.createUserVia(admin, "chk-payload-viewer", "ChkPayload-123", roleKey)
	scoped := env.loginAs(t, "chk-payload-viewer", "ChkPayload-123")

	return admin, scoped, srvIn, srvOut, check
}

// findCheck 在列表里按 ID 找检测项。
func findCheck(t *testing.T, items []checkPayload, id string) checkPayload {
	t.Helper()
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("响应里没有检测项 %s，实际 %d 项", id, len(items))
	return checkPayload{}
}

// TestCheckListAndDetailTrimServerFields 锁定 GET /checks 与 GET /checks/{id} 的范围裁剪：
//
// t36 F1 的实测形态是「检测项目标含组内、组外各一台」时，组范围用户的列表项里
// server_ids 带组外 UUID、last_result 是组外那条（含 message）、availability_24h=85.71。
// 也就是说 t35 只堵住了 /checks/{id}/results，列表与详情仍在泄露组外服务器的结果内容与统计。
//
// 本用例同时断言列表、详情、summary 三个入口：它们必须由同一个组装 helper 收敛，
// 否则很容易出现「列表修了、详情还漏」这种半修状态。
func TestCheckListAndDetailTrimServerFields(t *testing.T) {
	env := newTestEnv(t)
	admin, scoped, srvIn, srvOut, check := seedScopedCheckFixture(t, env)

	// ① 列表：组范围用户只看到组内那台，最近结果是组内那条失败，统计只算范围内。
	status, raw := env.request(http.MethodGet, "/api/v1/checks?limit=100", scoped, nil)
	if status != http.StatusOK {
		t.Fatalf("范围角色查询检测项列表失败: status=%d body=%s", status, raw)
	}
	if strings.Contains(string(raw), srvOut.ID) || strings.Contains(string(raw), "chk-out") {
		t.Fatalf("列表里不应出现组外服务器（server_ids / server_name 泄露）: %s", raw)
	}
	var list checkListBody
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("解析范围角色检测项列表失败: %v body=%s", err, raw)
	}
	item := findCheck(t, list.Data, check.ID)
	if len(item.ServerIDs) != 1 || item.ServerIDs[0] != srvIn.ID {
		t.Fatalf("server_ids 应只含组内服务器，实际 %+v: %s", item.ServerIDs, raw)
	}
	if item.TargetCount != 1 || item.ServerCount != 1 {
		t.Fatalf("target_count/server_count 应按范围内目标计数，实际 %d/%d", item.TargetCount, item.ServerCount)
	}
	if item.LastResult == nil || item.LastResult.ServerID != srvIn.ID {
		t.Fatalf("last_result 必须是组内那条（修复前是组外那条），实际 %+v", item.LastResult)
	}
	if item.LastResult.Message != "组内端口不可达" {
		t.Fatalf("范围内最近结果应保留自己的 message，实际 %q", item.LastResult.Message)
	}
	if item.Availability24h != 0 || item.Results24h != 1 {
		t.Fatalf("范围内统计应为 0%%/1 条（组内 1 条失败），实际 %.2f%%/%d",
			item.Availability24h, item.Results24h)
	}

	// ② 详情：与列表同一口径（同一 helper），不能出现「列表修了详情还漏」。
	status, raw = env.request(http.MethodGet, "/api/v1/checks/"+check.ID, scoped, nil)
	if status != http.StatusOK {
		t.Fatalf("范围角色查询检测项详情失败: status=%d body=%s", status, raw)
	}
	if strings.Contains(string(raw), srvOut.ID) || strings.Contains(string(raw), "chk-out") {
		t.Fatalf("详情里不应出现组外服务器: %s", raw)
	}
	var detail struct {
		Data checkPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("解析范围角色检测项详情失败: %v body=%s", err, raw)
	}
	if len(detail.Data.ServerIDs) != 1 || detail.Data.ServerIDs[0] != srvIn.ID {
		t.Fatalf("详情 server_ids 应只含组内服务器，实际 %+v: %s", detail.Data.ServerIDs, raw)
	}
	if detail.Data.LastResult == nil || detail.Data.LastResult.ServerID != srvIn.ID {
		t.Fatalf("详情 last_result 必须是组内那条，实际 %+v", detail.Data.LastResult)
	}
	if detail.Data.Availability24h != 0 || detail.Data.Results24h != 1 {
		t.Fatalf("详情统计应为 0%%/1 条，实际 %.2f%%/%d",
			detail.Data.Availability24h, detail.Data.Results24h)
	}

	// ③ summary：元素形态与列表一致，组范围用户不应从 summary 读到组外字段。
	status, raw = env.request(http.MethodGet, "/api/v1/checks/summary", scoped, nil)
	if status != http.StatusOK {
		t.Fatalf("范围角色查询检测汇总失败: status=%d body=%s", status, raw)
	}
	if strings.Contains(string(raw), srvOut.ID) || strings.Contains(string(raw), "chk-out") {
		t.Fatalf("summary 里不应出现组外服务器: %s", raw)
	}
	var summary checkSummaryBody
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("解析范围角色检测汇总失败: %v body=%s", err, raw)
	}
	if summary.Data.Total != 1 || len(summary.Data.Checks) != 1 {
		t.Fatalf("summary 应只统计范围内 1 项，实际 total=%d checks=%d",
			summary.Data.Total, len(summary.Data.Checks))
	}
	if got := summary.Data.Checks[0]; got.Availability24h != 0 || got.Results24h != 1 {
		t.Fatalf("summary 统计应为 0%%/1 条，实际 %.2f%%/%d", got.Availability24h, got.Results24h)
	}

	// ④ 管理员（全局范围）行为不变：两个目标、最近一条来自组外、统计是全局 6/7。
	status, raw = env.request(http.MethodGet, "/api/v1/checks?limit=100", admin, nil)
	if status != http.StatusOK {
		t.Fatalf("管理员查询检测项列表失败: status=%d body=%s", status, raw)
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("解析管理员检测项列表失败: %v body=%s", err, raw)
	}
	adminItem := findCheck(t, list.Data, check.ID)
	if len(adminItem.ServerIDs) != 2 {
		t.Fatalf("管理员应看到 2 个目标，实际 %+v: %s", adminItem.ServerIDs, raw)
	}
	if adminItem.LastResult == nil || adminItem.LastResult.ServerID != srvOut.ID {
		t.Fatalf("管理员最近结果应是组外那条（说明它只是被范围挡住，不是数据缺失），实际 %+v", adminItem.LastResult)
	}
	if adminItem.Availability24h != 85.71 || adminItem.Results24h != 7 {
		t.Fatalf("管理员统计应为全局 85.71%%/7 条，实际 %.2f%%/%d",
			adminItem.Availability24h, adminItem.Results24h)
	}
}

// TestCheckDetailOutOfScopeReturns404 锁定详情接口的越界惯例：目标全在范围外的检测项
// 按「不存在」处理（404 not_found），不泄露存在性、不返回空对象。
func TestCheckDetailOutOfScopeReturns404(t *testing.T) {
	env := newTestEnv(t)
	admin, scoped, _, srvOut, _ := seedScopedCheckFixture(t, env)

	outOnly := env.createCheckVia(admin, map[string]any{
		"name": "全组外检测", "kind": "tcp", "target": "127.0.0.1:23", "interval_s": 10,
		"server_ids": []string{srvOut.ID},
	})
	status, raw := env.request(http.MethodGet, "/api/v1/checks/"+outOnly.ID, scoped, nil)
	if status != http.StatusNotFound {
		t.Fatalf("组外检测项详情期望 404，实际 status=%d body=%s", status, raw)
	}
	if code, _, _ := decodeErrorBody(t, raw); code != codeNotFound {
		t.Fatalf("越界详情错误码应为 %s，实际 %s", codeNotFound, code)
	}
	// 管理员同 ID 能看到（说明只是被范围挡住）。
	if status, raw := env.request(http.MethodGet, "/api/v1/checks/"+outOnly.ID, admin, nil); status != http.StatusOK {
		t.Fatalf("管理员应能看到该检测项，实际 status=%d body=%s", status, raw)
	}
}

// TestCheckListEmptyScopeReturnsEmptyList 锁定空范围角色：GET /checks 与 summary 都是
// 200 + 空数组（不查库、不 403/500、不出现 IN ()），且不泄露任何服务器或检测项数据。
func TestCheckListEmptyScopeReturnsEmptyList(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()

	srv := env.createServerVia(admin, map[string]any{"name": "chk-empty-scope", "host": "10.34.1.1", "mode": "agent"})
	check := env.createCheckVia(admin, map[string]any{
		"name": "空范围检测", "kind": "tcp", "target": "127.0.0.1:22", "interval_s": 10,
		"server_ids": []string{srv.ID},
	})
	seedCheckResult(t, env, check.ID, srv.ID, store.NowMS(), true, "")

	emptyGroup := env.createGroupVia(admin, "空检测payload组")
	roleKey := env.createRoleVia(admin, "空范围检测payload只读", []string{"check.read"},
		map[string]any{"mode": "groups", "group_ids": []string{emptyGroup}, "server_ids": []string{}})
	env.createUserVia(admin, "chk-payload-empty", "ChkPayloadEmpty-123", roleKey)
	scoped := env.loginAs(t, "chk-payload-empty", "ChkPayloadEmpty-123")

	status, _, raw := env.doRequest(http.MethodGet, "/api/v1/checks", scoped, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("空范围列表期望 200 + 空数组，实际 status=%d body=%s", status, raw)
	}
	if !strings.Contains(string(raw), `"data":[]`) {
		t.Fatalf("空范围列表应返回 data:[]，实际: %s", raw)
	}
	if strings.Contains(string(raw), srv.ID) || strings.Contains(string(raw), check.ID) {
		t.Fatalf("空范围列表不应泄露任何检测项或服务器: %s", raw)
	}

	status, _, raw = env.doRequest(http.MethodGet, "/api/v1/checks/summary", scoped, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("空范围汇总期望 200，实际 status=%d body=%s", status, raw)
	}
	var summary checkSummaryBody
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("解析空范围汇总失败: %v body=%s", err, raw)
	}
	if summary.Data.Total != 0 || len(summary.Data.Checks) != 0 {
		t.Fatalf("空范围汇总应为 0 项，实际 total=%d checks=%d", summary.Data.Total, len(summary.Data.Checks))
	}

	// 反证：管理员同请求能看到数据，说明空集合只是被范围挡住。
	status, raw = env.request(http.MethodGet, "/api/v1/checks?limit=100", admin, nil)
	if status != http.StatusOK {
		t.Fatalf("管理员查询列表失败: status=%d body=%s", status, raw)
	}
	var list checkListBody
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("解析管理员列表失败: %v body=%s", err, raw)
	}
	if len(list.Data) != 1 || list.Data[0].ID != check.ID {
		t.Fatalf("管理员应看到 1 个检测项，实际: %s", raw)
	}
}
