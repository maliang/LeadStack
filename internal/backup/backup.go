// Package backup 实现 LSBAK1 加密备份归档与离线恢复。
//
// 契约见 docs/ARCHITECTURE.md 第 30–35 节（已冻结，签名不得改名）。
// 该包不得 import internal/store / internal/api：SQLite 只用只读连接执行
// `VACUUM INTO` 得到一致快照。
//
// 归档布局（第 30 节）：
//
//	magic "LSBAK1\n" | headerLen uint32 BE | header JSON | 分块 AES-256-GCM(tar.gz)
//
// 载荷用 1 MiB 分块的 AES-256-GCM 加密（header 中记录基础 nonce 与 chunk_size），
// 从而既能流式处理几百 MB 的制品，又不把整包读进内存。header 额外携带
// `pass_verify`（HMAC-SHA256(key, "lsbak-verify") 前 8 字节），用于在解密前
// 把「口令错误」与「归档损坏」区分开。
package backup

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LSBAK1 格式常量。
const (
	// Magic 是归档文件头（7 字节）。
	Magic = "LSBAK1\n"
	// FormatVersion 是当前归档格式版本。
	FormatVersion = 1

	// 归档内的固定文件名。
	DBFileName         = "leadstack.db"
	MasterKeyFileName  = "master.key"
	KnownHostsFileName = "known_hosts"
	ArtifactsDirName   = "artifacts"
	ManifestFileName   = "manifest.json"

	// 备份目录内的辅助文件名。
	ArchiveFileName      = "archive.lsbak"
	MetaFileName         = "meta.json"
	PassphraseFileName   = "backup.pass"
	DefaultBackupDirName = "backups"

	// EnvPassphrase 是备份口令环境变量。
	EnvPassphrase = "LEADSTACK_BACKUP_PASSPHRASE"
	// EnvMasterKey 是主密钥环境变量（master.key 缺失时的后备来源）。
	EnvMasterKey = "LEADSTACK_MASTER_KEY"
)

// KDF 与加密参数（header 中记录）。
const (
	KDFName    = "scrypt"
	KDFN       = 32768
	KDFR       = 8
	KDFP       = 1
	KeyLen     = 32
	SaltLen    = 16
	CipherName = "aes-256-gcm"
	NonceLen   = 12
	// VerifyTokenLabel 是 header 中口令校验值的 HMAC 标签。
	VerifyTokenLabel = "lsbak-verify"
	// VerifyTokenLen 是口令校验值截断长度（字节）。
	VerifyTokenLen = 8
	// ChunkSize 是载荷流式加解密的分块大小。
	ChunkSize = 1 << 20
)

// 哨兵错误。Any 校验类错误都可用 errors.Is 区分：
//   - ErrBadPassphrase：header 口令校验值不匹配（口令错误）；
//   - ErrCorrupt：magic/header/载荷认证/manifest 哈希/DB 完整性等异常；
//   - ErrSchemaTooNew：备份来自更新版本。
var (
	ErrBadPassphrase = errors.New("backup: 口令错误（请检查 " + EnvPassphrase + " 或 " + PassphraseFileName + "）")
	ErrCorrupt       = errors.New("backup: 归档损坏或格式不符")
	ErrSchemaTooNew  = errors.New("backup: 备份来自更新的版本")
)

// Options 是 Manager 的构造参数。
type Options struct {
	// DataDir 是要备份的数据目录。
	DataDir string
	// BackupDir 是归档输出目录（默认 <data_dir>/backups）。
	BackupDir string
	// Passphrase 显式口令；空则从环境变量 / backup.pass 解析。
	Passphrase string
	// Now 返回当前 Unix 毫秒；nil 时使用 time.Now。
	Now func() int64
	// AppVersion 写入归档 header/manifest（扩展字段；空则 "dev"）。
	AppVersion string
}

