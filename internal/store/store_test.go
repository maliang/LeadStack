package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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

// ------------------------------------------------ 冲突文案（409 精确原因） ------------------------------------------------

// TestConflictMessagesNameTheReason 锁定「…已存在」措辞：409 的 message 必须是可读原因，
// 而不是「创建 X: 记录冲突」这类过程描述（API 层 conflictDetail 依赖此约定，见 SPEC A17/A18）。
func TestConflictMessagesNameTheReason(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// 分组：重名创建 / 改名撞车
	g := &ServerGroup{ID: "g1", Name: "prod-group", Description: "生产", CreatedAt: NowMS(), UpdatedAt: NowMS()}
	if err := st.CreateServerGroup(ctx, g); err != nil {
		t.Fatalf("创建分组失败: %v", err)
	}
	err := st.CreateServerGroup(ctx, &ServerGroup{ID: "g2", Name: "prod-group", CreatedAt: NowMS(), UpdatedAt: NowMS()})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("重名分组期望 ErrConflict，实际 %v", err)
	}
	if want := "分组 prod-group 已存在"; !strings.Contains(err.Error(), want) {
		t.Fatalf("冲突文案应含 %q，实际 %q", want, err.Error())
	}
	g2 := &ServerGroup{ID: "g2", Name: "stage-group", CreatedAt: NowMS(), UpdatedAt: NowMS()}
	if err := st.CreateServerGroup(ctx, g2); err != nil {
		t.Fatalf("创建第二个分组失败: %v", err)
	}
	dup := "prod-group"
	if _, err := st.UpdateServerGroup(ctx, g2.ID, GroupUpdate{Name: &dup}, NowMS()); !errors.Is(err, ErrConflict) {
		t.Fatalf("改名撞车期望 ErrConflict，实际 %v", err)
	} else if !strings.Contains(err.Error(), "已被占用") {
		t.Fatalf("改名冲突文案应含「已被占用」，实际 %q", err.Error())
	}

	// 用户 / 凭证 / 应用 / 角色 的重名文案（这几张表有真实唯一约束）
	if err := st.CreateUser(ctx, &User{ID: "u1", Username: "admin", PasswordHash: "x", Role: "admin", CreatedAt: NowMS()}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := st.CreateUser(ctx, &User{ID: "u2", Username: "admin", PasswordHash: "y", Role: "admin", CreatedAt: NowMS()}); !errors.Is(err, ErrConflict) {
		t.Fatalf("重名用户期望 ErrConflict，实际 %v", err)
	} else if !strings.Contains(err.Error(), "用户 admin 已存在") {
		t.Fatalf("用户冲突文案不正确: %q", err.Error())
	}

	cred := &Credential{ID: "c1", Name: "deploy-key", Kind: "password", Username: "root", SecretEnc: []byte{1}, CreatedAt: NowMS()}
	if err := st.CreateCredential(ctx, cred); err != nil {
		t.Fatalf("创建凭证失败: %v", err)
	}
	if err := st.CreateCredential(ctx, &Credential{ID: "c2", Name: "deploy-key", Kind: "password", Username: "root", SecretEnc: []byte{2}, CreatedAt: NowMS()}); !errors.Is(err, ErrConflict) {
		t.Fatalf("重名凭证期望 ErrConflict，实际 %v", err)
	} else if !strings.Contains(err.Error(), "凭证 deploy-key 已存在") {
		t.Fatalf("凭证冲突文案不正确: %q", err.Error())
	}

	// 凭证被服务器引用 → 删除冲突必须带引用数量（A17）
	seedServer(t, st, "srv-cred", "n1", ModeAgent, "", nil)
	if _, err := st.UpdateServerFields(ctx, "srv-cred", ServerUpdate{CredentialID: &cred.ID}); err != nil {
		t.Fatalf("绑定凭证失败: %v", err)
	}
	if err := st.DeleteCredential(ctx, cred.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("删除被引用凭证期望 ErrConflict，实际 %v", err)
	} else if !strings.Contains(err.Error(), "仍被 1 台服务器引用") {
		t.Fatalf("凭证删除冲突文案应含引用数量: %q", err.Error())
	}

	app := &App{ID: "a1", Name: "官网", Slug: "site", CreatedAt: NowMS()}
	if err := st.CreateApp(ctx, app); err != nil {
		t.Fatalf("创建应用失败: %v", err)
	}
	if err := st.CreateApp(ctx, &App{ID: "a2", Name: "官网2", Slug: "site", CreatedAt: NowMS()}); !errors.Is(err, ErrConflict) {
		t.Fatalf("重复 slug 期望 ErrConflict，实际 %v", err)
	} else if !strings.Contains(err.Error(), "应用 site 已存在") {
		t.Fatalf("应用冲突文案不正确: %q", err.Error())
	}

	role := &Role{Key: "ops", Name: "运维", ScopeMode: "all", CreatedAt: NowMS(), UpdatedAt: NowMS()}
	if err := st.CreateRole(ctx, role); err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	if err := st.CreateRole(ctx, &Role{Key: "ops2", Name: "运维", ScopeMode: "all", CreatedAt: NowMS(), UpdatedAt: NowMS()}); !errors.Is(err, ErrConflict) {
		t.Fatalf("重名角色期望 ErrConflict，实际 %v", err)
	} else if !strings.Contains(err.Error(), "角色 运维 已存在") {
		t.Fatalf("角色冲突文案不正确: %q", err.Error())
	}
}

