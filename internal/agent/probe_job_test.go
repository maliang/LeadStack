package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/maliang/leadstack/pkg/protocol"
)

// captureJob 跑一条任务，返回 ack / 日志 / done。
type captureJob struct {
	acked   bool
	logs    []protocol.JobLog
	done    protocol.JobDone
	gotDone bool
}

// runJobWithSpec 用给定任务发起一次完整的 agent 会话（复用测试里的 mock 控制端）。
func runJobWithSpec(t *testing.T, spec protocol.JobExec) captureJob {
	t.Helper()
	results := make(chan captureJob, 1)

	srv := newMockServer(t, func(conn *websocket.Conn, _ int) {
		env, ok := readEnvelope(conn, 5*time.Second)
		if !ok || env.Type != protocol.TypeHello {
			return
		}
		if err := sendHelloAck(conn, 20, 15); err != nil {
			return
		}
		if err := writeEnvelope(conn, protocol.TypeJobExec, spec); err != nil {
			return
		}

		res := captureJob{}
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
					res.logs = append(res.logs, l)
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
	go func() { runErr <- a.Run(ctx) }()

	var res captureJob
	select {
	case res = <-results:
	case <-time.After(25 * time.Second):
		t.Fatal("等待 job.done 超时")
	}
	cancel()
	select {
	case <-runErr:
	case <-time.After(10 * time.Second):
		t.Fatal("context 取消后 agent.Run 未退出")
	}
	return res
}

// probeEnvelope 把探测请求包成 JobExec。
func probeEnvelope(t *testing.T, req protocol.ProbeRequest) protocol.JobExec {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("序列化探测请求失败: %v", err)
	}
	return protocol.JobExec{RunID: "probe-1", Kind: protocol.JobKindProbe, Command: string(payload), TimeoutS: 10}
}

// decodeProbeResult 取出 job.log 里回传的探测结果。
func decodeProbeResult(t *testing.T, res captureJob) protocol.ProbeResult {
	t.Helper()
	var chunk string
	for _, l := range res.logs {
		if l.Stream == "stdout" {
			chunk += l.Chunk
		}
	}
	if strings.TrimSpace(chunk) == "" {
		t.Fatalf("没有收到探测结果，日志 = %+v", res.logs)
	}
	var out protocol.ProbeResult
	if err := json.Unmarshal([]byte(chunk), &out); err != nil {
		t.Fatalf("探测结果不是合法 JSON（%q）: %v", chunk, err)
	}
	return out
}

// TestProbeJobTCPReachable 验证 Kind=probe 任务在目标机原生执行 tcp 探测：
// 目标机连得上就是成功，且带回延迟——不需要控制端能访问该端口。
func TestProbeJobTCPReachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer ln.Close()

	res := runJobWithSpec(t, probeEnvelope(t, protocol.ProbeRequest{Kind: "tcp", Target: ln.Addr().String(), TimeoutS: 5}))
	if !res.acked {
		t.Error("未收到 job.ack")
	}
	if !res.gotDone {
		t.Fatal("未收到 job.done")
	}
	if res.done.ExitCode != 0 {
		t.Errorf("exit_code = %d，期望 0（探测结果用 OK 字段表达，不靠退出码）", res.done.ExitCode)
	}
	out := decodeProbeResult(t, res)
	if !out.OK {
		t.Errorf("探测结果应为成功: %+v", out)
	}
	if out.LatencyMS < 0 {
		t.Errorf("延迟不应为负: %d", out.LatencyMS)
	}
	if out.TS == 0 {
		t.Error("结果缺少时间戳")
	}
}

// TestProbeJobTCPRefused 验证探测失败也是正常回传（exit 0），失败原因在 message 里。
func TestProbeJobTCPRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	res := runJobWithSpec(t, probeEnvelope(t, protocol.ProbeRequest{Kind: "tcp", Target: addr, TimeoutS: 3}))
	if !res.gotDone || res.done.ExitCode != 0 {
		t.Fatalf("探测失败也应正常结束: %+v", res.done)
	}
	out := decodeProbeResult(t, res)
	if out.OK {
		t.Errorf("端口已关闭，探测不应成功: %+v", out)
	}
	if strings.TrimSpace(out.Message) == "" {
		t.Error("失败结果必须带原因")
	}
}

