package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/maliang/leadstack/pkg/protocol"
)

// makeTestExe 在临时目录创建一个假的 agent 可执行文件。
func makeTestExe(t *testing.T, content string) string {
	t.Helper()
	exePath := filepath.Join(t.TempDir(), "leadstack-agent")
	if err := os.WriteFile(exePath, []byte(content), 0o755); err != nil {
		t.Fatalf("写入测试可执行文件失败: %v", err)
	}
	return exePath
}

// sha256Hex 返回内容的 sha256 十六进制串。
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// readFileString 读取文件内容（失败即 Fatal）。
func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取文件 %s 失败: %v", path, err)
	}
	return string(data)
}

// dirEntries 返回目录下的文件名列表。
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录 %s 失败: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestReplaceExecutableAB 验证 A/B 双文件替换：新文件到位、旧文件保留在 .old。
func TestReplaceExecutableAB(t *testing.T) {
	exePath := makeTestExe(t, "OLD")
	tmpPath := filepath.Join(filepath.Dir(exePath), ".leadstack-agent-new-test")
	if err := os.WriteFile(tmpPath, []byte("NEW"), 0o755); err != nil {
		t.Fatalf("写入临时新文件失败: %v", err)
	}

	oldPath, err := replaceExecutable(exePath, tmpPath)
	if err != nil {
		t.Fatalf("replaceExecutable 失败: %v", err)
	}
	if want := exePath + ".old"; oldPath != want {
		t.Errorf("oldPath = %q，期望 %q", oldPath, want)
	}
	if got := readFileString(t, exePath); got != "NEW" {
		t.Errorf("正式文件内容 = %q，期望 NEW", got)
	}
	if got := readFileString(t, oldPath); got != "OLD" {
		t.Errorf("备份文件内容 = %q，期望 OLD", got)
	}
	if _, err := os.Stat(tmpPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("临时文件未被 rename 走：%v", err)
	}
}

// TestReplaceExecutableRollback 验证第二次 rename 失败时回滚，旧文件仍是正式文件。
func TestReplaceExecutableRollback(t *testing.T) {
	exePath := makeTestExe(t, "OLD")
	tmpPath := filepath.Join(filepath.Dir(exePath), ".leadstack-agent-new-test")
	if err := os.WriteFile(tmpPath, []byte("NEW"), 0o755); err != nil {
		t.Fatalf("写入临时新文件失败: %v", err)
	}

	origRename := renameFile
	defer func() { renameFile = origRename }()

	var calls int
	renameFile = func(oldPath, newPath string) error {
		calls++
		if calls == 2 { // exePath -> old 之后，tmp -> exePath 失败
			return errors.New("模拟 rename 失败")
		}
		return os.Rename(oldPath, newPath)
	}

	if _, err := replaceExecutable(exePath, tmpPath); err == nil {
		t.Fatal("第二次 rename 失败时应返回 error")
	}
	if got := readFileString(t, exePath); got != "OLD" {
		t.Errorf("回滚后正式文件内容 = %q，期望 OLD", got)
	}
	if _, err := os.Stat(exePath + ".old"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("回滚后不应残留 .old 文件")
	}
}

// newUpgradeTestUpgrader 构造一个可注入路径/网络的 upgrader。
func newUpgradeTestUpgrader(exePath string, client *http.Client) *upgrader {
	return &upgrader{
		log:        testLogger(),
		exePath:    exePath,
		httpClient: client,
		restart: func(string) error {
			return errors.New("测试不应真正重启")
		},
	}
}

// TestPerformUpgradeSuccess 验证下载 + sha256 校验 + A/B 替换成功。
func TestPerformUpgradeSuccess(t *testing.T) {
	exePath := makeTestExe(t, "OLD-VERSION")
	newContent := []byte("NEW-VERSION-BINARY")

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(newContent)
	}))
	defer srv.Close()

	u := newUpgradeTestUpgrader(exePath, srv.Client())
	out, err := u.perform(context.Background(), "v1", protocol.AgentUpgrade{
		Version: "v2",
		URL:     srv.URL,
		SHA256:  sha256Hex(newContent),
	})
	if err != nil {
		t.Fatalf("perform 失败: %v", err)
	}
	if out == nil || out.Skipped {
		t.Fatalf("outcome 异常: %+v", out)
	}
	if got := readFileString(t, exePath); got != string(newContent) {
		t.Errorf("正式文件内容 = %q，期望新版本", got)
	}
	if got := readFileString(t, exePath+".old"); got != "OLD-VERSION" {
		t.Errorf("备份内容 = %q，期望旧版本", got)
	}
	info, err := os.Stat(exePath)
	if err != nil {
		t.Fatalf("stat 正式文件失败: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("正式文件权限 = %o，期望 755", perm)
	}
	if hits.Load() != 1 {
		t.Errorf("下载请求次数 = %d，期望 1", hits.Load())
	}
}

