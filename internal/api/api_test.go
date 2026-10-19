package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/maliang/leadstack/internal/auth"
	"github.com/maliang/leadstack/internal/collector"
	"github.com/maliang/leadstack/internal/config"
	"github.com/maliang/leadstack/internal/credential"
	"github.com/maliang/leadstack/internal/store"
	"github.com/maliang/leadstack/pkg/protocol"
)

// testAdminPassword 是测试用的管理员密码。
const testAdminPassword = "TestPassword-123"

// testEnv 是一套独立运行的控制端测试环境。
type testEnv struct {
	t      *testing.T
	st     *store.Store
	api    *API
	srv    *httptest.Server
	http   *http.Client
	cipher *credential.Cipher
	ssh    *fakeSSH
	runner *fakeCheckRunner
	jobs   *fakeJobRouter
}

// newTestEnv 启动临时目录 + 临时 SQLite + httptest 服务。
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "leadstack.db")

	ctx := context.Background()
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Logf("关闭测试数据库失败: %v", err)
		}
	})
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}

	hash, err := auth.HashPassword(testAdminPassword)
	if err != nil {
		t.Fatalf("生成测试密码哈希失败: %v", err)
	}
	user := &store.User{
		ID:           uuid.NewString(),
		Username:     "admin",
		PasswordHash: hash,
		Role:         "admin",
		CreatedAt:    store.NowMS(),
	}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatalf("创建测试管理员失败: %v", err)
	}

	manager, err := auth.NewManager(bytes.Repeat([]byte("k"), 48), time.Hour)
	if err != nil {
		t.Fatalf("创建 auth 管理器失败: %v", err)
	}

	cfg := &config.Config{
		Listen:          "127.0.0.1:0",
		DataDir:         dir,
		DBPath:          dbPath,
		LogLevel:        slog.LevelError,
		TokenTTL:        time.Hour,
		ShutdownTimeout: time.Second,
		Version:         "test",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	cipher, err := credential.NewCipher(bytes.Repeat([]byte("m"), credential.MasterKeySize))
	if err != nil {
		t.Fatalf("构造凭证加密器失败: %v", err)
	}
	sshStub := &fakeSSH{
		st: st,
		testResult: collector.TestResult{
			OK: true, LatencyMS: 12, SSHBanner: "SSH-2.0-OpenSSH_test", HostKeyFingerprint: "SHA256:test",
		},
		hostInfo: protocol.HostInfo{
			Hostname: "stub-host", OS: "linux", OSVersion: "1.0", Kernel: "6.1.0", Arch: "x86_64",
			CPUModel: "Test CPU", CPUCores: 4, MemTotalBytes: 8 << 30,
			DiskTotalBytes: 100 << 30, DiskUsedBytes: 50 << 30, UptimeS: 1000,
			Timezone: "UTC", IPAddrs: []string{"10.0.0.1"}, CollectedAt: store.NowMS(),
		},
	}

	runner := &fakeCheckRunner{result: CheckRunResult{OK: true, LatencyMS: 3, TS: store.NowMS()}}
	jobRouter := &fakeJobRouter{}

	a, err := New(Options{
		Config: cfg, Store: st, Auth: manager, Cipher: cipher, SSH: sshStub,
		CheckRunner: runner, Jobs: jobRouter,
		Logger: logger, Version: "test",
	})
	if err != nil {
		t.Fatalf("构造 API 失败: %v", err)
	}
	srv := httptest.NewServer(a.Router())
	t.Cleanup(srv.Close)

	return &testEnv{
		t: t, st: st, api: a, srv: srv, http: srv.Client(),
		cipher: cipher, ssh: sshStub, runner: runner, jobs: jobRouter,
	}
}

// fakeCheckRunner 是 CheckRunner 的测试替身。
type fakeCheckRunner struct {
	mu       sync.Mutex
	result   CheckRunResult
	err      error
	runCalls []string
	wakes    int
}

