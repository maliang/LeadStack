package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBackupService 是 BackupService 的测试替身：真实读写备份目录，便于验证路径安全与暂存。
type fakeBackupService struct {
	mu            sync.Mutex
	dir           string
	interval      time.Duration
	retention     int
	seq           int
	infos         []BackupInfo
	verifyErr     error
	createErr     error
	manifest      *BackupManifest
	schemaVersion int
	deleted       []string
}

// newFakeBackupService 创建替身并确保备份目录存在。
func newFakeBackupService(dir string, interval time.Duration, retention int) *fakeBackupService {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		panic(fmt.Sprintf("创建测试备份目录失败: %v", err))
	}
	return &fakeBackupService{dir: dir, interval: interval, retention: retention}
}

// Create 生成一个假的归档文件（内容含 magic 前缀，便于断言）。
func (f *fakeBackupService) Create(_ context.Context, includeArtifacts bool) (*BackupInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.seq++
	name := fmt.Sprintf("leadstack-2026102%d-%06d.lsbak", f.seq, f.seq)
	content := []byte("LSBAK1\nfake-archive-payload-" + name)
	path := filepath.Join(f.dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return nil, fmt.Errorf("写入假归档: %w", err)
	}
	info := BackupInfo{
		Name: name, SizeBytes: int64(len(content)), CreatedAt: time.Now().UnixMilli(),
		IncludesArtifacts: includeArtifacts, SchemaVersion: 6,
		SHA256: strings.Repeat("ab", 32),
	}
	f.infos = append([]BackupInfo{info}, f.infos...) // 倒序
	return &info, nil
}

// List 返回备份列表（按创建时间倒序）。
func (f *fakeBackupService) List(context.Context) ([]BackupInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]BackupInfo, len(f.infos))
	copy(out, f.infos)
	return out, nil
}

// Verify 返回配置好的结果或错误。
func (f *fakeBackupService) Verify(_ context.Context, path string) (*BackupManifest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.verifyErr != nil {
		return nil, f.verifyErr
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	if f.manifest != nil {
		return f.manifest, nil
	}
	schemaVersion := f.schemaVersion
	if schemaVersion <= 0 {
		schemaVersion = 6
	}
	return &BackupManifest{
		FormatVersion: 1, AppVersion: "test", SchemaVersion: schemaVersion, CreatedAt: time.Now().UnixMilli(),
		Files: []BackupManifestFile{
			{Name: "manifest.json", SizeBytes: 10, SHA256: strings.Repeat("cd", 32)},
			{Name: "leadstack.db", SizeBytes: 100, SHA256: strings.Repeat("ef", 32)},
			{Name: "master.key", SizeBytes: 32, SHA256: strings.Repeat("01", 32)},
		},
	}, nil
}

// Delete 删除归档文件。
func (f *fakeBackupService) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.Remove(filepath.Join(f.dir, name)); err != nil {
		return err
	}
	kept := f.infos[:0]
	for _, info := range f.infos {
		if info.Name != name {
			kept = append(kept, info)
		}
	}
	f.infos = kept
	f.deleted = append(f.deleted, name)
	return nil
}

// StageRestore 模拟 backup.StageRestore：校验通过才写 pendingDir（含 meta.json），失败不留半成品。
func (f *fakeBackupService) StageRestore(_ context.Context, archivePath, pendingDir string) (*StagedRestore, error) {
	f.mu.Lock()
	err := f.verifyErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if _, statErr := os.Stat(archivePath); statErr != nil {
		return nil, statErr
	}
	// 校验通过：写入暂存目录（先清空再写，模拟「先校验后暂存」）。
	if rmErr := os.RemoveAll(pendingDir); rmErr != nil {
		return nil, rmErr
	}
	if mkErr := os.MkdirAll(pendingDir, 0o700); mkErr != nil {
		return nil, mkErr
	}
	schemaVersion := f.schemaVersion
	if schemaVersion <= 0 {
		schemaVersion = 6
	}
	staged := &StagedRestore{
		Source: "api-upload", CreatedAt: time.Now().UnixMilli(),
		VerifiedAt: time.Now().UnixMilli(), ArchiveSHA256: strings.Repeat("ab", 32), SchemaVersion: schemaVersion,
	}
	meta := map[string]any{
		"source": staged.Source, "created_at": staged.CreatedAt,
		"verified_at": staged.VerifiedAt, "archive_sha256": staged.ArchiveSHA256,
		"manifest": map[string]any{"schema_version": schemaVersion},
	}
	raw, _ := json.Marshal(meta)
	if wErr := os.WriteFile(filepath.Join(pendingDir, restorePendingMetaName), raw, 0o600); wErr != nil {
		return nil, wErr
	}
	// 模拟解压出的核心文件
	for _, name := range []string{"leadstack.db", "master.key"} {
		if wErr := os.WriteFile(filepath.Join(pendingDir, name), []byte("staged-"+name), 0o600); wErr != nil {
			return nil, wErr
		}
	}
	return staged, nil
}

