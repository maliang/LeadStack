package main

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/maliang/leadstack/internal/api"
	"github.com/maliang/leadstack/internal/backup"
	"github.com/maliang/leadstack/internal/config"
)

// applyPendingRestoreIfAny 在**打开数据库之前**应用已暂存的恢复。
//
// 设计要点（契约第 33 节）：
//   - 必须在 store.Open 之前执行，否则可能以旧库运行在新文件之上；
//   - 失败时**拒绝启动**并给出手工处理指引 —— 带着损坏数据启动比不启动更糟；
//   - 成功后清理暂存目录（pre-restore 目录由 backup.Restore 保留，供人工回退）。
func applyPendingRestoreIfAny(cfg *config.Config, logger *slog.Logger) error {
	pendingDir := cfg.RestorePendingDir()
	pending, err := api.ReadPendingRestore(pendingDir)
	if err != nil {
		return fmt.Errorf("检测暂存恢复失败（请检查 %s）: %w", pendingDir, err)
	}
	if pending == nil {
		return nil
	}

	logger.Warn("检测到已暂存的恢复，正在应用（此时尚未打开数据库）",
		"pending_dir", pendingDir, "source", pending.Source,
		"schema_version", pending.SchemaVersion, "archive_sha256", pending.ArchiveSHA256)

	pass, perr := resolvePassphrase(cfg.DataDir, "", true)
	if perr != nil {
		return fmt.Errorf("存在待应用的恢复，但%w", perr)
	}
	res, rerr := backup.Restore(backup.RestoreOptions{
		File:             pendingDir,
		DataDir:          cfg.DataDir,
		Passphrase:       pass,
		MaxSchemaVersion: currentSchemaVersion,
	})
	if rerr != nil {
		hint := backupErrorHint(rerr)
		return fmt.Errorf("应用暂存恢复失败，已拒绝启动：%w\n"+
			"  数据目录未被替换；待恢复数据仍保留在 %s。\n"+
			"  处理建议：\n"+
			"    1) 修正口令/归档问题后重新启动，会自动再次尝试应用；\n"+
			"    2) 或执行 `leadstack restore --file <归档> --data-dir %s` 手工恢复；\n"+
			"    3) 放弃本次恢复：删除 %s 后即可正常启动（原数据如已被迁移，见 %s.pre-restore-* 目录）。",
			hint, pendingDir, cfg.DataDir, pendingDir, cfg.DataDir)
	}

	if cerr := api.ClearPendingRestore(pendingDir); cerr != nil {
		logger.Warn("恢复已应用，但清理暂存目录失败，请手工删除", "pending_dir", pendingDir, "err", cerr)
	}

	preDir := res.PreRestoreDir
	if strings.TrimSpace(preDir) == "" {
		preDir = "(未保留)"
	}
	logger.Warn("暂存恢复已应用，继续启动",
		"restored_files", res.RestoredFiles,
		"pre_restore_dir", preDir,
		"schema_version", res.Manifest.SchemaVersion)
	return nil
}

var _ = errors.Is
