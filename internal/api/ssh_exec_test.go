package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/checker"
	"github.com/maliang/leadstack/internal/credential"
	"github.com/maliang/leadstack/internal/devssh"
	"github.com/maliang/leadstack/internal/store"
)

// newSSHExecEnv 准备一套「真实 SSH 端点 + store + credential」的测试环境。
func newSSHExecEnv(t *testing.T) (*SSHExecutor, *store.Store, *credential.Cipher, string, int) {
	t.Helper()
	ctx := context.Background()

	sshSrv, err := devssh.Start(devssh.Config{
		Addr:     "127.0.0.1:0",
		Username: "devuser",
		Password: "devpass",
	})
	if err != nil {
		t.Fatalf("启动 devssh 失败: %v", err)
	}
	t.Cleanup(func() {
		if cerr := sshSrv.Close(); cerr != nil {
			t.Logf("关闭 devssh 失败: %v", cerr)
		}
	})

	host, portStr, err := net.SplitHostPort(sshSrv.Addr())
	if err != nil {
		t.Fatalf("解析 devssh 地址失败: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("解析 devssh 端口失败: %v", err)
	}

	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "sshexec.db"))
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

	cipher, err := credential.NewCipher(bytes.Repeat([]byte("k"), credential.MasterKeySize))
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	exec := NewSSHExecutor(SSHExecutorOptions{
		Store:          st,
		Cipher:         cipher,
		Logger:         logger,
		KnownHostsPath: filepath.Join(dir, "known_hosts"),
		Concurrency:    4,
	})
	if exec == nil {
		t.Fatal("构造 SSH 执行通道失败")
	}
	t.Cleanup(exec.Close)

	return exec, st, cipher, host, port
}

// createSSHServerWithPassword 建一条密码凭证 + 一台 ssh 模式服务器。
func createSSHServerWithPassword(t *testing.T, st *store.Store, cipher *credential.Cipher, host string, port int, name string) string {
	t.Helper()
	ctx := context.Background()

	plain, err := credential.Marshal(credential.Secret{
		Kind: credential.KindPassword, Username: "devuser", Password: "devpass",
	})
	if err != nil {
		t.Fatalf("序列化凭证失败: %v", err)
	}
	enc, err := cipher.Encrypt(plain)
	if err != nil {
		t.Fatalf("加密凭证失败: %v", err)
	}
	credID := uuid.NewString()
	if err := st.CreateCredential(ctx, &store.Credential{
		ID: credID, Name: "cred-" + credID[:8], Kind: string(credential.KindPassword),
		Username: "devuser", SecretEnc: enc, CreatedAt: store.NowMS(),
	}); err != nil {
		t.Fatalf("创建凭证失败: %v", err)
	}

	srv := &store.Server{
		ID: uuid.NewString(), Name: name, Host: host, Port: port,
		Mode: store.ModeSSH, TagsJSON: "[]", Status: "unknown",
		CreatedAt: store.NowMS(), CredentialID: &credID,
		HostKeyPolicy: store.HostKeyPolicyAcceptNew,
	}
	if err := st.CreateServer(ctx, srv); err != nil {
		t.Fatalf("创建服务器失败: %v", err)
	}
	return srv.ID
}