func (f *fakeCheckRunner) RunOnce(_ context.Context, checkID, serverID string) (CheckRunResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runCalls = append(f.runCalls, checkID+"/"+serverID)
	if f.err != nil {
		return CheckRunResult{}, f.err
	}
	return f.result, nil
}

func (f *fakeCheckRunner) Wake() {
	f.mu.Lock()
	f.wakes++
	f.mu.Unlock()
}

func (f *fakeCheckRunner) wakeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wakes
}

// fakeJobRouter 是 JobRouter 的测试替身，记录三类帧的路由调用。
type fakeJobRouter struct {
	mu     sync.Mutex
	acks   []string
	logs   []string
	dones  []string
	failed []string
}

func (f *fakeJobRouter) HandleAck(serverID, runID string) {
	f.mu.Lock()
	f.acks = append(f.acks, serverID+"/"+runID)
	f.mu.Unlock()
}

func (f *fakeJobRouter) HandleLog(serverID, runID string, seq int, stream, chunk string) {
	f.mu.Lock()
	f.logs = append(f.logs, fmt.Sprintf("%s/%s/%d/%s/%s", serverID, runID, seq, stream, chunk))
	f.mu.Unlock()
}

func (f *fakeJobRouter) HandleDone(serverID, runID string, exitCode int, errMsg string) {
	f.mu.Lock()
	f.dones = append(f.dones, fmt.Sprintf("%s/%s/%d/%s", serverID, runID, exitCode, errMsg))
	f.mu.Unlock()
}

func (f *fakeJobRouter) FailServer(serverID string, cause error) {
	f.mu.Lock()
	f.failed = append(f.failed, serverID)
	f.mu.Unlock()
}

func (f *fakeJobRouter) counts() (int, int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.acks), len(f.logs), len(f.dones), len(f.failed)
}

// fakeSSH 是 SSHService 的测试替身（不建立真实 SSH 连接）。
//
// 与真实 collector 一致：服务器不存在时返回 store.ErrNotFound（API 层映射为 404）。
type fakeSSH struct {
	st         *store.Store
	testResult collector.TestResult
	testErr    error
	hostInfo   protocol.HostInfo
	hostErr    error
}

func (f *fakeSSH) TestConnection(ctx context.Context, serverID string) (collector.TestResult, error) {
	if _, err := f.st.GetServer(ctx, serverID); err != nil {
		return collector.TestResult{}, err
	}
	if f.testErr != nil {
		return collector.TestResult{}, f.testErr
	}
	return f.testResult, nil
}

func (f *fakeSSH) RefreshHostInfo(ctx context.Context, serverID string) (protocol.HostInfo, error) {
	if _, err := f.st.GetServer(ctx, serverID); err != nil {
		return protocol.HostInfo{}, err
	}
	if f.hostErr != nil {
		return protocol.HostInfo{}, f.hostErr
	}
	return f.hostInfo, nil
}

// wsURL 把 httptest 的 http 地址转换成 ws 地址。
func (e *testEnv) wsURL(path string) string {
	return "ws" + strings.TrimPrefix(e.srv.URL, "http") + path
}

// request 发起一次 HTTP 请求，返回状态码与响应体。
func (e *testEnv) request(method, path, token string, body any) (int, []byte) {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, reader)
	if err != nil {
		e.t.Fatalf("构造请求失败: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := e.http.Do(req)
	if err != nil {
		e.t.Fatalf("发起请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("读取响应体失败: %v", err)
	}
	return resp.StatusCode, data
}

// login 登录并返回 JWT。
func (e *testEnv) login() string {
	e.t.Helper()
	status, body := e.request(http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"username": "admin", "password": testAdminPassword})
	if status != http.StatusOK {
		e.t.Fatalf("登录失败: status=%d body=%s", status, body)
	}
	var out struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		e.t.Fatalf("解析登录响应失败: %v", err)
	}
	if out.Data.Token == "" {
		e.t.Fatalf("登录响应缺少 token: %s", body)
	}
	return out.Data.Token
}