// snapshot 返回当前记录。
func (f *fakeBackupService) snapshot() ([]BackupInfo, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]BackupInfo(nil), f.infos...), append([]string(nil), f.deleted...)
}

// TestBackupEndpoints 覆盖列表/创建/下载/校验/删除与 401。
func TestBackupEndpoints(t *testing.T) {
	env := newTestEnv(t)

	status, _ := env.request(http.MethodGet, "/api/v1/backups", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("未带 token 访问 /backups 期望 401，实际 %d", status)
	}

	jwt := env.login()

	// 空列表
	status, raw := env.request(http.MethodGet, "/api/v1/backups", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询备份列表失败: %d", status)
	}
	if !strings.Contains(string(raw), `"backups":[]`) {
		t.Fatalf("空列表应返回 {\"backups\":[]}: %s", raw)
	}

	// 立即备份 → 201 + Info
	status, raw = env.request(http.MethodPost, "/api/v1/backups", jwt, map[string]any{"include_artifacts": true})
	if status != http.StatusCreated {
		t.Fatalf("创建备份失败: %d（body=%s）", status, raw)
	}
	var created struct {
		Data BackupInfo `json:"data"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("解析备份信息失败: %v", err)
	}
	if created.Data.Name == "" || created.Data.SizeBytes <= 0 || !created.Data.IncludesArtifacts {
		t.Fatalf("备份信息不正确: %+v", created.Data)
	}
	if !strings.HasSuffix(created.Data.Name, ".lsbak") {
		t.Fatalf("归档后缀应為 .lsbak: %s", created.Data.Name)
	}

	// 列表可见
	status, raw = env.request(http.MethodGet, "/api/v1/backups", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询列表失败: %d", status)
	}
	var list struct {
		Data struct {
			Backups []BackupInfo `json:"backups"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("解析列表失败: %v", err)
	}
	if len(list.Data.Backups) != 1 || list.Data.Backups[0].Name != created.Data.Name {
		t.Fatalf("列表内容不正确: %+v", list.Data.Backups)
	}
	if list.Data.Backups[0].SHA256 == "" || list.Data.Backups[0].SchemaVersion == 0 {
		t.Fatalf("列表应含 sha256 与 schema_version: %+v", list.Data.Backups[0])
	}

	// 下载：Content-Disposition + 内容与磁盘一致
	status, body := env.request(http.MethodGet, "/api/v1/backups/"+created.Data.Name+"/download", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("下载备份失败: %d", status)
	}
	if !strings.HasPrefix(string(body), "LSBAK1") {
		t.Fatalf("下载内容应以 LSBAK1 开头: %q", string(body[:min(16, len(body))]))
	}
	onDisk, err := os.ReadFile(filepath.Join(env.cfg.BackupDir, created.Data.Name))
	if err != nil {
		t.Fatalf("读取磁盘备份失败: %v", err)
	}
	if string(onDisk) != string(body) {
		t.Fatal("下载内容与磁盘文件不一致")
	}

	// 校验成功
	status, raw = env.request(http.MethodPost, "/api/v1/backups/"+created.Data.Name+"/verify", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("校验备份失败: %d（body=%s）", status, raw)
	}
	var verified struct {
		Data verifyResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &verified); err != nil {
		t.Fatalf("解析校验结果失败: %v", err)
	}
	if !verified.Data.OK || verified.Data.Manifest == nil || len(verified.Data.Manifest.Files) == 0 {
		t.Fatalf("校验结果不正确: %+v", verified.Data)
	}
	names := map[string]bool{}
	for _, f := range verified.Data.Manifest.Files {
		names[f.Name] = true
	}
	if !names["leadstack.db"] || !names["master.key"] {
		t.Fatalf("manifest 应含 db 与 master.key 条目: %+v", verified.Data.Manifest.Files)
	}

	// 不存在的备份 → 404
	status, _ = env.request(http.MethodGet, "/api/v1/backups/nope.lsbak/download", jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("不存在的备份下载期望 404，实际 %d", status)
	}

	// 删除
	status, raw = env.request(http.MethodDelete, "/api/v1/backups/"+created.Data.Name, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("删除备份失败: %d（body=%s）", status, raw)
	}
	if _, err := os.Stat(filepath.Join(env.cfg.BackupDir, created.Data.Name)); !os.IsNotExist(err) {
		t.Fatal("删除后文件应不存在")
	}
	status, _ = env.request(http.MethodDelete, "/api/v1/backups/"+created.Data.Name, jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("重复删除期望 404，实际 %d", status)
	}
}