// Info 是一份备份的元数据（REST 列表 / 边车 .json）。
type Info struct {
	Name              string `json:"name"`
	SizeBytes         int64  `json:"size_bytes"`
	CreatedAt         int64  `json:"created_at"`
	IncludesArtifacts bool   `json:"includes_artifacts"`
	SchemaVersion     int    `json:"schema_version"`
	SHA256            string `json:"sha256"`
}

// Manifest 是归档内 manifest.json 的结构。
type Manifest struct {
	FormatVersion     int            `json:"format_version"`
	AppVersion        string         `json:"app_version"`
	SchemaVersion     int            `json:"schema_version"`
	CreatedAt         int64          `json:"created_at"`
	IncludesArtifacts bool           `json:"includes_artifacts"`
	Files             []ManifestFile `json:"files"`
}

// ManifestFile 是 manifest 中的单个文件条目。
type ManifestFile struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

// Manager 负责备份的创建、列表、校验与删除。
type Manager struct {
	dataDir    string
	backupDir  string
	passphrase string
	now        func() int64
	appVersion string
}

// NewManager 校验并规范化 Options。
func NewManager(opts Options) (*Manager, error) {
	dataDir, err := mustAbs(opts.DataDir)
	if err != nil {
		return nil, fmt.Errorf("backup: DataDir 非法: %w", err)
	}
	backupDir := opts.BackupDir
	if strings.TrimSpace(backupDir) == "" {
		backupDir = filepath.Join(dataDir, DefaultBackupDirName)
	}
	backupDir, err = mustAbs(backupDir)
	if err != nil {
		return nil, fmt.Errorf("backup: BackupDir 非法: %w", err)
	}
	now := opts.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	appVersion := strings.TrimSpace(opts.AppVersion)
	if appVersion == "" {
		appVersion = "dev"
	}
	return &Manager{
		dataDir:    dataDir,
		backupDir:  backupDir,
		passphrase: opts.Passphrase,
		now:        now,
		appVersion: appVersion,
	}, nil
}

// Create 生成一份备份。在线执行：不停止服务，不阻塞写入过久（VACUUM INTO 快照）。
func (m *Manager) Create(ctx context.Context, includeArtifacts bool) (*Info, error) {
	if err := os.MkdirAll(m.backupDir, 0o750); err != nil {
		return nil, fmt.Errorf("backup: 创建备份目录 %s 失败: %w", m.backupDir, err)
	}
	now := m.now()
	base := "leadstack-" + time.UnixMilli(now).UTC().Format("20060102-150405") + ".lsbak"
	return m.createTo(ctx, filepath.Join(m.backupDir, base), includeArtifacts, true, true)
}

