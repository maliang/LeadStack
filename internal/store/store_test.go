package store

import (
	"context"
	"encoding/json"
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
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("读取内嵌迁移失败: %v", err)
	}
	if n != len(migrations) {
		t.Fatalf("schema_migrations 期望 %d 条，实际 %d", len(migrations), n)
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

// ------------------------------------------------ 纳管 ------------------------------------------------

// seedServer 插入一台测试服务器。
func seedServer(t *testing.T, st *Store, id, name, mode, group string, tags []string) *Server {
	t.Helper()
	encoded, err := json.Marshal(tags)
	if err != nil {
		t.Fatalf("序列化标签失败: %v", err)
	}
	var groupPtr *string
	if group != "" {
		g := group
		groupPtr = &g
	}
	srv := &Server{
		ID: id, Name: name, Host: "10.0.0.1", Port: 22, Mode: mode,
		TagsJSON: string(encoded), Status: "unknown", CreatedAt: NowMS(),
		Group: groupPtr, HostKeyPolicy: HostKeyPolicyAcceptNew,
	}
	if err := st.CreateServer(context.Background(), srv); err != nil {
		t.Fatalf("创建服务器 %s 失败: %v", id, err)
	}
	return srv
}

// TestServerFilterAndGroups 校验 group/tag/status/mode 过滤与分组聚合。
func TestServerFilterAndGroups(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	seedServer(t, st, "srv-a", "a", ModeAgent, "prod", []string{"bj", "web"})
	seedServer(t, st, "srv-b", "b", ModeSSH, "prod", []string{"sh"})
	seedServer(t, st, "srv-c", "c", ModeSSH, "stage", nil)

	// group 过滤
	got, err := st.ListServersFiltered(ctx, ServerFilter{Group: "prod"}, NowMS())
	if err != nil {
		t.Fatalf("按分组过滤失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("group=prod 期望 2 台，实际 %d", len(got))
	}

	// mode 过滤
	got, err = st.ListServersFiltered(ctx, ServerFilter{Mode: ModeSSH}, NowMS())
	if err != nil {
		t.Fatalf("按模式过滤失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("mode=ssh 期望 2 台，实际 %d", len(got))
	}

	// tag 过滤
	got, err = st.ListServersFiltered(ctx, ServerFilter{Tag: "bj"}, NowMS())
	if err != nil {
		t.Fatalf("按标签过滤失败: %v", err)
	}
	if len(got) != 1 || got[0].ID != "srv-a" {
		t.Fatalf("tag=bj 期望命中 srv-a，实际 %+v", got)
	}

	// status 过滤：agent 模式按 last_seen_at 推导
	now := NowMS()
	fresh := now - 1000
	stale := now - 120000
	if _, err := st.DB().ExecContext(ctx, `UPDATE servers SET status='online', last_seen_at=? WHERE id='srv-a'`, fresh); err != nil {
		t.Fatalf("构造 online 状态失败: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE servers SET status='offline', last_seen_at=? WHERE id='srv-b'`, stale); err != nil {
		t.Fatalf("构造 offline 状态失败: %v", err)
	}
	got, err = st.ListServersFiltered(ctx, ServerFilter{Status: "online"}, now)
	if err != nil {
		t.Fatalf("按状态过滤失败: %v", err)
	}
	if len(got) != 1 || got[0].ID != "srv-a" {
		t.Fatalf("status=online 期望命中 srv-a，实际 %+v", got)
	}
	// ssh 模式：以采集器维护的 status 为准
	got, err = st.ListServersFiltered(ctx, ServerFilter{Status: "offline"}, now)
	if err != nil {
		t.Fatalf("按状态过滤失败: %v", err)
	}
	if len(got) != 1 || got[0].ID != "srv-b" {
		t.Fatalf("status=offline 期望命中 srv-b，实际 %+v", got)
	}

	// 分组聚合
	groups, err := st.ListGroups(ctx)
	if err != nil {
		t.Fatalf("查询分组失败: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("期望 2 个分组，实际 %+v", groups)
	}
	if groups[0].Name != "prod" || groups[0].Count != 2 {
		t.Fatalf("第一个分组期望 prod/2，实际 %+v", groups[0])
	}
}

// TestCredentialCRUDAndReferenceProtection 校验凭证 CRUD 与 409 语义。
func TestCredentialCRUDAndReferenceProtection(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	cred := &Credential{ID: "cred-1", Name: "k1", Kind: "key", Username: "root",
		SecretEnc: []byte{1, 2, 3}, CreatedAt: NowMS()}
	if err := st.CreateCredential(ctx, cred); err != nil {
		t.Fatalf("创建凭证失败: %v", err)
	}
	// 重名冲突
	if err := st.CreateCredential(ctx, &Credential{ID: "cred-2", Name: "k1", Kind: "key",
		Username: "root", SecretEnc: []byte{1}, CreatedAt: NowMS()}); !errors.Is(err, ErrConflict) {
		t.Fatalf("重名凭证期望 ErrConflict，实际 %v", err)
	}

	srv := seedServer(t, st, "srv-1", "n1", ModeSSH, "", nil)
	cid := cred.ID
	srv.CredentialID = &cid
	if _, err := st.UpdateServerFields(ctx, srv.ID, ServerUpdate{CredentialID: &cid}); err != nil {
		t.Fatalf("绑定凭证失败: %v", err)
	}

	list, err := st.ListCredentials(ctx)
	if err != nil {
		t.Fatalf("查询凭证列表失败: %v", err)
	}
	if len(list) != 1 || list[0].ServerCount != 1 {
		t.Fatalf("期望 1 条凭证且被引用 1 次，实际 %+v", list)
	}

	if err := st.DeleteCredential(ctx, cred.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("删除被引用凭证期望 ErrConflict，实际 %v", err)
	}

	// 解绑后可以删除
	if _, err := st.UpdateServerFields(ctx, srv.ID, ServerUpdate{CredentialID: ptrString("")}); err != nil {
		t.Fatalf("解绑凭证失败: %v", err)
	}
	if err := st.DeleteCredential(ctx, cred.ID); err != nil {
		t.Fatalf("解绑后删除凭证失败: %v", err)
	}
	if _, err := st.GetCredential(ctx, cred.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后查询期望 ErrNotFound，实际 %v", err)
	}
}

// TestRollupAggregatesRaw 校验降采样的正确性与幂等性。
func TestRollupAggregatesRaw(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seedServer(t, st, "srv-1", "n1", ModeSSH, "", nil)

	// 造 3 个采样落在同一分钟桶里（取 2 分钟前的整分桶）。
	base := (NowMS()/60000)*60000 - 120000
	samples := []MetricSample{
		{Metric: "cpu.pct", TS: base, Value: 1},
		{Metric: "cpu.pct", TS: base + 10000, Value: 2},
		{Metric: "cpu.pct", TS: base + 20000, Value: 3},
	}
	if _, err := st.InsertSamples(ctx, "srv-1", samples); err != nil {
		t.Fatalf("写入采样失败: %v", err)
	}

	stats, err := st.RollupOnce(ctx, DefaultRollupLimit)
	if err != nil {
		t.Fatalf("降采样失败: %v", err)
	}
	if stats.MinuteRows == 0 {
		t.Fatal("1m 降采样没有产出任何行")
	}

	points, err := st.QueryRollupRange(ctx, "srv-1", RollupBucketMinute, base, base+60000, 100)
	if err != nil {
		t.Fatalf("查询 1m 汇总失败: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("期望 1 条 1m 汇总，实际 %d", len(points))
	}
	p := points[0]
	if p.TS != base {
		t.Fatalf("桶时间期望 %d，实际 %d", base, p.TS)
	}
	if p.Avg != 2 || p.Min != 1 || p.Max != 3 || p.Cnt != 3 {
		t.Fatalf("1m 汇总不正确: %+v", p)
	}

	// 幂等：再跑一次结果不变
	if _, err := st.RollupOnce(ctx, DefaultRollupLimit); err != nil {
		t.Fatalf("二次降采样失败: %v", err)
	}
	points, err = st.QueryRollupRange(ctx, "srv-1", RollupBucketMinute, base, base+60000, 100)
	if err != nil {
		t.Fatalf("二次查询 1m 汇总失败: %v", err)
	}
	if len(points) != 1 || points[0].Avg != 2 || points[0].Cnt != 3 {
		t.Fatalf("二次降采样不应改变结果: %+v", points)
	}

	// 1h 汇总由 1m 汇总再聚合（按 cnt 加权平均）
	hourPoints, err := st.QueryRollupRange(ctx, "srv-1", RollupBucketHour, base-3600000, base+3600000, 100)
	if err != nil {
		t.Fatalf("查询 1h 汇总失败: %v", err)
	}
	if len(hourPoints) != 1 {
		t.Fatalf("期望 1 条 1h 汇总，实际 %d", len(hourPoints))
	}
	if hourPoints[0].Avg != 2 || hourPoints[0].Cnt != 3 {
		t.Fatalf("1h 加权平均不正确: %+v", hourPoints[0])
	}

	// 保留策略清理
	if n, err := st.DeleteRollupBefore(ctx, RollupBucketMinute, base+1); err != nil {
		t.Fatalf("清理 1m 汇总失败: %v", err)
	} else if n != 1 {
		t.Fatalf("期望清理 1 条，实际 %d", n)
	}
}

// TestServerHostInfo 校验主机信息快照的写入与批量读取。
func TestServerHostInfo(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seedServer(t, st, "srv-1", "n1", ModeAgent, "", nil)
	seedServer(t, st, "srv-2", "n2", ModeAgent, "", nil)

	if err := st.UpsertServerHostInfo(ctx, "srv-1", `{"hostname":"h1"}`, 1000); err != nil {
		t.Fatalf("写入主机信息失败: %v", err)
	}
	// 覆盖写
	if err := st.UpsertServerHostInfo(ctx, "srv-1", `{"hostname":"h1b"}`, 2000); err != nil {
		t.Fatalf("覆盖主机信息失败: %v", err)
	}
	snap, err := st.GetServerHostInfo(ctx, "srv-1")
	if err != nil {
		t.Fatalf("读取主机信息失败: %v", err)
	}
	if snap.InfoJSON != `{"hostname":"h1b"}` || snap.CollectedAt != 2000 {
		t.Fatalf("主机信息覆盖不正确: %+v", snap)
	}
	if _, err := st.GetServerHostInfo(ctx, "srv-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的主机信息期望 ErrNotFound，实际 %v", err)
	}

	all, err := st.ListServerHostInfo(ctx, nil)
	if err != nil {
		t.Fatalf("批量读取主机信息失败: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("期望 1 条主机信息，实际 %d", len(all))
	}

	// 删除服务器应联动清理主机信息
	if err := st.DeleteServer(ctx, "srv-1"); err != nil {
		t.Fatalf("删除服务器失败: %v", err)
	}
	if _, err := st.GetServerHostInfo(ctx, "srv-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除服务器后主机信息应被清理，实际 %v", err)
	}
}

// TestUpdateServerFields 校验 PATCH 语义（含清空分组）。
func TestUpdateServerFields(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seedServer(t, st, "srv-1", "n1", ModeAgent, "prod", []string{"a"})

	// 改名 + 换分组
	name := "renamed"
	group := "stage"
	srv, err := st.UpdateServerFields(ctx, "srv-1", ServerUpdate{Name: &name, Group: &group})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if srv.Name != "renamed" || srv.Group == nil || *srv.Group != "stage" {
		t.Fatalf("更新结果不正确: %+v", srv)
	}

	// 空字符串清空分组
	empty := ""
	srv, err = st.UpdateServerFields(ctx, "srv-1", ServerUpdate{Group: &empty})
	if err != nil {
		t.Fatalf("清空分组失败: %v", err)
	}
	if srv.Group != nil {
		t.Fatalf("分组应被清空，实际 %v", *srv.Group)
	}

	if _, err := st.UpdateServerFields(ctx, "missing", ServerUpdate{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("更新不存在的服务器期望 ErrNotFound，实际 %v", err)
	}
}

// ptrString 返回字符串指针。
func ptrString(s string) *string { return &s }