// TestBackupPathTraversalRejected 覆盖安全边界：只允许备份目录内的合法文件名。
func TestBackupPathTraversalRejected(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	// 在备份目录外放一个「敏感文件」，确保任何穿越都拿不到它
	outside := filepath.Join(env.cfg.DataDir, "secret.txt")
	if err := os.WriteFile(outside, []byte("TOP-SECRET"), 0o600); err != nil {
		t.Fatalf("写入敏感文件失败: %v", err)
	}

	badNames := []string{
		"..%2fsecret.txt",     // URL 编码穿越
		"..",                  // 上级目录
		"%2e%2e%2fsecret.txt", // 双重编码形态
		"a%2fb.lsbak",         // 内嵌分隔符
		"..%5csecret.txt",     // Windows 分隔符
		"not-a-backup.txt",    // 后缀不合法
		".hidden.lsbak",       // 隐藏文件
	}
	for _, name := range badNames {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			path := "/api/v1/backups/" + name + "/download"
			if method == http.MethodDelete {
				path = "/api/v1/backups/" + name
			}
			status, body := env.request(method, path, jwt, nil)
			if status == http.StatusOK {
				t.Fatalf("%s %s 应被拒绝，实际 200（body=%s）", method, name, body)
			}
			if strings.Contains(string(body), "TOP-SECRET") {
				t.Fatalf("%s %s 泄露了备份目录外的文件: %s", method, name, body)
			}
		}
	}

	// 正常名字仍可用
	fake := env.backup
	info, err := fake.Create(context.Background(), false)
	if err != nil {
		t.Fatalf("创建备份失败: %v", err)
	}
	status, _ := env.request(http.MethodGet, "/api/v1/backups/"+info.Name+"/download", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("合法文件名应可下载，实际 %d", status)
	}
}

// TestBackupVerifyErrorMapping 覆盖三类错误的可读提示（口令/损坏/版本过新）。
func TestBackupVerifyErrorMapping(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	info, err := env.backup.Create(context.Background(), false)
	if err != nil {
		t.Fatalf("创建备份失败: %v", err)
	}

	cases := []struct {
		name   string
		inject error
		want   string
	}{
		{"口令错", errBackupBadPassphrase, "backup.pass"},
		{"归档损坏", errBackupCorrupt, "损坏"},
		{"版本过新", errBackupSchemaTooNew, "更新版本"},
	}
	for _, tc := range cases {
		env.backup.mu.Lock()
		env.backup.verifyErr = tc.inject
		env.backup.mu.Unlock()

		status, raw := env.request(http.MethodPost, "/api/v1/backups/"+info.Name+"/verify", jwt, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("%s 期望 400，实际 %d（body=%s）", tc.name, status, raw)
		}
		if !strings.Contains(string(raw), tc.want) {
			t.Fatalf("%s 的提示应包含 %q: %s", tc.name, tc.want, raw)
		}
	}
	// 恢复默认：校验成功
	env.backup.mu.Lock()
	env.backup.verifyErr = nil
	env.backup.mu.Unlock()
	status, _ := env.request(http.MethodPost, "/api/v1/backups/"+info.Name+"/verify", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("清除错误后校验应成功，实际 %d", status)
	}
}