// seedReleaseWithStatuses 创建一条发布，并为每台服务器写入指定状态的 run。
func seedReleaseWithStatuses(t *testing.T, st *Store, releaseID string, statuses ...string) {
	t.Helper()
	ctx := context.Background()
	rel := &Release{
		ID:               releaseID,
		AppID:            "app-" + releaseID,
		TemplateID:       "tpl-" + releaseID,
		Version:          "1.0.0",
		TemplateSnapshot: "{}",
		Status:           ReleaseStatusPending,
		CreatedAt:        NowMS(),
		Concurrency:      4,
	}
	runs := make([]ReleaseRun, 0, len(statuses))
	for i, status := range statuses {
		runs = append(runs, ReleaseRun{
			ID:        fmt.Sprintf("%s-run-%d", releaseID, i),
			ReleaseID: releaseID,
			ServerID:  fmt.Sprintf("%s-srv-%d", releaseID, i),
			Status:    status,
		})
	}
	if err := st.CreateRelease(ctx, rel, runs); err != nil {
		t.Fatalf("创建发布 %s 失败: %v", releaseID, err)
	}
}

// TestSummarizeReleaseStatusTerminalSemantics 锁定发布终态的汇聚规则（契约第 23 节 / A26）。
// 关键语义：用户主动中止（没有机器失败）必须收敛为 canceled，而不是 failed——
// 部分成功 + 部分取消（灰度发完第一批后中止）同样属于「中止」，不是「失败」。
func TestSummarizeReleaseStatusTerminalSemantics(t *testing.T) {
	cases := []struct {
		name     string
		statuses []string
		want     string
	}{
		{"全成功", []string{ReleaseStatusSuccess, ReleaseStatusSuccess}, ReleaseStatusSuccess},
		{"全失败", []string{ReleaseStatusFailed, ReleaseStatusFailed}, ReleaseStatusFailed},
		{"全取消", []string{ReleaseStatusCanceled, ReleaseStatusCanceled}, ReleaseStatusCanceled},
		{"失败+取消仍是失败", []string{ReleaseStatusFailed, ReleaseStatusCanceled}, ReleaseStatusFailed},
		// A26 回归：中止发布时已成功的一批保持 success、未开始的标记 canceled，
		// 之前会被 default 汇总成 failed，页面上显示「失败」，与用户点的「中止」矛盾。
		{"成功+取消应收敛为取消（部分中止）", []string{ReleaseStatusSuccess, ReleaseStatusCanceled}, ReleaseStatusCanceled},
		{"成功+失败仍是失败", []string{ReleaseStatusSuccess, ReleaseStatusFailed}, ReleaseStatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			releaseID := "rel-" + strings.ReplaceAll(tc.name, "+", "-")
			seedReleaseWithStatuses(t, st, releaseID, tc.statuses...)
			got, err := st.SummarizeReleaseStatus(context.Background(), releaseID)
			if err != nil {
				t.Fatalf("汇总发布状态失败: %v", err)
			}
			if got != tc.want {
				t.Fatalf("runs=%v 汇总状态期望 %q，实际 %q", tc.statuses, tc.want, got)
			}
		})
	}
}

