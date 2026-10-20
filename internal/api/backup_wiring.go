package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/maliang/leadstack/internal/backup"
	"github.com/maliang/leadstack/internal/config"
)

// backupAdapter 把 internal/backup 的能力适配成 api.BackupService。
//
// 职责只有三件：类型转换（backup.Info → api.BackupInfo 等）、错误归一化
// （backup 的哨兵错误 → api 的哨兵错误，供 HTTP 层映射可读提示）、口令解析。
type backupAdapter struct {
	manager    *backup.Manager
	cfg        *config.Config
	logger     *slog.Logger
	passphrase string
}

// 编译期确认接口实现。
var _ BackupService = (*backupAdapter)(nil)

// NewBackupService 创建备份服务；配置不全或 Manager 构造失败时返回 error。
//
// 口令解析沿用 internal/backup.ResolvePassphrase 的优先级：
// 显式（LEADSTACK_BACKUP_PASSPHRASE）> 环境变量 > <data_dir>/backup.pass > 自动生成 0600。
func NewBackupService(cfg *config.Config, logger *slog.Logger) (BackupService, error) {
	if cfg == nil {
		return nil, errors.New("初始化备份服务: 配置为空")
	}
	if logger == nil {
		logger = slog.Default()
	}
	manager, err := backup.NewManager(backup.Options{
		DataDir:    cfg.DataDir,
		BackupDir:  cfg.BackupDir,
		Passphrase: cfg.BackupPassphrase,
		AppVersion: cfg.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("初始化备份管理器: %w", err)
	}
	passphrase := cfg.BackupPassphrase
	if passphrase == "" {
		resolved, rerr := backup.ResolvePassphrase(cfg.DataDir, "")
		if rerr != nil {
			return nil, fmt.Errorf("解析备份口令: %w", rerr)
		}
		passphrase = resolved
	}
	return &backupAdapter{manager: manager, cfg: cfg, logger: logger, passphrase: passphrase}, nil
}

// Create 生成一份备份。
func (a *backupAdapter) Create(ctx context.Context, includeArtifacts bool) (*BackupInfo, error) {
	info, err := a.manager.Create(ctx, includeArtifacts)
	if err != nil {
		return nil, mapBackupAdapterError(err)
	}
	converted := toBackupInfo(*info)
	return &converted, nil
}

// List 列出备份归档（按创建时间倒序）。
func (a *backupAdapter) List(ctx context.Context) ([]BackupInfo, error) {
	infos, err := a.manager.List(ctx)
	if err != nil {
		return nil, mapBackupAdapterError(err)
	}
	out := make([]BackupInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, toBackupInfo(info))
	}
	return out, nil
}

// Verify 校验归档并返回 manifest。
func (a *backupAdapter) Verify(ctx context.Context, path string) (*BackupManifest, error) {
	manifest, err := a.manager.Verify(ctx, path)
	if err != nil {
		return nil, mapBackupAdapterError(err)
	}
	converted := toBackupManifest(*manifest)
	return &converted, nil
}

// Delete 删除归档（internal/backup 只接受纯文件名）。
func (a *backupAdapter) Delete(ctx context.Context, name string) error {
	if err := a.manager.Delete(ctx, name); err != nil {
		return mapBackupAdapterError(err)
	}
	return nil
}

// StageRestore 校验上传的归档并解压暂存到 pendingDir（失败不留半成品）。
func (a *backupAdapter) StageRestore(ctx context.Context, archivePath, pendingDir string) (*StagedRestore, error) {
	staged, err := backup.StageRestore(ctx, archivePath, pendingDir, a.passphrase)
	if err != nil {
		return nil, mapBackupAdapterError(err)
	}
	out := &StagedRestore{Source: staged.Source, CreatedAt: staged.CreatedAt, VerifiedAt: staged.VerifiedAt, ArchiveSHA256: staged.ArchiveSHA256}
	if staged.Manifest != nil {
		out.SchemaVersion = staged.Manifest.SchemaVersion
	}
	return out, nil
}

