package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ---------- 构造“手工归档”的辅助（用于路径穿越 / 哈希不符等安全测试） ----------

type craftedEntry struct {
	Name     string
	Data     []byte
	Typeflag byte
	Linkname string
}

func shaHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeCraftedArchive 用与生产相同的 LSBAK1 格式写一个可由本包解开的归档。
func writeCraftedArchive(t *testing.T, path, passphrase string, manifest *Manifest, entries []craftedEntry) {
	t.Helper()
	salt := randomTestBytes(t, SaltLen)
	nonce := randomTestBytes(t, NonceLen)
	key, err := deriveKey(passphrase, salt)
	if err != nil {
		t.Fatalf("deriveKey: %v", err)
	}
	headerBytes, _, err := newArchiveHeader("crafted", manifest.SchemaVersion, manifest.CreatedAt, manifest.IncludesArtifacts, salt, nonce, passVerifyToken(key))
	if err != nil {
		t.Fatalf("newArchiveHeader: %v", err)
	}
	aead, err := newAEAD(key)
	if err != nil {
		t.Fatalf("newAEAD: %v", err)
	}

	var buf bytes.Buffer
	if err := writeHeader(&buf, headerBytes); err != nil {
		t.Fatalf("writeHeader: %v", err)
	}
	cw := newChunkWriter(&buf, aead, nonce, headerBytes, ChunkSize)
	gz := gzip.NewWriter(cw)
	tw := tar.NewWriter(gz)

	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: ManifestFileName, Mode: 0o600, Size: int64(len(manifestBytes)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("write manifest header: %v", err)
	}
	if _, err := tw.Write(manifestBytes); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.Name,
			Mode:     0o600,
			Typeflag: e.Typeflag,
			Linkname: e.Linkname,
		}
		if e.Typeflag == tar.TypeReg || e.Typeflag == tar.TypeRegA || e.Typeflag == 0 {
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(e.Data))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", e.Name, err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write(e.Data); err != nil {
				t.Fatalf("write %q: %v", e.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := cw.Close(); err != nil {
		t.Fatalf("chunk close: %v", err)
	}
	writeTestFile(t, path, buf.Bytes(), 0o600)
}

// validCraftManifest 生成只声明 db/master.key 的合法 manifest。
func validCraftManifest(schema int, createdAt int64, extra ...ManifestFile) *Manifest {
	files := []ManifestFile{
		{Name: DBFileName, SizeBytes: int64(len("db-bytes")), SHA256: shaHex([]byte("db-bytes"))},
		{Name: MasterKeyFileName, SizeBytes: int64(len("key-bytes")), SHA256: shaHex([]byte("key-bytes"))},
	}
	files = append(files, extra...)
	return &Manifest{
		FormatVersion: FormatVersion,
		AppVersion:    "crafted",
		SchemaVersion: schema,
		CreatedAt:     createdAt,
		Files:         files,
	}
}

func craftBaseEntries() []craftedEntry {
	return []craftedEntry{
		{Name: DBFileName, Data: []byte("db-bytes"), Typeflag: tar.TypeReg},
		{Name: MasterKeyFileName, Data: []byte("key-bytes"), Typeflag: tar.TypeReg},
	}
}

// ---------- 路径穿越 / 非法条目 ----------

func TestVerifyRejectsPathTraversalEntries(t *testing.T) {
	m := newTestManager(t, t.TempDir(), filepath.Join(t.TempDir(), "backups"), testPassphrase)
	dir := t.TempDir()

	cases := []struct {
		name  string
		entry craftedEntry
	}{
		{"父目录穿越", craftedEntry{Name: "../evil", Data: []byte("x"), Typeflag: tar.TypeReg}},
		{"多级穿越", craftedEntry{Name: "artifacts/../../evil", Data: []byte("x"), Typeflag: tar.TypeReg}},
		{"绝对路径", craftedEntry{Name: "/tmp/leadstack-evil", Data: []byte("x"), Typeflag: tar.TypeReg}},
		{"反斜杠穿越", craftedEntry{Name: `..\evil`, Data: []byte("x"), Typeflag: tar.TypeReg}},
		{"未知文件", craftedEntry{Name: "passwd", Data: []byte("x"), Typeflag: tar.TypeReg}},
		{"符号链接", craftedEntry{Name: KnownHostsFileName, Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := validCraftManifest(6, 1)
			if tc.entry.Name == KnownHostsFileName {
				manifest.Files = append(manifest.Files, ManifestFile{Name: KnownHostsFileName, SizeBytes: 0, SHA256: shaHex(nil)})
			}
			entries := append(craftBaseEntries(), tc.entry)
			path := filepath.Join(dir, "crafted.lsbak")
			writeCraftedArchive(t, path, testPassphrase, manifest, entries)
			if _, err := m.Verify(context.Background(), path); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("非法条目 %q 应返回 ErrCorrupt，实际: %v", tc.entry.Name, err)
			}
		})
	}
}