// CreateTo 生成一份备份并写入指定归档路径（CLI --out 使用；扩展 API）。
func (m *Manager) CreateTo(ctx context.Context, outPath string, includeArtifacts bool) (*Info, error) {
	if strings.TrimSpace(outPath) == "" {
		return nil, errors.New("backup: --out 路径为空")
	}
	finalPath, err := mustAbs(outPath)
	if err != nil {
		return nil, fmt.Errorf("backup: --out 路径非法: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o750); err != nil {
		return nil, fmt.Errorf("backup: 创建输出目录失败: %w", err)
	}
	writeSidecar := filepath.Dir(finalPath) == m.backupDir
	return m.createTo(ctx, finalPath, includeArtifacts, false, writeSidecar)
}

// createTo 是 Create / CreateTo 的共同实现。
//
// autoUnique=true 时以硬链接发布并自动避开重名（并发 Create 不会互相覆盖）；
// 为 false 时按调用方给定路径原子覆盖（CLI --out 语义）。
func (m *Manager) createTo(ctx context.Context, finalPath string, includeArtifacts, autoUnique, writeSidecar bool) (*Info, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	passphrase, err := resolvePassphrase(m.dataDir, m.passphrase, true)
	if err != nil {
		return nil, err
	}
	now := m.now()

	// 临时工作目录放在归档同目录，保证后续硬链接/rename 在同一文件系统内。
	workDir, err := os.MkdirTemp(filepath.Dir(finalPath), ".leadstack-backup-*")
	if err != nil {
		return nil, fmt.Errorf("backup: 创建备份临时目录失败: %w", err)
	}
	defer os.RemoveAll(workDir)

	// 1) 一致性数据库快照（绝不直接拷贝 WAL 模式的 .db 文件）。
	dbSnapshot := filepath.Join(workDir, DBFileName)
	schemaVersion, err := snapshotDatabase(ctx, dbFileName(m.dataDir), dbSnapshot)
	if err != nil {
		return nil, err
	}

	// 2) 收集其余 payload 并计算 size/sha256（流式）。
	payloads, err := m.collectPayloads(includeArtifacts, workDir)
	if err != nil {
		return nil, err
	}
	if err := hashPayloads(ctx, payloads); err != nil {
		return nil, err
	}

	// 3) manifest + header（header 明文，载荷加密）。
	manifest := buildManifest(m.appVersion, schemaVersion, now, includeArtifacts, payloads)
	manifestBytes, err := jsonMarshal(manifest)
	if err != nil {
		return nil, err
	}

	salt, err := randomBytes(SaltLen)
	if err != nil {
		return nil, err
	}
	nonce, err := randomBytes(NonceLen)
	if err != nil {
		return nil, err
	}
	key, err := deriveKey(passphrase, salt)
	if err != nil {
		return nil, err
	}
	headerBytes, _, err := newArchiveHeader(m.appVersion, schemaVersion, now, includeArtifacts, salt, nonce, passVerifyToken(key))
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}

	// 4) 写临时归档：magic/header → 分块加密的 tar.gz，同时计算整包 sha256。
	tmpPath := filepath.Join(workDir, ArchiveFileName+".tmp")
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("backup: 创建归档临时文件失败: %w", err)
	}
	hasher := sha256.New()
	bw := bufio.NewWriterSize(io.MultiWriter(f, hasher), 256<<10)
	writeErr := func() error {
		if err := writeHeader(bw, headerBytes); err != nil {
			return err
		}
		cw := newChunkWriter(bw, aead, nonce, headerBytes, ChunkSize)
		if err := writePayload(ctx, cw, manifestBytes, payloads); err != nil {
			return err
		}
		if err := cw.Close(); err != nil {
			return err
		}
		return bw.Flush()
	}()
	if writeErr != nil {
		_ = f.Close()
		return nil, writeErr
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("backup: 落盘归档失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("backup: 关闭归档失败: %w", err)
	}
	fi, err := os.Stat(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("backup: 读取归档状态失败: %w", err)
	}
	sum := hex.EncodeToString(hasher.Sum(nil))

	// 5) 发布为最终文件。
	published := finalPath
	if autoUnique {
		published, err = publishUnique(tmpPath, filepath.Dir(finalPath), filepath.Base(finalPath))
		if err != nil {
			return nil, err
		}
	} else {
		// CLI --out 语义：允许覆盖；Windows 上 rename 不能覆盖已存在文件，先删再改名。
		if _, statErr := os.Lstat(finalPath); statErr == nil {
			if err := os.Remove(finalPath); err != nil {
				return nil, fmt.Errorf("backup: 覆盖已有归档 %s 失败: %w", finalPath, err)
			}
		}
		if err := os.Rename(tmpPath, finalPath); err != nil {
			return nil, fmt.Errorf("backup: 发布归档 %s 失败: %w", finalPath, err)
		}
	}
	syncDir(filepath.Dir(published))

	info := &Info{
		Name:              filepath.Base(published),
		SizeBytes:         fi.Size(),
		CreatedAt:         now,
		IncludesArtifacts: includeArtifacts,
		SchemaVersion:     schemaVersion,
		SHA256:            sum,
	}
	if writeSidecar {
		if err := writeSidecarFile(published, info); err != nil {
			return nil, err
		}
	}
	return info, nil
}

