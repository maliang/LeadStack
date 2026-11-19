package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestReleaseCarriesAppName 固化发布列表/详情的应用名契约：
//  1. 应用存在时，ListReleases / GetRelease 返回 app_name 与 app_slug；
//  2. 应用被删（历史孤儿发布）时，两字段是空串而不是报错（LEFT JOIN）；
//  3. JSON 里字段始终存在（没有 omitempty），前端能区分「空串」与「字段缺失」。
func TestReleaseCarriesAppName(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	app := &App{ID: "app-rel-name", Name: "领栈演示", Slug: "rel-name", CreatedAt: NowMS()}
	if err := st.CreateApp(ctx, app); err != nil {
		t.Fatalf("创建应用失败: %v", err)
	}
	rel := &Release{
		ID:               "rel-app-name",
		AppID:            app.ID,
		TemplateID:       "tpl-app-name",
		Version:          "1.0.0",
		TemplateSnapshot: "{}",
		Status:           ReleaseStatusPending,
		CreatedAt:        NowMS(),
		Concurrency:      4,
	}
	runs := []ReleaseRun{{
		ID:        "rel-app-name-run",
		ReleaseID: rel.ID,
		ServerID:  "srv-app-name",
		Status:    ReleaseStatusPending,
	}}
	if err := st.CreateRelease(ctx, rel, runs); err != nil {
		t.Fatalf("创建发布失败: %v", err)
	}

	// 列表：应用存在时带出名字与 slug。
	rels, err := st.ListReleases(ctx, ReleaseFilter{AppID: app.ID, Limit: 10})
	if err != nil {
		t.Fatalf("查询发布列表失败: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("应返回 1 条发布，实际 %d", len(rels))
	}
	if rels[0].AppName != app.Name || rels[0].AppSlug != app.Slug {
		t.Fatalf("列表应用字段不符: app_name=%q app_slug=%q，期望 %q/%q",
			rels[0].AppName, rels[0].AppSlug, app.Name, app.Slug)
	}

	// 详情：同一条 SELECT 路径（scanRelease 列数必须同步）。
	got, err := st.GetRelease(ctx, rel.ID)
	if err != nil {
		t.Fatalf("查询发布详情失败: %v", err)
	}
	if got.AppName != app.Name || got.AppSlug != app.Slug {
		t.Fatalf("详情应用字段不符: app_name=%q app_slug=%q", got.AppName, got.AppSlug)
	}

	// 模拟应用被删后留下的孤儿发布（DeleteApp 在有发布时返回 409，运维直删仍可能
	// 产生这种行）。LEFT JOIN 必须让查询继续成功，两字段为空串。
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM apps WHERE id = ?`, app.ID); err != nil {
		t.Fatalf("删除应用失败: %v", err)
	}
	rels, err = st.ListReleases(ctx, ReleaseFilter{AppID: app.ID, Limit: 10})
	if err != nil {
		t.Fatalf("应用删除后查询发布列表失败: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("应用删除后仍应返回 1 条发布，实际 %d", len(rels))
	}
	if rels[0].AppName != "" || rels[0].AppSlug != "" {
		t.Fatalf("应用删除后应用字段应为空串，实际 app_name=%q app_slug=%q", rels[0].AppName, rels[0].AppSlug)
	}

	got, err = st.GetRelease(ctx, rel.ID)
	if err != nil {
		t.Fatalf("应用删除后查询发布详情失败: %v", err)
	}
	if got.AppName != "" || got.AppSlug != "" {
		t.Fatalf("应用删除后详情应用字段应为空串，实际 app_name=%q app_slug=%q", got.AppName, got.AppSlug)
	}

	// JSON 形状：没有 omitempty，空串字段也必须出现（前端据此区分空串与缺失）。
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("序列化发布失败: %v", err)
	}
	for _, key := range []string{`"app_name":""`, `"app_slug":""`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("JSON 应包含 %s，实际: %s", key, raw)
		}
	}
}
