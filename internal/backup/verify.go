package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// maxManifestSize 限制 manifest.json 体积，防止恶意归档占用内存。
const maxManifestSize = 16 << 20

// archiveReadOptions 控制 readArchive 的行为。
type archiveReadOptions struct {
	// DestDir 非空时把校验通过的归档内容解压到该目录；为空则只校验（DB 仍会落临时文件做完整性检查）。
	DestDir string
	// MaxSchemaVersion > 0 时，备份 schema 更高则返回 ErrSchemaTooNew。
	MaxSchemaVersion int
}

// readArchive 是 Verify / Restore / StageRestore 的共同实现：
// 校验 header 与口令 → 流式解密 tar.gz → manifest 与逐文件哈希校验 → DB 完整性校验。
func readArchive(ctx context.Context, archivePath, passphrase string, opt archiveReadOptions) (*Manifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("backup: 打开归档 %s 失败: %w", archivePath, err)
	}
	defer f.Close()

	headerBytes, hdr, err := readHeader(f)
	if err != nil {
		return nil, err
	}
	key, err := checkPassphrase(hdr, passphrase)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce, err := hdr.nonceBytes()
	if err != nil {
		return nil, err
	}

	cr := newChunkReader(f, aead, nonce, headerBytes)
	gz, err := gzip.NewReader(ctxReader{ctx: ctx, r: cr})
	if err != nil {
		return nil, fmt.Errorf("%w: gzip 解码失败: %v", ErrCorrupt, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	manifest, manifestRaw, err := readManifestEntry(tr)
	if err != nil {
		return nil, err
	}
	if opt.MaxSchemaVersion > 0 && manifest.SchemaVersion > opt.MaxSchemaVersion {
		return nil, fmt.Errorf("%w: 备份 schema_version=%d，当前程序最高支持 %d", ErrSchemaTooNew, manifest.SchemaVersion, opt.MaxSchemaVersion)
	}

	expected := make(map[string]ManifestFile, len(manifest.Files))
	totalBytes := int64(0)
	for _, mf := range manifest.Files {
		expected[mf.Name] = mf
		if mf.SizeBytes > 0 && totalBytes > (1<<62)-mf.SizeBytes {
			return nil, fmt.Errorf("%w: manifest 声明体积溢出", ErrCorrupt)
		}
		totalBytes += mf.SizeBytes
	}

	var tempDir string
	if opt.DestDir == "" {
		tempDir, err = os.MkdirTemp("", "leadstack-verify-*")
		if err != nil {
			return nil, fmt.Errorf("backup: 创建临时校验目录失败: %w", err)
		}
		defer os.RemoveAll(tempDir)
	}

	seen := make(map[string]struct{}, len(expected))
	dbTempPath := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: 读取 tar 失败: %v", ErrCorrupt, err)
		}
		name := hdr.Name
		if err := validateEntryName(name); err != nil {
			return nil, err
		}
		if name == ManifestFileName {
			return nil, fmt.Errorf("%w: manifest.json 只能出现一次且必须位于第一个条目", ErrCorrupt)
		}
		mf, ok := expected[name]
		if !ok {
			return nil, fmt.Errorf("%w: 归档包含 manifest 未声明的文件 %q", ErrCorrupt, name)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("%w: 条目 %q 不是普通文件（拒绝符号链接/目录/设备）", ErrCorrupt, name)
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("%w: 归档包含重复条目 %q", ErrCorrupt, name)
		}
		seen[name] = struct{}{}
		if hdr.Size != mf.SizeBytes {
			return nil, fmt.Errorf("%w: %s 大小 %d 与 manifest 声明 %d 不一致", ErrCorrupt, name, hdr.Size, mf.SizeBytes)
		}

		mode := os.FileMode(0o600)
		var dstPath string
		switch {
		case opt.DestDir != "":
			dstPath = filepath.Join(opt.DestDir, filepath.FromSlash(name))
			if err := filepathRel(opt.DestDir, dstPath); err != nil {
				return nil, fmt.Errorf("%w: 条目 %q 路径非法: %v", ErrCorrupt, name, err)
			}
			if err := os.MkdirAll(filepath.Dir(dstPath), 0o700); err != nil {
				return nil, fmt.Errorf("backup: 创建解压目录失败: %w", err)
			}
		case name == DBFileName:
			dstPath = filepath.Join(tempDir, DBFileName)
		}

		var (
			dstFile *os.File
			hashW   = sha256.New()
			writers []io.Writer
		)
		writers = append(writers, hashW)
		if dstPath != "" {
			dstFile, err = os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return nil, fmt.Errorf("backup: 创建解压文件 %s 失败: %w", name, err)
			}
			writers = append(writers, dstFile)
		}
		n, copyErr := io.Copy(io.MultiWriter(writers...), io.LimitReader(ctxReader{ctx: ctx, r: tr}, hdr.Size))
		if dstFile != nil {
			if syncErr := dstFile.Sync(); syncErr != nil && copyErr == nil {
				copyErr = syncErr
			}
			if closeErr := dstFile.Close(); closeErr != nil && copyErr == nil {
				copyErr = closeErr
			}
		}
		if copyErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, fmt.Errorf("%w: 读取条目 %q 失败: %v", ErrCorrupt, name, copyErr)
		}
		if n != mf.SizeBytes {
			return nil, fmt.Errorf("%w: %s 实际大小 %d 与 manifest %d 不一致", ErrCorrupt, name, n, mf.SizeBytes)
		}
		if got := hex.EncodeToString(hashW.Sum(nil)); got != mf.SHA256 {
			return nil, fmt.Errorf("%w: %s 的 sha256 与 manifest 不一致", ErrCorrupt, name)
		}
		if name == DBFileName {
			dbTempPath = dstPath
		}
	}

	// tar 结束：manifest 声明的每个文件都必须出现。
	for name := range expected {
		if _, ok := seen[name]; !ok {
			return nil, fmt.Errorf("%w: 归档缺少 manifest 声明的文件 %q", ErrCorrupt, name)
		}
	}

	// 读完 gzip 剩余流：校验 CRC/长度，并让尾部垃圾数据暴露出来。
	if _, err := io.Copy(io.Discard, gz); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: gzip 流校验失败（可能被截断或篡改）: %v", ErrCorrupt, err)
	}

	if dbTempPath == "" {
		return nil, fmt.Errorf("%w: 归档缺少 %s", ErrCorrupt, DBFileName)
	}
	if err := checkDatabaseIntegrity(ctx, dbTempPath); err != nil {
		return nil, err
	}
	schema, err := readSchemaVersion(ctx, dbTempPath)
	if err != nil {
		return nil, err
	}
	if schema != manifest.SchemaVersion {
		return nil, fmt.Errorf("%w: 数据库 schema 版本 %d 与 manifest 声明 %d 不一致", ErrCorrupt, schema, manifest.SchemaVersion)
	}

	if opt.DestDir != "" {
		// 最后才写 manifest.json：解压目录里的 manifest 只作为暂存元数据，
		// 真正的校验以归档内第一条目为准。
		if err := os.WriteFile(filepath.Join(opt.DestDir, ManifestFileName), manifestRaw, 0o600); err != nil {
			return nil, fmt.Errorf("backup: 写 manifest.json 失败: %w", err)
		}
	}
	return manifest, nil
}