// createEnrollToken 通过 REST 生成一枚接入令牌，serverID 可为空。
func (e *testEnv) createEnrollToken(jwt, serverID string) string {
	e.t.Helper()
	req := map[string]any{"note": "测试令牌", "ttl_s": 3600}
	if serverID != "" {
		req["server_id"] = serverID
	}
	status, body := e.request(http.MethodPost, "/api/v1/enroll-tokens", jwt, req)
	if status != http.StatusCreated {
		e.t.Fatalf("生成接入令牌失败: status=%d body=%s", status, body)
	}
	var out struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		e.t.Fatalf("解析接入令牌响应失败: %v", err)
	}
	return out.Data.Token
}

// listServers 返回服务器列表。
func (e *testEnv) listServers(jwt string) []serverPayload {
	e.t.Helper()
	status, body := e.request(http.MethodGet, "/api/v1/servers", jwt, nil)
	if status != http.StatusOK {
		e.t.Fatalf("查询服务器列表失败: status=%d body=%s", status, body)
	}
	var out struct {
		Data []serverPayload `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		e.t.Fatalf("解析服务器列表失败: %v", err)
	}
	return out.Data
}

// dialAgent 以 agent 身份连接 /agent/ws。
func (e *testEnv) dialAgent(token string) (*websocket.Conn, *http.Response, error) {
	e.t.Helper()
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	return dialer.Dial(e.wsURL("/agent/ws"), header)
}

// sendFrame 发送一帧消息，payload 会序列化到 payload 字段。
func sendFrame(t *testing.T, conn *websocket.Conn, msgType string, payload any) {
	t.Helper()
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("序列化 %s 载荷失败: %v", msgType, err)
		}
		raw = b
	}
	env := protocol.Envelope{Type: msgType, ID: uuid.NewString(), TS: store.NowMS(), Payload: raw}
	if err := conn.WriteJSON(env); err != nil {
		t.Fatalf("发送 %s 帧失败: %v", msgType, err)
	}
}

// readFrame 读取一帧消息，带超时。
func readFrame(t *testing.T, conn *websocket.Conn, timeout time.Duration) protocol.Envelope {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("设置读超时失败: %v", err)
	}
	var env protocol.Envelope
	if err := conn.ReadJSON(&env); err != nil {
		t.Fatalf("读取帧失败: %v", err)
	}
	return env
}

// TestHealthzAndAuth 覆盖验收第 6 条：healthz 免鉴权，未带 token 访问受保护接口返回 401。
func TestHealthzAndAuth(t *testing.T) {
	env := newTestEnv(t)

	status, body := env.request(http.MethodGet, "/healthz", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /healthz 期望 200，实际 %d（body=%s）", status, body)
	}
	var health struct {
		Data healthResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("解析 healthz 响应失败: %v（body=%s）", err, body)
	}
	if health.Data.Status != "ok" {
		t.Fatalf("healthz status 期望 ok，实际 %q", health.Data.Status)
	}

	status, body = env.request(http.MethodGet, "/api/v1/servers", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("未带 token 访问 /api/v1/servers 期望 401，实际 %d（body=%s）", status, body)
	}
	if !bytes.Contains(body, []byte(`"code":"unauthorized"`)) {
		t.Fatalf("401 响应体不符合契约: %s", body)
	}

	status, body = env.request(http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"username": "admin", "password": "wrong-password"})
	if status != http.StatusUnauthorized {
		t.Fatalf("错误密码期望 401，实际 %d（body=%s）", status, body)
	}

	jwt := env.login()

	status, body = env.request(http.MethodGet, "/api/v1/servers", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("带 token 访问 /api/v1/servers 期望 200，实际 %d（body=%s）", status, body)
	}
	var list struct {
		Data []serverPayload `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("解析服务器列表失败: %v", err)
	}
	if len(list.Data) != 0 {
		t.Fatalf("初始服务器列表应为空，实际 %d 条", len(list.Data))
	}

	status, body = env.request(http.MethodGet, "/api/v1/auth/me", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/auth/me 期望 200，实际 %d（body=%s）", status, body)
	}
	if !bytes.Contains(body, []byte(`"username":"admin"`)) {
		t.Fatalf("me 响应缺少用户名: %s", body)
	}
}

