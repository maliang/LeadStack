package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- 测试辅助 ----------

const testPassphrase = "test-backup-passphrase-9f3a"

func writeTestFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("写文件 %s 失败: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("设置权限 %s 失败: %v", path, err)
	}
}

func randomTestBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("生成随机数失败: %v", err)
	}
	return b
}

// openTestDB 创建一个 WAL 模式的真实 SQLite 库（schema 6 的基本表结构）。
func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open(sqliteDriverName, path)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.SetMaxOpenConns(1)
	stmts := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		`CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS servers(id TEXT PRIMARY KEY, name TEXT NOT NULL, secret TEXT)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			t.Fatalf("执行 %q 失败: %v", stmt, err)
		}
	}
	for v := 1; v <= 6; v++ {
		if _, err := db.Exec(`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(?, ?)`, v, int64(v)); err != nil {
			db.Close()
			t.Fatalf("写入 schema_migrations 失败: %v", err)
		}
	}
	return db
}

func seedServer(t *testing.T, db *sql.DB, id, name, secret string) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR REPLACE INTO servers(id, name, secret) VALUES(?,?,?)`, id, name, secret); err != nil {
		t.Fatalf("插入服务器失败: %v", err)
	}
}

// setupDataDir 造一个含 master.key / known_hosts / artifacts / 数据库的数据目录。
func setupDataDir(t *testing.T) (dir string, db *sql.DB) {
	t.Helper()
	dir = t.TempDir()
	writeTestFile(t, filepath.Join(dir, MasterKeyFileName), randomTestBytes(t, 32), 0o600)
	writeTestFile(t, filepath.Join(dir, KnownHostsFileName), []byte("example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAA\n"), 0o600)
	writeTestFile(t, filepath.Join(dir, ArtifactsDirName, "art-1"), []byte("artifact-one-content"), 0o640)
	writeTestFile(t, filepath.Join(dir, ArtifactsDirName, "art-2"), []byte("artifact-two-content"), 0o640)
	db = openTestDB(t, filepath.Join(dir, DBFileName))
	seedServer(t, db, "srv-1", "web-1", "secret-pw-123")
	seedServer(t, db, "srv-2", "db-1", "another-secret")
	return dir, db
}

