package api

import (
	"context"
	"fmt"
	"time"

	"github.com/maliang/leadstack/internal/store"
	"github.com/maliang/leadstack/pkg/protocol"
)

// 后台清理参数。
const (
	// defaultSweepInterval 是离线清扫周期；契约要求 45 秒内变 offline，5 秒粒度足够。
	defaultSweepInterval = 5 * time.Second
	// retentionInterval 是数据保留清理周期。
	retentionInterval = time.Hour
	// rollupInterval 是降采样推进周期。
	//
	// 与离线清扫同频（5s）：单次最多 5000 行、且水位线只在最新桶上重复计算，开销很小；
	// 这样新接入的服务器采集完几秒内 step=1m 就能查到汇总（验收标准 12.6）。
	rollupInterval = 5 * time.Second
	// metricsRetentionMS 是原始指标保留时长（7 天）。
	metricsRetentionMS = int64(7*24*3600) * 1000
	// minuteRollupRetentionMS 是 1m 汇总保留时长（30 天）。
	minuteRollupRetentionMS = int64(30*24*3600) * 1000
	// hourRollupRetentionMS 是 1h 汇总保留时长（1 年）。
	hourRollupRetentionMS = int64(365*24*3600) * 1000
	// checkResultsRetentionMS 是检测结果保留时长（30 天，契约第 16 节）。
	checkResultsRetentionMS = int64(30*24*3600) * 1000
)

// SweepOnce 执行一次离线清扫：把超时未心跳的服务器置为 offline 并记事件。
//
// 返回本次被判定为离线的服务器数量。
func (a *API) SweepOnce(ctx context.Context) (int, error) {
	threshold := a.nowMS() - a.offlineThresholdMS()
	ids, err := a.st.MarkStaleServersOffline(ctx, threshold)
	if err != nil {
		return 0, fmt.Errorf("离线清扫: %w", err)
	}
	for _, id := range ids {
		a.logger.Info("服务器离线", "server_id", id, "offline_after_s", protocol.OfflineAfterS)
		a.recordEvent(id, store.EventKindOffline,
			fmt.Sprintf("超过 %d 秒未收到心跳，判定为离线", protocol.OfflineAfterS))
	}
	return len(ids), nil
}

// RunSweeper 周期执行离线清扫与数据保留清理，直到 ctx 被取消。
func (a *API) RunSweeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = defaultSweepInterval
	}
	sweepTicker := time.NewTicker(interval)
	defer sweepTicker.Stop()
	retentionTicker := time.NewTicker(retentionInterval)
	defer retentionTicker.Stop()
	rollupTicker := time.NewTicker(rollupInterval)
	defer rollupTicker.Stop()

	// 启动即扫一次，避免进程重启后残留的 online 状态误导 UI，并顺带补一次降采样。
	if n, err := a.SweepOnce(ctx); err != nil {
		a.logger.Warn("离线清扫失败", "err", err)
	} else if n > 0 {
		a.logger.Info("启动清扫：已把超时服务器置为离线", "count", n)
	}
	a.runRollup(ctx)

	for {
		select {
		case <-ctx.Done():
			a.logger.Info("后台清扫协程退出")
			return
		case <-sweepTicker.C:
			if n, err := a.SweepOnce(ctx); err != nil {
				a.logger.Warn("离线清扫失败", "err", err)
			} else if n > 0 {
				a.logger.Info("离线清扫完成", "count", n)
			}
		case <-rollupTicker.C:
			a.runRollup(ctx)
		case <-retentionTicker.C:
			a.runRetention(ctx)
		}
	}
}

// runRollup 推进一次降采样（原始 → 1m → 1h），单次最多 5000 行。
func (a *API) runRollup(ctx context.Context) {
	stats, err := a.st.RollupOnce(ctx, store.DefaultRollupLimit)
	if err != nil {
		a.logger.Warn("降采样失败", "err", err)
		return
	}
	if stats.MinuteRows > 0 || stats.HourRows > 0 {
		a.logger.Debug("降采样完成", "minute_rows", stats.MinuteRows, "hour_rows", stats.HourRows)
	}
}

// runRetention 按保留策略清理数据，并顺带推进一次降采样（先汇总再清理，避免丢数据）。
//
// 保留策略（契约第 11 节）：原始 7 天 / 1m 30 天 / 1h 1 年。
func (a *API) runRetention(ctx context.Context) {
	// 先补一次降采样，尽量把即将过期的原始数据汇总下来。
	a.runRollup(ctx)

	nowMS := a.nowMS()
	if n, err := a.st.DeleteMetricsBefore(ctx, nowMS-metricsRetentionMS); err != nil {
		a.logger.Warn("清理历史指标失败", "err", err)
	} else if n > 0 {
		a.logger.Info("清理历史指标完成", "deleted", n)
	}
	if n, err := a.st.DeleteRollupBefore(ctx, store.RollupBucketMinute, nowMS-minuteRollupRetentionMS); err != nil {
		a.logger.Warn("清理 1m 汇总失败", "err", err)
	} else if n > 0 {
		a.logger.Info("清理 1m 汇总完成", "deleted", n)
	}
	if n, err := a.st.DeleteRollupBefore(ctx, store.RollupBucketHour, nowMS-hourRollupRetentionMS); err != nil {
		a.logger.Warn("清理 1h 汇总失败", "err", err)
	} else if n > 0 {
		a.logger.Info("清理 1h 汇总完成", "deleted", n)
	}
	if n, err := a.st.DeleteCheckResultsBefore(ctx, nowMS-checkResultsRetentionMS); err != nil {
		a.logger.Warn("清理历史检测结果失败", "err", err)
	} else if n > 0 {
		a.logger.Info("清理历史检测结果完成", "deleted", n)
	}
	if days := a.cfg.AlertRetentionDays; days > 0 {
		cutoff := nowMS - int64(days)*24*3600*1000
		if n, err := a.st.DeleteResolvedAlertsBefore(ctx, cutoff); err != nil {
			a.logger.Warn("清理历史告警失败", "err", err)
		} else if n > 0 {
			a.logger.Info("清理历史告警完成", "deleted", n, "retention_days", days)
		}
	}
	if n, err := a.st.DeleteExpiredEnrollTokens(ctx, nowMS); err != nil {
		a.logger.Warn("清理过期接入令牌失败", "err", err)
	} else if n > 0 {
		a.logger.Info("清理过期接入令牌完成", "deleted", n)
	}
}