// TestSSHExecutorReusesConnection 覆盖 lead 关注的「连接复用」：多次执行只建一条连接。
func TestSSHExecutorReusesConnection(t *testing.T) {
	exec, st, cipher, host, port := newSSHExecEnv(t)
	serverID := createSSHServerWithPassword(t, st, cipher, host, port, "ssh-exec")
	ctx := context.Background()

	stdout, stderr, code, err := exec.Exec(ctx, serverID, "echo hello-reuse", 10)
	if err != nil {
		t.Fatalf("执行命令失败: %v（stderr=%s）", err, stderr)
	}
	if code != 0 {
		t.Fatalf("退出码期望 0，实际 %d（stderr=%s）", code, stderr)
	}
	if !bytes.Contains([]byte(stdout), []byte("hello-reuse")) {
		t.Fatalf("stdout 未包含预期输出: %q", stdout)
	}
	if n := exec.PoolSize(); n != 1 {
		t.Fatalf("首次执行后连接池期望 1，实际 %d", n)
	}

	// 再执行 3 次：连接应被复用（池大小保持 1）
	for i := 0; i < 3; i++ {
		if _, _, code, err = exec.Exec(ctx, serverID, "echo again", 10); err != nil {
			t.Fatalf("第 %d 次复用执行失败: %v", i+1, err)
		}
		if code != 0 {
			t.Fatalf("第 %d 次退出码期望 0，实际 %d", i+1, code)
		}
	}
	if n := exec.PoolSize(); n != 1 {
		t.Fatalf("复用执行后连接池应仍为 1，实际 %d（说明每次都新建了连接）", n)
	}

	// 非 0 退出不算传输层错误：错误码透传、err 为 nil
	_, _, code, err = exec.Exec(ctx, serverID, "exit 7", 10)
	if err != nil {
		t.Fatalf("非 0 退出不应报错，实际 %v", err)
	}
	if code != 7 {
		t.Fatalf("退出码期望 7，实际 %d", code)
	}

	// stderr 能正确带回
	_, stderr, code, err = exec.Exec(ctx, serverID, "echo err-line 1>&2; exit 3", 10)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if code != 3 || !bytes.Contains([]byte(stderr), []byte("err-line")) {
		t.Fatalf("stderr/exitCode 不正确: code=%d stderr=%q", code, stderr)
	}

	// Close 后连接池清空
	exec.Close()
	if n := exec.PoolSize(); n != 0 {
		t.Fatalf("Close 后连接池应为 0，实际 %d", n)
	}
}

// TestSSHExecutorPerServerPool 校验每台服务器各持一条连接，互不干扰。
func TestSSHExecutorPerServerPool(t *testing.T) {
	exec, st, cipher, host, port := newSSHExecEnv(t)
	first := createSSHServerWithPassword(t, st, cipher, host, port, "ssh-a")
	second := createSSHServerWithPassword(t, st, cipher, host, port, "ssh-b")
	ctx := context.Background()

	if _, _, _, err := exec.Exec(ctx, first, "echo a", 10); err != nil {
		t.Fatalf("服务器 A 执行失败: %v", err)
	}
	if _, _, _, err := exec.Exec(ctx, second, "echo b", 10); err != nil {
		t.Fatalf("服务器 B 执行失败: %v", err)
	}
	if n := exec.PoolSize(); n != 2 {
		t.Fatalf("两台服务器期望 2 条连接，实际 %d", n)
	}
}

// TestSSHExecutorMissingCredential 校验未绑定凭证时的错误语义。
func TestSSHExecutorMissingCredential(t *testing.T) {
	exec, st, _, _, _ := newSSHExecEnv(t)
	ctx := context.Background()

	srv := &store.Server{
		ID: uuid.NewString(), Name: "no-cred", Host: "127.0.0.1", Port: 1,
		Mode: store.ModeSSH, TagsJSON: "[]", Status: "unknown",
		CreatedAt: store.NowMS(), HostKeyPolicy: store.HostKeyPolicyAcceptNew,
	}
	if err := st.CreateServer(ctx, srv); err != nil {
		t.Fatalf("创建服务器失败: %v", err)
	}
	if _, _, _, err := exec.Exec(ctx, srv.ID, "echo x", 5); err == nil {
		t.Fatal("未绑定凭证应报错")
	}

	if _, _, _, err := exec.Exec(ctx, "ghost", "echo x", 5); err == nil {
		t.Fatal("服务器不存在应报错")
	}
}

// recordingExecutor 记录调用情况，用于验证按 mode 路由。
type recordingExecutor struct {
	calls []string
}

func (r *recordingExecutor) Exec(_ context.Context, serverID, command string, _ int) (string, string, int, error) {
	r.calls = append(r.calls, serverID+":"+command)
	return "ok", "", 0, nil
}

