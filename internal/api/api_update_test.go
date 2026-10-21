package api

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestSemverAndMarkers 覆盖版本比较与 release body 标记解析。
func TestSemverAndMarkers(t *testing.T) {
	cases := []struct {
		candidate, current string
		newer              bool
	}{
		{"v0.3.0", "v0.2.0", true},
		{"v0.2.0", "v0.3.0", false},
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.1", "v1.0.0", true},
		{"v2.0.0", "v1.9.9", true},
		{"v1.0.0-rc1", "v1.0.0", false},
		{"v1.0.0", "v1.0.0-rc1", true},
	}
	for _, tc := range cases {
		if got := versionIsNewer(tc.candidate, tc.current); got != tc.newer {
			t.Fatalf("%s > %s 期望 %t，实际 %t", tc.candidate, tc.current, tc.newer, got)
		}
	}
	// dev 当前版本：任何合法标签都算更新
	if !versionIsNewer("v0.1.0", "dev") {
		t.Fatal("dev 版本应视为可升级")
	}
	// schema 标记
	if !parseSchemaChangedMarker("changes\n<!-- schema_changed: true -->\n") {
		t.Fatal("应识别 schema_changed: true")
	}
	if parseSchemaChangedMarker("<!-- schema_changed: false -->") {
		t.Fatal("false 不应识别为有迁移")
	}
	if parseSchemaChangedMarker("no marker here") {
		t.Fatal("无标记应为 false")
	}
	// notes 截断到 4KB
	long := strings.Repeat("说", 3000) // 9000 字节
	if got := truncateNotes(long); len(got) > upgradeNotesMaxBytes+64 {
		t.Fatalf("notes 未截断: %d 字节", len(got))
	}
}

// TestLookupChecksum 覆盖 checksums.txt 解析。
func TestLookupChecksum(t *testing.T) {
	content := "abc123  leadstack_linux_amd64.tar.gz\nDEF456 *other.zip\n"
	if got := lookupChecksum(content, "leadstack_linux_amd64.tar.gz"); got != "abc123" {
		t.Fatalf("校验和解析错误: %q", got)
	}
	if got := lookupChecksum(content, "other.zip"); got != "DEF456" {
		t.Fatalf("带 * 前缀的条目解析错误: %q", got)
	}
	if got := lookupChecksum(content, "missing.tgz"); got != "" {
		t.Fatalf("不存在的文件应返回空: %q", got)
	}
	if got := checksumsURLFor(UpdateAsset{URL: "http://x/dl/v1.tar.gz"}); got != "http://x/dl/checksums.txt" {
		t.Fatalf("checksums 地址推导错误: %q", got)
	}
}

// TestPlatformFromAssetName 覆盖产物文件名推断。
func TestPlatformFromAssetName(t *testing.T) {
	cases := map[string][2]string{
		"leadstack_linux_amd64.tar.gz":  {"linux", "amd64"},
		"leadstack_darwin_arm64.tar.gz": {"darwin", "arm64"},
		"leadstack_windows_386.zip":     {"windows", "386"},
		"checksums.txt":                 {"", ""},
	}
	for name, want := range cases {
		osName, arch := platformFromAssetName(name)
		if osName != want[0] || arch != want[1] {
			t.Fatalf("%s → %s/%s，期望 %v", name, osName, arch, want)
		}
	}
}

