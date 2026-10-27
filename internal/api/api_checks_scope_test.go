package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/maliang/leadstack/internal/store"
)

// checkResultListBody 是 GET /checks/{id}/results 的响应形状（data 下是数组）。
type checkResultListBody struct {
	Data []struct {
		ServerID   string `json:"server_id"`
		ServerName string `json:"server_name"`
		TS         int64  `json:"ts"`
	} `json:"data"`
}

// TestCheckResultsScopeFiltersByServer 复现并锁定「检测项结果越权读」：
//
// 检测项是「一台机器可达即可见」的对象（checkVisible 只看目标集合与范围有没有交集），
// 但同一检测项下的结果分属不同服务器。修复前 handler 只过了 checkVisible 就把整条
// 时间线返回，组范围用户因此能读到组外服务器的结果，还带 server_name —— 真实越权读，
// 不是理论问题。
//
// 本用例同时验证三件事：
//  1. 组范围用户只拿到「检测项目标 ∩ 范围」的行（组外服务器一条都不出现，也不泄露名字）；
//  2. server_id 指向组外服务器 → 404（与 requireServerInScope 同口径，不泄露存在性）；
//  3. 管理员（全局范围）行为不变。
func TestCheckResultsScopeFiltersByServer(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()
	base := store.NowMS()

	srvIn := env.createServerVia(admin, map[string]any{"name": "chk-in", "host": "10.33.0.1", "mode": "agent"})
	srvOut := env.createServerVia(admin, map[string]any{"name": "chk-out", "host": "10.33.0.2", "mode": "agent"})
	// 同一个检测项同时覆盖组内与组外服务器：这正是越权的发生条件。
	check := env.createCheckVia(admin, map[string]any{
		"name": "双目标检测", "kind": "tcp", "target": "127.0.0.1:22", "interval_s": 10,
		"server_ids": []string{srvIn.ID, srvOut.ID},
	})

	// 组外服务器的结果更新（若在 Go 里先取「全局最新 N 条」再过滤，组内那条会被挤掉）。
	for i := 0; i < 6; i++ {
		seedCheckResult(t, env, check.ID, srvOut.ID, base-int64(i+1)*1000, true, "")
	}
	seedCheckResult(t, env, check.ID, srvIn.ID, base-7*1000, false, "端口不可达")

	groupID := env.createGroupVia(admin, "检测结果范围组")
	env.setGroupMembers(admin, groupID, []string{srvIn.ID})
	roleKey := env.createRoleVia(admin, "检测结果范围只读", []string{"check.read"},
		map[string]any{"mode": "groups", "group_ids": []string{groupID}, "server_ids": []string{}})
	env.createUserVia(admin, "chk-scope-viewer", "ChkScope-123", roleKey)
	scoped := env.loginAs(t, "chk-scope-viewer", "ChkScope-123")

	// ① 组范围用户：只有组内服务器那一行，且不出现组外服务器的名字（server_name 泄露）。
	status, raw := env.request(http.MethodGet, "/api/v1/checks/"+check.ID+"/results?limit=5", scoped, nil)
	if status != http.StatusOK {
		t.Fatalf("范围角色查询结果时间线失败: status=%d body=%s", status, raw)
	}
	var scopedBody checkResultListBody
	if err := json.Unmarshal(raw, &scopedBody); err != nil {
		t.Fatalf("解析范围角色结果失败: %v body=%s", err, raw)
	}
	if len(scopedBody.Data) != 1 || scopedBody.Data[0].ServerID != srvIn.ID {
		t.Fatalf("组范围用户应只拿到组内服务器的一条结果（修复前是 5 条组外结果），实际: %s", raw)
	}
	if scopedBody.Data[0].ServerName != "chk-in" {
		t.Fatalf("server_name 应为组内服务器，实际 %q", scopedBody.Data[0].ServerName)
	}
	if strings.Contains(string(raw), "chk-out") {
		t.Fatalf("响应里不应出现组外服务器的名字（越权泄露）: %s", raw)
	}
	// limit 放大到覆盖全表也不能把组外结果捞回来。
	status, raw = env.request(http.MethodGet, "/api/v1/checks/"+check.ID+"/results?limit=1000", scoped, nil)
	if status != http.StatusOK {
		t.Fatalf("范围角色全量查询失败: status=%d body=%s", status, raw)
	}
	var scopedAll checkResultListBody
	if err := json.Unmarshal(raw, &scopedAll); err != nil {
		t.Fatalf("解析范围角色全量结果失败: %v body=%s", err, raw)
	}
	for _, row := range scopedAll.Data {
		if row.ServerID != srvIn.ID {
			t.Fatalf("范围角色的结果只能来自组内服务器，实际 %q: %s", row.ServerID, raw)
		}
	}

	// 越界的检测项（目标全在范围外）：404 not_found（非零范围仍走 404 惯例）。
	otherCheck := env.createCheckVia(admin, map[string]any{
		"name": "全组外检测", "kind": "tcp", "target": "127.0.0.1:23", "interval_s": 10,
		"server_ids": []string{srvOut.ID},
	})
	status, raw = env.request(http.MethodGet, "/api/v1/checks/"+otherCheck.ID+"/results", scoped, nil)
	if status != http.StatusNotFound {
		t.Fatalf("目标全在范围外的检测项期望 404，实际 status=%d body=%s", status, raw)
	}
	if code, _, _ := decodeErrorBody(t, raw); code != codeNotFound {
		t.Fatalf("越界检测项错误码应为 %s，实际 %s", codeNotFound, code)
	}

	// ② server_id 指向组外服务器：404 not_found（不是 403、不是 200 空数组）。
	status, raw = env.request(http.MethodGet, "/api/v1/checks/"+check.ID+"/results?server_id="+srvOut.ID, scoped, nil)
	if status != http.StatusNotFound {
		t.Fatalf("越界 server_id 期望 404，实际 status=%d body=%s", status, raw)
	}
	if code, msg, _ := decodeErrorBody(t, raw); code != codeNotFound {
		t.Fatalf("越界 server_id 错误码应为 %s，实际 %s（%s）", codeNotFound, code, msg)
	}

	// 组内 server_id 正常返回；管理员查询全局行为不变（最新 5 条，含组外结果）。
	status, raw = env.request(http.MethodGet, "/api/v1/checks/"+check.ID+"/results?server_id="+srvIn.ID, scoped, nil)
	if status != http.StatusOK {
		t.Fatalf("组内 server_id 应返回 200，实际 status=%d body=%s", status, raw)
	}
	var inBody checkResultListBody
	if err := json.Unmarshal(raw, &inBody); err != nil {
		t.Fatalf("解析组内 server_id 结果失败: %v body=%s", err, raw)
	}
	if len(inBody.Data) != 1 || inBody.Data[0].ServerID != srvIn.ID {
		t.Fatalf("组内 server_id 应恰好返回一条，实际: %s", raw)
	}

	status, raw = env.request(http.MethodGet, "/api/v1/checks/"+check.ID+"/results?limit=5", admin, nil)
	if status != http.StatusOK {
		t.Fatalf("管理员查询结果时间线失败: status=%d body=%s", status, raw)
	}
	var adminBody checkResultListBody
	if err := json.Unmarshal(raw, &adminBody); err != nil {
		t.Fatalf("解析管理员结果失败: %v body=%s", err, raw)
	}
	if len(adminBody.Data) != 5 {
		t.Fatalf("管理员应返回最新 5 条，实际 %d 条: %s", len(adminBody.Data), raw)
	}
	if !strings.Contains(string(raw), `"server_id":"`+srvOut.ID+`"`) {
		t.Fatalf("管理员应能看到组外服务器的结果（说明它只是被范围条件挡住，不是数据缺失）: %s", raw)
	}
}

