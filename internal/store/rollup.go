package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// 降采样桶与保留策略（契约第 11 节）。
const (
	// RollupBucketRaw 表示原始采样（非汇总）。
	RollupBucketRaw = "raw"
	// RollupBucketMinute 是 1 分钟汇总桶。
	RollupBucketMinute = "1m"
	// RollupBucketHour 是 1 小时汇总桶。
	RollupBucketHour = "1h"

	rollupMinuteMS = int64(60 * 1000)
	rollupHourMS   = int64(3600 * 1000)

	// 首次运行的回填窗口：1m 从原始数据（保留 7 天）回填，1h 从 1m 汇总（保留 30 天）回填。
	rollupMinuteBackfillMS = int64(7*24*3600) * 1000
	rollupHourBackfillMS   = int64(30*24*3600) * 1000

	settingsRollupMinuteWM = "rollup.1m.watermark"
	settingsRollupHourWM   = "rollup.1h.watermark"

	// DefaultRollupLimit 是单次降采样最多处理的行数。
	DefaultRollupLimit = 5000
)

// RollupStats 是一次降采样的处理结果。
type RollupStats struct {
	MinuteRows int
	HourRows   int
}

// RollupOnce 推进一次降采样：原始 → 1m，1m → 1h。
//
// 采用「水位线 + 重算最新桶」策略：
//   - 水位线记录上次处理到的桶起点，下次从该桶起点继续，已完成的桶不会重复扫描；
//   - 水位线始终停在最新桶的起点，因此当前尚未结束的桶每轮都会被重算（幂等 upsert），
//     保证接口能尽快看到 1m/1h 数据；
//   - 单轮最多处理 limit 行（按桶数计），避免长事务。
func (s *Store) RollupOnce(ctx context.Context, limit int) (RollupStats, error) {
	if limit <= 0 {
		limit = DefaultRollupLimit
	}
	var stats RollupStats

	minuteRows, minuteWM, err := s.rollupMinute(ctx, NowMS(), limit)
	if err != nil {
		return stats, err
	}
	if minuteRows > 0 {
		if err := s.SetSetting(ctx, settingsRollupMinuteWM, strconv.FormatInt(minuteWM, 10)); err != nil {
			return stats, err
		}
	}
	stats.MinuteRows = minuteRows

	hourRows, hourWM, err := s.rollupHour(ctx, NowMS(), limit)
	if err != nil {
		return stats, err
	}
	if hourRows > 0 {
		if err := s.SetSetting(ctx, settingsRollupHourWM, strconv.FormatInt(hourWM, 10)); err != nil {
			return stats, err
		}
	}
	stats.HourRows = hourRows

	return stats, nil
}

// rollupMinute 把原始指标汇总成 1m 桶，返回处理行数与新水位线。
func (s *Store) rollupMinute(ctx context.Context, nowMS int64, limit int) (int, int64, error) {
	from, err := s.rollupWatermark(ctx, settingsRollupMinuteWM, nowMS-rollupMinuteBackfillMS)
	if err != nil {
		return 0, 0, err
	}
	if from >= nowMS {
		return 0, 0, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT server_id, metric, (ts / ?) * ? AS bucket_ts,
		        AVG(value), MIN(value), MAX(value), COUNT(*)
		   FROM metrics_samples
		  WHERE ts >= ? AND ts < ?
		  GROUP BY server_id, metric, bucket_ts
		  ORDER BY bucket_ts ASC, server_id ASC, metric ASC
		  LIMIT ?`,
		rollupMinuteMS, rollupMinuteMS, from, nowMS, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("汇总原始指标到 1m: %w", err)
	}
	points, err := scanRollupRows(rows)
	if err != nil {
		return 0, 0, err
	}
	if len(points) == 0 {
		return 0, 0, nil
	}

	if err := s.upsertRollups(ctx, points); err != nil {
		return 0, 0, err
	}
	// 水位线停在最后处理到的桶起点：该桶（可能是当前未结束的桶）下一轮会重算。
	return len(points), points[len(points)-1].TS, nil
}

// rollupHour 把 1m 汇总再汇总成 1h 桶（按 cnt 加权平均），返回处理行数与新水位线。
func (s *Store) rollupHour(ctx context.Context, nowMS int64, limit int) (int, int64, error) {
	from, err := s.rollupWatermark(ctx, settingsRollupHourWM, nowMS-rollupHourBackfillMS)
	if err != nil {
		return 0, 0, err
	}
	if from >= nowMS {
		return 0, 0, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT server_id, metric, (ts / ?) * ? AS bucket_ts,
		        COALESCE(SUM(avg * cnt) / NULLIF(SUM(cnt), 0), 0),
		        MIN(min), MAX(max), SUM(cnt)
		   FROM metrics_rollup
		  WHERE bucket = ? AND ts >= ? AND ts < ?
		  GROUP BY server_id, metric, bucket_ts
		  ORDER BY bucket_ts ASC, server_id ASC, metric ASC
		  LIMIT ?`,
		rollupHourMS, rollupHourMS, RollupBucketMinute, from, nowMS, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("汇总 1m 指标到 1h: %w", err)
	}
	points, err := scanRollupRows(rows)
	if err != nil {
		return 0, 0, err
	}
	if len(points) == 0 {
		return 0, 0, nil
	}
	for i := range points {
		points[i].Bucket = RollupBucketHour
	}

	if err := s.upsertRollups(ctx, points); err != nil {
		return 0, 0, err
	}
	return len(points), points[len(points)-1].TS, nil
}