func TestVerifyDetectsManifestHashMismatch(t *testing.T) {
	m := newTestManager(t, t.TempDir(), filepath.Join(t.TempDir(), "backups"), testPassphrase)
	manifest := validCraftManifest(6, 1)
	// 声明错误的 db 哈希。
	manifest.Files[0].SHA256 = shaHex([]byte("different-content"))
	entries := craftBaseEntries()
	path := filepath.Join(t.TempDir(), "hash-mismatch.lsbak")
	writeCraftedArchive(t, path, testPassphrase, manifest, entries)

	_, err := m.Verify(context.Background(), path)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("manifest 哈希不符应返回 ErrCorrupt，实际: %v", err)
	}
}

func TestVerifyRejectsMissingManifestFile(t *testing.T) {
	m := newTestManager(t, t.TempDir(), filepath.Join(t.TempDir(), "backups"), testPassphrase)
	manifest := validCraftManifest(6, 1)
	// 只写 db，不写 master.key。
	entries := []craftedEntry{{Name: DBFileName, Data: []byte("db-bytes"), Typeflag: tar.TypeReg}}
	path := filepath.Join(t.TempDir(), "missing-file.lsbak")
	writeCraftedArchive(t, path, testPassphrase, manifest, entries)

	if _, err := m.Verify(context.Background(), path); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("归档缺文件应返回 ErrCorrupt，实际: %v", err)
	}
}

// ---------- 恢复往返 ----------