// TestCheckResultsEmptyScopeReturnsEmptyList 锁定空范围角色的读口径：200 + 空数组，
// 不查库、不 403/500，也不出现 IN ()。
//
// 判定放在 loadCheck 之前是有意的：零范围角色看不到任何检测项，如果先按可见性判 404，
// 「检测项是否存在」就能靠 404/200 的差异被探测出来。因此对已存在与不存在的检测项都
// 返回同样的空集合（下面两条断言一起锁定这一点）。
func TestCheckResultsEmptyScopeReturnsEmptyList(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()

	srv := env.createServerVia(admin, map[string]any{"name": "chk-empty-srv", "host": "10.33.1.1", "mode": "agent"})
	check := env.createCheckVia(admin, map[string]any{
		"name": "空范围检测", "kind": "tcp", "target": "127.0.0.1:22", "interval_s": 10,
		"server_ids": []string{srv.ID},
	})
	seedCheckResult(t, env, check.ID, srv.ID, store.NowMS(), true, "")

	emptyGroup := env.createGroupVia(admin, "空检测结果组")
	roleKey := env.createRoleVia(admin, "空范围检测只读", []string{"check.read"},
		map[string]any{"mode": "groups", "group_ids": []string{emptyGroup}, "server_ids": []string{}})
	env.createUserVia(admin, "chk-empty-viewer", "ChkEmpty-123", roleKey)
	scoped := env.loginAs(t, "chk-empty-viewer", "ChkEmpty-123")

	// 已存在的检测项：200 + 空数组（不是 403，也不是带着全量数据）。
	for _, path := range []string{
		"/api/v1/checks/" + check.ID + "/results",
		"/api/v1/checks/" + check.ID + "/results?server_id=" + srv.ID,
		"/api/v1/checks/" + check.ID + "/results?limit=1000",
		"/api/v1/checks/ghost-check/results",
	} {
		status, _, raw := env.doRequest(http.MethodGet, path, scoped, nil, nil)
		if status != http.StatusOK {
			t.Fatalf("%s 期望 200 + 空数组，实际 status=%d body=%s", path, status, raw)
		}
		if !strings.Contains(string(raw), `"data":[]`) {
			t.Fatalf("%s 应返回空数组 data:[]，实际: %s", path, raw)
		}
		if strings.Contains(string(raw), srv.ID) {
			t.Fatalf("%s 不应泄露任何服务器数据: %s", path, raw)
		}
	}

	// 反证：管理员同一请求能看到数据，说明空集合只是被权限挡住，不是接口坏了。
	status, raw := env.request(http.MethodGet, "/api/v1/checks/"+check.ID+"/results", admin, nil)
	if status != http.StatusOK {
		t.Fatalf("管理员查询结果失败: status=%d body=%s", status, raw)
	}
	var body checkResultListBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("解析管理员结果失败: %v body=%s", err, raw)
	}
	if len(body.Data) != 1 || body.Data[0].ServerID != srv.ID {
		t.Fatalf("管理员应看到唯一一条结果，实际: %s", raw)
	}
}
