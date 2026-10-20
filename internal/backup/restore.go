package backup

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RestoreOptions 是 Restore 的参数。
type RestoreOptions struct {
	File       string
	DataDir    string
	Passphrase string
	// KeepOld 为 true 时保留 <dataDir>.pre-restore-<ts>/，默认 true。
	// 注：bool 零值无法区分「未设置」与「显式 false」，本实现默认保留旧数据，
	// 仅在显式设置 DeletePreRestore=true 时才删除 pre-restore 目录。
	KeepOld bool
	// DeletePreRestore 为 true 时恢复成功后删除 pre-restore 目录（扩展字段）。
	DeletePreRestore bool
	// MaxSchemaVersion 是当前程序支持的最高迁移版本，用于拒绝「来自更新版本」的备份。
	MaxSchemaVersion int
}

// RestoreResult 是恢复结果。
type RestoreResult struct {
	Manifest      *Manifest
	PreRestoreDir string
	RestoredFiles []string
}

// StagedInfo 是暂存恢复目录内 meta.json 的结构（扩展 API）。
type StagedInfo struct {
	Source        string    `json:"source"`
	CreatedAt     int64     `json:"created_at"`
	VerifiedAt    int64     `json:"verified_at"`
	ArchiveSHA256 string    `json:"archive_sha256"`
	Manifest      *Manifest `json:"manifest"`
}

// managedRestoreNames 是恢复时需要「先移走、再放置新文件」的受管文件。
// backup.pass 与 backups/ 故意不在其中：口令与备份本身必须跨恢复保留。
func managedRestoreNames() []string {
	return []string{
		KnownHostsFileName,
		ArtifactsDirName,
		MasterKeyFileName,
		DBFileName,
		DBFileName + "-wal",
		DBFileName + "-shm",
	}
}

// Restore 离线恢复：解压到临时目录 → 校验 → 把现有受管文件移到 <dataDir>.pre-restore-<ts>/ → 放置新文件。
// **要求服务已停止**（调用方负责）；恢复前自动做一次安全快照（目录级 move，不复制大文件）。
//
// File 既可以是 LSBAK1 归档文件，也可以是 StageRestore 产出的暂存目录（扩展行为）。
func Restore(opts RestoreOptions) (*RestoreResult, error) {
	dataDir, err := mustAbs(opts.DataDir)
	if err != nil {
		return nil, fmt.Errorf("backup: DataDir 非法: %w", err)
	}
	if strings.TrimSpace(opts.File) == "" {
		return nil, errors.New("backup: 恢复文件不能为空")
	}
	file, err := mustAbs(opts.File)
	if err != nil {
		return nil, fmt.Errorf("backup: File 非法: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dataDir), 0o750); err != nil {
		return nil, fmt.Errorf("backup: 创建数据目录父目录失败: %w", err)
	}

	fi, err := os.Stat(file)
	if err != nil {
		return nil, fmt.Errorf("backup: 恢复源 %s 不可读: %w", file, err)
	}

	ctx := context.Background()
	var (
		manifest *Manifest
		srcDir   string
		cleanup  func()
	)
	if fi.IsDir() {
		// 暂存目录（StageRestore 产出）：重新做一次全量校验，不信任磁盘上的旧校验结果。
		manifest, err = validateStagedDir(ctx, file)
		if err != nil {
			return nil, err
		}
		srcDir = file
	} else {
		passphrase, perr := resolvePassphrase(dataDir, opts.Passphrase, false)
		if perr != nil {
			return nil, perr
		}
		tmpDir, terr := os.MkdirTemp(filepath.Dir(dataDir), ".leadstack-restore-*")
		if terr != nil {
			return nil, fmt.Errorf("backup: 创建恢复临时目录失败: %w", terr)
		}
		cleanup = func() { _ = os.RemoveAll(tmpDir) }
		manifest, err = readArchive(ctx, file, passphrase, archiveReadOptions{
			DestDir:          tmpDir,
			MaxSchemaVersion: opts.MaxSchemaVersion,
		})
		if err != nil {
			cleanup()
			return nil, err
		}
		srcDir = tmpDir
	}
	if cleanup != nil {
		defer cleanup()
	}
	if opts.MaxSchemaVersion > 0 && manifest.SchemaVersion > opts.MaxSchemaVersion {
		return nil, fmt.Errorf("%w: 备份 schema_version=%d，当前程序最高支持 %d", ErrSchemaTooNew, manifest.SchemaVersion, opts.MaxSchemaVersion)
	}
	return applyRestore(dataDir, srcDir, manifest, opts)
}

// applyRestore 执行「移走旧文件 → 放置新文件」的事务式流程，失败时 best-effort 回滚。
func applyRestore(dataDir, srcDir string, manifest *Manifest, opts RestoreOptions) (*RestoreResult, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("backup: 创建数据目录失败: %w", err)
	}
	preDir, err := uniquePreRestoreDir(dataDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(preDir, 0o700); err != nil {
		return nil, fmt.Errorf("backup: 创建 pre-restore 目录失败: %w", err)
	}

	moved, err := moveManagedToPreRestore(dataDir, preDir)
	if err != nil {
		if rbErr := rollbackRestore(dataDir, preDir, nil, moved); rbErr != nil {
			return nil, fmt.Errorf("%w；回滚旧文件也失败：%v（旧数据仍保留在 %s）", err, rbErr, preDir)
		}
		return nil, err
	}

	placed := make([]string, 0, len(manifest.Files))
	rollback := func(cause error) (*RestoreResult, error) {
		if rbErr := rollbackRestore(dataDir, preDir, placed, moved); rbErr != nil {
			return nil, fmt.Errorf("%w；自动回滚失败：%v（旧数据仍保留在 %s，请手工恢复）", cause, rbErr, preDir)
		}
		return nil, fmt.Errorf("%w；已自动回滚到恢复前状态", cause)
	}

	for _, mf := range manifest.Files {
		src := filepath.Join(srcDir, filepath.FromSlash(mf.Name))
		dst := filepath.Join(dataDir, filepath.FromSlash(mf.Name))
		if err := filepathRel(dataDir, dst); err != nil {
			return rollback(fmt.Errorf("backup: 恢复文件 %q 路径非法: %w", mf.Name, err))
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return rollback(fmt.Errorf("backup: 创建目标目录失败: %w", err))
		}
		if err := os.Rename(src, dst); err != nil {
			// 跨文件系统时退化为流式复制 + 删除源文件。
			if copyErr := copyFileStream(src, dst, 0o600); copyErr != nil {
				return rollback(fmt.Errorf("backup: 放置文件 %q 失败: %w", mf.Name, copyErr))
			}
			_ = os.Remove(src)
		}
		placed = append(placed, mf.Name)
	}
	syncDir(dataDir)

	result := &RestoreResult{
		Manifest:      manifest,
		PreRestoreDir: preDir,
		RestoredFiles: placed,
	}
	if opts.DeletePreRestore {
		if err := os.RemoveAll(preDir); err != nil {
			return result, fmt.Errorf("backup: 恢复成功，但删除 pre-restore 目录失败: %w", err)
		}
		result.PreRestoreDir = ""
	}
	return result, nil
}

// moveManagedToPreRestore 把现有受管文件 move 到 pre-restore 目录（不复制大文件）。
func moveManagedToPreRestore(dataDir, preDir string) ([]string, error) {
	moved := make([]string, 0, len(managedRestoreNames()))
	for _, name := range managedRestoreNames() {
		src := filepath.Join(dataDir, name)
		if _, err := os.Lstat(src); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return moved, fmt.Errorf("backup: 检查现有文件 %s 失败: %w", name, err)
		}
		dst := filepath.Join(preDir, name)
		if err := os.Rename(src, dst); err != nil {
			return moved, fmt.Errorf("backup: 移动现有文件 %s 到 %s 失败: %w", name, preDir, err)
		}
		moved = append(moved, name)
	}
	return moved, nil
}