// TestAgentWebSocketLifecycle 覆盖验收第 4 条：agent 接入后服务器在线并带上主机信息与指标。
func TestAgentWebSocketLifecycle(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	// 先在控制台「添加服务器」，再生成绑定该服务器的接入令牌。
	status, body := env.request(http.MethodPost, "/api/v1/servers", jwt,
		map[string]any{"name": "测试节点", "host": "10.0.0.9", "port": 22, "mode": "agent"})
	if status != http.StatusCreated {
		t.Fatalf("新增服务器失败: status=%d body=%s", status, body)
	}
	var created struct {
		Data serverPayload `json:"data"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("解析新增服务器响应失败: %v", err)
	}
	serverID := created.Data.ID
	if created.Data.Status != protocol.StatusUnknown {
		t.Fatalf("新服务器状态期望 unknown，实际 %q", created.Data.Status)
	}

	token := env.createEnrollToken(jwt, serverID)

	conn, _, err := env.dialAgent(token)
	if err != nil {
		t.Fatalf("agent WebSocket 连接失败: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// 1) hello → hello_ack
	sendFrame(t, conn, protocol.TypeHello, protocol.Hello{
		Hostname: "test-agent", OS: "darwin", Arch: "arm64",
		Kernel: "24.0.0", AgentVersion: "0.1.0-test", BootTime: store.NowMS() - 100000,
	})
	ack := readFrame(t, conn, 5*time.Second)
	if ack.Type != protocol.TypeHelloAck {
		t.Fatalf("期望 hello_ack，实际 %q", ack.Type)
	}
	var ackPayload protocol.HelloAck
	if err := json.Unmarshal(ack.Payload, &ackPayload); err != nil {
		t.Fatalf("解析 hello_ack 失败: %v", err)
	}
	if ackPayload.ServerID != serverID {
		t.Fatalf("hello_ack.server_id 期望 %s，实际 %s", serverID, ackPayload.ServerID)
	}
	if ackPayload.HeartbeatIntervalS != protocol.DefaultHeartbeatIntervalS ||
		ackPayload.MetricsIntervalS != protocol.DefaultMetricsIntervalS {
		t.Fatalf("hello_ack 节奏配置不符合契约: %+v", ackPayload)
	}

	// 2) 未知类型必须被忽略且不断开连接
	sendFrame(t, conn, "future.unknown.type", map[string]any{"x": 1})

	// 3) heartbeat → 服务器变 online（心跳里的读数也会顺带落库）
	sendFrame(t, conn, protocol.TypeHeartbeat, protocol.Heartbeat{
		CPUPct: 12.5, MemUsedPct: 40.25, DiskUsedPt: 61.5, Load1: 0.75, UptimeS: 12345,
	})

	// 等待服务端把心跳落库（异步处理）。
	waitForServer(t, env, jwt, func(s serverPayload) bool {
		return s.Status == protocol.StatusOnline && s.MetricsSummary[protocol.MetricCPUPct] == 12.5
	})

	servers := env.listServers(jwt)
	if len(servers) != 1 {
		t.Fatalf("服务器列表应恰好 1 条（令牌已预绑定，不应重复创建），实际 %d", len(servers))
	}
	srv := servers[0]
	if srv.LastSeenAt == nil {
		t.Fatal("last_seen_at 不应为空")
	}
	if srv.OS == nil || *srv.OS != "darwin" || srv.Arch == nil || *srv.Arch != "arm64" {
		t.Fatalf("OS/Arch 未按 hello 更新: os=%v arch=%v", srv.OS, srv.Arch)
	}
	if srv.AgentVersion == nil || *srv.AgentVersion != "0.1.0-test" {
		t.Fatalf("agent_version 未按 hello 更新: %v", srv.AgentVersion)
	}
	if srv.Name != "测试节点" {
		t.Fatalf("控制台设置的名称不应被 hello 覆盖，实际 %q", srv.Name)
	}
	if _, ok := srv.MetricsSummary[protocol.MetricLoad1]; !ok {
		t.Fatalf("心跳中的 load.1 未落库: %v", srv.MetricsSummary)
	}

	// 4) metrics 批量上报：时间戳取“下一次心跳之后”，确保覆盖心跳值
	metricsTS := store.NowMS() + 1
	sendFrame(t, conn, protocol.TypeMetrics, protocol.Metrics{Samples: []protocol.Sample{
		{Metric: protocol.MetricCPUPct, Value: 13.5, TS: metricsTS},
		{Metric: protocol.MetricMemUsedB, Value: 8 << 30, TS: metricsTS},
		{Metric: "unknown.metric", Value: 1, TS: metricsTS},
	}})
	srv = waitForServer(t, env, jwt, func(s serverPayload) bool {
		return s.MetricsSummary[protocol.MetricCPUPct] == 13.5
	})
	if got := srv.MetricsSummary[protocol.MetricMemUsedB]; got != float64(8<<30) {
		t.Fatalf("mem.used_bytes 期望 %v，实际 %v", float64(8<<30), got)
	}
	if _, ok := srv.MetricsSummary["unknown.metric"]; ok {
		t.Fatalf("契约外的指标不应落库: %v", srv.MetricsSummary)
	}

	// 5) 指标去重：重复上报同一 (metric, ts) 不覆盖既有值
	sendFrame(t, conn, protocol.TypeMetrics, protocol.Metrics{Samples: []protocol.Sample{
		{Metric: protocol.MetricCPUPct, Value: 99, TS: metricsTS},
	}})
	time.Sleep(150 * time.Millisecond)
	if got := env.listServers(jwt)[0].MetricsSummary[protocol.MetricCPUPct]; got != 13.5 {
		t.Fatalf("重复采样不应覆盖同 (metric, ts) 的值，实际 %v", got)
	}

	// 6) 令牌已被标记使用并绑定服务器
	tokens := env.listEnrollTokens(jwt)
	if len(tokens) != 1 {
		t.Fatalf("接入令牌列表应恰好 1 条，实际 %d", len(tokens))
	}
	if tokens[0].UsedAt == nil {
		t.Fatal("agent 接入后 used_at 应被写入")
	}
	if tokens[0].ServerID == nil || *tokens[0].ServerID != serverID {
		t.Fatalf("令牌未绑定到服务器 %s: %v", serverID, tokens[0].ServerID)
	}
}

// waitForServer 轮询服务器列表，直到条件满足或超时。
func waitForServer(t *testing.T, env *testEnv, jwt string, ok func(serverPayload) bool) serverPayload {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last serverPayload
	for time.Now().Before(deadline) {
		servers := env.listServers(jwt)
		if len(servers) > 0 {
			last = servers[0]
			if ok(last) {
				return last
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待服务器状态超时，最后一次结果: %+v", last)
	return last
}

// listEnrollTokens 返回接入令牌列表。
func (e *testEnv) listEnrollTokens(jwt string) []enrollTokenPayload {
	e.t.Helper()
	status, body := e.request(http.MethodGet, "/api/v1/enroll-tokens", jwt, nil)
	if status != http.StatusOK {
		e.t.Fatalf("查询接入令牌失败: status=%d body=%s", status, body)
	}
	var out struct {
		Data []enrollTokenPayload `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		e.t.Fatalf("解析接入令牌列表失败: %v", err)
	}
	return out.Data
}