// toBackupInfo 转换 Info（字段一一对应）。
func toBackupInfo(info backup.Info) BackupInfo {
	return BackupInfo{
		Name:              info.Name,
		SizeBytes:         info.SizeBytes,
		CreatedAt:         info.CreatedAt,
		IncludesArtifacts: info.IncludesArtifacts,
		SchemaVersion:     info.SchemaVersion,
		SHA256:            info.SHA256,
	}
}

// toBackupManifest 转换 Manifest（字段一一对应）。
func toBackupManifest(manifest backup.Manifest) BackupManifest {
	files := make([]BackupManifestFile, 0, len(manifest.Files))
	for _, f := range manifest.Files {
		files = append(files, BackupManifestFile{Name: f.Name, SizeBytes: f.SizeBytes, SHA256: f.SHA256})
	}
	return BackupManifest{
		FormatVersion:     manifest.FormatVersion,
		AppVersion:        manifest.AppVersion,
		SchemaVersion:     manifest.SchemaVersion,
		CreatedAt:         manifest.CreatedAt,
		IncludesArtifacts: manifest.IncludesArtifacts,
		Files:             files,
	}
}

// mapBackupAdapterError 把 internal/backup 的哨兵错误变成 api 的哨兵错误，
// 使 HTTP 层能用 errors.Is 给出「口令错/损坏/版本过新」三类可执行提示。
func mapBackupAdapterError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, backup.ErrBadPassphrase):
		return fmt.Errorf("%w: %v", errBackupBadPassphrase, err)
	case errors.Is(err, backup.ErrSchemaTooNew):
		return fmt.Errorf("%w: %v", errBackupSchemaTooNew, err)
	case errors.Is(err, backup.ErrCorrupt):
		return fmt.Errorf("%w: %v", errBackupCorrupt, err)
	default:
		return err
	}
}

// ReadPendingRestore 读取「暂存恢复」目录里的 meta.json；不存在返回 (nil, nil)。
//
// meta.json 由 internal/backup.StageRestore 写入（字段见 backup.StagedInfo）。
func ReadPendingRestore(pendingDir string) (*PendingRestore, error) {
	raw, err := os.ReadFile(filepath.Join(pendingDir, restorePendingMetaName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取暂存恢复元数据 %s: %w", pendingDir, err)
	}
	var meta struct {
		Source        string `json:"source"`
		CreatedAt     int64  `json:"created_at"`
		VerifiedAt    int64  `json:"verified_at"`
		ArchiveSHA256 string `json:"archive_sha256"`
		Manifest      *struct {
			SchemaVersion int `json:"schema_version"`
		} `json:"manifest"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("解析暂存恢复元数据 %s: %w", pendingDir, err)
	}
	out := &PendingRestore{
		Source: meta.Source, CreatedAt: meta.CreatedAt,
		VerifiedAt: meta.VerifiedAt, ArchiveSHA256: meta.ArchiveSHA256,
	}
	if meta.Manifest != nil {
		out.SchemaVersion = meta.Manifest.SchemaVersion
	}
	return out, nil
}

// PendingRestore 描述一份等待下次启动应用的恢复。
type PendingRestore struct {
	Source        string `json:"source"`
	CreatedAt     int64  `json:"created_at"`
	VerifiedAt    int64  `json:"verified_at"`
	ArchiveSHA256 string `json:"archive_sha256"`
	SchemaVersion int    `json:"schema_version"`
}

// ClearPendingRestore 删除暂存恢复目录（恢复成功应用后调用）。
func ClearPendingRestore(pendingDir string) error {
	if err := os.RemoveAll(pendingDir); err != nil {
		return fmt.Errorf("清理暂存恢复目录 %s: %w", pendingDir, err)
	}
	return nil
}

// StagedRestore 是「已暂存待应用」的恢复。
type StagedRestore struct {
	Source        string `json:"source"`
	CreatedAt     int64  `json:"created_at"`
	VerifiedAt    int64  `json:"verified_at"`
	ArchiveSHA256 string `json:"archive_sha256"`
	SchemaVersion int    `json:"schema_version"`
}
