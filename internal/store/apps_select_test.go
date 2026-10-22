package store

import (
	"context"
	"testing"
)

// TestAppSelectsCoverAllColumns 防止新增 apps 列时漏改某条 SELECT（scanApp 列数不匹配会 500）。
func TestAppSelectsCoverAllColumns(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	app := &App{ID: "app-sel", Name: "选择", Slug: "sel", CreatedAt: NowMS(), DeployMode: DeployModeScript}
	script := "git pull"
	app.RollbackScript = &script
	if err := st.CreateApp(ctx, app); err != nil {
		t.Fatalf("创建应用失败: %v", err)
	}
	// 三条 SELECT 路径都要能扫出来
	if _, err := st.GetApp(ctx, app.ID); err != nil {
		t.Fatalf("GetApp 失败: %v", err)
	}
	if _, err := st.GetAppBySlug(ctx, app.Slug); err != nil {
		t.Fatalf("GetAppBySlug 失败: %v", err)
	}
	apps, err := st.ListApps(ctx)
	if err != nil {
		t.Fatalf("ListApps 失败: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("应返回 1 个应用，实际 %d", len(apps))
	}
	if apps[0].RollbackScript == nil || *apps[0].RollbackScript != script {
		t.Fatalf("rollback_script 未正确读出: %+v", apps[0].RollbackScript)
	}
	if apps[0].DeployMode != DeployModeScript {
		t.Fatalf("deploy_mode 读出不符: %s", apps[0].DeployMode)
	}
}