// TestRoutingExecutorPicksChannelByMode 校验 agent/ssh 两条通道按服务器 mode 路由。
func TestRoutingExecutorPicksChannelByMode(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	jwt := env.login()

	agentSrv := env.createServerVia(jwt, map[string]any{"name": "agent-mode", "host": "10.9.0.1", "mode": "agent"})
	sshSrv := env.createServerVia(jwt, map[string]any{"name": "ssh-mode", "host": "10.9.0.2", "mode": "ssh"})

	agentExec := &recordingExecutor{}
	sshExec := &recordingExecutor{}
	router := NewRoutingExecutor(env.st, agentExec, sshExec, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if _, _, _, err := router.Exec(ctx, agentSrv.ID, "uptime", 5); err != nil {
		t.Fatalf("agent 通道执行失败: %v", err)
	}
	if len(agentExec.calls) != 1 || len(sshExec.calls) != 0 {
		t.Fatalf("agent 模式应走 agent 通道: agent=%v ssh=%v", agentExec.calls, sshExec.calls)
	}

	if _, _, _, err := router.Exec(ctx, sshSrv.ID, "uptime", 5); err != nil {
		t.Fatalf("ssh 通道执行失败: %v", err)
	}
	if len(sshExec.calls) != 1 || len(agentExec.calls) != 1 {
		t.Fatalf("ssh 模式应走 SSH 通道: agent=%v ssh=%v", agentExec.calls, sshExec.calls)
	}

	if _, _, _, err := router.Exec(ctx, "ghost", "uptime", 5); err == nil {
		t.Fatal("服务器不存在应报错")
	}
}

// TestCheckerStoreAdapter 校验 checker.Store 适配器：Mode 带出与结果/状态写入。
func TestCheckerStoreAdapter(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	jwt := env.login()

	agentSrv := env.createServerVia(jwt, map[string]any{"name": "ca", "host": "10.10.0.1", "mode": "agent"})
	sshSrv := env.createServerVia(jwt, map[string]any{"name": "cs", "host": "10.10.0.2", "mode": "ssh"})
	check := env.createCheckVia(jwt, map[string]any{
		"name": "适配器", "kind": "tcp", "target": "127.0.0.1:22", "interval_s": 10,
		"timeout_s": 3, "params": map[string]any{"fail_threshold": 2},
		"server_ids": []string{agentSrv.ID, sshSrv.ID},
	})

	adapter := NewCheckerStore(env.st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	targets, err := adapter.ListEnabledTargets(ctx)
	if err != nil {
		t.Fatalf("ListEnabledTargets 失败: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("期望 2 个目标，实际 %d", len(targets))
	}
	modes := map[string]string{}
	for _, tg := range targets {
		modes[tg.ServerID] = tg.Mode
		if tg.CheckID != check.ID || tg.Spec.Kind != "tcp" || tg.Spec.TimeoutS != 3 {
			t.Fatalf("目标内容不正确: %+v", tg)
		}
		if threshold, ok := tg.Spec.Params["fail_threshold"].(float64); !ok || int(threshold) != 2 {
			t.Fatalf("Spec.Params 未带出 fail_threshold: %+v", tg.Spec.Params)
		}
	}
	if modes[agentSrv.ID] != "agent" || modes[sshSrv.ID] != "ssh" {
		t.Fatalf("Mode 未正确带出: %+v", modes)
	}

	// SaveResult → 结果落库
	now := store.NowMS()
	if err := adapter.SaveResult(ctx, check.ID, agentSrv.ID, checkerResult(now)); err != nil {
		t.Fatalf("SaveResult 失败: %v", err)
	}
	rows, err := env.st.ListCheckResults(ctx, store.CheckResultFilter{CheckID: check.ID, Limit: 10})
	if err != nil {
		t.Fatalf("查询结果失败: %v", err)
	}
	if len(rows) != 1 || rows[0].OK || rows[0].Message == nil {
		t.Fatalf("结果落库不正确: %+v", rows)
	}
	if rows[0].StatusCode == nil || *rows[0].StatusCode != 404 {
		t.Fatalf("status_code 未落库: %+v", rows[0])
	}

	// UpdateState → 状态机推进
	if err := adapter.UpdateState(ctx, check.ID, agentSrv.ID, false, now); err != nil {
		t.Fatalf("UpdateState 失败: %v", err)
	}
	st, err := env.st.GetCheckState(ctx, check.ID, agentSrv.ID)
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if st.OK || st.ConsecutiveFail != 1 || st.LastMessage == nil {
		t.Fatalf("状态机结果不正确: %+v", st)
	}
}

// checkerResult 构造一个失败的探测结果（含状态码与文案）。
func checkerResult(ts int64) checker.Result {
	return checker.Result{
		OK: false, LatencyMS: 12, StatusCode: 404, Message: "期望 200，实际 404", TS: ts,
	}
}