// rollbackRestore 删除本轮已放置的新文件，并把旧文件从 pre-restore 移回。
func rollbackRestore(dataDir, preDir string, placed, moved []string) error {
	var problems []string
	for i := len(placed) - 1; i >= 0; i-- {
		p := filepath.Join(dataDir, filepath.FromSlash(placed[i]))
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			problems = append(problems, fmt.Sprintf("删除新文件 %s: %v", placed[i], err))
		}
	}
	// 若本轮创建了 artifacts 目录且已空，顺手清掉。
	_ = os.Remove(filepath.Join(dataDir, ArtifactsDirName))

	for _, name := range moved {
		src := filepath.Join(preDir, name)
		dst := filepath.Join(dataDir, name)
		if err := os.Rename(src, dst); err != nil {
			problems = append(problems, fmt.Sprintf("移回旧文件 %s: %v", name, err))
		}
	}
	if len(problems) == 0 {
		_ = os.Remove(preDir)
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}

// uniquePreRestoreDir 生成不冲突的 <dataDir>.pre-restore-<ts> 路径。
func uniquePreRestoreDir(dataDir string) (string, error) {
	base := dataDir + ".pre-restore-" + time.Now().Format("20060102-150405")
	for i := 0; i < 1000; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", base, i+1)
		}
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
	}
	return "", errors.New("backup: 无法生成唯一的 pre-restore 目录名")
}

