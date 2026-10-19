package collector

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/credential"
	"github.com/maliang/leadstack/internal/devssh"
	"github.com/maliang/leadstack/internal/store"
	"github.com/maliang/leadstack/pkg/protocol"
)

// testPassword 是 devssh 固定密码。
const testPassword = "devpass"

// startTestSSH 起一个测试专用 SSH 端点（真实握手 + 本机真实执行命令）。
func startTestSSH(t *testing.T) *devssh.Server {
	t.Helper()
	srv, err := devssh.Start(devssh.Config{
		Addr:     "127.0.0.1:0",
		Username: "devuser",
		Password: testPassword,
	})
	if err != nil {
		t.Fatalf("启动 devssh 失败: %v", err)
	}
	t.Cleanup(func() {
		if cerr := srv.Close(); cerr != nil {
			t.Logf("关闭 devssh 失败: %v", cerr)
		}
	})
	return srv
}

// splitHostPort 拆分 host:port。
func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("解析地址 %s 失败: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("解析端口 %s 失败: %v", portStr, err)
	}
	return host, port
}

// newTestEnv 构造采集器测试环境（store + cipher + collector）。
func newTestEnv(t *testing.T, knownHosts string) (*Collector, *store.Store, *credential.Cipher) {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "collector.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Logf("关闭测试数据库失败: %v", cerr)
		}
	})
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	cipher, err := credential.NewCipher(bytes.Repeat([]byte("m"), credential.MasterKeySize))
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	c, err := New(Options{
		Store:          st,
		Cipher:         cipher,
		Logger:         logger,
		Interval:       time.Hour, // 测试里手动触发
		Timeout:        20 * time.Second,
		Concurrency:    4,
		KnownHostsPath: knownHosts,
	})
	if err != nil {
		t.Fatalf("构造采集器失败: %v", err)
	}
	return c, st, cipher
}

// createPasswordCredential 写入一条密码凭证。
func createPasswordCredential(t *testing.T, st *store.Store, cipher *credential.Cipher, username, password string) string {
	t.Helper()
	plain, err := credential.Marshal(credential.Secret{
		Kind:     credential.KindPassword,
		Username: username,
		Password: password,
	})
	if err != nil {
		t.Fatalf("序列化凭证失败: %v", err)
	}
	enc, err := cipher.Encrypt(plain)
	if err != nil {
		t.Fatalf("加密凭证失败: %v", err)
	}
	id := uuid.NewString()
	if err := st.CreateCredential(context.Background(), &store.Credential{
		ID: id, Name: "cred-" + id[:8], Kind: string(credential.KindPassword),
		Username: username, SecretEnc: enc, CreatedAt: store.NowMS(),
	}); err != nil {
		t.Fatalf("创建凭证失败: %v", err)
	}
	return id
}

// createSSHServer 写入一台 mode=ssh 服务器。
func createSSHServer(t *testing.T, st *store.Store, credentialID, host string, port int) *store.Server {
	t.Helper()
	srv := &store.Server{
		ID: uuid.NewString(), Name: "ssh-" + host, Host: host, Port: port,
		Mode: store.ModeSSH, TagsJSON: "[]", Status: protocol.StatusUnknown,
		CreatedAt: store.NowMS(), CredentialID: &credentialID,
		HostKeyPolicy: store.HostKeyPolicyAcceptNew,
	}
	if err := st.CreateServer(context.Background(), srv); err != nil {
		t.Fatalf("创建服务器失败: %v", err)
	}
	return srv
}