// scanRollupRows 读取聚合结果并关闭 rows。
func scanRollupRows(rows *sql.Rows) ([]RollupPoint, error) {
	defer func() { _ = rows.Close() }()

	points := make([]RollupPoint, 0, 64)
	for rows.Next() {
		p := RollupPoint{Bucket: RollupBucketMinute}
		if err := rows.Scan(&p.ServerID, &p.Metric, &p.TS, &p.Avg, &p.Min, &p.Max, &p.Cnt); err != nil {
			return nil, fmt.Errorf("扫描降采样结果: %w", err)
		}
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历降采样结果: %w", err)
	}
	return points, nil
}

// upsertRollups 在一个事务里幂等写入汇总行。
func (s *Store) upsertRollups(ctx context.Context, points []RollupPoint) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO metrics_rollup (server_id, metric, bucket, ts, avg, min, max, cnt)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(server_id, metric, bucket, ts) DO UPDATE SET
			     avg = excluded.avg, min = excluded.min, max = excluded.max, cnt = excluded.cnt`)
		if err != nil {
			return fmt.Errorf("准备汇总写入语句: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		for _, p := range points {
			if _, err := stmt.ExecContext(ctx,
				p.ServerID, p.Metric, p.Bucket, p.TS, p.Avg, p.Min, p.Max, p.Cnt); err != nil {
				return fmt.Errorf("写入汇总 %s/%s@%d: %w", p.ServerID, p.Metric, p.TS, err)
			}
		}
		return nil
	})
}

// rollupWatermark 读取水位线；不存在或非法时返回 fallback。
func (s *Store) rollupWatermark(ctx context.Context, key string, fallback int64) (int64, error) {
	raw, ok, err := s.GetSetting(ctx, key)
	if err != nil {
		return 0, err
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || v <= 0 {
		return fallback, nil
	}
	return v, nil
}

// QueryRollupRange 查询某服务器某个 bucket 的汇总序列（[from, to] 闭区间），按 metric、ts 升序。
func (s *Store) QueryRollupRange(ctx context.Context, serverID, bucket string, from, to int64, limit int) ([]RollupPoint, error) {
	if limit <= 0 {
		limit = DefaultRollupLimit
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT server_id, metric, bucket, ts, avg, min, max, cnt
		   FROM metrics_rollup
		  WHERE server_id = ? AND bucket = ? AND ts >= ? AND ts <= ?
		  ORDER BY metric ASC, ts ASC
		  LIMIT ?`,
		serverID, bucket, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("查询汇总指标 %s/%s: %w", serverID, bucket, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]RollupPoint, 0, 64)
	for rows.Next() {
		var p RollupPoint
		if err := rows.Scan(&p.ServerID, &p.Metric, &p.Bucket, &p.TS,
			&p.Avg, &p.Min, &p.Max, &p.Cnt); err != nil {
			return nil, fmt.Errorf("扫描汇总指标: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历汇总指标: %w", err)
	}
	return out, nil
}

// DeleteRollupBefore 清理某个 bucket 中早于 before（Unix 毫秒）的汇总，返回删除条数。
func (s *Store) DeleteRollupBefore(ctx context.Context, bucket string, before int64) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM metrics_rollup WHERE bucket = ? AND ts < ?`, bucket, before)
	if err != nil {
		return 0, fmt.Errorf("清理汇总指标 %s: %w", bucket, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("清理汇总指标后读取影响行数: %w", err)
	}
	return n, nil
}