// collectPayloads 收集 DB 快照之外的受管文件（master.key 必选，known_hosts/artifacts 可选）。
func (m *Manager) collectPayloads(includeArtifacts bool, workDir string) ([]payloadFile, error) {
	files := []payloadFile{
		{Name: DBFileName, Path: filepath.Join(workDir, DBFileName), Mode: 0o640},
	}

	// master.key：凭证主密钥。文件缺失时允许用 LEADSTACK_MASTER_KEY 兜底。
	masterKeyPath := filepath.Join(m.dataDir, MasterKeyFileName)
	if fi, err := os.Stat(masterKeyPath); err == nil {
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("backup: %s 不是普通文件", masterKeyPath)
		}
		files = append(files, payloadFile{Name: MasterKeyFileName, Path: masterKeyPath, Mode: 0o600})
	} else if errors.Is(err, os.ErrNotExist) {
		if data := masterKeyFromEnv(); data != nil {
			files = append(files, payloadFile{Name: MasterKeyFileName, Data: data, Mode: 0o600})
		} else {
			return nil, fmt.Errorf("backup: 数据目录缺少 %s（也未设置 %s），无法备份凭证密钥", MasterKeyFileName, EnvMasterKey)
		}
	} else {
		return nil, fmt.Errorf("backup: 读取 %s 失败: %w", masterKeyPath, err)
	}

	// known_hosts：SSH 采集期间可能被追加写，先快照到临时目录再入包，避免哈希与内容不一致。
	knownHostsPath := filepath.Join(m.dataDir, KnownHostsFileName)
	if fi, err := os.Stat(knownHostsPath); err == nil && fi.Mode().IsRegular() {
		staged := filepath.Join(workDir, KnownHostsFileName+".snapshot")
		if err := copyRegularFile(knownHostsPath, staged, 0o600); err != nil {
			return nil, fmt.Errorf("backup: 快照 %s 失败: %w", KnownHostsFileName, err)
		}
		files = append(files, payloadFile{Name: KnownHostsFileName, Path: staged, Mode: 0o600})
	}

	// artifacts：默认不包含（体积大），由开关控制。
	if includeArtifacts {
		artDir := filepath.Join(m.dataDir, ArtifactsDirName)
		entries, err := os.ReadDir(artDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("backup: 读取制品目录失败: %w", err)
		}
		var names []string
		for _, e := range entries {
			if !e.Type().IsRegular() {
				continue // 跳过子目录与符号链接
			}
			if err := validateArtifactName(e.Name()); err != nil {
				return nil, fmt.Errorf("backup: 制品文件名非法 %q: %w", e.Name(), err)
			}
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			files = append(files, payloadFile{
				Name: ArtifactsDirName + "/" + name,
				Path: filepath.Join(artDir, name),
				Mode: 0o640,
			})
		}
	}
	return files, nil
}

// hashPayloads 流式计算全部 payload 的 size/sha256。
func hashPayloads(ctx context.Context, files []payloadFile) error {
	for i := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := hashPayload(&files[i]); err != nil {
			return err
		}
	}
	return nil
}

// publishUnique 用硬链接原子发布归档并自动避开重名；并发 Create 不会互相覆盖。
func publishUnique(tmpPath, dir, base string) (string, error) {
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 0; i < 1000; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d%s", stem, i+1, ext)
		}
		dst := filepath.Join(dir, name)
		err := os.Link(tmpPath, dst)
		if err == nil {
			return dst, nil
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return "", fmt.Errorf("backup: 发布归档 %s 失败: %w", dst, err)
	}
	return "", errors.New("backup: 同名归档过多，无法生成唯一备份文件名")
}