// TestPerformUpgradeSHA256MismatchKeepsOld 验证 sha256 校验失败时保留旧版本、清理临时文件。
func TestPerformUpgradeSHA256MismatchKeepsOld(t *testing.T) {
	exePath := makeTestExe(t, "OLD-VERSION")
	dir := filepath.Dir(exePath)
	newContent := []byte("MALICIOUS-PAYLOAD")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(newContent)
	}))
	defer srv.Close()

	u := newUpgradeTestUpgrader(exePath, srv.Client())
	_, err := u.perform(context.Background(), "v1", protocol.AgentUpgrade{
		Version: "v2",
		URL:     srv.URL,
		SHA256:  strings.Repeat("0", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("期望 sha256 校验错误，实际: %v", err)
	}
	if got := readFileString(t, exePath); got != "OLD-VERSION" {
		t.Errorf("校验失败后旧版本被破坏：%q", got)
	}
	if _, err := os.Stat(exePath + ".old"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("校验失败后不应产生 .old")
	}
	if names := dirEntries(t, dir); len(names) != 1 {
		t.Errorf("校验失败后临时文件未清理，目录内容: %v", names)
	}
}

// TestPerformUpgradeDownloadFailureKeepsOld 验证下载失败时保留旧版本。
func TestPerformUpgradeDownloadFailureKeepsOld(t *testing.T) {
	exePath := makeTestExe(t, "OLD-VERSION")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	u := newUpgradeTestUpgrader(exePath, srv.Client())
	_, err := u.perform(context.Background(), "v1", protocol.AgentUpgrade{
		Version: "v2",
		URL:     srv.URL,
		SHA256:  strings.Repeat("a", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("期望下载失败错误，实际: %v", err)
	}
	if got := readFileString(t, exePath); got != "OLD-VERSION" {
		t.Errorf("下载失败后旧版本被破坏：%q", got)
	}
}

// TestPerformUpgradeSameVersionSkipped 验证目标版本与当前一致时不下载、不改文件。
func TestPerformUpgradeSameVersionSkipped(t *testing.T) {
	exePath := makeTestExe(t, "OLD-VERSION")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	u := newUpgradeTestUpgrader(exePath, srv.Client())
	out, err := u.perform(context.Background(), "v2", protocol.AgentUpgrade{
		Version: "v2",
		URL:     srv.URL,
		SHA256:  strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("perform 失败: %v", err)
	}
	if out == nil || !out.Skipped {
		t.Fatalf("同版本应返回 Skipped，实际 %+v", out)
	}
	if hits.Load() != 0 {
		t.Errorf("同版本不应发起下载，实际请求 %d 次", hits.Load())
	}
	if got := readFileString(t, exePath); got != "OLD-VERSION" {
		t.Errorf("同版本不应修改文件，实际 %q", got)
	}
}

// TestPerformUpgradeRejectsBadInput 验证非法 URL / sha256 被拒绝且不动文件。
func TestPerformUpgradeRejectsBadInput(t *testing.T) {
	exePath := makeTestExe(t, "OLD-VERSION")
	u := newUpgradeTestUpgrader(exePath, &http.Client{Timeout: time.Second})

	cases := []protocol.AgentUpgrade{
		{Version: "v2", URL: "ftp://example.com/a", SHA256: strings.Repeat("a", 64)},
		{Version: "v2", URL: "http://example.com/a", SHA256: ""},
		{Version: "v2", URL: "http://example.com/a", SHA256: "not-hex"},
		{Version: "v2", URL: "", SHA256: strings.Repeat("a", 64)},
	}
	for _, spec := range cases {
		if _, err := u.perform(context.Background(), "v1", spec); err == nil {
			t.Errorf("非法升级参数应报错: %+v", spec)
		}
	}
	if got := readFileString(t, exePath); got != "OLD-VERSION" {
		t.Errorf("非法参数不应修改文件，实际 %q", got)
	}
}

// waitUpgradeResult 从通道等待升级结果帧。
func waitUpgradeResult(t *testing.T, ch <-chan protocol.JobDone, timeout time.Duration) protocol.JobDone {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(timeout):
		t.Fatal("等待升级结果 job.done 超时")
		return protocol.JobDone{}
	}
}

// TestAgentUpgradeEndToEnd 通过 mock 控制端验证：
// 成功升级替换文件；sha 校验失败保留新版本（即当前版本）且连接不断（仍有心跳）。
func TestAgentUpgradeEndToEnd(t *testing.T) {
	dir := t.TempDir()
	exePath := filepath.Join(dir, "leadstack-agent")
	if err := os.WriteFile(exePath, []byte("V1-BINARY"), 0o755); err != nil {
		t.Fatalf("写入假可执行文件失败: %v", err)
	}
	newContent := []byte("V2-BINARY")
	sum := sha256Hex(newContent)

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(newContent)
	}))
	defer httpSrv.Close()

	doneCh := make(chan protocol.JobDone, 4)
	heartbeats := make(chan struct{}, 16)

	srv := newMockServer(t, func(conn *websocket.Conn, _ int) {
		env, ok := readEnvelope(conn, 5*time.Second)
		if !ok || env.Type != protocol.TypeHello {
			return
		}
		// 心跳 1s，便于验证失败升级后连接仍然存活。
		if err := sendHelloAck(conn, 1, 15); err != nil {
			return
		}

		collectDone := func() {
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				env, ok := readEnvelope(conn, time.Until(deadline))
				if !ok {
					return
				}
				switch env.Type {
				case protocol.TypeHeartbeat:
					select {
					case heartbeats <- struct{}{}:
					default:
					}
				case protocol.TypeJobDone:
					var d protocol.JobDone
					if json.Unmarshal(env.Payload, &d) == nil && strings.HasPrefix(d.RunID, upgradeRunIDPrefix) {
						doneCh <- d
						return
					}
				}
			}
		}

		// 第一次：成功升级。
		if err := writeEnvelope(conn, protocol.TypeAgentUpgrade, protocol.AgentUpgrade{
			Version: "v2",
			URL:     httpSrv.URL,
			SHA256:  sum,
			Restart: false,
		}); err != nil {
			return
		}
		collectDone()

		// 第二次：sha 错误，必须失败并保留当前文件。
		if err := writeEnvelope(conn, protocol.TypeAgentUpgrade, protocol.AgentUpgrade{
			Version: "v3",
			URL:     httpSrv.URL,
			SHA256:  strings.Repeat("0", 64),
			Restart: false,
		}); err != nil {
			return
		}
		collectDone()

		// 之后继续读取，确认连接未断（收集心跳直到测试取消）。
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			env, ok := readEnvelope(conn, time.Until(deadline))
			if !ok {
				return
			}
			if env.Type == protocol.TypeHeartbeat {
				select {
				case heartbeats <- struct{}{}:
				default:
				}
			}
		}
	})

	a := New(Config{Server: srv.URL, Token: "test-token", Version: "v1", Logger: testLogger()})
	a.upgrader.exePath = exePath
	a.upgrader.httpClient = httpSrv.Client()
	a.upgrader.restart = func(string) error {
		t.Error("Restart=false 时不应调用重启")
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx) }()

	first := waitUpgradeResult(t, doneCh, 15*time.Second)
	if first.ExitCode != 0 || first.Error != "" {
		t.Fatalf("成功升级的结果帧异常: %+v", first)
	}
	if got := readFileString(t, exePath); got != string(newContent) {
		t.Fatalf("成功升级后文件内容 = %q，期望新版本", got)
	}

	second := waitUpgradeResult(t, doneCh, 15*time.Second)
	if second.ExitCode == 0 || !strings.Contains(second.Error, "sha256") {
		t.Fatalf("sha 校验失败应返回失败结果，实际: %+v", second)
	}
	if got := readFileString(t, exePath); got != string(newContent) {
		t.Fatalf("失败升级不应破坏当前版本，实际内容 %q", got)
	}

	// 失败升级后连接必须仍然存活：等待一条心跳。
	select {
	case <-heartbeats:
	case <-time.After(5 * time.Second):
		t.Fatal("升级失败后连接疑似断开（未再收到心跳）")
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(10 * time.Second):
		t.Fatal("context 取消后 agent.Run 未退出")
	}
}