// readManifestEntry 读取 tar 的第一个条目（必须是 manifest.json）。
func readManifestEntry(tr *tar.Reader) (*Manifest, []byte, error) {
	hdr, err := tr.Next()
	if errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("%w: 归档为空", ErrCorrupt)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%w: 读取 tar 失败: %v", ErrCorrupt, err)
	}
	if hdr.Name != ManifestFileName {
		return nil, nil, fmt.Errorf("%w: 第一个 tar 条目必须是 %s，实际为 %q", ErrCorrupt, ManifestFileName, hdr.Name)
	}
	if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
		return nil, nil, fmt.Errorf("%w: manifest.json 不是普通文件", ErrCorrupt)
	}
	if hdr.Size <= 0 || hdr.Size > maxManifestSize {
		return nil, nil, fmt.Errorf("%w: manifest.json 大小非法 %d", ErrCorrupt, hdr.Size)
	}
	raw, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: 读取 manifest.json 失败: %v", ErrCorrupt, err)
	}
	if int64(len(raw)) != hdr.Size {
		return nil, nil, fmt.Errorf("%w: manifest.json 被截断", ErrCorrupt)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, fmt.Errorf("%w: 解析 manifest.json 失败: %v", ErrCorrupt, err)
	}
	if err := validateManifest(&m); err != nil {
		return nil, nil, err
	}
	return &m, raw, nil
}