// TestSummarizeReleaseStatusInFlight 确认未收敛的组合仍然优先返回 running/pending，
// 不会被「有取消即取消」的新规则提前判成终态。
func TestSummarizeReleaseStatusInFlight(t *testing.T) {
	cases := []struct {
		name     string
		statuses []string
		want     string
	}{
		{"有 running 即 running", []string{ReleaseStatusSuccess, ReleaseStatusRunning}, ReleaseStatusRunning},
		{"无 running 但有 pending 即 pending", []string{ReleaseStatusSuccess, ReleaseStatusCanceled, ReleaseStatusPending}, ReleaseStatusPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			releaseID := "rel-flight-" + strings.ReplaceAll(tc.name, " ", "-")
			seedReleaseWithStatuses(t, st, releaseID, tc.statuses...)
			got, err := st.SummarizeReleaseStatus(context.Background(), releaseID)
			if err != nil {
				t.Fatalf("汇总发布状态失败: %v", err)
			}
			if got != tc.want {
				t.Fatalf("runs=%v 汇总状态期望 %q，实际 %q", tc.statuses, tc.want, got)
			}
		})
	}
}

// seedReleaseOnServer 写入一条「目标服务器固定」的发布（单 run），created_at 显式指定，
// 便于构造「范围内发布落在全局窗口之外」的排序。
func seedReleaseOnServer(t *testing.T, st *Store, releaseID, serverID string, createdAt int64) {
	t.Helper()
	ctx := context.Background()
	rel := &Release{
		ID:               releaseID,
		AppID:            "app-" + releaseID,
		TemplateID:       "tpl-" + releaseID,
		Version:          "1.0.0",
		TemplateSnapshot: "{}",
		Status:           ReleaseStatusPending,
		CreatedAt:        createdAt,
		Concurrency:      4,
	}
	runs := []ReleaseRun{{
		ID:        releaseID + "-run",
		ReleaseID: releaseID,
		ServerID:  serverID,
		Status:    ReleaseStatusPending,
	}}
	if err := st.CreateRelease(ctx, rel, runs); err != nil {
		t.Fatalf("创建发布 %s 失败: %v", releaseID, err)
	}
}