// writeSidecarFile 原子写入边车 .json 元数据。
func writeSidecarFile(archivePath string, info *Info) error {
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("backup: 编码边车元数据失败: %w", err)
	}
	path := archivePath + ".json"
	tmp, err := os.CreateTemp(filepath.Dir(path), ".lsbak-json-*")
	if err != nil {
		return fmt.Errorf("backup: 创建边车临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("backup: 设置边车权限失败: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("backup: 写边车文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("backup: 落盘边车文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("backup: 关闭边车临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("backup: 发布边车文件失败: %w", err)
	}
	return nil
}

// List 从备份目录读取（按创建时间倒序）；元数据存同名 .json 边车文件。
func (m *Manager) List(ctx context.Context) ([]Info, error) {
	entries, err := os.ReadDir(m.backupDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Info{}, nil
		}
		return nil, fmt.Errorf("backup: 读取备份目录失败: %w", err)
	}
	out := make([]Info, 0, len(entries))
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".lsbak") {
			continue
		}
		info, err := m.infoFor(e.Name())
		if err != nil {
			// 单个无法识别的归档不应让整个列表接口失败；跳过即可。
			continue
		}
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].Name > out[j].Name
	})
	return out, nil
}

// infoFor 读取单个备份的元数据：优先边车 json，缺失时回退到 header + 流式哈希。
func (m *Manager) infoFor(name string) (*Info, error) {
	if err := validateBackupName(name); err != nil {
		return nil, err
	}
	archivePath := filepath.Join(m.backupDir, name)
	if b, err := os.ReadFile(archivePath + ".json"); err == nil {
		var info Info
		if json.Unmarshal(b, &info) == nil && info.SizeBytes > 0 {
			info.Name = name
			if fi, statErr := os.Stat(archivePath); statErr == nil {
				info.SizeBytes = fi.Size()
			}
			return &info, nil
		}
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("backup: 打开归档失败: %w", err)
	}
	_, hdr, headerErr := readHeader(f)
	_ = f.Close()
	if headerErr != nil {
		return nil, headerErr
	}
	fi, err := os.Stat(archivePath)
	if err != nil {
		return nil, fmt.Errorf("backup: 读取归档状态失败: %w", err)
	}
	sum, err := fileSHA256(archivePath)
	if err != nil {
		return nil, err
	}
	return &Info{
		Name:              name,
		SizeBytes:         fi.Size(),
		CreatedAt:         hdr.CreatedAt,
		IncludesArtifacts: hdr.IncludesArtifacts,
		SchemaVersion:     hdr.SchemaVersion,
		SHA256:            sum,
	}, nil
}

// Verify 解密 + 校验 header/manifest/文件哈希/DB 完整性，任何异常都报错。
func (m *Manager) Verify(ctx context.Context, path string) (*Manifest, error) {
	passphrase, err := resolvePassphrase(m.dataDir, m.passphrase, false)
	if err != nil {
		return nil, err
	}
	return readArchive(ctx, path, passphrase, archiveReadOptions{})
}

// Delete 删除归档及其边车文件。
func (m *Manager) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateBackupName(name); err != nil {
		return err
	}
	archivePath := filepath.Join(m.backupDir, name)
	if err := os.Remove(archivePath); err != nil {
		return fmt.Errorf("backup: 删除归档 %s 失败: %w", name, err)
	}
	if err := os.Remove(archivePath + ".json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("backup: 删除边车元数据失败: %w", err)
	}
	return nil
}

// validateBackupName 只接受备份目录内的纯文件名，拒绝路径穿越与绝对路径。
func validateBackupName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("backup: 非法备份文件名 %q", name)
	}
	if filepath.Base(name) != name {
		return fmt.Errorf("backup: 备份文件名不能包含路径（%q）", name)
	}
	if strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return fmt.Errorf("backup: 备份文件名包含非法字符（%q）", name)
	}
	if !strings.HasSuffix(name, ".lsbak") {
		return fmt.Errorf("backup: 备份文件名必须以 .lsbak 结尾（%q）", name)
	}
	return nil
}

// fileSHA256 流式计算文件校验和。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("backup: 打开 %s 失败: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("backup: 计算 %s 校验和失败: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// syncDir 尽力 fsync 目录，保证 rename/link 持久化。
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