// TestAgentlessCollectorAgainstDevSSH 用真实 SSH 端点验证采集全链路。
func TestAgentlessCollectorAgainstDevSSH(t *testing.T) {
	sshSrv := startTestSSH(t)
	host, port := splitHostPort(t, sshSrv.Addr())

	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	c, st, cipher := newTestEnv(t, knownHosts)

	credID := createPasswordCredential(t, st, cipher, "devuser", testPassword)
	srv := createSSHServer(t, st, credID, host, port)

	ctx := context.Background()

	// 1) 连通性测试
	result, err := c.TestConnection(ctx, srv.ID)
	if err != nil {
		t.Fatalf("连通性测试返回错误: %v", err)
	}
	if !result.OK {
		t.Fatalf("连通性测试期望成功，实际: %+v", result)
	}
	if result.SSHBanner == "" {
		t.Fatal("SSH banner 不应为空")
	}
	if result.HostKeyFingerprint == "" {
		t.Fatal("主机密钥指纹不应为空")
	}
	if result.LatencyMS < 0 {
		t.Fatalf("延迟不应为负: %d", result.LatencyMS)
	}

	// accept-new 应把主机密钥落到 known_hosts
	if _, err := filepath.Glob(knownHosts); err != nil {
		t.Fatalf("检查 known_hosts 失败: %v", err)
	}

	// 2) 主机信息采集
	info, err := c.RefreshHostInfo(ctx, srv.ID)
	if err != nil {
		t.Fatalf("刷新主机信息失败: %v", err)
	}
	if info.Hostname == "" || info.OS == "" {
		t.Fatalf("主机信息缺少关键字段: %+v", info)
	}
	snap, err := st.GetServerHostInfo(ctx, srv.ID)
	if err != nil {
		t.Fatalf("主机信息未落库: %v", err)
	}
	if snap.CollectedAt == 0 || snap.InfoJSON == "" {
		t.Fatalf("主机信息快照不完整: %+v", snap)
	}

	// 3) 采集一轮指标
	c.CollectOnce(ctx)
	saved, err := st.GetServer(ctx, srv.ID)
	if err != nil {
		t.Fatalf("读取服务器失败: %v", err)
	}
	if saved.Status != protocol.StatusOnline {
		t.Fatalf("采集成功后状态期望 online，实际 %q（last_error=%v）", saved.Status, saved.LastError)
	}
	if saved.LastError != nil {
		t.Fatalf("采集成功后 last_error 应为空，实际 %q", *saved.LastError)
	}
	metrics, err := st.LatestMetrics(ctx, []string{srv.ID})
	if err != nil {
		t.Fatalf("查询最新指标失败: %v", err)
	}
	if len(metrics[srv.ID]) == 0 {
		t.Fatal("采集一轮后应至少落库一个指标")
	}
}

// TestAgentlessCollectorMarksOfflineAfterFailures 验证连续 3 次失败置 offline 并写 last_error。
func TestAgentlessCollectorMarksOfflineAfterFailures(t *testing.T) {
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	c, st, cipher := newTestEnv(t, knownHosts)

	// 用一个必然连不上的端口（先占后放，保证被拒绝）。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占位监听失败: %v", err)
	}
	host, port := splitHostPort(t, listener.Addr().String())
	if err := listener.Close(); err != nil {
		t.Fatalf("关闭占位监听失败: %v", err)
	}

	credID := createPasswordCredential(t, st, cipher, "devuser", testPassword)
	srv := createSSHServer(t, st, credID, host, port)

	ctx := context.Background()
	for i := 1; i <= offlineAfterFailures; i++ {
		c.CollectOnce(ctx)
		saved, err := st.GetServer(ctx, srv.ID)
		if err != nil {
			t.Fatalf("读取服务器失败: %v", err)
		}
		if saved.LastError == nil || *saved.LastError == "" {
			t.Fatalf("第 %d 次失败后 last_error 应被写入", i)
		}
		if i < offlineAfterFailures && saved.Status == protocol.StatusOffline {
			t.Fatalf("第 %d 次失败就置为 offline，期望第 %d 次", i, offlineAfterFailures)
		}
	}

	saved, err := st.GetServer(ctx, srv.ID)
	if err != nil {
		t.Fatalf("读取服务器失败: %v", err)
	}
	if saved.Status != protocol.StatusOffline {
		t.Fatalf("连续 %d 次失败后期望 offline，实际 %q", offlineAfterFailures, saved.Status)
	}

	// 事件里应有 offline 记录
	events, err := st.ListEvents(ctx, srv.ID, 10)
	if err != nil {
		t.Fatalf("查询事件失败: %v", err)
	}
	found := false
	for _, ev := range events {
		if ev.Kind == store.EventKindOffline {
			found = true
		}
	}
	if !found {
		t.Fatalf("期望存在 offline 事件，实际 %+v", events)
	}
}