// TestListReleasesFiltersByServerBeforeLimit 固化 F2（t23 reviewer 报的 medium 缺陷）：
// 范围过滤必须发生在 LIMIT 之前。
//
// 缺陷形态：handler 先取「全局最新 5 条」再按 release_runs 交集过滤，组范围运维角色的
// 发布只要不在全局最新窗口内就会被截掉，前端显示「你所授权分组与服务器范围内暂无发布
// 记录」——发布其实存在，用户只能去怀疑权限配置。
func TestListReleasesFiltersByServerBeforeLimit(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	base := NowMS()

	// 6 条范围外发布更新（占满全局最新 5 条窗口），范围内那条最早（全局第 7）。
	for i := 0; i < 6; i++ {
		seedReleaseOnServer(t, st, fmt.Sprintf("rel-other-%d", i), "srv-other", base-int64(6-i)*1000)
	}
	seedReleaseOnServer(t, st, "rel-scope-old", "srv-scope", base-7*1000)

	// 不传 ServerIDs：行为与改动前完全一致——只返回全局最新 5 条，范围内那条落在窗口外。
	global, err := st.ListReleases(ctx, ReleaseFilter{Limit: 5})
	if err != nil {
		t.Fatalf("查询全局发布列表失败: %v", err)
	}
	if len(global) != 5 {
		t.Fatalf("全局最新 5 条期望 5 条，实际 %d（%v）", len(global), releaseIDs(global))
	}
	for _, rel := range global {
		if rel.ID == "rel-scope-old" {
			t.Fatalf("全局窗口不应包含第 7 条旧发布，实际 %v", releaseIDs(global))
		}
	}

	// 传 ServerIDs：SQL 先按范围过滤再 LIMIT，旧发布能被取到（F2 的复现点）。
	scoped, err := st.ListReleases(ctx, ReleaseFilter{Limit: 5, ServerIDs: []string{"srv-scope"}})
	if err != nil {
		t.Fatalf("按服务器范围查询发布列表失败: %v", err)
	}
	if len(scoped) != 1 || scoped[0].ID != "rel-scope-old" {
		t.Fatalf("范围内应只返回 rel-scope-old，实际 %v", releaseIDs(scoped))
	}

	// 范围内新增一条更新的发布：只返回范围内的两条，且仍按创建时间倒序。
	seedReleaseOnServer(t, st, "rel-scope-new", "srv-scope", base)
	scoped, err = st.ListReleases(ctx, ReleaseFilter{Limit: 5, ServerIDs: []string{"srv-scope"}})
	if err != nil {
		t.Fatalf("再次按服务器范围查询失败: %v", err)
	}
	if len(scoped) != 2 || scoped[0].ID != "rel-scope-new" || scoped[1].ID != "rel-scope-old" {
		t.Fatalf("范围内期望 [rel-scope-new rel-scope-old]，实际 %v", releaseIDs(scoped))
	}

	// 范围外服务器：一条都取不到（不是「不过滤」）。
	none, err := st.ListReleases(ctx, ReleaseFilter{Limit: 5, ServerIDs: []string{"srv-nobody"}})
	if err != nil {
		t.Fatalf("按范围外服务器查询失败: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("范围外服务器不应有发布，实际 %v", releaseIDs(none))
	}
}

// releaseIDs 返回发布 ID 切片，便于断言失败时打印。
func releaseIDs(releases []Release) []string {
	out := make([]string, 0, len(releases))
	for _, rel := range releases {
		out = append(out, rel.ID)
	}
	return out
}

// seedAlertOnServer 写入一条告警（started_at 显式指定，便于构造排序）；
// serverID 传空串表示规则级告警（alerts.server_id 为 NULL）。
func seedAlertOnServer(t *testing.T, st *Store, id, serverID string, startedAt int64) {
	t.Helper()
	var server *string
	if serverID != "" {
		s := serverID
		server = &s
	}
	if err := st.CreateAlert(context.Background(), Alert{
		ID:        id,
		RuleID:    "rule-scope",
		DedupKey:  "dedup-" + id,
		ServerID:  server,
		Status:    AlertStatusFiring,
		Severity:  "critical",
		Title:     "告警 " + id,
		StartedAt: startedAt,
		UpdatedAt: startedAt,
	}); err != nil {
		t.Fatalf("创建告警 %s 失败: %v", id, err)
	}
}

// TestListAlertsFiltersByServerBeforeLimit 固化「告警列表先 LIMIT 后按范围过滤」的缺陷：
// 范围过滤必须发生在 LIMIT 之前，否则范围用户会看到「暂无告警」——真有故障却告诉
// 用户没事，是误报代价最高的一种。
//
// 同时锁定契约 §53.3：规则级告警（server_id 为 NULL）不在范围过滤结果里，只有
// ServerIDs 为 nil（全局角色）时才可见。
func TestListAlertsFiltersByServerBeforeLimit(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	base := NowMS()

	// 6 条范围外告警更新（占满全局最新 5 条窗口），范围内那条排全局第 7，
	// 另加一条最早的规则级告警（server_id = NULL，全局第 8）。
	for i := 0; i < 6; i++ {
		seedAlertOnServer(t, st, fmt.Sprintf("al-out-%d", i), "srv-other", base-int64(6-i)*1000)
	}
	seedAlertOnServer(t, st, "al-scope-old", "srv-scope", base-7*1000)
	seedAlertOnServer(t, st, "al-rule-level", "", base-8*1000)

	// ServerIDs = nil：与改动前逐条一致（有序 + 只取最新 5 条）。
	global, err := st.ListAlerts(ctx, AlertFilter{Limit: 5})
	if err != nil {
		t.Fatalf("查询全局告警列表失败: %v", err)
	}
	wantGlobal := []string{"al-out-5", "al-out-4", "al-out-3", "al-out-2", "al-out-1"}
	if got := alertIDs(global); strings.Join(got, ",") != strings.Join(wantGlobal, ",") {
		t.Fatalf("全局最新 5 条期望 %v，实际 %v", wantGlobal, got)
	}

	// 传 ServerIDs：SQL 先按范围过滤再 LIMIT，旧告警能被取到（缺陷的复现点）。
	scoped, err := st.ListAlerts(ctx, AlertFilter{Limit: 5, ServerIDs: []string{"srv-scope"}})
	if err != nil {
		t.Fatalf("按服务器范围查询告警列表失败: %v", err)
	}
	if got := alertIDs(scoped); len(got) != 1 || got[0] != "al-scope-old" {
		t.Fatalf("范围内应只返回 al-scope-old，实际 %v", got)
	}

	// 规则级告警（server_id 为 NULL）不因范围过滤被放开：全量查 Nor 范围查都取不到它。
	all, err := st.ListAlerts(ctx, AlertFilter{Limit: 100, ServerIDs: []string{"srv-scope", "srv-other"}})
	if err != nil {
		t.Fatalf("按范围全量查询失败: %v", err)
	}
	for _, a := range all {
		if a.ID == "al-rule-level" {
			t.Fatalf("规则级告警不应出现在范围过滤结果里（契约 §53.3）: %v", alertIDs(all))
		}
		if a.ServerID == nil {
			t.Fatalf("范围过滤结果不应包含 server_id 为 NULL 的告警: %v", alertIDs(all))
		}
	}
	// 反证：全局角色（ServerIDs 为 nil）能看到规则级告警，说明它只是被范围条件挡住。
	globalAll, err := st.ListAlerts(ctx, AlertFilter{Limit: 100})
	if err != nil {
		t.Fatalf("查询全局全量告警失败: %v", err)
	}
	foundRule := false
	for _, a := range globalAll {
		if a.ID == "al-rule-level" {
			foundRule = true
		}
	}
	if !foundRule {
		t.Fatalf("全局角色应能看到规则级告警（server_id 为 NULL），实际 %v", alertIDs(globalAll))
	}

	// 范围内有多条时仍按 started_at 倒序，且只含范围内的。
	seedAlertOnServer(t, st, "al-scope-new", "srv-scope", base)
	scoped, err = st.ListAlerts(ctx, AlertFilter{Limit: 5, ServerIDs: []string{"srv-scope"}})
	if err != nil {
		t.Fatalf("再次按范围查询失败: %v", err)
	}
	if got := alertIDs(scoped); len(got) != 2 || got[0] != "al-scope-new" || got[1] != "al-scope-old" {
		t.Fatalf("范围内期望 [al-scope-new al-scope-old]，实际 %v", got)
	}

	// 范围外服务器：一条都取不到（不是「不过滤」）。
	none, err := st.ListAlerts(ctx, AlertFilter{Limit: 5, ServerIDs: []string{"srv-nobody"}})
	if err != nil {
		t.Fatalf("按范围外服务器查询失败: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("范围外服务器不应有告警，实际 %v", alertIDs(none))
	}
}

// alertIDs 返回告警 ID 切片，便于断言失败时打印。
func alertIDs(alerts []Alert) []string {
	out := make([]string, 0, len(alerts))
	for _, a := range alerts {
		out = append(out, a.ID)
	}
	return out
}