// validateStagedDir 重新校验 StageRestore 产出的暂存目录（manifest + 文件哈希 + DB 完整性）。
func validateStagedDir(ctx context.Context, dir string) (*Manifest, error) {
	manifestPath := filepath.Join(dir, ManifestFileName)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("backup: 暂存目录缺少 %s: %w", ManifestFileName, err)
	}
	if len(raw) > maxManifestSize {
		return nil, fmt.Errorf("%w: 暂存 manifest.json 过大", ErrCorrupt)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("%w: 解析暂存 manifest.json 失败: %v", ErrCorrupt, err)
	}
	if err := validateManifest(&manifest); err != nil {
		return nil, err
	}

	for _, mf := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := filepath.Join(dir, filepath.FromSlash(mf.Name))
		if err := filepathRel(dir, path); err != nil {
			return nil, fmt.Errorf("%w: 暂存文件 %q 路径非法: %v", ErrCorrupt, mf.Name, err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("%w: 暂存文件 %q 不可读: %v", ErrCorrupt, mf.Name, err)
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: 暂存文件 %q 不是普通文件", ErrCorrupt, mf.Name)
		}
		if fi.Size() != mf.SizeBytes {
			return nil, fmt.Errorf("%w: 暂存文件 %q 大小不符", ErrCorrupt, mf.Name)
		}
		sum, err := fileSHA256(path)
		if err != nil {
			return nil, err
		}
		if sum != mf.SHA256 {
			return nil, fmt.Errorf("%w: 暂存文件 %q 校验和不符", ErrCorrupt, mf.Name)
		}
	}
	dbPath := filepath.Join(dir, DBFileName)
	if err := checkDatabaseIntegrity(ctx, dbPath); err != nil {
		return nil, err
	}
	schema, err := readSchemaVersion(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	if schema != manifest.SchemaVersion {
		return nil, fmt.Errorf("%w: 暂存数据库 schema %d 与 manifest %d 不一致", ErrCorrupt, schema, manifest.SchemaVersion)
	}
	return &manifest, nil
}

// StageRestore 校验归档并把内容解压暂存到 pendingDir（含 meta.json），供下次启动时 Restore 应用。
// 校验失败不会留下半成品（扩展 API）。
func StageRestore(ctx context.Context, file, pendingDir, passphrase string) (*StagedInfo, error) {
	if strings.TrimSpace(file) == "" || strings.TrimSpace(pendingDir) == "" {
		return nil, errors.New("backup: 归档路径与暂存目录都不能为空")
	}
	src, err := mustAbs(file)
	if err != nil {
		return nil, fmt.Errorf("backup: 归档路径非法: %w", err)
	}
	pending, err := mustAbs(pendingDir)
	if err != nil {
		return nil, fmt.Errorf("backup: 暂存目录非法: %w", err)
	}
	fi, err := os.Stat(src)
	if err != nil {
		return nil, fmt.Errorf("backup: 归档 %s 不可读: %w", src, err)
	}
	if fi.IsDir() {
		return nil, errors.New("backup: 暂存源必须是 LSBAK1 归档文件")
	}
	if err := os.MkdirAll(filepath.Dir(pending), 0o750); err != nil {
		return nil, fmt.Errorf("backup: 创建暂存父目录失败: %w", err)
	}

	tmpDir, err := os.MkdirTemp(filepath.Dir(pending), ".restore-stage-*")
	if err != nil {
		return nil, fmt.Errorf("backup: 创建暂存临时目录失败: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(tmpDir)
		}
	}()
	if err := os.Chmod(tmpDir, 0o700); err != nil {
		return nil, fmt.Errorf("backup: 设置暂存目录权限失败: %w", err)
	}

	manifest, err := readArchive(ctx, src, passphrase, archiveReadOptions{DestDir: tmpDir})
	if err != nil {
		return nil, err
	}
	archiveSHA, err := fileSHA256(src)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	staged := &StagedInfo{
		Source:        filepath.Base(src),
		CreatedAt:     now,
		VerifiedAt:    now,
		ArchiveSHA256: archiveSHA,
		Manifest:      manifest,
	}
	metaBytes, err := json.MarshalIndent(staged, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("backup: 编码 meta.json 失败: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, MetaFileName), metaBytes, 0o600); err != nil {
		return nil, fmt.Errorf("backup: 写 meta.json 失败: %w", err)
	}
	if err := os.Chmod(filepath.Join(tmpDir, MetaFileName), 0o600); err != nil {
		return nil, fmt.Errorf("backup: 设置 meta.json 权限失败: %w", err)
	}

	// 原子替换暂存目录：旧目录先改名为 .old-xxx，成功后删除；失败则恢复旧目录。
	oldDir := ""
	if _, err := os.Lstat(pending); err == nil {
		suffix, rerr := randomHex(8)
		if rerr != nil {
			return nil, rerr
		}
		oldDir = pending + ".old-" + suffix
		if err := os.Rename(pending, oldDir); err != nil {
			return nil, fmt.Errorf("backup: 移走旧暂存目录失败: %w", err)
		}
	}
	if err := os.Rename(tmpDir, pending); err != nil {
		if oldDir != "" {
			_ = os.Rename(oldDir, pending)
		}
		return nil, fmt.Errorf("backup: 发布暂存恢复目录失败: %w", err)
	}
	ok = true
	if oldDir != "" {
		_ = os.RemoveAll(oldDir)
	}
	syncDir(filepath.Dir(pending))
	return staged, nil
}

// randomHex 生成 n 字节随机数的 hex 字符串。
func randomHex(n int) (string, error) {
	b, err := randomBytes(n)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// copyFileStream 流式复制文件（用于跨文件系统放置文件）。
func copyFileStream(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
