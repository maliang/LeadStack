package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/maliang/leadstack/internal/store"
)

// TestReleasesExposeAppName 校验发布接口对外形状：列表与详情里的 release 都带
// app_name/app_slug，前端不必再为每条发布回查 /apps 才能渲染应用名。
func TestReleasesExposeAppName(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login()
	app := env.createAppVia(admin, "发布应用名", "release-app-name")

	rel := &store.Release{
		ID:               "rel-app-name-api",
		AppID:            app.ID,
		TemplateID:       "tpl-app-name-api",
		Version:          "1.0.0",
		TemplateSnapshot: "{}",
		Status:           store.ReleaseStatusPending,
		CreatedAt:        store.NowMS(),
		Concurrency:      4,
	}
	runs := []store.ReleaseRun{{
		ID:        "rel-app-name-api-run",
		ReleaseID: rel.ID,
		ServerID:  "srv-app-name-api",
		Status:    store.ReleaseStatusPending,
	}}
	if err := env.st.CreateRelease(context.Background(), rel, runs); err != nil {
		t.Fatalf("创建发布失败: %v", err)
	}

	// 列表
	status, raw := env.request(http.MethodGet, "/api/v1/releases?app_id="+app.ID, admin, nil)
	if status != http.StatusOK {
		t.Fatalf("查询发布列表失败: status=%d body=%s", status, raw)
	}
	var list []store.Release
	decodeData(t, raw, &list)
	if len(list) != 1 {
		t.Fatalf("应返回 1 条发布，实际 %d 条: %s", len(list), raw)
	}
	if list[0].AppName != app.Name || list[0].AppSlug != app.Slug {
		t.Fatalf("列表应用字段不符: app_name=%q app_slug=%q，期望 %q/%q",
			list[0].AppName, list[0].AppSlug, app.Name, app.Slug)
	}

	// 详情
	status, raw = env.request(http.MethodGet, "/api/v1/releases/"+rel.ID, admin, nil)
	if status != http.StatusOK {
		t.Fatalf("查询发布详情失败: status=%d body=%s", status, raw)
	}
	var detail struct {
		Release store.Release `json:"release"`
	}
	decodeData(t, raw, &detail)
	if detail.Release.AppName != app.Name || detail.Release.AppSlug != app.Slug {
		t.Fatalf("详情应用字段不符: app_name=%q app_slug=%q，期望 %q/%q",
			detail.Release.AppName, detail.Release.AppSlug, app.Name, app.Slug)
	}
}