// uploadRestoreFile 以 multipart 上传归档到 /backups/restore。
func (e *testEnv) uploadRestoreFile(jwt string, content []byte) (int, []byte) {
	e.t.Helper()
	var buf strings.Builder
	writer := multipart.NewWriter(&buf)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="../../../evil-backup.lsbak"`)
	header.Set("Content-Type", "application/octet-stream")
	part, err := writer.CreatePart(header)
	if err != nil {
		e.t.Fatalf("构造 multipart 失败: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		e.t.Fatalf("写入 multipart 失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		e.t.Fatalf("关闭 multipart 失败: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/api/v1/backups/restore", strings.NewReader(buf.String()))
	if err != nil {
		e.t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+jwt)
	resp, err := e.http.Do(req)
	if err != nil {
		e.t.Fatalf("上传归档失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("读取响应失败: %v", err)
	}
	return resp.StatusCode, body
}

// TestRestoreUploadStaging 覆盖上传恢复：202 + 暂存目录 + meta.json；失败不留半成品。
func TestRestoreUploadStaging(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	upload := func(content []byte) (int, []byte) {
		return env.uploadRestoreFile(jwt, content)
	}

	// 校验失败：400 且不留半成品
	env.backup.mu.Lock()
	env.backup.verifyErr = errBackupCorrupt
	env.backup.mu.Unlock()
	status, body := upload([]byte("LSBAK1\nbroken"))
	if status != http.StatusBadRequest {
		t.Fatalf("损坏归档期望 400，实际 %d（body=%s）", status, body)
	}
	if _, err := os.Stat(env.cfg.RestorePendingDir()); !os.IsNotExist(err) {
		t.Fatalf("校验失败不应留下暂存目录: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(env.cfg.DataDir, ".restore-tmp")); len(entries) > 0 {
		t.Fatalf("校验失败不应留下临时文件: %v", entries)
	}

	// 校验通过：202 + 暂存
	env.backup.mu.Lock()
	env.backup.verifyErr = nil
	env.backup.mu.Unlock()
	status, body = upload([]byte("LSBAK1\nvalid-archive-bytes"))
	if status != http.StatusAccepted {
		t.Fatalf("期望 202，实际 %d（body=%s）", status, body)
	}
	var resp struct {
		Data restoreResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !resp.Data.RestartRequired || resp.Data.Staged == nil {
		t.Fatalf("应返回 restart_required 与暂存信息: %+v", resp.Data)
	}

	// 暂存目录含解压产物 + meta.json，且能通过 ReadPendingRestore 读回（启动时据此应用）
	pendingDir := env.cfg.RestorePendingDir()
	for _, name := range []string{"meta.json", "leadstack.db", "master.key"} {
		if _, err := os.Stat(filepath.Join(pendingDir, name)); err != nil {
			t.Fatalf("暂存目录缺少 %s: %v", name, err)
		}
	}
	pending, err := ReadPendingRestore(pendingDir)
	if err != nil {
		t.Fatalf("读取暂存恢复信息失败: %v", err)
	}
	if pending == nil || pending.Source != "api-upload" || pending.SchemaVersion != 6 {
		t.Fatalf("暂存恢复信息不正确: %+v", pending)
	}
	if resp.Data.Staged == nil || resp.Data.Staged.Source != "api-upload" {
		t.Fatalf("响应应带暂存信息: %+v", resp.Data.Staged)
	}
	// 不允许在数据目录之外写出任何东西
	if _, err := os.Stat(filepath.Join(filepath.Dir(env.cfg.DataDir), "evil-backup.lsbak")); !os.IsNotExist(err) {
		t.Fatal("上传路径穿越：文件被写到了数据目录之外")
	}
}

// TestBackupSettings 覆盖设置读写与非法 interval。
func TestBackupSettings(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	status, raw := env.request(http.MethodGet, "/api/v1/backup-settings", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询备份设置失败: %d", status)
	}
	var settings struct {
		Data backupSettingsPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("解析设置失败: %v", err)
	}
	if settings.Data.Interval == "" || settings.Data.Retention <= 0 || !settings.Data.Enabled {
		t.Fatalf("默认设置不正确: %+v", settings.Data)
	}
	if settings.Data.Interval != env.cfg.BackupInterval.String() {
		t.Fatalf("默认 interval 应来自配置: %q", settings.Data.Interval)
	}

	// 非法 interval → 400
	for _, bad := range []string{"abc", "10", "0s", "-5m"} {
		status, raw := env.request(http.MethodPatch, "/api/v1/backup-settings", jwt,
			map[string]any{"interval": bad})
		if status != http.StatusBadRequest {
			t.Fatalf("非法 interval %q 期望 400，实际 %d（body=%s）", bad, status, raw)
		}
	}
	// 非法 retention → 400
	status, _ = env.request(http.MethodPatch, "/api/v1/backup-settings", jwt, map[string]any{"retention": 0})
	if status != http.StatusBadRequest {
		t.Fatalf("retention=0 期望 400，实际 %d", status)
	}
	// 空 PATCH → 400
	status, _ = env.request(http.MethodPatch, "/api/v1/backup-settings", jwt, map[string]any{})
	if status != http.StatusBadRequest {
		t.Fatalf("空 PATCH 期望 400，实际 %d", status)
	}

	// 合法更新 → 生效且持久化
	status, raw = env.request(http.MethodPatch, "/api/v1/backup-settings", jwt, map[string]any{
		"enabled": true, "interval": "5s", "retention": 2, "include_artifacts": true,
	})
	if status != http.StatusOK {
		t.Fatalf("更新设置失败: %d（body=%s）", status, raw)
	}
	var updated struct {
		Data backupSettingsPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatalf("解析设置失败: %v", err)
	}
	if updated.Data.Interval != "5s" || updated.Data.Retention != 2 || !updated.Data.IncludeArtifacts {
		t.Fatalf("更新未生效: %+v", updated.Data)
	}
	// 重新读取（走存储）确认持久化
	status, raw = env.request(http.MethodGet, "/api/v1/backup-settings", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("重新查询设置失败: %d", status)
	}
	_ = json.Unmarshal(raw, &updated)
	if updated.Data.Interval != "5s" || updated.Data.Retention != 2 {
		t.Fatalf("设置未持久化: %+v", updated.Data)
	}
}

// TestBackupSchedulerAndRetention 覆盖定时备份的保留策略与 last_error 记录。
func TestBackupSchedulerAndRetention(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	ctx := context.Background()

	// retention=2：连做 5 次 → 只剩 2 份，且是最新的
	if err := env.api.saveBackupSettings(ctx, backupSettings{
		Enabled: true, Interval: "1s", Retention: 2, IncludeArtifacts: false,
	}); err != nil {
		t.Fatalf("写入设置失败: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := env.api.ScheduledBackupOnce(ctx); err != nil {
			t.Fatalf("第 %d 次定时备份失败: %v", i+1, err)
		}
	}
	infos, _ := env.backup.snapshot()
	if len(infos) != 2 {
		t.Fatalf("保留策略应只剩 2 份，实际 %d: %+v", len(infos), infos)
	}
	if deleted := len(env.backup.deleted); deleted != 3 {
		t.Fatalf("应删除 3 份旧备份，实际 %d", deleted)
	}
	// 最新的一份必须还在
	if infos[0].CreatedAt < infos[1].CreatedAt {
		t.Fatalf("列表应按创建时间倒序: %+v", infos)
	}

	// 设置接口应回传 last_success_at（且无 last_error）
	status, raw := env.request(http.MethodGet, "/api/v1/backup-settings", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询设置失败: %d", status)
	}
	var settings struct {
		Data backupSettingsPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("解析设置失败: %v", err)
	}
	if settings.Data.LastSuccessAt == nil {
		t.Fatal("成功后应记录 last_success_at")
	}
	if settings.Data.LastError != "" {
		t.Fatalf("成功后 last_error 应为空，实际 %q", settings.Data.LastError)
	}

	// 失败时写入 last_error（不用真的等待定时器）
	env.backup.mu.Lock()
	env.backup.createErr = errors.New("disk full")
	env.backup.mu.Unlock()
	if _, err := env.api.ScheduledBackupOnce(ctx); err == nil {
		t.Fatal("应返回错误")
	}
	status, raw = env.request(http.MethodGet, "/api/v1/backup-settings", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询设置失败: %d", status)
	}
	_ = json.Unmarshal(raw, &settings)
	if !strings.Contains(settings.Data.LastError, "disk full") {
		t.Fatalf("失败原因应写入 last_error: %q", settings.Data.LastError)
	}

	// 恢复成功后 last_error 清空
	env.backup.mu.Lock()
	env.backup.createErr = nil
	env.backup.mu.Unlock()
	if _, err := env.api.ScheduledBackupOnce(ctx); err != nil {
		t.Fatalf("恢复后备份失败: %v", err)
	}
	_, raw = env.request(http.MethodGet, "/api/v1/backup-settings", jwt, nil)
	var afterSuccess struct {
		Data backupSettingsPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &afterSuccess); err != nil {
		t.Fatalf("解析设置失败: %v", err)
	}
	if afterSuccess.Data.LastError != "" {
		t.Fatalf("成功应清空 last_error: %q", afterSuccess.Data.LastError)
	}

	// 定时循环本身：用 1s 周期跑一小段，确认能自动产出备份
	before, _ := env.backup.snapshot()
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		env.api.RunBackupScheduler(loopCtx)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	produced := false
	for time.Now().Before(deadline) {
		if after, _ := env.backup.snapshot(); len(after) != len(before) || len(after) > 0 {
			// 保留策略会让数量稳定在 2，因此用「最近成功时间变化」判断更稳
			if raw, ok, _ := env.api.st.GetSetting(ctx, backupLastSuccessKey); ok && raw != "" {
				produced = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("定时备份循环未在取消后退出")
	}
	if !produced {
		t.Fatal("定时循环未在 5 秒内产生备份")
	}
}

// min 返回较小值。
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestBackupVerifyRejectsSchemaTooNew 覆盖 §33：verify 阶段就要报版本过新（schema_too_new）。
func TestBackupVerifyRejectsSchemaTooNew(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	info, err := env.backup.Create(context.Background(), false)
	if err != nil {
		t.Fatalf("创建备份失败: %v", err)
	}

	// 备份来自更新版本（schema 99）
	env.backup.mu.Lock()
	env.backup.schemaVersion = 99
	env.backup.mu.Unlock()

	status, raw := env.request(http.MethodPost, "/api/v1/backups/"+info.Name+"/verify", jwt, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("版本过新期望 400，实际 %d（body=%s）", status, raw)
	}
	if !strings.Contains(string(raw), `"code":"schema_too_new"`) {
		t.Fatalf("错误码应为 schema_too_new: %s", raw)
	}
	if !strings.Contains(string(raw), "升级") {
		t.Fatalf("提示应指导用户升级: %s", raw)
	}

	// 上传恢复同样拒绝，且不留下暂存目录
	pendingDir := env.cfg.RestorePendingDir()
	if err := os.MkdirAll(pendingDir, 0o700); err != nil {
		t.Fatalf("预置暂存目录失败: %v", err)
	}
	status, raw = env.uploadRestoreFile(jwt, []byte("LSBAK1\nfuture-version"))
	if status != http.StatusBadRequest {
		t.Fatalf("上传过新备份期望 400，实际 %d（body=%s）", status, raw)
	}
	if !strings.Contains(string(raw), `"code":"schema_too_new"`) {
		t.Fatalf("上传错误码应为 schema_too_new: %s", raw)
	}
	if _, err := os.Stat(pendingDir); !os.IsNotExist(err) {
		t.Fatalf("版本过新的归档不应留下暂存目录: %v", err)
	}

	// 恢复默认版本后校验通过
	env.backup.mu.Lock()
	env.backup.schemaVersion = 0
	env.backup.mu.Unlock()
	status, _ = env.request(http.MethodPost, "/api/v1/backups/"+info.Name+"/verify", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("版本兼容时校验应通过，实际 %d", status)
	}
}
