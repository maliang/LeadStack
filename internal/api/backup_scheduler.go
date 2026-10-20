package api

import (
	"context"
	"fmt"
	"time"
)

// 定时备份参数。
const (
	// backupDisabledPollInterval 是备份被禁用时的设置轮询周期（有 PATCH 时会被立即唤醒）。
	backupDisabledPollInterval = time.Minute
	// backupJobTimeout 是单次定时备份的最长执行时间。
	backupJobTimeout = 30 * time.Minute
)

// RunBackupScheduler 按 `/backup-settings` 的配置周期执行定时备份，直到 ctx 取消。
//
// 与清扫循环完全解耦（独立 goroutine）；设置在 PATCH 时会向 backupWake 发信号立即重排，
// 因此修改 interval/enabled 无需重启进程。
func (a *API) RunBackupScheduler(ctx context.Context) {
	if a.backups == nil {
		a.logger.Info("备份模块未启用，定时备份不启动")
		return
	}
	a.logger.Info("定时备份循环启动")

	for {
		settings, err := a.loadBackupSettings(ctx)
		if err != nil {
			a.logger.Warn("读取备份设置失败，使用默认周期", "err", err)
			settings = backupSettings{
				Enabled:   true,
				Interval:  a.cfg.BackupInterval.String(),
				Retention: a.cfg.BackupRetention,
			}
		}
		interval, perr := time.ParseDuration(settings.Interval)
		if perr != nil || interval < time.Second {
			a.logger.Warn("备份周期非法，回退默认值", "interval", settings.Interval, "err", perr)
			interval = a.cfg.BackupInterval
			if interval < time.Second {
				interval = defaultBackupInterval
			}
		}

		wait := interval
		if !settings.Enabled {
			wait = backupDisabledPollInterval
		}
		a.logger.Debug("定时备份等待下一轮", "enabled", settings.Enabled, "wait", wait.String())

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			a.logger.Info("定时备份循环退出")
			return
		case <-a.backupWake:
			// 设置变更：立即重排（不执行备份）。
			timer.Stop()
			continue
		case <-timer.C:
			if !settings.Enabled {
				continue
			}
			a.runScheduledBackup(ctx, settings)
		}
	}
}

// runScheduledBackup 执行一次定时备份并按保留策略清理。
//
// 失败会把原因写进 backup-settings 的 last_error（settings 表），成功则清空并记录时间。
func (a *API) runScheduledBackup(ctx context.Context, settings backupSettings) {
	jobCtx, cancel := context.WithTimeout(ctx, backupJobTimeout)
	defer cancel()

	info, err := a.backups.Create(jobCtx, settings.IncludeArtifacts)
	if err != nil {
		a.recordBackupResult(ctx, err)
		msg, _ := mapBackupError(err)
		a.logger.Warn("定时备份失败", "err", err, "hint", msg)
		return
	}
	a.recordBackupResult(ctx, nil)
	a.pruneBackups(ctx, settings.Retention)
	a.logger.Info("定时备份完成",
		"name", info.Name, "size_bytes", info.SizeBytes,
		"includes_artifacts", info.IncludesArtifacts, "retention", settings.Retention)
}

// wakeBackupScheduler 通知定时器重排（非阻塞；供测试与内部调用）。
func (a *API) wakeBackupScheduler() {
	if a == nil || a.backupWake == nil {
		return
	}
	select {
	case a.backupWake <- struct{}{}:
	default:
	}
}

// ScheduledBackupOnce 立即按当前设置执行一轮「备份 + 保留」，返回备份信息（测试与运维用）。
func (a *API) ScheduledBackupOnce(ctx context.Context) (*BackupInfo, error) {
	if a.backups == nil {
		return nil, fmt.Errorf("备份模块未启用")
	}
	settings, err := a.loadBackupSettings(ctx)
	if err != nil {
		return nil, err
	}
	info, err := a.backups.Create(ctx, settings.IncludeArtifacts)
	if err != nil {
		a.recordBackupResult(ctx, err)
		return nil, err
	}
	a.recordBackupResult(ctx, nil)
	a.pruneBackups(ctx, settings.Retention)
	return info, nil
}
