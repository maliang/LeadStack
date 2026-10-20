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
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// payloadFile 是归档中的一个待写文件：来自磁盘或内存（环境变量提供的主密钥）。
type payloadFile struct {
	Name      string
	Path      string
	Data      []byte
	SizeBytes int64
	SHA256    string
	Mode      os.FileMode
}

// content 打开 payload 内容流。
func (p payloadFile) content() (io.ReadCloser, error) {
	if p.Data != nil {
		return io.NopCloser(bytes.NewReader(p.Data)), nil
	}
	f, err := os.Open(p.Path)
	if err != nil {
		return nil, fmt.Errorf("backup: 打开 %s 失败: %w", p.Name, err)
	}
	return f, nil
}

// validateEntryName 是归档路径白名单：只允许契约第 30 节列出的文件名。
// 任何绝对路径、`..`、符号链接式条目、未知文件都会被拒绝。
func validateEntryName(name string) error {
	switch name {
	case ManifestFileName, DBFileName, MasterKeyFileName, KnownHostsFileName:
		return nil
	}
	if strings.HasPrefix(name, ArtifactsDirName+"/") {
		rest := strings.TrimPrefix(name, ArtifactsDirName+"/")
		if err := validateArtifactName(rest); err != nil {
			return fmt.Errorf("%w: 非法制品条目 %q: %v", ErrCorrupt, name, err)
		}
		return nil
	}
	return fmt.Errorf("%w: 归档包含未知条目 %q（不在白名单内）", ErrCorrupt, name)
}