func TestRestoreRoundTrip(t *testing.T) {
	dataDir, db := setupDataDir(t)
	backupDir := filepath.Join(t.TempDir(), "backups")
	m := newTestManager(t, dataDir, backupDir, testPassphrase)
	origKey, err := os.ReadFile(filepath.Join(dataDir, MasterKeyFileName))
	if err != nil {
		t.Fatalf("读取 master.key: %v", err)
	}
	info, err := m.Create(context.Background(), true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	db.Close()

	// 破坏当前数据：删一台服务器、换主密钥、删 known_hosts 与制品。
	db2 := openTestDB(t, filepath.Join(dataDir, DBFileName))
	if _, err := db2.Exec(`DELETE FROM servers WHERE id='srv-1'`); err != nil {
		t.Fatalf("删除服务器: %v", err)
	}
	db2.Close()
	writeTestFile(t, filepath.Join(dataDir, MasterKeyFileName), randomTestBytes(t, 32), 0o600)
	if err := os.Remove(filepath.Join(dataDir, KnownHostsFileName)); err != nil {
		t.Fatalf("删除 known_hosts: %v", err)
	}
	if err := os.Remove(filepath.Join(dataDir, ArtifactsDirName, "art-1")); err != nil {
		t.Fatalf("删除制品: %v", err)
	}

	cleanupPreRestore(t, dataDir)
	defer cleanupPreRestore(t, dataDir)

	res, err := Restore(RestoreOptions{
		File:             filepath.Join(backupDir, info.Name),
		DataDir:          dataDir,
		Passphrase:       testPassphrase,
		MaxSchemaVersion: 6,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Manifest == nil || res.Manifest.SchemaVersion != 6 {
		t.Fatalf("恢复结果 manifest 异常: %+v", res.Manifest)
	}
	if res.PreRestoreDir == "" {
		t.Fatal("默认应保留 pre-restore 目录")
	}
	if _, err := os.Stat(res.PreRestoreDir); err != nil {
		t.Fatalf("pre-restore 目录不存在: %v", err)
	}
	if len(res.RestoredFiles) < 2 {
		t.Fatalf("RestoredFiles 异常: %v", res.RestoredFiles)
	}

	// 数据库恢复：被删的 srv-1 回来。
	names := readServerNames(t, filepath.Join(dataDir, DBFileName))
	if len(names) != 2 || names[0] != "db-1" || names[1] != "web-1" {
		t.Fatalf("恢复后的服务器列表异常: %v", names)
	}
	// 主密钥恢复：字节完全一致（凭证才能解密）。
	gotKey, err := os.ReadFile(filepath.Join(dataDir, MasterKeyFileName))
	if err != nil {
		t.Fatalf("读取恢复后的 master.key: %v", err)
	}
	if !bytes.Equal(gotKey, origKey) {
		t.Fatal("恢复后的 master.key 与原密钥不一致")
	}
	// known_hosts 与制品恢复。
	if _, err := os.Stat(filepath.Join(dataDir, KnownHostsFileName)); err != nil {
		t.Fatalf("known_hosts 未恢复: %v", err)
	}
	art, err := os.ReadFile(filepath.Join(dataDir, ArtifactsDirName, "art-1"))
	if err != nil || string(art) != "artifact-one-content" {
		t.Fatalf("制品未恢复: %v %q", err, string(art))
	}

	// pre-restore 里保留了恢复前的旧数据库（只有 db-1）。
	preNames := readServerNames(t, filepath.Join(res.PreRestoreDir, DBFileName))
	if len(preNames) != 1 || preNames[0] != "db-1" {
		t.Fatalf("pre-restore 内旧数据异常: %v", preNames)
	}
}

func TestRestoreWrongPassphrase(t *testing.T) {
	dataDir, db := setupDataDir(t)
	backupDir := filepath.Join(t.TempDir(), "backups")
	m := newTestManager(t, dataDir, backupDir, testPassphrase)
	info, err := m.Create(context.Background(), false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	db.Close()
	cleanupPreRestore(t, dataDir)
	defer cleanupPreRestore(t, dataDir)

	_, err = Restore(RestoreOptions{
		File:       filepath.Join(backupDir, info.Name),
		DataDir:    dataDir,
		Passphrase: "wrong-passphrase",
	})
	if !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("错误口令应返回 ErrBadPassphrase，实际: %v", err)
	}
	// 口令错误发生在任何文件移动之前：不应产生 pre-restore 目录。
	matches, _ := filepath.Glob(dataDir + ".pre-restore-*")
	if len(matches) != 0 {
		t.Fatalf("口令错误不应产生 pre-restore 目录: %v", matches)
	}
}

func TestRestoreRejectsSchemaTooNew(t *testing.T) {
	dataDir, db := setupDataDir(t)
	backupDir := filepath.Join(t.TempDir(), "backups")
	m := newTestManager(t, dataDir, backupDir, testPassphrase)
	info, err := m.Create(context.Background(), false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	db.Close()
	cleanupPreRestore(t, dataDir)
	defer cleanupPreRestore(t, dataDir)

	_, err = Restore(RestoreOptions{
		File:             filepath.Join(backupDir, info.Name),
		DataDir:          dataDir,
		Passphrase:       testPassphrase,
		MaxSchemaVersion: 5,
	})
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("schema 过新应返回 ErrSchemaTooNew，实际: %v", err)
	}
	matches, _ := filepath.Glob(dataDir + ".pre-restore-*")
	if len(matches) != 0 {
		t.Fatalf("schema 校验失败不应改动数据目录: %v", matches)
	}
}

func TestRestoreDeletePreRestore(t *testing.T) {
	dataDir, db := setupDataDir(t)
	backupDir := filepath.Join(t.TempDir(), "backups")
	m := newTestManager(t, dataDir, backupDir, testPassphrase)
	info, err := m.Create(context.Background(), false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	db.Close()
	cleanupPreRestore(t, dataDir)
	defer cleanupPreRestore(t, dataDir)

	res, err := Restore(RestoreOptions{
		File:             filepath.Join(backupDir, info.Name),
		DataDir:          dataDir,
		Passphrase:       testPassphrase,
		DeletePreRestore: true,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.PreRestoreDir != "" {
		t.Fatalf("DeletePreRestore=true 时 PreRestoreDir 应为空: %q", res.PreRestoreDir)
	}
	matches, _ := filepath.Glob(dataDir + ".pre-restore-*")
	if len(matches) != 0 {
		t.Fatalf("pre-restore 目录应已删除: %v", matches)
	}
}

// ---------- 暂存恢复（REST 上传路径） ----------

func TestStageRestoreAndApply(t *testing.T) {
	dataDir, db := setupDataDir(t)
	backupDir := filepath.Join(t.TempDir(), "backups")
	m := newTestManager(t, dataDir, backupDir, testPassphrase)
	info, err := m.Create(context.Background(), true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	db.Close()

	stagedDir := filepath.Join(dataDir, ".restore-pending")
	staged, err := StageRestore(context.Background(), filepath.Join(backupDir, info.Name), stagedDir, testPassphrase)
	if err != nil {
		t.Fatalf("StageRestore: %v", err)
	}
	if staged.Manifest == nil || staged.ArchiveSHA256 != info.SHA256 {
		t.Fatalf("StagedInfo 异常: %+v", staged)
	}
	// 暂存目录应包含 manifest、db、master.key、known_hosts、artifacts 与 meta.json。
	for _, name := range []string{ManifestFileName, DBFileName, MasterKeyFileName, KnownHostsFileName, MetaFileName, filepath.Join(ArtifactsDirName, "art-1")} {
		if _, err := os.Stat(filepath.Join(stagedDir, name)); err != nil {
			t.Fatalf("暂存目录缺少 %s: %v", name, err)
		}
	}
	metaRaw, err := os.ReadFile(filepath.Join(stagedDir, MetaFileName))
	if err != nil {
		t.Fatalf("读取 meta.json: %v", err)
	}
	var meta StagedInfo
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("解析 meta.json: %v", err)
	}
	if meta.Manifest == nil || meta.Source != info.Name {
		t.Fatalf("meta.json 内容异常: %+v", meta)
	}

	// 破坏数据后从暂存目录恢复。
	mutDB := openTestDB(t, filepath.Join(dataDir, DBFileName))
	if _, err := mutDB.Exec(`DELETE FROM servers`); err != nil {
		t.Fatalf("清空服务器: %v", err)
	}
	mutDB.Close()
	cleanupPreRestore(t, dataDir)
	defer cleanupPreRestore(t, dataDir)

	res, err := Restore(RestoreOptions{File: stagedDir, DataDir: dataDir, MaxSchemaVersion: 6})
	if err != nil {
		t.Fatalf("Restore(staged): %v", err)
	}
	if len(res.Manifest.Files) < 2 {
		t.Fatalf("暂存恢复 manifest 异常: %+v", res.Manifest)
	}
	names := readServerNames(t, filepath.Join(dataDir, DBFileName))
	if len(names) != 2 {
		t.Fatalf("暂存恢复后服务器数量 = %d, 期望 2", len(names))
	}
}

func TestStageRestoreRejectsCorruptArchive(t *testing.T) {
	dataDir, db := setupDataDir(t)
	backupDir := filepath.Join(t.TempDir(), "backups")
	m := newTestManager(t, dataDir, backupDir, testPassphrase)
	info, err := m.Create(context.Background(), false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	db.Close()

	raw, err := os.ReadFile(filepath.Join(backupDir, info.Name))
	if err != nil {
		t.Fatalf("读取归档: %v", err)
	}
	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)-32] ^= 0xAA
	tamperedPath := filepath.Join(t.TempDir(), "bad.lsbak")
	writeTestFile(t, tamperedPath, tampered, 0o600)

	pendingDir := filepath.Join(dataDir, ".restore-pending")
	if _, err := StageRestore(context.Background(), tamperedPath, pendingDir, testPassphrase); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("篡改归档暂存应返回 ErrCorrupt，实际: %v", err)
	}
	if _, err := os.Stat(pendingDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("校验失败不应留下暂存目录: %v", err)
	}
}

func TestRestoreStagedDirDetectsCorruption(t *testing.T) {
	dataDir, db := setupDataDir(t)
	backupDir := filepath.Join(t.TempDir(), "backups")
	m := newTestManager(t, dataDir, backupDir, testPassphrase)
	info, err := m.Create(context.Background(), false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	db.Close()

	stagedDir := filepath.Join(dataDir, ".restore-pending")
	if _, err := StageRestore(context.Background(), filepath.Join(backupDir, info.Name), stagedDir, testPassphrase); err != nil {
		t.Fatalf("StageRestore: %v", err)
	}
	// 篡改暂存文件：恢复时必须重新校验并拒绝。
	writeTestFile(t, filepath.Join(stagedDir, KnownHostsFileName), []byte("tampered"), 0o600)
	cleanupPreRestore(t, dataDir)
	defer cleanupPreRestore(t, dataDir)

	if _, err := Restore(RestoreOptions{File: stagedDir, DataDir: dataDir}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("暂存文件被篡改时应返回 ErrCorrupt，实际: %v", err)
	}
}

// TestApplyRestoreRollbackOnPlaceFailure 直接构造「放置新文件失败」，
// 验证旧文件会被移回、已放置的新文件会被清掉。
func TestApplyRestoreRollbackOnPlaceFailure(t *testing.T) {
	dataDir := t.TempDir()
	writeTestFile(t, filepath.Join(dataDir, DBFileName), []byte("old-db"), 0o600)
	writeTestFile(t, filepath.Join(dataDir, MasterKeyFileName), []byte("old-key"), 0o600)
	cleanupPreRestore(t, dataDir)
	defer cleanupPreRestore(t, dataDir)

	srcDir := t.TempDir()
	writeTestFile(t, filepath.Join(srcDir, DBFileName), []byte("new-db"), 0o600)
	writeTestFile(t, filepath.Join(srcDir, MasterKeyFileName), []byte("new-key"), 0o600)
	// manifest 声明一个源目录中不存在的文件，使放置阶段失败。
	manifest := &Manifest{
		FormatVersion: FormatVersion,
		SchemaVersion: 6,
		Files: []ManifestFile{
			{Name: DBFileName, SizeBytes: int64(len("new-db")), SHA256: shaHex([]byte("new-db"))},
			{Name: MasterKeyFileName, SizeBytes: int64(len("new-key")), SHA256: shaHex([]byte("new-key"))},
			{Name: ArtifactsDirName + "/missing", SizeBytes: 1, SHA256: shaHex([]byte("x"))},
		},
	}

	if _, err := applyRestore(dataDir, srcDir, manifest, RestoreOptions{}); err == nil {
		t.Fatal("放置文件失败时应返回错误")
	}
	oldDB, err := os.ReadFile(filepath.Join(dataDir, DBFileName))
	if err != nil || string(oldDB) != "old-db" {
		t.Fatalf("回滚后旧数据库应恢复: %q %v", string(oldDB), err)
	}
	oldKey, err := os.ReadFile(filepath.Join(dataDir, MasterKeyFileName))
	if err != nil || string(oldKey) != "old-key" {
		t.Fatalf("回滚后旧主密钥应恢复: %q %v", string(oldKey), err)
	}
	if matches, _ := filepath.Glob(dataDir + ".pre-restore-*"); len(matches) != 0 {
		t.Fatalf("完整回滚后不应残留 pre-restore 目录: %v", matches)
	}
}

// ---------- 一些防御性用例 ----------

func TestRestoreRequiresExistingSource(t *testing.T) {
	dataDir := t.TempDir()
	if _, err := Restore(RestoreOptions{File: filepath.Join(t.TempDir(), "nope.lsbak"), DataDir: dataDir}); err == nil {
		t.Fatal("不存在的恢复源应报错")
	}
	if _, err := Restore(RestoreOptions{File: "", DataDir: dataDir}); err == nil {
		t.Fatal("空 File 应报错")
	}
}

func TestStageRestoreRequiresArchive(t *testing.T) {
	dir := t.TempDir()
	if _, err := StageRestore(context.Background(), dir, filepath.Join(dir, "pending"), testPassphrase); err == nil {
		t.Fatal("暂存源为目录时应报错")
	}
}

func TestRestoreKeepsBackupPass(t *testing.T) {
	dataDir, db := setupDataDir(t)
	m := newTestManager(t, dataDir, filepath.Join(t.TempDir(), "backups"), "")
	info, err := m.Create(context.Background(), false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	db.Close()

	passPath := filepath.Join(dataDir, PassphraseFileName)
	passBefore, err := os.ReadFile(passPath)
	if err != nil {
		t.Fatalf("应有 backup.pass: %v", err)
	}
	cleanupPreRestore(t, dataDir)
	defer cleanupPreRestore(t, dataDir)

	// 显式传口令做恢复；backup.pass 不属于受管文件，必须原地保留。
	if _, err := Restore(RestoreOptions{
		File:       filepath.Join(m.backupDir, info.Name),
		DataDir:    dataDir,
		Passphrase: string(bytes.TrimSpace(passBefore)),
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	passAfter, err := os.ReadFile(passPath)
	if err != nil {
		t.Fatalf("恢复后 backup.pass 丢失: %v", err)
	}
	if !bytes.Equal(passBefore, passAfter) {
		t.Fatal("backup.pass 在恢复过程中被改动")
	}
}