// TestUpdateCheckWithMock 覆盖检查更新：正常/缓存/失败保留旧结果/资产可见。
func TestUpdateCheckWithMock(t *testing.T) {
	assetURL := ""
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/maliang/leadstack/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v0.3.0", "html_url": "https://example.com/release",
			"body":         "fixes\n<!-- schema_changed: true -->",
			"published_at": time.Now().UTC().Format(time.RFC3339),
			"draft":        false,
			"prerelease":   false,
			"assets": []map[string]any{
				{"name": fmt.Sprintf("leadstack_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH),
					"browser_download_url": assetURL, "size": 1234},
				{"name": "checksums.txt", "browser_download_url": assetURL + ".sum", "size": 10},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	assetURL = srv.URL + "/dl/leadstack.tar.gz"

	checker := newUpdateChecker("maliang/leadstack", srv.URL, "v0.2.0", time.Hour, noopLogger{})
	info := checker.Get(context.Background(), true)
	if info.Error != "" {
		t.Fatalf("检查失败: %s", info.Error)
	}
	if info.Current != "v0.2.0" || info.Latest != "v0.3.0" || !info.HasUpdate {
		t.Fatalf("检查结果不正确: %+v", info)
	}
	if !info.SchemaChanged {
		t.Fatalf("应识别 schema_changed: %+v", info)
	}
	if !info.Upgradable || len(info.Assets) != 1 {
		// checksums.txt 没有 os/arch，不应出现在 assets 里
		t.Fatalf("资产解析不正确: %+v", info.Assets)
	}
	if info.Assets[0].OS != runtime.GOOS || info.Assets[0].Arch != runtime.GOARCH {
		t.Fatalf("资产平台不正确: %+v", info.Assets[0])
	}
	if info.PublishedAt == 0 || info.ReleaseURL == "" || info.CheckedAt == 0 {
		t.Fatalf("字段缺失: %+v", info)
	}

	// 缓存命中：不重复请求（间隔 1h）
	srv.Close()
	cached := checker.Get(context.Background(), false)
	if cached.Error != "" || cached.Latest != "v0.3.0" {
		t.Fatalf("缓存应返回旧结果: %+v", cached)
	}
	// 强制刷新失败：保留旧数据 + error 非空
	failed := checker.Get(context.Background(), true)
	if failed.Error == "" {
		t.Fatal("强制刷新失败应带 error")
	}
	if failed.Latest != "v0.3.0" || !failed.HasUpdate {
		t.Fatalf("失败时应保留上次结果: %+v", failed)
	}
}

// noopLogger 是测试用日志器。
type noopLogger struct{}

func (noopLogger) Warn(string, ...any)  {}
func (noopLogger) Info(string, ...any)  {}
func (noopLogger) Debug(string, ...any) {}

// TestApplyGuards 覆盖开关/平台/并发/备份失败等防护。
func TestApplyGuards(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	// 禁用升级 → 403
	env.api.cfg.UpgradeEnabled = false
	status, raw := env.request(http.MethodPost, "/api/v1/system/update/apply", jwt,
		map[string]any{"version": "v0.9.9"})
	if status != http.StatusForbidden || !strings.Contains(string(raw), "UPGRADE_ENABLED") {
		t.Fatalf("禁用升级期望 403，实际 %d（body=%s）", status, raw)
	}
	env.api.cfg.UpgradeEnabled = true

	// 并发 → 409
	env.api.upgrader.mu.Lock()
	env.api.upgrader.running = true
	env.api.upgrader.mu.Unlock()
	status, raw = env.request(http.MethodPost, "/api/v1/system/update/apply", jwt,
		map[string]any{"version": "v0.9.9"})
	if status != http.StatusConflict {
		t.Fatalf("并发升级期望 409，实际 %d（body=%s）", status, raw)
	}
	env.api.upgrader.mu.Lock()
	env.api.upgrader.running = false
	env.api.upgrader.mu.Unlock()

	// viewer → 403（路由层）
	env.createUserVia(jwt, "vw-up", "viewer-pass-123", "viewer")
	vw := env.loginAs(t, "vw-up", "viewer-pass-123")
	status, _ = env.request(http.MethodPost, "/api/v1/system/update/apply", vw, map[string]any{"version": "v0.9.9"})
	if status != http.StatusForbidden {
		t.Fatalf("viewer 升级期望 403，实际 %d", status)
	}

	// 版本不高于当前 → 400
	status, raw = env.request(http.MethodPost, "/api/v1/system/update/apply", jwt,
		map[string]any{"version": "v0.0.0"})
	if status != http.StatusBadRequest {
		t.Fatalf("旧版本期望 400，实际 %d（body=%s）", status, raw)
	}
}

// TestApplyAbortsWhenBackupFails 覆盖「备份失败绝不替换二进制」。
func TestApplyAbortsWhenBackupFails(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	// 让备份失败
	env.backup.mu.Lock()
	env.backup.createErr = errors.New("disk full")
	env.backup.mu.Unlock()

	// 伪造更新信息（指向本地 mock 资产）
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("stub"))
	}))
	defer mock.Close()
	env.api.updates = &updateChecker{
		repo: "maliang/leadstack", apiBase: mock.URL, currentVersion: "v0.0.1",
		client: mock.Client(), logger: noopLogger{},
		cached: &UpdateInfo{
			Current: "v0.0.1", Latest: "v0.9.9", HasUpdate: true, Upgradable: true,
			Assets:    []UpdateAsset{{OS: runtime.GOOS, Arch: runtime.GOARCH, URL: mock.URL + "/dl/new.tar.gz"}},
			CheckedAt: time.Now().UnixMilli(),
		},
	}
	// 记录当前 exe 状态（测试进程本身不能真的被替换）
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("定位可执行文件失败: %v", err)
	}
	before, err := os.Stat(exe)
	if err != nil {
		t.Fatalf("stat 失败: %v", err)
	}

	status, raw := env.request(http.MethodPost, "/api/v1/system/update/apply", jwt,
		map[string]any{"version": "v0.9.9"})
	if status != http.StatusAccepted {
		t.Fatalf("apply 应返回 202（后台执行），实际 %d（body=%s）", status, raw)
	}
	// 等待后台失败
	deadline := time.Now().Add(10 * time.Second)
	var job *upgradeJob
	for time.Now().Before(deadline) {
		job = env.api.upgrader.getJob()
		if job != nil && job.Done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if job == nil || !job.Done {
		t.Fatal("升级任务未在 10s 内结束")
	}
	if job.Error == "" || !strings.Contains(job.Error, "备份失败") {
		t.Fatalf("应因备份失败中止: %+v", job)
	}
	// 关键：二进制没被动过
	after, err := os.Stat(exe)
	if err != nil {
		t.Fatalf("stat 失败: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("备份失败时不应替换二进制")
	}
	if _, err := os.Stat(exe + upgradePrevSuffix); err == nil {
		t.Fatal("备份失败时不应产生 .prev")
	}
	// 状态里的 phase 应是 backup
	if job.Phase != "backup" {
		t.Fatalf("应停在 backup 阶段，实际 %q", job.Phase)
	}
}

// TestExtractBinaryAndSelfCheck 覆盖解包与自检判定。
func TestExtractBinaryAndSelfCheck(t *testing.T) {
	env := newTestEnv(t)
	// 造一个含可执行文件的 tar.gz
	dir := t.TempDir()
	archive := filepath.Join(dir, "v0.9.9.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatalf("创建归档失败: %v", err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	payload := []byte("#!/bin/sh\necho fake\n")
	if err := tw.WriteHeader(&tar.Header{Name: "leadstack", Mode: 0o755, Size: int64(len(payload))}); err != nil {
		t.Fatalf("写头失败: %v", err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatalf("写内容失败: %v", err)
	}
	_ = tw.Close()
	_ = gz.Close()
	_ = file.Close()

	extracted, err := env.api.extractBinary(archive, "v0.9.9")
	if err != nil {
		t.Fatalf("解包失败: %v", err)
	}
	if !isExecutable(extracted) {
		t.Fatal("解出的文件应可执行")
	}
	defer func() { _ = os.Remove(extracted) }()

	// 自检只在新版本接管时执行：先写一份 state.json 模拟「刚升级完」。
	statePath := upgradeStatePath(env.cfg.DataDir)
	if err := WriteUpgradeState(statePath, &UpgradeState{
		TargetVersion: "v0.9.9", FromVersion: "v0.0.1", NewExe: extracted,
		DataDir: env.cfg.DataDir, StartedAt: 1, Deadline: 2,
	}); err != nil {
		t.Fatalf("写升级状态失败: %v", err)
	}

	// 自检：schema 版本不匹配 → 不通过（state.json 必须保留，watchdog 据此回退）
	result, _, err := RunUpgradeSelfCheck(context.Background(), env.cfg.DBPath, env.cfg.DataDir, 999)
	if err != nil {
		t.Fatalf("自检执行失败: %v", err)
	}
	if result.Passed {
		t.Fatal("schema 版本不匹配时自检不应通过")
	}
	if !result.IntegrityOK {
		t.Fatalf("新库完整性检查应通过: %+v", result)
	}
	if state, _ := ReadUpgradeState(statePath); state == nil {
		t.Fatal("自检失败时不应删除 state.json")
	}
	// 版本匹配 → 通过
	result, _, err = RunUpgradeSelfCheck(context.Background(), env.cfg.DBPath, env.cfg.DataDir, 7)
	if err != nil {
		t.Fatalf("自检执行失败: %v", err)
	}
	if !result.Passed {
		t.Fatalf("schema 匹配时自检应通过: %+v", result)
	}
	if state, _ := ReadUpgradeState(statePath); state != nil {
		t.Fatal("自检通过后应删除 state.json（watchdog 据此认定成功）")
	}
}

// TestUpgradeStateRoundTrip 覆盖 state.json/history 读写。
func TestUpgradeStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := upgradeStatePath(dir)
	state := &UpgradeState{
		TargetVersion: "v0.3.0", FromVersion: "v0.2.0", PrevExe: "/x/leadstack.prev",
		NewExe: "/x/leadstack", BackupFile: "/x/b.lsbak", SchemaChanged: true,
		StartedAt: 1, Deadline: 2, Args: []string{"leadstack", "--listen", "127.0.0.1:1"},
		DataDir: dir, ActiveListen: "127.0.0.1:1",
	}
	if err := WriteUpgradeState(path, state); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}
	got, err := ReadUpgradeState(path)
	if err != nil || got == nil || got.TargetVersion != "v0.3.0" || len(got.Args) != 3 {
		t.Fatalf("状态读回不正确: %+v %v", got, err)
	}
	// history
	if err := AppendUpgradeHistory(dir, &UpgradeHistoryEntry{TS: 1, Outcome: "success", TargetVersion: "v0.3.0"}); err != nil {
		t.Fatalf("写历史失败: %v", err)
	}
	if err := AppendUpgradeHistory(dir, &UpgradeHistoryEntry{TS: 2, Outcome: "rollback", Reason: "boom"}); err != nil {
		t.Fatalf("写历史失败: %v", err)
	}
	list := ReadUpgradeHistory(dir, 10)
	if len(list) != 2 || list[0].Outcome != "rollback" {
		t.Fatalf("历史应倒序: %+v", list)
	}
	// 删除
	if err := RemoveUpgradeState(path); err != nil {
		t.Fatalf("删状态失败: %v", err)
	}
	if got, _ := ReadUpgradeState(path); got != nil {
		t.Fatal("删除后应读不到")
	}
}