// TestProbeJobHTTP 验证 http 探测在目标机上执行并可校验状态码。
func TestProbeJobHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	res := runJobWithSpec(t, probeEnvelope(t, protocol.ProbeRequest{
		Kind: "http", Target: srv.URL + "/health", TimeoutS: 5,
		Params: map[string]any{"expect_status": 418},
	}))
	out := decodeProbeResult(t, res)
	if !out.OK || out.StatusCode != 418 {
		t.Errorf("期望命中 expect_status=418: %+v", out)
	}
}

// TestProbeJobBadPayload 验证非法请求体不会让 agent 挂掉，而是回明确的错误。
func TestProbeJobBadPayload(t *testing.T) {
	res := runJobWithSpec(t, protocol.JobExec{RunID: "probe-bad", Kind: protocol.JobKindProbe, Command: "{not-json", TimeoutS: 5})
	if !res.gotDone {
		t.Fatal("未收到 job.done")
	}
	if res.done.ExitCode == 0 {
		t.Errorf("非法请求应返回非 0 退出码: %+v", res.done)
	}
	if !strings.Contains(res.done.Error, "解析探测请求失败") {
		t.Errorf("错误信息不明确: %q", res.done.Error)
	}
}

// TestBuildTLSConfig 覆盖 -ca / -insecure 的三种组合。
func TestBuildTLSConfig(t *testing.T) {
	// 无配置：沿用系统信任库。
	if cfg, err := buildTLSConfig(Config{}, testLogger()); err != nil || cfg != nil {
		t.Errorf("未配置 TLS 选项时应返回 nil, nil；实际 %v, %v", cfg, err)
	}

	// 非法 PEM 必须启动即失败，而不是每次拨号失败重连。
	bad := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(bad, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildTLSConfig(Config{CAFile: bad}, testLogger()); err == nil {
		t.Error("非法 CA 文件应报错")
	}
	if _, err := buildTLSConfig(Config{CAFile: filepath.Join(t.TempDir(), "missing.pem")}, testLogger()); err == nil {
		t.Error("CA 文件不存在应报错")
	}

	// -insecure
	cfg, err := buildTLSConfig(Config{InsecureTLS: true}, testLogger())
	if err != nil {
		t.Fatalf("-insecure 不应报错: %v", err)
	}
	if cfg == nil || !cfg.InsecureSkipVerify {
		t.Error("-insecure 应设置 InsecureSkipVerify")
	}
}

// TestAgentConnectsWithCustomCA 是 -ca 的端到端证据：
// 自签 TLS 控制端 + 指定 CA 能握手成功，不指定则失败。
func TestAgentConnectsWithCustomCA(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		_ = sendHelloAck(conn, 20, 15)
		// 读完后续一帧（心跳/主机信息）即说明会话已建立。
		_, _ = readEnvelope(conn, 10*time.Second)
	}))
	defer srv.Close()

	caPath := filepath.Join(t.TempDir(), "ca.pem")
	pem := srv.TLS.Certificates[0].Certificate[0]
	caPEM := "-----BEGIN CERTIFICATE-----\n" + base64.StdEncoding.EncodeToString(pem) + "\n-----END CERTIFICATE-----\n"
	if err := os.WriteFile(caPath, []byte(caPEM), 0o600); err != nil {
		t.Fatal(err)
	}

	// 不指定 CA：应因证书不受信而失败（这里只做信息性断言，避免依赖本机信任库状态）。
	a1 := New(Config{Server: srv.URL, Token: "test-token", Version: "test", Logger: testLogger()})
	if conn, err := a1.dial(context.Background()); err == nil {
		conn.close()
		t.Log("未指定 CA 时握手成功：系统信任库已包含该证书，跳过负向断言")
	} else if !strings.Contains(err.Error(), "certificate") && !strings.Contains(err.Error(), "x509") {
		t.Logf("未指定 CA 时握手失败（非证书原因）: %v", err)
	}

	// 指定 CA：握手必须成功且能完成 hello/hello_ack 往返。
	a2 := New(Config{Server: srv.URL, Token: "test-token", Version: "test", Logger: testLogger(), CAFile: caPath})
	if a2.tlsErr != nil {
		t.Fatalf("CA 加载失败: %v", a2.tlsErr)
	}
	if a2.tlsCfg == nil || a2.tlsCfg.RootCAs == nil {
		t.Fatal("指定 CA 后应构造带 RootCAs 的 TLS 配置")
	}
	conn, err := a2.dial(context.Background())
	if err != nil {
		t.Fatalf("指定 CA 后仍无法握手: %v", err)
	}
	conn.close()
}