// TestAgentWebSocketRejectsBadToken 覆盖握手鉴权：无效/缺失令牌必须 401。
func TestAgentWebSocketRejectsBadToken(t *testing.T) {
	env := newTestEnv(t)

	if _, resp, err := env.dialAgent("totally-invalid-token"); err == nil {
		t.Fatal("无效令牌不应连接成功")
	} else if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("无效令牌期望 401，实际 %d", status)
	}

	if _, resp, err := env.dialAgent(""); err == nil {
		t.Fatal("缺少令牌不应连接成功")
	} else if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("缺少令牌期望 401，实际 %d", status)
	}

	// 过期令牌同样拒绝。
	jwt := env.login()
	status, body := env.request(http.MethodPost, "/api/v1/enroll-tokens", jwt,
		map[string]any{"note": "过期令牌", "ttl_s": 60})
	if status != http.StatusCreated {
		t.Fatalf("生成令牌失败: status=%d body=%s", status, body)
	}
	var created struct {
		Data struct {
			ID    string `json:"id"`
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("解析令牌响应失败: %v", err)
	}
	if _, err := env.st.DB().ExecContext(context.Background(),
		`UPDATE enroll_tokens SET expires_at = ? WHERE id = ?`, store.NowMS()-1000, created.Data.ID); err != nil {
		t.Fatalf("制造过期令牌失败: %v", err)
	}
	if _, resp, err := env.dialAgent(created.Data.Token); err == nil {
		t.Fatal("过期令牌不应连接成功")
	} else if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("过期令牌期望 401，实际 %d", status)
	}
}

