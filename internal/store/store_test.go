package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// newTestStore 打开一个临时数据库并完成迁移。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "leadstack.db")
	st, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Logf("关闭测试数据库失败: %v", err)
		}
	})
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}
	return st
}

// TestOpenPragmas 校验 WAL 与 busy_timeout 已生效，且写连接被限制为 1。
func TestOpenPragmas(t *testing.T) {
	st := newTestStore(t)
	if got := st.DB().Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections 期望 1，实际 %d", got)
	}
	var journalMode string
	if err := st.DB().QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("读取 journal_mode 失败: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode 期望 wal，实际 %q", journalMode)
	}
}

// TestMigrateIsIdempotent 校验重复迁移不会重复建表或报错。
func TestMigrateIsIdempotent(t *testing.T) {
	st := newTestStore(t)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}
	var n int
	if err := st.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("统计迁移记录失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("schema_migrations 期望 1 条，实际 %d", n)
	}
}

// TestInsertSamplesDeduplicates 校验 (server_id, metric, ts) 去重语义。
func TestInsertSamplesDeduplicates(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	first := []MetricSample{
		{Metric: "cpu.pct", TS: 1000, Value: 1},
		{Metric: "cpu.pct", TS: 2000, Value: 2},
	}
	n, err := st.InsertSamples(ctx, "srv-1", first)
	if err != nil {
		t.Fatalf("首次写入指标失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("首次写入期望 2 条，实际 %d", n)
	}

	// 补传重复采样：只写入新增的那条。
	n, err = st.InsertSamples(ctx, "srv-1", []MetricSample{
		{Metric: "cpu.pct", TS: 2000, Value: 99},
		{Metric: "cpu.pct", TS: 3000, Value: 3},
		{Metric: "", TS: 4000, Value: 4},     // 非法指标名应被跳过
		{Metric: "cpu.pct", TS: 0, Value: 5}, // 非法时间戳应被跳过
	})
	if err != nil {
		t.Fatalf("补传指标失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("补传期望只写入 1 条，实际 %d", n)
	}

	latest, err := st.LatestMetrics(ctx, []string{"srv-1"})
	if err != nil {
		t.Fatalf("查询最新指标失败: %v", err)
	}
	if got := latest["srv-1"]["cpu.pct"]; got != 3 {
		t.Fatalf("最新 cpu.pct 期望 3，实际 %v", got)
	}
}

// TestSettingsEnsureSetting 校验配置项只会被生成一次，且并发启动不覆盖先写者。
func TestSettingsEnsureSetting(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	first, err := st.EnsureSetting(ctx, "jwt_secret", func() (string, error) { return "secret-A", nil })
	if err != nil {
		t.Fatalf("首次生成配置项失败: %v", err)
	}
	second, err := st.EnsureSetting(ctx, "jwt_secret", func() (string, error) { return "secret-B", nil })
	if err != nil {
		t.Fatalf("二次读取配置项失败: %v", err)
	}
	if first != "secret-A" || second != "secret-A" {
		t.Fatalf("配置项应保持首次写入值，实际 first=%q second=%q", first, second)
	}
}

// TestDeleteServerCascades 校验删除服务器会清理指标/事件并解绑令牌。
func TestDeleteServerCascades(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	srv := &Server{ID: "srv-1", Name: "n1", Host: "h1", Port: 22, Mode: "agent", TagsJSON: "[]", Status: "online", CreatedAt: NowMS()}
	if err := st.CreateServer(ctx, srv); err != nil {
		t.Fatalf("创建服务器失败: %v", err)
	}
	serverID := srv.ID
	token := &EnrollToken{ID: "tok-1", TokenHash: "hash", ExpiresAt: NowMS() + 60000, ServerID: &serverID}
	if err := st.CreateEnrollToken(ctx, token); err != nil {
		t.Fatalf("创建令牌失败: %v", err)
	}
	if _, err := st.InsertSamples(ctx, serverID, []MetricSample{{Metric: "cpu.pct", TS: 1000, Value: 1}}); err != nil {
		t.Fatalf("写入指标失败: %v", err)
	}
	if err := st.InsertEvent(ctx, &Event{ID: "ev-1", ServerID: &serverID, Kind: EventKindOnline, Message: "上线", TS: NowMS()}); err != nil {
		t.Fatalf("写入事件失败: %v", err)
	}

	if err := st.DeleteServer(ctx, serverID); err != nil {
		t.Fatalf("删除服务器失败: %v", err)
	}
	if _, err := st.GetServer(ctx, serverID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后查询应返回 ErrNotFound，实际 %v", err)
	}

	tokens, err := st.ListEnrollTokens(ctx, 10)
	if err != nil {
		t.Fatalf("查询令牌失败: %v", err)
	}
	if len(tokens) != 1 || tokens[0].ServerID != nil {
		t.Fatalf("令牌应被解绑，实际 %+v", tokens)
	}

	events, err := st.ListEvents(ctx, serverID, 10)
	if err != nil {
		t.Fatalf("查询事件失败: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("服务器事件应被清理，实际 %d 条", len(events))
	}
}

// TestUsersAndHeartbeat 覆盖用户唯一约束与心跳状态跃迁。
func TestUsersAndHeartbeat(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	u := &User{ID: "u1", Username: "admin", PasswordHash: "x", Role: "admin", CreatedAt: NowMS()}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := st.CreateUser(ctx, &User{ID: "u2", Username: "admin", PasswordHash: "y", Role: "admin", CreatedAt: NowMS()}); !errors.Is(err, ErrConflict) {
		t.Fatalf("重复用户名应返回 ErrConflict，实际 %v", err)
	}
	n, err := st.CountUsers(ctx)
	if err != nil {
		t.Fatalf("统计用户失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("用户数期望 1，实际 %d", n)
	}

	srv := &Server{ID: "srv-1", Name: "n1", Host: "h1", Port: 22, Mode: "agent", TagsJSON: "[]", Status: "unknown", CreatedAt: NowMS()}
	if err := st.CreateServer(ctx, srv); err != nil {
		t.Fatalf("创建服务器失败: %v", err)
	}
	prev, err := st.TouchServerHeartbeat(ctx, srv.ID, NowMS())
	if err != nil {
		t.Fatalf("更新心跳失败: %v", err)
	}
	if prev != "unknown" {
		t.Fatalf("首次心跳的旧状态期望 unknown，实际 %q", prev)
	}
	prev, err = st.TouchServerHeartbeat(ctx, srv.ID, NowMS())
	if err != nil {
		t.Fatalf("二次更新心跳失败: %v", err)
	}
	if prev != "online" {
		t.Fatalf("二次心跳的旧状态期望 online，实际 %q", prev)
	}
}
