package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
)

// InsertSamples 批量写入指标采样。
//
// 使用单事务 + 预编译语句写入；主键 (server_id, metric, ts) 冲突时忽略，
// 以支持 agent 断线重连后的补传去重（见契约第 4.3 节）。
// 返回实际写入（非重复）的条数。
func (s *Store) InsertSamples(ctx context.Context, serverID string, samples []MetricSample) (int, error) {
	if serverID == "" {
		return 0, fmt.Errorf("写入指标: server_id 为空")
	}
	if len(samples) == 0 {
		return 0, nil
	}

	written := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT OR IGNORE INTO metrics_samples (server_id, metric, ts, value) VALUES (?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("准备指标写入语句: %w", err)
		}
		defer func() {
			if cerr := stmt.Close(); cerr != nil {
				slog.Warn("关闭指标写入语句失败", "err", cerr)
			}
		}()

		for _, smp := range samples {
			if smp.Metric == "" || smp.TS <= 0 {
				continue // 跳过非法采样，不因单条脏数据丢掉整批
			}
			if math.IsNaN(smp.Value) || math.IsInf(smp.Value, 0) {
				continue
			}
			res, err := stmt.ExecContext(ctx, serverID, smp.Metric, smp.TS, smp.Value)
			if err != nil {
				return fmt.Errorf("写入指标 %s@%d: %w", smp.Metric, smp.TS, err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("读取指标写入影响行数: %w", err)
			}
			written += int(n)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return written, nil
}

// QuerySeries 查询某台服务器某个指标在 [since, until) 区间内的采样，按时间升序。
func (s *Store) QuerySeries(ctx context.Context, serverID, metric string, since, until int64, limit int) ([]MetricSample, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT server_id, metric, ts, value FROM metrics_samples
		  WHERE server_id = ? AND metric = ? AND ts >= ? AND ts < ?
		  ORDER BY ts ASC LIMIT ?`,
		serverID, metric, since, until, limit)
	if err != nil {
		return nil, fmt.Errorf("查询指标 %s/%s: %w", serverID, metric, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]MetricSample, 0, 64)
	for rows.Next() {
		var smp MetricSample
		if err := rows.Scan(&smp.ServerID, &smp.Metric, &smp.TS, &smp.Value); err != nil {
			return nil, fmt.Errorf("扫描指标采样: %w", err)
		}
		out = append(out, smp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历指标采样: %w", err)
	}
	return out, nil
}

// DeleteMetricsBefore 清理 before（Unix 毫秒）之前的指标，返回删除条数。
func (s *Store) DeleteMetricsBefore(ctx context.Context, before int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM metrics_samples WHERE ts < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("清理历史指标: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("清理历史指标后读取影响行数: %w", err)
	}
	return n, nil
}