func newTestManager(t *testing.T, dataDir, backupDir, passphrase string) *Manager {
	t.Helper()
	t.Setenv(EnvPassphrase, "")
	m, err := NewManager(Options{
		DataDir:    dataDir,
		BackupDir:  backupDir,
		Passphrase: passphrase,
		AppVersion: "test-1.0.0",
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

// cleanupPreRestore 删除恢复测试产生的兄弟目录，避免污染系统临时目录。
func cleanupPreRestore(t *testing.T, dataDir string) {
	t.Helper()
	matches, _ := filepath.Glob(dataDir + ".pre-restore-*")
	for _, m := range matches {
		_ = os.RemoveAll(m)
	}
	matches, _ = filepath.Glob(filepath.Dir(dataDir) + "/.leadstack-restore-*")
	for _, m := range matches {
		_ = os.RemoveAll(m)
	}
}

func readServerNames(t *testing.T, dbPath string) []string {
	t.Helper()
	db, err := sql.Open(sqliteDriverName, dbPath)
	if err != nil {
		t.Fatalf("打开恢复后的数据库失败: %v", err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM servers ORDER BY name`)
	if err != nil {
		t.Fatalf("查询恢复后的数据库失败: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, name)
	}
	return names
}

// ---------- 基本往返 / 加密 / 列表 ----------

func TestCreateVerifyRoundTrip(t *testing.T) {
	dataDir, db := setupDataDir(t)
	defer db.Close()
	backupDir := filepath.Join(t.TempDir(), "backups")
	m := newTestManager(t, dataDir, backupDir, testPassphrase)

	info, err := m.Create(context.Background(), false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(info.Name, "leadstack-") || !strings.HasSuffix(info.Name, ".lsbak") {
		t.Fatalf("备份文件名异常: %q", info.Name)
	}
	if info.SizeBytes <= 0 {
		t.Fatalf("备份大小应为正数: %d", info.SizeBytes)
	}
	if info.SchemaVersion != 6 {
		t.Fatalf("schema_version = %d, 期望 6", info.SchemaVersion)
	}
	if info.IncludesArtifacts {
		t.Fatal("includeArtifacts=false 时不应包含制品")
	}

	archivePath := filepath.Join(backupDir, info.Name)
	raw, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("读取归档: %v", err)
	}
	if !bytes.HasPrefix(raw, []byte(Magic)) {
		t.Fatalf("归档头不是 LSBAK1: %q", raw[:min(16, len(raw))])
	}
	if int64(len(raw)) != info.SizeBytes {
		t.Fatalf("Info.SizeBytes=%d 与实际 %d 不符", info.SizeBytes, len(raw))
	}
	sum, err := fileSHA256(archivePath)
	if err != nil {
		t.Fatalf("fileSHA256: %v", err)
	}
	if sum != info.SHA256 {
		t.Fatalf("Info.SHA256=%s 与实际 %s 不符", info.SHA256, sum)
	}

	manifest, err := m.Verify(context.Background(), archivePath)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if manifest.FormatVersion != FormatVersion || manifest.SchemaVersion != 6 {
		t.Fatalf("manifest 元数据异常: %+v", manifest)
	}
	names := make([]string, 0, len(manifest.Files))
	for _, f := range manifest.Files {
		names = append(names, f.Name)
		if f.SizeBytes <= 0 || len(f.SHA256) != 64 {
			t.Fatalf("manifest 文件条目异常: %+v", f)
		}
	}
	for _, want := range []string{DBFileName, MasterKeyFileName, KnownHostsFileName} {
		if !contains(names, want) {
			t.Fatalf("manifest 缺少 %s（实际 %v）", want, names)
		}
	}
	for _, name := range names {
		if name == PassphraseFileName || strings.HasPrefix(name, DefaultBackupDirName+"/") {
			t.Fatalf("归档不得包含 %s", name)
		}
	}

	// 边车元数据必须存在，并与归档一致。
	sidecar := archivePath + ".json"
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("边车 json 缺失: %v", err)
	}

	list, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != info.Name || list[0].SHA256 != info.SHA256 {
		t.Fatalf("List 结果异常: %+v", list)
	}
}

func TestArchiveIsEncrypted(t *testing.T) {
	dataDir, db := setupDataDir(t)
	defer db.Close()
	m := newTestManager(t, dataDir, filepath.Join(t.TempDir(), "backups"), testPassphrase)

	info, err := m.Create(context.Background(), true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(m.backupDir, info.Name))
	if err != nil {
		t.Fatalf("读取归档: %v", err)
	}

	masterKey, err := os.ReadFile(filepath.Join(dataDir, MasterKeyFileName))
	if err != nil {
		t.Fatalf("读取 master.key: %v", err)
	}
	features := [][]byte{
		[]byte("SQLite format 3"),
		[]byte("secret-pw-123"),
		[]byte("another-secret"),
		[]byte("artifact-one-content"),
		[]byte("BEGIN OPENSSH PRIVATE KEY"),
		masterKey,
		[]byte(PassphraseFileName),
	}
	for _, feature := range features {
		if len(feature) == 0 {
			continue
		}
		if bytes.Contains(raw, feature) {
			t.Fatalf("归档中出现明文特征 %q", truncateBytes(feature, 40))
		}
	}
	// header 是明文的，但其中也不应出现主密钥。
	if bytes.Contains(raw, []byte(hex.EncodeToString(masterKey)[:32])) {
		t.Fatal("归档中出现主密钥 hex 片段")
	}
}

func TestVerifyWrongPassphrase(t *testing.T) {
	dataDir, db := setupDataDir(t)
	defer db.Close()
	m := newTestManager(t, dataDir, filepath.Join(t.TempDir(), "backups"), testPassphrase)
	info, err := m.Create(context.Background(), false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	archivePath := filepath.Join(m.backupDir, info.Name)

	wrong, err := NewManager(Options{DataDir: dataDir, BackupDir: m.backupDir, Passphrase: "totally-wrong"})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	_, err = wrong.Verify(context.Background(), archivePath)
	if !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("错误口令应返回 ErrBadPassphrase，实际: %v", err)
	}
	if errors.Is(err, ErrCorrupt) {
		t.Fatal("错误口令不应被误报为 ErrCorrupt")
	}
	if !strings.Contains(err.Error(), "口令") {
		t.Fatalf("错误信息应包含「口令」: %v", err)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	dataDir, db := setupDataDir(t)
	defer db.Close()
	m := newTestManager(t, dataDir, filepath.Join(t.TempDir(), "backups"), testPassphrase)
	info, err := m.Create(context.Background(), false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	archivePath := filepath.Join(m.backupDir, info.Name)
	raw, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("读取归档: %v", err)
	}
	// 翻转载荷尾部一个字节（在 GCM 密文内）。
	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)-40] ^= 0xFF
	tamperedPath := filepath.Join(t.TempDir(), "tampered.lsbak")
	writeTestFile(t, tamperedPath, tampered, 0o600)

	if _, err := m.Verify(context.Background(), tamperedPath); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("篡改归档应返回 ErrCorrupt，实际: %v", err)
	}

	// 截断归档也必须被拒绝。
	truncated := append([]byte(nil), raw[:len(raw)/2]...)
	truncatedPath := filepath.Join(t.TempDir(), "truncated.lsbak")
	writeTestFile(t, truncatedPath, truncated, 0o600)
	if _, err := m.Verify(context.Background(), truncatedPath); err == nil {
		t.Fatal("截断归档应校验失败")
	}
}

func TestVerifyGarbage(t *testing.T) {
	dataDir, db := setupDataDir(t)
	defer db.Close()
	m := newTestManager(t, dataDir, filepath.Join(t.TempDir(), "backups"), testPassphrase)

	path := filepath.Join(t.TempDir(), "garbage.lsbak")
	writeTestFile(t, path, []byte("not an archive at all"), 0o600)
	if _, err := m.Verify(context.Background(), path); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("非归档文件应返回 ErrCorrupt，实际: %v", err)
	}
	if _, err := m.Verify(context.Background(), filepath.Join(t.TempDir(), "missing.lsbak")); err == nil {
		t.Fatal("不存在的归档应报错")
	}
}

func TestArtifactsToggle(t *testing.T) {
	dataDir, db := setupDataDir(t)
	defer db.Close()
	backupDir := filepath.Join(t.TempDir(), "backups")
	m := newTestManager(t, dataDir, backupDir, testPassphrase)

	without, err := m.Create(context.Background(), false)
	if err != nil {
		t.Fatalf("Create(false): %v", err)
	}
	manifestWithout, err := m.Verify(context.Background(), filepath.Join(backupDir, without.Name))
	if err != nil {
		t.Fatalf("Verify(false): %v", err)
	}
	if manifestWithout.IncludesArtifacts {
		t.Fatal("includeArtifacts=false 的 manifest 不应标记包含制品")
	}
	for _, f := range manifestWithout.Files {
		if strings.HasPrefix(f.Name, ArtifactsDirName+"/") {
			t.Fatalf("includeArtifacts=false 却包含制品 %s", f.Name)
		}
	}

	with, err := m.Create(context.Background(), true)
	if err != nil {
		t.Fatalf("Create(true): %v", err)
	}
	manifestWith, err := m.Verify(context.Background(), filepath.Join(backupDir, with.Name))
	if err != nil {
		t.Fatalf("Verify(true): %v", err)
	}
	if !manifestWith.IncludesArtifacts {
		t.Fatal("includeArtifacts=true 的 manifest 应标记包含制品")
	}
	var arts []string
	for _, f := range manifestWith.Files {
		if strings.HasPrefix(f.Name, ArtifactsDirName+"/") {
			arts = append(arts, strings.TrimPrefix(f.Name, ArtifactsDirName+"/"))
		}
	}
	sort.Strings(arts)
	if len(arts) != 2 || arts[0] != "art-1" || arts[1] != "art-2" {
		t.Fatalf("制品条目异常: %v", arts)
	}
}

func TestPassphraseFileGenerationAndExclusion(t *testing.T) {
	dataDir, db := setupDataDir(t)
	defer db.Close()
	t.Setenv(EnvPassphrase, "")
	m := newTestManager(t, dataDir, filepath.Join(t.TempDir(), "backups"), "")

	info, err := m.Create(context.Background(), false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	passPath := filepath.Join(dataDir, PassphraseFileName)
	fi, err := os.Stat(passPath)
	if err != nil {
		t.Fatalf("应自动生成 backup.pass: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("backup.pass 权限 = %o, 期望 600", perm)
	}
	passBytes, err := os.ReadFile(passPath)
	if err != nil {
		t.Fatalf("读取 backup.pass: %v", err)
	}
	if len(strings.TrimSpace(string(passBytes))) == 0 {
		t.Fatal("backup.pass 不应为空")
	}

	// 同一个 Manager 用生成的口令能校验成功。
	if _, err := m.Verify(context.Background(), filepath.Join(m.backupDir, info.Name)); err != nil {
		t.Fatalf("用生成的 backup.pass 校验失败: %v", err)
	}

	// backup.pass 不得进入归档：manifest 里不能有它，归档原始字节里也不能出现其内容。
	manifest, err := m.Verify(context.Background(), filepath.Join(m.backupDir, info.Name))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	pass := strings.TrimSpace(string(passBytes))
	for _, f := range manifest.Files {
		if f.Name == PassphraseFileName || strings.Contains(f.Name, "backup.pass") {
			t.Fatalf("manifest 不应包含 backup.pass: %+v", f)
		}
	}
	raw, err := os.ReadFile(filepath.Join(m.backupDir, info.Name))
	if err != nil {
		t.Fatalf("读取归档: %v", err)
	}
	if bytes.Contains(raw, []byte(pass)) {
		t.Fatal("归档中出现了 backup.pass 的口令明文")
	}

	// 环境变量优先于 backup.pass。
	t.Setenv(EnvPassphrase, "env-passphrase")
	envM, err := NewManager(Options{DataDir: dataDir, BackupDir: m.backupDir})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := envM.Verify(context.Background(), filepath.Join(m.backupDir, info.Name)); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("环境变量口令优先级下应报口令错误，实际: %v", err)
	}
}

func TestListSortedAndSidecarFallback(t *testing.T) {
	dataDir, db := setupDataDir(t)
	defer db.Close()
	backupDir := filepath.Join(t.TempDir(), "backups")

	clock := int64(1_700_000_000_000)
	m, err := NewManager(Options{
		DataDir:    dataDir,
		BackupDir:  backupDir,
		Passphrase: testPassphrase,
		Now:        func() int64 { return clock },
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	first, err := m.Create(context.Background(), false)
	if err != nil {
		t.Fatalf("Create #1: %v", err)
	}
	clock += 5 * time.Minute.Milliseconds()
	second, err := m.Create(context.Background(), false)
	if err != nil {
		t.Fatalf("Create #2: %v", err)
	}

	list, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].Name != second.Name || list[1].Name != first.Name {
		t.Fatalf("List 未按创建时间倒序: %+v", list)
	}

	// 删除边车后 List 仍应回退到 header + 流式哈希。
	if err := os.Remove(filepath.Join(backupDir, first.Name+".json")); err != nil {
		t.Fatalf("删除边车: %v", err)
	}
	list, err = m.List(context.Background())
	if err != nil {
		t.Fatalf("List(fallback): %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("边车缺失时 List 数量异常: %+v", list)
	}
	for _, info := range list {
		if info.Name == first.Name {
			if info.SHA256 != first.SHA256 || info.SchemaVersion != 6 || info.CreatedAt != first.CreatedAt {
				t.Fatalf("回退元数据异常: %+v vs %+v", info, first)
			}
		}
	}
}

func TestDeleteRejectsTraversal(t *testing.T) {
	dataDir, db := setupDataDir(t)
	defer db.Close()
	m := newTestManager(t, dataDir, filepath.Join(t.TempDir(), "backups"), testPassphrase)
	info, err := m.Create(context.Background(), false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, bad := range []string{"../x.lsbak", "/etc/passwd", "a/b.lsbak", "..", "x.lsbak/../y.lsbak", "no-suffix.dat"} {
		if err := m.Delete(context.Background(), bad); err == nil {
			t.Fatalf("非法备份名 %q 应被拒绝", bad)
		}
	}
	if err := m.Delete(context.Background(), info.Name); err != nil {
		t.Fatalf("删除合法备份失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.backupDir, info.Name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("归档未被删除: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.backupDir, info.Name+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("边车未被删除: %v", err)
	}
}

func TestConcurrentCreate(t *testing.T) {
	dataDir, db := setupDataDir(t)
	defer db.Close()
	m := newTestManager(t, dataDir, filepath.Join(t.TempDir(), "backups"), testPassphrase)

	const n = 4
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		infos   []*Info
		firstEr error
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := m.Create(context.Background(), false)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstEr == nil {
					firstEr = err
				}
				return
			}
			infos = append(infos, info)
		}()
	}
	wg.Wait()
	if firstEr != nil {
		t.Fatalf("并发 Create 失败: %v", firstEr)
	}
	if len(infos) != n {
		t.Fatalf("并发 Create 只成功 %d/%d", len(infos), n)
	}
	seen := map[string]bool{}
	for _, info := range infos {
		if seen[info.Name] {
			t.Fatalf("并发 Create 产生了重复文件名 %s", info.Name)
		}
		seen[info.Name] = true
		if _, err := m.Verify(context.Background(), filepath.Join(m.backupDir, info.Name)); err != nil {
			t.Fatalf("并发产出的归档 %s 校验失败: %v", info.Name, err)
		}
	}
	list, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != n {
		t.Fatalf("List 应有 %d 份，实际 %d", n, len(list))
	}
}

func TestCreateContextCanceled(t *testing.T) {
	dataDir, db := setupDataDir(t)
	defer db.Close()
	m := newTestManager(t, dataDir, filepath.Join(t.TempDir(), "backups"), testPassphrase)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Create(ctx, false); err == nil {
		t.Fatal("已取消的 context 不应创建备份")
	}
	if _, err := m.Verify(ctx, filepath.Join(m.backupDir, "x.lsbak")); err == nil {
		t.Fatal("已取消的 context 不应执行校验")
	}
}

func TestCreateRequiresDBAndMasterKey(t *testing.T) {
	t.Run("缺数据库", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, MasterKeyFileName), randomTestBytes(t, 32), 0o600)
		m := newTestManager(t, dir, filepath.Join(t.TempDir(), "backups"), testPassphrase)
		if _, err := m.Create(context.Background(), false); err == nil {
			t.Fatal("数据库缺失时应报错")
		}
	})

	t.Run("缺 master.key", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(EnvMasterKey, "")
		db := openTestDB(t, filepath.Join(dir, DBFileName))
		defer db.Close()
		m := newTestManager(t, dir, filepath.Join(t.TempDir(), "backups"), testPassphrase)
		if _, err := m.Create(context.Background(), false); err == nil {
			t.Fatal("master.key 缺失且未设置环境变量时应报错")
		}
	})

	t.Run("master.key 来自环境变量", func(t *testing.T) {
		dir := t.TempDir()
		key := randomTestBytes(t, 32)
		t.Setenv(EnvMasterKey, hex.EncodeToString(key))
		db := openTestDB(t, filepath.Join(dir, DBFileName))
		defer db.Close()
		m := newTestManager(t, dir, filepath.Join(t.TempDir(), "backups"), testPassphrase)
		info, err := m.Create(context.Background(), false)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		manifest, err := m.Verify(context.Background(), filepath.Join(m.backupDir, info.Name))
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		found := false
		for _, f := range manifest.Files {
			if f.Name == MasterKeyFileName {
				found = true
				if f.SizeBytes != int64(len(key)) {
					t.Fatalf("环境变量主密钥大小 = %d", f.SizeBytes)
				}
			}
		}
		if !found {
			t.Fatal("manifest 缺少 master.key")
		}
	})
}

func TestCreateToArbitraryPath(t *testing.T) {
	dataDir, db := setupDataDir(t)
	defer db.Close()
	m := newTestManager(t, dataDir, filepath.Join(t.TempDir(), "backups"), testPassphrase)

	out := filepath.Join(t.TempDir(), "manual", "custom.lsbak")
	info, err := m.CreateTo(context.Background(), out, false)
	if err != nil {
		t.Fatalf("CreateTo: %v", err)
	}
	if info.Name != "custom.lsbak" {
		t.Fatalf("Info.Name = %q", info.Name)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("读取 --out 归档: %v", err)
	}
	if !bytes.HasPrefix(raw, []byte(Magic)) {
		t.Fatal("--out 归档头不是 LSBAK1")
	}
	verifyM := newTestManager(t, dataDir, m.backupDir, testPassphrase)
	if _, err := verifyM.Verify(context.Background(), out); err != nil {
		t.Fatalf("Verify(--out): %v", err)
	}
}

// ---------- 小工具 ----------

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func truncateBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}
