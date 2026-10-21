package api

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/maliang/leadstack/internal/config"
)

// UpgradeRunner 是「不依赖 HTTP 服务」的升级执行器（供 CLI `leadstack upgrade` 使用）。
//
// 它与 REST 的 apply 走**同一段代码**（runUpgrade），因此备份/校验/替换/watchdog 语义完全一致。
type UpgradeRunner struct {
	api *API
}

// NewUpgradeRunner 构造独立升级执行器。
func NewUpgradeRunner(cfg *config.Config, version string, logger *slog.Logger) (*UpgradeRunner, error) {
	if cfg == nil {
		return nil, fmt.Errorf("配置为空")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if version == "" {
		version = "dev"
	}
	backupService, err := NewBackupService(cfg, logger)
	if err != nil {
		return nil, err
	}
	a := &API{
		cfg:      cfg,
		backups:  backupService,
		updates:  newUpdateChecker(cfg.UpdateRepo, cfg.UpdateAPIBase, version, cfg.UpdateCheckInterval, logger),
		upgrader: &upgrader{},
		logger:   logger,
		version:  version,
	}
	return &UpgradeRunner{api: a}, nil
}

// Check 查询最新版本信息。
func (r *UpgradeRunner) Check(ctx context.Context) (UpdateInfo, error) {
	if r == nil || r.api == nil {
		return UpdateInfo{}, fmt.Errorf("升级执行器未初始化")
	}
	info := r.api.updates.Get(ctx, true)
	if info.Error != "" {
		return info, fmt.Errorf("检查更新失败: %s", info.Error)
	}
	return info, nil
}

// Apply 执行升级（同步走完 备份 → 下载 → 校验 → 替换 → spawn watchdog → exec）。
//
// 注意：成功时会 syscall.Exec 成新版本，本函数不会返回（CLI 进程被替换成服务进程）。
func (r *UpgradeRunner) Apply(ctx context.Context, target string, allowMajor bool) error {
	if r == nil || r.api == nil {
		return fmt.Errorf("升级执行器未初始化")
	}
	a := r.api
	if !a.cfg.UpgradeEnabled {
		return fmt.Errorf("控制端自升级已禁用（LEADSTACK_UPGRADE_ENABLED=false）")
	}
	if reason := platformUpgradeUnsupported(); reason != "" {
		return fmt.Errorf("%s", reason)
	}
	if err := a.validateTargetVersion(target, allowMajor); err != nil {
		return err
	}
	info := a.updates.Get(ctx, true)
	if info.Error != "" {
		return fmt.Errorf("检查更新失败: %s", info.Error)
	}
	asset, err := a.pickAsset(info, target)
	if err != nil {
		return err
	}
	job := &upgradeJob{
		JobID: "cli-" + target, Phase: "validating", TargetVersion: target,
		FromVersion: a.version, StartedAt: a.nowMS(),
	}
	a.upgrader.setJob(job)
	a.upgrader.begin()
	// CLI 升级完成后要回到「启动服务」，而不是再次运行 upgrade 子命令。
	a.runUpgrade(job, asset, info.SchemaChanged, []string{os.Args[0]})
	if job.Error != "" {
		return fmt.Errorf("%s", job.Error)
	}
	return nil
}