// TestMissingCredentialOnlyRecordsError 未绑定凭证只记录原因，不误判离线。
func TestMissingCredentialOnlyRecordsError(t *testing.T) {
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	c, st, _ := newTestEnv(t, knownHosts)

	srv := &store.Server{
		ID: uuid.NewString(), Name: "no-cred", Host: "127.0.0.1", Port: 22,
		Mode: store.ModeSSH, TagsJSON: "[]", Status: protocol.StatusUnknown,
		CreatedAt: store.NowMS(), HostKeyPolicy: store.HostKeyPolicyAcceptNew,
	}
	if err := st.CreateServer(context.Background(), srv); err != nil {
		t.Fatalf("创建服务器失败: %v", err)
	}

	for i := 0; i < offlineAfterFailures+1; i++ {
		c.CollectOnce(context.Background())
	}
	saved, err := st.GetServer(context.Background(), srv.ID)
	if err != nil {
		t.Fatalf("读取服务器失败: %v", err)
	}
	if saved.Status == protocol.StatusOffline {
		t.Fatal("未绑定凭证不应被判为 offline")
	}
	if saved.LastError == nil {
		t.Fatal("应记录 last_error 说明原因")
	}
}

// TestCollectorClearsLastErrorOnRecovery 覆盖 task-16：SSH 采集恢复成功后清空 last_error。
func TestCollectorClearsLastErrorOnRecovery(t *testing.T) {
	sshSrv := startTestSSH(t)
	host, port := splitHostPort(t, sshSrv.Addr())

	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	c, st, cipher := newTestEnv(t, knownHosts)
	ctx := context.Background()

	credID := createPasswordCredential(t, st, cipher, "devuser", testPassword)

	// 先用一个必然连不上的端口，制造失败与 last_error。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占位监听失败: %v", err)
	}
	_, deadPort := splitHostPort(t, listener.Addr().String())
	if err := listener.Close(); err != nil {
		t.Fatalf("关闭占位监听失败: %v", err)
	}
	srv := createSSHServer(t, st, credID, host, deadPort)

	for i := 0; i < offlineAfterFailures; i++ {
		c.CollectOnce(ctx)
	}
	failed, err := st.GetServer(ctx, srv.ID)
	if err != nil {
		t.Fatalf("读取服务器失败: %v", err)
	}
	if failed.LastError == nil || *failed.LastError == "" {
		t.Fatalf("连续失败后应写入 last_error，实际 %v", failed.LastError)
	}
	if failed.Status != protocol.StatusOffline {
		t.Fatalf("连续失败后状态期望 offline，实际 %q", failed.Status)
	}

	// 把端口改成真实 devssh 端口：下一轮采集应成功并清空 last_error。
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE servers SET port = ? WHERE id = ?`, port, srv.ID); err != nil {
		t.Fatalf("更新服务器端口失败: %v", err)
	}

	c.CollectOnce(ctx)
	recovered, err := st.GetServer(ctx, srv.ID)
	if err != nil {
		t.Fatalf("读取服务器失败: %v", err)
	}
	if recovered.Status != protocol.StatusOnline {
		t.Fatalf("恢复后状态期望 online，实际 %q（last_error=%v）", recovered.Status, recovered.LastError)
	}
	if recovered.LastError != nil {
		t.Fatalf("恢复成功后 last_error 应被清空，实际 %q", *recovered.LastError)
	}
}