// TestOfflineSweeper 覆盖验收第 5 条的核心逻辑：超时未心跳即置为离线并记事件。
func TestOfflineSweeper(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	status, body := env.request(http.MethodPost, "/api/v1/servers", jwt,
		map[string]any{"name": "离线测试节点", "host": "10.0.0.10"})
	if status != http.StatusCreated {
		t.Fatalf("新增服务器失败: status=%d body=%s", status, body)
	}
	var created struct {
		Data serverPayload `json:"data"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("解析新增服务器响应失败: %v", err)
	}
	serverID := created.Data.ID

	// 模拟“刚收到过心跳，但随后失联超过 45 秒”。
	staleSeenAt := store.NowMS() - int64(protocol.OfflineAfterS)*1000 - 5000
	if _, err := env.st.DB().ExecContext(context.Background(),
		`UPDATE servers SET status = 'online', last_seen_at = ? WHERE id = ?`, staleSeenAt, serverID); err != nil {
		t.Fatalf("构造在线状态失败: %v", err)
	}

	// 清扫前列表接口就应按 last_seen_at 实时推导为 offline。
	if got := env.listServers(jwt)[0].Status; got != protocol.StatusOffline {
		t.Fatalf("list 接口实时推导状态期望 offline，实际 %q", got)
	}

	n, err := env.api.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("离线清扫失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("期望清扫 1 台服务器，实际 %d", n)
	}

	srv, err := env.st.GetServer(context.Background(), serverID)
	if err != nil {
		t.Fatalf("读取服务器失败: %v", err)
	}
	if srv.Status != protocol.StatusOffline {
		t.Fatalf("库内状态期望 offline，实际 %q", srv.Status)
	}

	events, err := env.st.ListEvents(context.Background(), serverID, 10)
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(events) != 1 || events[0].Kind != store.EventKindOffline {
		t.Fatalf("期望 1 条 offline 事件，实际 %+v", events)
	}

	// 在线计数也应随之归零。
	status, body = env.request(http.MethodGet, "/api/v1/system/info", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询系统信息失败: status=%d body=%s", status, body)
	}
	var info struct {
		Data systemInfoResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatalf("解析系统信息失败: %v", err)
	}
	if info.Data.ServerCount != 1 || info.Data.OnlineCount != 0 {
		t.Fatalf("system/info 期望 1 台/0 在线，实际 %+v", info.Data)
	}
}

// TestServerCRUD 覆盖服务器改名、标签、删除与 404 语义。
func TestServerCRUD(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	status, body := env.request(http.MethodPost, "/api/v1/servers", jwt,
		map[string]any{"name": "改名节点", "host": "10.0.0.11"})
	if status != http.StatusCreated {
		t.Fatalf("新增服务器失败: status=%d body=%s", status, body)
	}
	var created struct {
		Data serverPayload `json:"data"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("解析新增服务器响应失败: %v", err)
	}
	id := created.Data.ID

	status, body = env.request(http.MethodPatch, "/api/v1/servers/"+id, jwt,
		map[string]any{"name": "已改名", "tags": []string{"prod", "bj", "prod"}})
	if status != http.StatusOK {
		t.Fatalf("PATCH 服务器失败: status=%d body=%s", status, body)
	}
	var patched struct {
		Data serverPayload `json:"data"`
	}
	if err := json.Unmarshal(body, &patched); err != nil {
		t.Fatalf("解析 PATCH 响应失败: %v", err)
	}
	if patched.Data.Name != "已改名" {
		t.Fatalf("名称未更新: %q", patched.Data.Name)
	}
	if len(patched.Data.Tags) != 2 {
		t.Fatalf("标签应去重为 2 个，实际 %v", patched.Data.Tags)
	}

	status, body = env.request(http.MethodPatch, "/api/v1/servers/"+id, jwt, map[string]any{})
	if status != http.StatusBadRequest {
		t.Fatalf("空 PATCH 期望 400，实际 %d（body=%s）", status, body)
	}

	status, body = env.request(http.MethodGet, "/api/v1/servers/"+uuid.NewString(), jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("查询不存在服务器期望 404，实际 %d（body=%s）", status, body)
	}

	status, body = env.request(http.MethodDelete, "/api/v1/servers/"+id, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("删除服务器失败: status=%d body=%s", status, body)
	}
	status, _ = env.request(http.MethodDelete, "/api/v1/servers/"+id, jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("重复删除期望 404，实际 %d", status)
	}
}

