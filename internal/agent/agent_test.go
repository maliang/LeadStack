package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	gnet "github.com/shirou/gopsutil/v4/net"

	"github.com/maliang/leadstack/pkg/protocol"
)

// testLogger 返回一个丢弃全部输出的日志器，避免测试日志噪音。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

var testUpgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

// newMockServer 启动一个模拟控制端的 WebSocket 服务，
// 校验路径与 Bearer 令牌后把连接交给 handler。
func newMockServer(t *testing.T, handler func(conn *websocket.Conn, connNo int)) *httptest.Server {
	t.Helper()
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/ws" {
			t.Errorf("WebSocket 路径错误：%s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("鉴权头错误：%q", got)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("升级 WebSocket 失败：%v", err)
			return
		}
		defer conn.Close()
		handler(conn, int(count.Add(1)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// readEnvelope 在超时内读取一帧。
func readEnvelope(conn *websocket.Conn, timeout time.Duration) (protocol.Envelope, bool) {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	var env protocol.Envelope
	if err := conn.ReadJSON(&env); err != nil {
		return protocol.Envelope{}, false
	}
	return env, true
}

// writeEnvelope 写出一帧（测试侧不并发写，可直接写）。
func writeEnvelope(conn *websocket.Conn, msgType string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return conn.WriteJSON(protocol.Envelope{Type: msgType, TS: nowMillis(), Payload: raw})
}

// sendHelloAck 下发 hello_ack。
func sendHelloAck(conn *websocket.Conn, heartbeatS, metricsS int) error {
	return writeEnvelope(conn, protocol.TypeHelloAck, protocol.HelloAck{
		ServerID:           "srv-test",
		HeartbeatIntervalS: heartbeatS,
		MetricsIntervalS:   metricsS,
	})
}

func TestWSURL(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "http://127.0.0.1:8080", want: "ws://127.0.0.1:8080/agent/ws"},
		{in: "http://127.0.0.1:8080/", want: "ws://127.0.0.1:8080/agent/ws"},
		{in: "https://example.com", want: "wss://example.com/agent/ws"},
		{in: "ws://example.com:9000/prefix", want: "ws://example.com:9000/prefix/agent/ws"},
		{in: "wss://example.com/agent/ws", want: "wss://example.com/agent/ws"},
		{in: "", wantErr: true},
		{in: "ftp://example.com", wantErr: true},
		{in: "http://", wantErr: true},
	}
	for _, tc := range cases {
		got, err := wsURL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("wsURL(%q) 期望报错，实际得到 %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("wsURL(%q) 返回错误: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("wsURL(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestJitterAndBackoff(t *testing.T) {
	for _, f := range []float64{0, 0.25, 0.5, 0.999} {
		got := jitterDuration(time.Second, f)
		if got < 800*time.Millisecond || got > 1200*time.Millisecond {
			t.Errorf("jitterDuration(1s, %v) = %v，超出 ±20%%", f, got)
		}
	}

	if got := nextBackoff(minBackoff); got != 2*time.Second {
		t.Errorf("nextBackoff(1s) = %v，期望 2s", got)
	}
	cur := minBackoff
	var last time.Duration
	for i := 0; i < 20; i++ {
		cur = nextBackoff(cur)
		last = cur
	}
	if last != maxBackoff {
		t.Errorf("退避上限 = %v，期望 %v", last, maxBackoff)
	}
}

func TestSampleBufferRingAndRetention(t *testing.T) {
	now := time.Now()
	b := newSampleBuffer(3, 15*time.Minute)
	b.now = func() time.Time { return now }

	samples := make([]protocol.Sample, 0, 5)
	for i := 1; i <= 5; i++ {
		samples = append(samples, protocol.Sample{Metric: "m", Value: float64(i), TS: now.UnixMilli()})
	}
	b.Add(samples)
	if got := b.Len(); got != 3 {
		t.Fatalf("环形缓冲容量限制失效：Len=%d，期望 3", got)
	}
	got := b.Drain()
	if len(got) != 3 || got[0].Value != 3 || got[2].Value != 5 {
		t.Fatalf("环形缓冲覆盖顺序错误：%+v", got)
	}
	if b.Len() != 0 {
		t.Fatalf("Drain 后缓冲未清空：Len=%d", b.Len())
	}

	// 超过 15 分钟保留窗口的样本必须被丢弃。
	b.Add([]protocol.Sample{
		{Metric: "m", Value: 1, TS: now.Add(-16 * time.Minute).UnixMilli()},
		{Metric: "m", Value: 2, TS: now.UnixMilli()},
	})
	got = b.Drain()
	if len(got) != 1 || got[0].Value != 2 {
		t.Fatalf("过期样本清理错误：%+v", got)
	}
}

// TestRunOnce 验证 --once 路径：hello → hello_ack → host_info → heartbeat → metrics，
// 同时验证未知 type 被忽略而不断开。
func TestRunOnce(t *testing.T) {
	type capture struct {
		types    []string
		hello    protocol.Hello
		hostInfo protocol.HostInfo
		metrics  protocol.Metrics
	}
	var mu sync.Mutex
	var c capture
	done := make(chan struct{})
	var doneOnce sync.Once

	srv := newMockServer(t, func(conn *websocket.Conn, _ int) {
		// 先下发一个未识别的类型，agent 必须忽略而不是断开。
		if err := writeEnvelope(conn, "future.unknown", map[string]string{"x": "y"}); err != nil {
			return
		}
		env, ok := readEnvelope(conn, 5*time.Second)
		if !ok {
			return
		}
		mu.Lock()
		c.types = append(c.types, env.Type)
		if env.Type == protocol.TypeHello {
			_ = json.Unmarshal(env.Payload, &c.hello)
		}
		mu.Unlock()
		if env.Type != protocol.TypeHello {
			return
		}
		if err := sendHelloAck(conn, 20, 15); err != nil {
			return
		}
		for {
			env, ok := readEnvelope(conn, 5*time.Second)
			if !ok {
				return
			}
			mu.Lock()
			c.types = append(c.types, env.Type)
			switch env.Type {
			case protocol.TypeHostInfo:
				_ = json.Unmarshal(env.Payload, &c.hostInfo)
			case protocol.TypeMetrics:
				_ = json.Unmarshal(env.Payload, &c.metrics)
			}
			mu.Unlock()
			if env.Type == protocol.TypeMetrics {
				doneOnce.Do(func() { close(done) })
				return
			}
		}
	})

	a := New(Config{
		Server:  srv.URL,
		Token:   "test-token",
		Version: "test-version",
		Once:    true,
		Logger:  testLogger(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := a.Run(ctx); err != nil {
		t.Fatalf("Run(--once) 失败: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("服务端未收到 metrics")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(c.types) < 4 {
		t.Fatalf("收到的帧过少：%v", c.types)
	}
	if c.types[0] != protocol.TypeHello {
		t.Errorf("首帧应为 hello，实际 %q", c.types[0])
	}
	// host_info 必须在握手完成后、心跳/指标之前或之间到达。
	foundHostInfo := false
	for _, tp := range c.types {
		if tp == protocol.TypeHostInfo {
			foundHostInfo = true
		}
	}
	if !foundHostInfo {
		t.Fatalf("未收到 host_info，收到类型：%v", c.types)
	}
	if c.hello.Hostname == "" {
		t.Error("hello 缺少 hostname")
	}
	if c.hello.OS != runtime.GOOS || c.hello.Arch != runtime.GOARCH {
		t.Errorf("hello 的 os/arch 错误：%s/%s", c.hello.OS, c.hello.Arch)
	}
	if c.hello.AgentVersion != "test-version" {
		t.Errorf("hello 的 agent_version 错误：%q", c.hello.AgentVersion)
	}
	assertHostInfo(t, c.hostInfo)
	if len(c.metrics.Samples) == 0 {
		t.Fatal("metrics 没有任何样本")
	}
	allowed := map[string]bool{
		protocol.MetricCPUPct:     true,
		protocol.MetricMemUsedPct: true,
		protocol.MetricMemUsedB:   true,
		protocol.MetricLoad1:      true,
		protocol.MetricDiskUsedPt: true,
		protocol.MetricNetRxBytes: true,
		protocol.MetricNetTxBytes: true,
		protocol.MetricProcCount:  true,
	}
	for _, s := range c.metrics.Samples {
		if !allowed[s.Metric] {
			t.Errorf("出现未定义的指标名：%q", s.Metric)
		}
		if s.TS <= 0 {
			t.Errorf("指标 %q 的 ts 非法：%d", s.Metric, s.TS)
		}
	}
}

// assertHostInfo 校验主机信息关键字段的合法性。
func assertHostInfo(t *testing.T, info protocol.HostInfo) {
	t.Helper()
	if info.Hostname == "" {
		t.Error("host_info 缺少 hostname")
	}
	if info.OS != runtime.GOOS || info.Arch != runtime.GOARCH {
		t.Errorf("host_info 的 os/arch 错误：%s/%s", info.OS, info.Arch)
	}
	if info.CPUCores <= 0 {
		t.Errorf("host_info 的 cpu_cores 非法：%d", info.CPUCores)
	}
	if info.MemTotalBytes == 0 {
		t.Error("host_info 缺少 mem_total_bytes")
	}
	if info.DiskTotalBytes == 0 {
		t.Error("host_info 缺少 disk_total_bytes")
	}
	if info.UptimeS <= 0 {
		t.Errorf("host_info 的 uptime_s 非法：%d", info.UptimeS)
	}
	if info.CollectedAt <= 0 {
		t.Errorf("host_info 的 collected_at 非法：%d", info.CollectedAt)
	}
	if info.Timezone == "" {
		t.Error("host_info 缺少 timezone")
	}
	for _, ipStr := range info.IPAddrs {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			t.Errorf("host_info 的 IP 非法：%q", ipStr)
			continue
		}
		if ip.IsLoopback() {
			t.Errorf("host_info 不应包含回环地址：%q", ipStr)
		}
	}
}

// TestReconnectAfterServerClose 验证控制端主动断开后 agent 会退避重连并重新 hello。
func TestReconnectAfterServerClose(t *testing.T) {
	hellos := make(chan int, 4)
	block := make(chan struct{})
	defer close(block)

	srv := newMockServer(t, func(conn *websocket.Conn, connNo int) {
		env, ok := readEnvelope(conn, 5*time.Second)
		if !ok {
			return
		}
		if env.Type != protocol.TypeHello {
			return
		}
		if err := sendHelloAck(conn, 20, 15); err != nil {
			return
		}
		hellos <- connNo
		if connNo == 1 {
			// 第一次连接立即关闭，触发 agent 重连。
			return
		}
		// 后续连接保持到测试结束。
		<-block
	})

	a := New(Config{Server: srv.URL, Token: "test-token", Version: "test", Logger: testLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- a.Run(ctx) }()

	for want := 1; want <= 2; want++ {
		select {
		case got := <-hellos:
			if got != want {
				t.Errorf("期望第 %d 个连接，实际第 %d 个", want, got)
			}
		case <-time.After(8 * time.Second):
			t.Fatalf("等待第 %d 次 hello 超时（重连未生效）", want)
		}
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run 返回非预期错误: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("context 取消后 agent.Run 未退出（goroutine 泄漏）")
	}
}

// jobCapture 汇总一次任务执行过程中收到的 job.ack / job.log / job.done。
type jobCapture struct {
	acked   bool
	logs    []string
	done    protocol.JobDone
	gotDone bool
}

// runJobScenario 启动 mock 控制端与 agent，下发一条命令任务并等待 job.done。
func runJobScenario(t *testing.T, command string, timeoutS int) (jobCapture, time.Duration) {
	t.Helper()
	results := make(chan jobCapture, 1)

	srv := newMockServer(t, func(conn *websocket.Conn, _ int) {
		env, ok := readEnvelope(conn, 5*time.Second)
		if !ok || env.Type != protocol.TypeHello {
			return
		}
		if err := sendHelloAck(conn, 20, 15); err != nil {
			return
		}
		if err := writeEnvelope(conn, protocol.TypeJobExec, protocol.JobExec{
			RunID:    "run-1",
			Kind:     "command",
			Command:  command,
			TimeoutS: timeoutS,
		}); err != nil {
			return
		}

		res := jobCapture{}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			env, ok := readEnvelope(conn, time.Until(deadline))
			if !ok {
				break
			}
			switch env.Type {
			case protocol.TypeJobAck:
				res.acked = true
			case protocol.TypeJobLog:
				var l protocol.JobLog
				if json.Unmarshal(env.Payload, &l) == nil {
					res.logs = append(res.logs, l.Stream+":"+l.Chunk)
				}
			case protocol.TypeJobDone:
				if json.Unmarshal(env.Payload, &res.done) == nil {
					res.gotDone = true
				}
				results <- res
				return
			}
		}
		results <- res
	})

	a := New(Config{Server: srv.URL, Token: "test-token", Version: "test", Logger: testLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	start := time.Now()
	go func() { runErr <- a.Run(ctx) }()

	var res jobCapture
	select {
	case res = <-results:
	case <-time.After(25 * time.Second):
		t.Fatal("等待 job.done 超时")
	}
	elapsed := time.Since(start)

	cancel()
	select {
	case <-runErr:
	case <-time.After(10 * time.Second):
		t.Fatal("context 取消后 agent.Run 未退出")
	}
	return res, elapsed
}

// TestJobExec 验证命令任务的 ack / 日志流 / done 与退出码。
func TestJobExec(t *testing.T) {
	res, _ := runJobScenario(t, "printf hello-stdout; printf hello-stderr >&2; exit 3", 10)
	if !res.acked {
		t.Error("未收到 job.ack")
	}
	if !res.gotDone {
		t.Fatal("未收到 job.done")
	}
	joined := strings.Join(res.logs, "")
	if !strings.Contains(joined, "hello-stdout") || !strings.Contains(joined, "hello-stderr") {
		t.Errorf("日志未完整回传：%q", joined)
	}
	if res.done.ExitCode != 3 {
		t.Errorf("exit_code = %d，期望 3", res.done.ExitCode)
	}
}

// TestJobTimeout 验证超时任务会被终止并回报错误（进程组终止路径）。
func TestJobTimeout(t *testing.T) {
	res, elapsed := runJobScenario(t, "sleep 30", 1)
	if !res.gotDone {
		t.Fatal("超时任务未收到 job.done")
	}
	if res.done.ExitCode == 0 {
		t.Errorf("超时任务 exit_code 不应为 0：%+v", res.done)
	}
	if !strings.Contains(res.done.Error, "超时") {
		t.Errorf("超时任务的 error 信息缺失：%q", res.done.Error)
	}
	if elapsed > 15*time.Second {
		t.Errorf("超时任务耗时过长：%v", elapsed)
	}
}

// TestBacklogFlushedAfterReconnect 验证断线期间采集的指标会在重连后按原始时间戳补传。
func TestBacklogFlushedAfterReconnect(t *testing.T) {
	// 先占用再释放一个端口，保证稍后能在同一地址启动 mock 控制端。
	ln0, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("申请端口失败: %v", err)
	}
	addr := ln0.Addr().String()
	_ = ln0.Close()

	a := New(Config{Server: "http://" + addr, Token: "test-token", Version: "test", Logger: testLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx) }()

	// 先让 agent 在「控制端不可用」状态下跑一会儿，采样循环会立即采一次并写入环形缓冲。
	time.Sleep(1200 * time.Millisecond)
	if a.buf.Len() == 0 {
		t.Fatal("断线期间未缓存任何指标样本")
	}

	metricsCh := make(chan protocol.Metrics, 8)
	httpSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		env, ok := readEnvelope(conn, 5*time.Second)
		if !ok || env.Type != protocol.TypeHello {
			return
		}
		if err := sendHelloAck(conn, 20, 15); err != nil {
			return
		}
		for {
			env, ok := readEnvelope(conn, 5*time.Second)
			if !ok {
				return
			}
			if env.Type == protocol.TypeMetrics {
				var m protocol.Metrics
				if json.Unmarshal(env.Payload, &m) == nil && len(m.Samples) > 0 {
					select {
					case metricsCh <- m:
					default:
					}
				}
			}
		}
	})}

	var ln net.Listener
	for i := 0; i < 30; i++ {
		ln, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if ln == nil {
		t.Fatalf("无法在 %s 上监听: %v", addr, err)
	}
	go func() { _ = httpSrv.Serve(ln) }()
	defer func() { _ = httpSrv.Close() }()

	var got protocol.Metrics
	select {
	case got = <-metricsCh:
	case <-time.After(10 * time.Second):
		t.Fatal("重连后未收到补传的指标")
	}
	if len(got.Samples) == 0 {
		t.Fatal("补传的 metrics 为空")
	}
	for _, s := range got.Samples {
		if s.TS <= 0 {
			t.Errorf("补传样本的时间戳非法: %+v", s)
		}
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(10 * time.Second):
		t.Fatal("取消后 agent.Run 未退出")
	}
}

// TestCollectorShortCache 验证采集器的 CPU 百分比范围与 500ms 短期快照缓存。
func TestCollectorShortCache(t *testing.T) {
	c := NewCollector(testLogger())
	first := c.Snapshot()
	if !first.HasCPU {
		t.Fatal("首次采集未得到 CPU 使用率")
	}
	if first.CPUPct < 0 || first.CPUPct > 100 {
		t.Errorf("CPU 使用率超出 [0,100]：%v", first.CPUPct)
	}
	if first.TS <= 0 {
		t.Errorf("采集时间戳非法：%d", first.TS)
	}

	second := c.Snapshot()
	if second.TS != first.TS {
		t.Errorf("500ms 内的重复采集未复用快照：%d != %d", second.TS, first.TS)
	}

	time.Sleep(minSnapshotGap + 100*time.Millisecond)
	third := c.Snapshot()
	if third.TS == first.TS {
		t.Error("超过缓存有效期后仍返回旧快照")
	}
}

// TestCollectorExtraMetrics 验证 纳管 新增的网络累计字节与进程数指标。
func TestCollectorExtraMetrics(t *testing.T) {
	c := NewCollector(testLogger())
	snap := c.Snapshot()
	if !snap.HasNet {
		t.Error("未采集到网络累计字节指标")
	} else if snap.NetRxBytes < 0 || snap.NetTxBytes < 0 {
		t.Errorf("网络字节数非法：rx=%v tx=%v", snap.NetRxBytes, snap.NetTxBytes)
	}
	if !snap.HasProc {
		t.Error("未采集到进程数指标")
	} else if snap.ProcCount <= 0 {
		t.Errorf("进程数非法：%v", snap.ProcCount)
	}

	names := make(map[string]bool)
	for _, s := range snap.Samples() {
		names[s.Metric] = true
	}
	for _, want := range []string{
		protocol.MetricNetRxBytes,
		protocol.MetricNetTxBytes,
		protocol.MetricProcCount,
	} {
		if !names[want] {
			t.Errorf("Samples 缺少指标 %q", want)
		}
	}
}

// TestHostInfoCollection 验证主机信息采集的字段与 Docker 探测（探测结果注入）。
func TestHostInfoCollection(t *testing.T) {
	oldProbe := dockerDetect
	dockerDetect = func(*slog.Logger) (bool, string) { return true, "24.0.7-test" }
	defer func() { dockerDetect = oldProbe }()

	c := NewCollector(testLogger())
	info := c.HostInfo()
	assertHostInfo(t, info)

	if info.CPUCores != runtime.NumCPU() {
		t.Errorf("cpu_cores = %d，期望 runtime.NumCPU() = %d", info.CPUCores, runtime.NumCPU())
	}
	if info.OSVersion == "" {
		t.Error("host_info 缺少 os_version")
	}
	if !info.Docker || info.DockerVersion != "24.0.7-test" {
		t.Errorf("Docker 探测结果错误：docker=%v version=%q", info.Docker, info.DockerVersion)
	}
}

// TestHostInfoPeriodic 验证收到 hello_ack 后立即上报 host_info，并按周期重复上报；
// 同时确认主机信息采集不会阻塞心跳。
func TestHostInfoPeriodic(t *testing.T) {
	oldInterval := hostInfoInterval
	oldProbe := dockerDetect
	hostInfoInterval = 150 * time.Millisecond
	dockerDetect = func(*slog.Logger) (bool, string) { return false, "" }
	defer func() {
		hostInfoInterval = oldInterval
		dockerDetect = oldProbe
	}()

	const want = 3
	hostInfos := make(chan protocol.HostInfo, 8)
	heartbeats := make(chan protocol.Heartbeat, 8)
	srv := newMockServer(t, func(conn *websocket.Conn, _ int) {
		env, ok := readEnvelope(conn, 5*time.Second)
		if !ok || env.Type != protocol.TypeHello {
			return
		}
		if err := sendHelloAck(conn, 20, 15); err != nil {
			return
		}
		for {
			env, ok := readEnvelope(conn, 5*time.Second)
			if !ok {
				return
			}
			switch env.Type {
			case protocol.TypeHostInfo:
				var hi protocol.HostInfo
				if json.Unmarshal(env.Payload, &hi) == nil {
					select {
					case hostInfos <- hi:
					default:
					}
				}
			case protocol.TypeHeartbeat:
				var hb protocol.Heartbeat
				if json.Unmarshal(env.Payload, &hb) == nil {
					select {
					case heartbeats <- hb:
					default:
					}
				}
			}
		}
	})

	a := New(Config{Server: srv.URL, Token: "test-token", Version: "test", Logger: testLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx) }()

	for i := 0; i < want; i++ {
		select {
		case info := <-hostInfos:
			assertHostInfo(t, info)
		case <-time.After(8 * time.Second):
			t.Fatalf("等待第 %d 次 host_info 超时", i+1)
		}
	}

	// 主机信息采集（含 Docker 探测）是独立协程，心跳必须照常发送。
	select {
	case <-heartbeats:
	case <-time.After(3 * time.Second):
		t.Fatal("未收到 heartbeat，主机信息采集可能阻塞了心跳")
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run 返回非预期错误: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("context 取消后 agent.Run 未退出")
	}
}

// TestVirtualInterfaceFilter 验证虚拟/桥接接口黑名单的前缀匹配（大小写不敏感）。
func TestVirtualInterfaceFilter(t *testing.T) {
	virtual := []string{
		"docker0", "DOCKER0", "veth1a2b3c", "VethXYZ", "br-abcdef", "BR-42",
		"virbr0", "vmnet8", "utun3", "awdl0", "llw0", "bridge0", "tun0", "tap0", "p2p0", "ap1",
	}
	for _, name := range virtual {
		if !isVirtualInterface(name) {
			t.Errorf("接口 %q 应被识别为虚拟/桥接接口", name)
		}
	}
	physical := []string{"en0", "en1", "eth0", "enp0s3", "wlan0", "em0", "pdp_ip0"}
	for _, name := range physical {
		if isVirtualInterface(name) {
			t.Errorf("接口 %q 不应被识别为虚拟接口", name)
		}
	}
}

// TestNetCountersInterfaceFilter 覆盖四种情况：
// 1) 容器/虚拟接口被排除、物理接口被计入；
// 2) 多个物理接口求和（黑名单大小写不敏感）；
// 3) 物理接口全被排除时跳过 net 指标（ok=false，而不是上报 0）；
// 4) 有物理接口但计数器缺失时同样跳过。
func TestNetCountersInterfaceFilter(t *testing.T) {
	mkIface := func(name string, flags net.Flags) net.Interface {
		return net.Interface{Name: name, Flags: flags}
	}
	mkCounter := func(name string, rx, tx uint64) gnet.IOCountersStat {
		return gnet.IOCountersStat{Name: name, BytesRecv: rx, BytesSent: tx}
	}

	// 情况 1：只有 en0 是物理接口，回环/容器/桥接/隧道/点到点全部排除。
	ifaces := []net.Interface{
		mkIface("lo0", net.FlagUp|net.FlagLoopback),
		mkIface("docker0", net.FlagUp),
		mkIface("veth1a2b3c", net.FlagUp),
		mkIface("br-abcdef", net.FlagUp),
		mkIface("en0", net.FlagUp|net.FlagBroadcast),
		mkIface("utun3", net.FlagUp|net.FlagPointToPoint),
		mkIface("awdl0", net.FlagUp),
	}
	counters := []gnet.IOCountersStat{
		mkCounter("lo0", 100, 100),
		mkCounter("docker0", 1000, 2000),
		mkCounter("veth1a2b3c", 3000, 4000),
		mkCounter("br-abcdef", 5000, 6000),
		mkCounter("en0", 10, 20),
		mkCounter("utun3", 7000, 8000),
		mkCounter("awdl0", 9000, 10000),
	}
	rx, tx, ok := sumPhysicalNetCounters(ifaces, counters)
	if !ok {
		t.Fatal("存在物理接口 en0 时不应跳过 net 指标")
	}
	if rx != 10 || tx != 20 {
		t.Errorf("net 汇总错误：rx=%v tx=%v，期望 10/20", rx, tx)
	}

	// 情况 2：多个物理接口求和；黑名单大小写不敏感（DOCKER0 仍被排除）。
	ifaces = []net.Interface{
		mkIface("en0", net.FlagUp|net.FlagBroadcast),
		mkIface("en7", net.FlagUp|net.FlagBroadcast),
		mkIface("DOCKER0", net.FlagUp),
	}
	counters = []gnet.IOCountersStat{
		mkCounter("en0", 1, 2),
		mkCounter("en7", 3, 4),
		mkCounter("DOCKER0", 100, 100),
	}
	rx, tx, ok = sumPhysicalNetCounters(ifaces, counters)
	if !ok || rx != 4 || tx != 6 {
		t.Errorf("多物理接口汇总错误：ok=%v rx=%v tx=%v，期望 true/4/6", ok, rx, tx)
	}

	// 情况 3：物理接口全被排除（只剩回环、容器与虚拟接口）→ 跳过指标，不上报 0。
	ifaces = []net.Interface{
		mkIface("lo", net.FlagUp|net.FlagLoopback),
		mkIface("docker0", net.FlagUp),
		mkIface("veth99", net.FlagUp),
		mkIface("virbr0", net.FlagUp),
	}
	counters = []gnet.IOCountersStat{
		mkCounter("lo", 1, 1),
		mkCounter("docker0", 2, 2),
		mkCounter("veth99", 3, 3),
	}
	if rx, tx, ok = sumPhysicalNetCounters(ifaces, counters); ok {
		t.Errorf("物理接口全被排除时必须跳过 net 指标，实际 rx=%v tx=%v", rx, tx)
	}

	// 情况 4：有物理接口但计数器缺失 → 同样跳过。
	if rx, tx, ok = sumPhysicalNetCounters([]net.Interface{mkIface("en0", net.FlagUp)}, nil); ok {
		t.Errorf("计数器缺失时必须跳过 net 指标，实际 rx=%v tx=%v", rx, tx)
	}
}
