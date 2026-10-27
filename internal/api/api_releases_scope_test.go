package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/maliang/leadstack/internal/store"
)

// seedReleaseForScope 直接落库一条发布（单台目标服务器），created_at 显式指定，
// 用来构造「范围内发布落在全局最新窗口之外」的排序。
//
// 走 store 而不是 POST /releases：后者会真的起编排执行，与本次要验证的「列表范围
// 过滤是否在 LIMIT 之前」无关，只会让用例变慢变脆。
func seedReleaseForScope(t *testing.T, st *store.Store, releaseID, serverID string, createdAt int64) {
	t.Helper()
	rel := &store.Release{
		ID:               releaseID,
		AppID:            "app-" + releaseID,
		TemplateID:       "tpl-" + releaseID,
		Version:          "1.0.0",
		TemplateSnapshot: "{}",
		Status:           store.ReleaseStatusPending,
		CreatedAt:        createdAt,
		Concurrency:      4,
	}
	runs := []store.ReleaseRun{{
		ID:        releaseID + "-run",
		ReleaseID: releaseID,
		ServerID:  serverID,
		Status:    store.ReleaseStatusPending,
	}}
	if err := st.CreateRelease(context.Background(), rel, runs); err != nil {
		t.Fatalf("创建发布 %s 失败: %v", releaseID, err)
	}
}

// TestListReleasesScopeFilterAppliesBeforeLimit 复现 t23 F2：GET /releases?limit=5
// 必须先按角色范围过滤、再截断，而不是先取全局最新 5 条再过滤。
//
// 缺陷形态：组范围运维角色的发布只要不在全局最新 5 条里，前端就会显示「你所授权
// 分组与服务器范围内暂无发布记录」——这是事实性误报，用户会去反复检查权限配置，
// 而发布一直都在。
func TestListReleasesScopeFilterAppliesBeforeLimit(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	admin := env.login()
	now := store.NowMS()

	srvIn := env.createServerVia(admin, map[string]any{"name": "rel-scope-in", "host": "10.31.0.1", "mode": "agent"})
	srvOut := env.createServerVia(admin, map[string]any{"name": "rel-scope-out", "host": "10.31.0.2", "mode": "agent"})
	groupID := env.createGroupVia(admin, "发布范围组")
	env.setGroupMembers(admin, groupID, []string{srvIn.ID})

	roleKey := env.createRoleVia(admin, "范围内发布只读", []string{"deploy.read"},
		map[string]any{"mode": "groups", "group_ids": []string{groupID}, "server_ids": []string{}})
	env.createUserVia(admin, "rel-scope-viewer", "RelScope-123", roleKey)
	scoped := env.loginAs(t, "rel-scope-viewer", "RelScope-123")

	// 6 条范围外发布更新（占满全局最新 5 条的窗口），范围内那条最早（全局第 7）。
	for i := 0; i < 6; i++ {
		seedReleaseForScope(t, env.st, fmt.Sprintf("rel-out-%d", i), srvOut.ID, now-int64(6-i)*1000)
	}
	seedReleaseForScope(t, env.st, "rel-in-old", srvIn.ID, now-7*1000)

	// 全局角色：与改动前一致，只看到最新 5 条，且不含范围内那条旧发布。
	status, raw := env.request(http.MethodGet, "/api/v1/releases?limit=5", admin, nil)
	if status != http.StatusOK {
		t.Fatalf("管理员查询发布列表失败: status=%d body=%s", status, raw)
	}
	var global []store.Release
	decodeData(t, raw, &global)
	if len(global) != 5 {
		t.Fatalf("全局角色应返回最新 5 条，实际 %d 条: %s", len(global), raw)
	}
	for _, rel := range global {
		if rel.ID == "rel-in-old" {
			t.Fatalf("全局最新 5 条不应包含第 7 条旧发布，实际: %s", raw)
		}
	}

	// 组范围角色：范围内那条较旧发布必须出现在结果里（F2 的复现点）。
	status, raw = env.request(http.MethodGet, "/api/v1/releases?limit=5", scoped, nil)
	if status != http.StatusOK {
		t.Fatalf("范围角色查询发布列表失败: status=%d body=%s", status, raw)
	}
	var scopedList []store.Release
	decodeData(t, raw, &scopedList)
	if len(scopedList) != 1 || scopedList[0].ID != "rel-in-old" {
		t.Fatalf("范围内应返回 rel-in-old（不能被全局窗口截掉），实际: %s", raw)
	}

	// 结果集的每一条都必须满足「目标服务器 ∩ 范围 ≠ ∅」。
	ids := make([]string, 0, len(scopedList))
	for _, rel := range scopedList {
		ids = append(ids, rel.ID)
	}
	releaseServers, err := env.st.ReleaseServerIDs(ctx, ids)
	if err != nil {
		t.Fatalf("查询发布目标服务器失败: %v", err)
	}
	for _, rel := range scopedList {
		hit := false
		for _, sid := range releaseServers[rel.ID] {
			if sid == srvIn.ID {
				hit = true
				break
			}
		}
		if !hit {
			t.Fatalf("发布 %s 的目标服务器 %v 不在调用者范围内", rel.ID, releaseServers[rel.ID])
		}
	}

	// 范围内新增一条更新的发布：两条都返回，且仍按创建时间倒序。
	seedReleaseForScope(t, env.st, "rel-in-new", srvIn.ID, now)
	status, raw = env.request(http.MethodGet, "/api/v1/releases?limit=5", scoped, nil)
	if status != http.StatusOK {
		t.Fatalf("范围角色再次查询失败: status=%d body=%s", status, raw)
	}
	var scopedList2 []store.Release
	decodeData(t, raw, &scopedList2)
	if len(scopedList2) != 2 || scopedList2[0].ID != "rel-in-new" || scopedList2[1].ID != "rel-in-old" {
		t.Fatalf("范围内期望 [rel-in-new rel-in-old]，实际: %s", raw)
	}
}