// validateArtifactName 校验 artifacts/<id> 的 id 部分：必须是一层普通文件名。
func validateArtifactName(id string) error {
	if id == "" || id == "." || id == ".." {
		return errors.New("制品名为空或非法")
	}
	if len(id) > 255 {
		return errors.New("制品名过长")
	}
	if strings.ContainsAny(id, `/\`) || strings.ContainsRune(id, 0) {
		return errors.New("制品名包含路径分隔符")
	}
	if id != path.Base(id) {
		return errors.New("制品名不是单层文件名")
	}
	return nil
}

// validateManifest 深度校验 manifest 的结构与文件条目。
func validateManifest(m *Manifest) error {
	if m == nil {
		return fmt.Errorf("%w: manifest 为空", ErrCorrupt)
	}
	if m.FormatVersion != FormatVersion {
		return fmt.Errorf("%w: manifest 格式版本 %d 不受支持", ErrCorrupt, m.FormatVersion)
	}
	if m.SchemaVersion < 0 {
		return fmt.Errorf("%w: manifest schema_version 非法", ErrCorrupt)
	}
	if len(m.Files) == 0 {
		return fmt.Errorf("%w: manifest 文件列表为空", ErrCorrupt)
	}
	if len(m.Files) > 1_000_000 {
		return fmt.Errorf("%w: manifest 文件数量过多", ErrCorrupt)
	}
	seen := make(map[string]struct{}, len(m.Files))
	for i := range m.Files {
		f := &m.Files[i]
		if err := validateEntryName(f.Name); err != nil {
			return err
		}
		if _, dup := seen[f.Name]; dup {
			return fmt.Errorf("%w: manifest 含重复文件 %q", ErrCorrupt, f.Name)
		}
		seen[f.Name] = struct{}{}
		if f.SizeBytes < 0 {
			return fmt.Errorf("%w: 文件 %q 大小为负", ErrCorrupt, f.Name)
		}
		if len(f.SHA256) != 64 {
			return fmt.Errorf("%w: 文件 %q sha256 格式非法", ErrCorrupt, f.Name)
		}
		if _, err := hex.DecodeString(f.SHA256); err != nil {
			return fmt.Errorf("%w: 文件 %q sha256 非 hex", ErrCorrupt, f.Name)
		}
	}
	if _, ok := seen[DBFileName]; !ok {
		return fmt.Errorf("%w: manifest 缺少 %s", ErrCorrupt, DBFileName)
	}
	if _, ok := seen[MasterKeyFileName]; !ok {
		return fmt.Errorf("%w: manifest 缺少 %s", ErrCorrupt, MasterKeyFileName)
	}
	return nil
}

// hashPayload 计算 payload 的 size 与 sha256（流式）。
func hashPayload(p *payloadFile) error {
	if p.Data != nil {
		sum := sha256.Sum256(p.Data)
		p.SizeBytes = int64(len(p.Data))
		p.SHA256 = hex.EncodeToString(sum[:])
		return nil
	}
	f, err := os.Open(p.Path)
	if err != nil {
		return fmt.Errorf("backup: 打开 %s 失败: %w", p.Name, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("backup: 读取 %s 状态失败: %w", p.Name, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("backup: %s 不是普通文件", p.Name)
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return fmt.Errorf("backup: 计算 %s 校验和失败: %w", p.Name, err)
	}
	p.SizeBytes = n
	p.SHA256 = hex.EncodeToString(h.Sum(nil))
	return nil
}

// writePayload 把 manifest.json（必须是第一个条目）与所有 payload 写成 tar.gz 流。
func writePayload(ctx context.Context, w io.Writer, manifestBytes []byte, files []payloadFile) error {
	gz, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		return fmt.Errorf("backup: 初始化 gzip 失败: %w", err)
	}
	tw := tar.NewWriter(gz)

	if err := writeTarBytes(tw, ManifestFileName, manifestBytes, 0o600); err != nil {
		return err
	}
	for i := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeTarPayload(ctx, tw, &files[i]); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("backup: 结束 tar 失败: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("backup: 结束 gzip 失败: %w", err)
	}
	return nil
}

func writeTarBytes(tw *tar.Writer, name string, data []byte, mode os.FileMode) error {
	hdr := &tar.Header{
		Name:     name,
		Mode:     int64(mode.Perm()),
		Size:     int64(len(data)),
		Typeflag: tar.TypeReg,
		ModTime:  time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("backup: 写 tar header（%s）失败: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("backup: 写 tar 内容（%s）失败: %w", name, err)
	}
	return nil
}

// writeTarPayload 流式写一个文件，并在写的同时重新计算 sha256；
// 若文件在「算哈希」与「写入」之间被修改则报错，避免产出校验不一致的归档。
func writeTarPayload(ctx context.Context, tw *tar.Writer, p *payloadFile) error {
	hdr := &tar.Header{
		Name:     p.Name,
		Mode:     int64(p.Mode.Perm()),
		Size:     p.SizeBytes,
		Typeflag: tar.TypeReg,
		ModTime:  time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("backup: 写 tar header（%s）失败: %w", nameOr(p.Name), err)
	}
	src, err := p.content()
	if err != nil {
		return err
	}
	defer src.Close()

	h := sha256.New()
	written, err := io.Copy(tw, io.TeeReader(ctxReader{ctx: ctx, r: io.LimitReader(src, p.SizeBytes)}, h))
	if err != nil {
		return fmt.Errorf("backup: 写入 %s 失败: %w", p.Name, err)
	}
	if written != p.SizeBytes {
		return fmt.Errorf("backup: %s 在备份过程中发生变化（期望 %d 字节，实际 %d 字节）", p.Name, p.SizeBytes, written)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != p.SHA256 {
		return fmt.Errorf("backup: %s 在备份过程中被修改（哈希不一致）", p.Name)
	}
	return nil
}

func nameOr(s string) string {
	if s == "" {
		return "<unknown>"
	}
	return s
}

// buildManifest 由 payload 列表生成 manifest。
func buildManifest(appVersion string, schemaVersion int, createdAt int64, includesArtifacts bool, files []payloadFile) *Manifest {
	m := &Manifest{
		FormatVersion:     FormatVersion,
		AppVersion:        appVersion,
		SchemaVersion:     schemaVersion,
		CreatedAt:         createdAt,
		IncludesArtifacts: includesArtifacts,
		Files:             make([]ManifestFile, 0, len(files)),
	}
	for _, f := range files {
		m.Files = append(m.Files, ManifestFile{Name: f.Name, SizeBytes: f.SizeBytes, SHA256: f.SHA256})
	}
	return m
}

// copyRegularFile 把普通文件流式复制到目标路径（用于快照已知可变的小文件）。
func copyRegularFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("backup: %s 不是普通文件", src)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
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
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// masterKeyFromEnv 在 master.key 文件缺失时尝试从 LEADSTACK_MASTER_KEY 读取 32 字节 hex 密钥。
func masterKeyFromEnv() []byte {
	raw := strings.TrimSpace(os.Getenv(EnvMasterKey))
	if raw == "" {
		return nil
	}
	cleaned := strings.TrimPrefix(strings.TrimPrefix(raw, "0x"), "0X")
	cleaned = strings.NewReplacer(" ", "", ":", "", "-", "", "\n", "", "\t", "").Replace(cleaned)
	b, err := hex.DecodeString(cleaned)
	if err != nil || len(b) != 32 {
		return nil
	}
	return b
}

// hashFile 计算磁盘文件的 size 与 sha256（流式）。
func hashFile(p string) (int64, string, error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// jsonMarshal 是 json.Marshal 的薄封装，统一错误信息。
func jsonMarshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("backup: JSON 编码失败: %w", err)
	}
	return b, nil
}

// filepathRel 检查 child 是否位于 parent 之内（用于解包的纵深防御）。
func filepathRel(parent, child string) error {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("路径逃逸出目标目录")
	}
	return nil
}