// TestInstallScriptCommand 校验一键安装命令的生成与参数防护。
func TestInstallScriptCommand(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	token := env.createEnrollToken(jwt, "")

	status, body := env.request(http.MethodPost, "/api/v1/agent/install-script", jwt,
		map[string]string{"token": token, "server_url": "http://127.0.0.1:8080/"})
	if status != http.StatusOK {
		t.Fatalf("生成安装命令失败: status=%d body=%s", status, body)
	}
	var out struct {
		Data installScriptResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("解析安装命令响应失败: %v", err)
	}
	want := fmt.Sprintf("curl -fsSL 'http://127.0.0.1:8080/install.sh' | sh -s -- --server 'http://127.0.0.1:8080' --token '%s'", token)
	if out.Data.Command != want {
		t.Fatalf("安装命令不符合预期:\n got: %s\nwant: %s", out.Data.Command, want)
	}

	// 命令注入防护：token 与 server_url 只允许安全字符。
	status, body = env.request(http.MethodPost, "/api/v1/agent/install-script", jwt,
		map[string]string{"token": "abc; rm -rf /", "server_url": "http://127.0.0.1:8080"})
	if status != http.StatusBadRequest {
		t.Fatalf("非法 token 期望 400，实际 %d（body=%s）", status, body)
	}
	status, body = env.request(http.MethodPost, "/api/v1/agent/install-script", jwt,
		map[string]string{"token": token, "server_url": "ftp://127.0.0.1"})
	if status != http.StatusBadRequest {
		t.Fatalf("非法 server_url 期望 400，实际 %d（body=%s）", status, body)
	}
}

// TestSPAFallback 校验前端产物回退：未知前端路由返回 index.html，未知 API 返回 JSON 404。
func TestSPAFallback(t *testing.T) {
	env := newTestEnv(t)

	status, body := env.request(http.MethodGet, "/login", "", nil)
	if status != http.StatusOK {
		t.Fatalf("前端路由回退期望 200，实际 %d", status)
	}
	if !bytes.Contains(body, []byte("<!doctype html")) {
		t.Fatalf("前端路由回退应返回 index.html，实际: %s", truncate(body, 120))
	}

	status, body = env.request(http.MethodGet, "/api/v1/not-exist", "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("未知 API 期望 404，实际 %d", status)
	}
	if !bytes.Contains(body, []byte(`"code":"not_found"`)) {
		t.Fatalf("未知 API 应返回 JSON 404，实际: %s", truncate(body, 120))
	}

	status, _ = env.request(http.MethodGet, "/assets/not-exist.js", "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("缺失静态资源期望 404，实际 %d", status)
	}
}

// truncate 截断字符串，便于断言失败时输出。
func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
