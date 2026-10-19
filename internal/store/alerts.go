package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// alertRuleColumns 是 alert_rules 表的固定列顺序。
const alertRuleColumns = `id, name, enabled, source, check_ids, server_ids, groups, tags,
	metric, comparator, threshold, for_duration_s, severity, dedup_window_s, channel_ids,
	silence_until, created_at`

// alertColumns 是 alerts 表的固定列顺序。
const alertColumns = `id, rule_id, dedup_key, server_id, check_id, status, severity, title, message,
	value, started_at, updated_at, resolved_at, acked_at, acked_by, notify_count, last_notify_at, silence_until`

// ------------------------------------------------ 告警规则 ------------------------------------------------

// CreateAlertRule 插入一条告警规则。
func (s *Store) CreateAlertRule(ctx context.Context, r *AlertRule) error {
	if r == nil {
		return errors.New("创建告警规则: 参数为空")
	}
	encoded, err := s.encodeRuleJSON(r)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO alert_rules (`+alertRuleColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Name, boolToInt(r.Enabled), r.Source,
		encoded.checkIDs, encoded.serverIDs, encoded.groups, encoded.tags,
		nullableStr(r.Metric), nullableStr(r.Comparator), nullableFloat(r.Threshold),
		r.ForDurationS, r.Severity, r.DedupWindowS, encoded.channelIDs,
		nullableInt(r.SilenceUntil), r.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("创建告警规则 %s: %w", r.Name, ErrConflict)
		}
		return fmt.Errorf("创建告警规则 %s: %w", r.Name, err)
	}
	return nil
}

// GetAlertRule 按 ID 查询规则。
func (s *Store) GetAlertRule(ctx context.Context, id string) (*AlertRule, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+alertRuleColumns+` FROM alert_rules WHERE id = ?`, id)
	return scanAlertRule(row)
}

// ListAlertRules 返回全部规则，按创建时间倒序。
func (s *Store) ListAlertRules(ctx context.Context) ([]AlertRule, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+alertRuleColumns+` FROM alert_rules ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("查询告警规则列表: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]AlertRule, 0, 8)
	for rows.Next() {
		r, err := scanAlertRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历告警规则列表: %w", err)
	}
	return out, nil
}

// ListEnabledAlertRules 返回启用中的规则（引擎 Reload 用）。
func (s *Store) ListEnabledAlertRules(ctx context.Context) ([]AlertRule, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+alertRuleColumns+` FROM alert_rules WHERE enabled = 1 ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询启用告警规则: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]AlertRule, 0, 8)
	for rows.Next() {
		r, err := scanAlertRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历启用告警规则: %w", err)
	}
	return out, nil
}

// AlertRuleUpdate 描述 PATCH 语义的规则更新；nil 表示不修改。
type AlertRuleUpdate struct {
	Name         *string
	Enabled      *bool
	Source       *string
	CheckIDs     *[]string
	ServerIDs    *[]string
	Groups       *[]string
	Tags         *[]string
	Metric       *string
	Comparator   *string
	Threshold    *float64
	ForDurationS *int
	Severity     *string
	DedupWindowS *int
	ChannelIDs   *[]string
	SilenceUntil *int64
	// ClearSilence 为 true 时把 silence_until 置 NULL（取消静默）。
	// 单独一个开关是因为 SilenceUntil 的 nil 语义是「不改动」。
	ClearSilence *bool
}

// UpdateAlertRule 按 PATCH 语义更新规则。
func (s *Store) UpdateAlertRule(ctx context.Context, id string, upd AlertRuleUpdate) (*AlertRule, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		sets := make([]string, 0, 8)
		args := make([]any, 0, 9)
		add := func(sqlText string, arg any) {
			sets = append(sets, sqlText)
			args = append(args, arg)
		}
		if upd.Name != nil {
			add("name = ?", *upd.Name)
		}
		if upd.Enabled != nil {
			add("enabled = ?", boolToInt(*upd.Enabled))
		}
		if upd.Source != nil {
			add("source = ?", *upd.Source)
		}
		if upd.CheckIDs != nil {
			add("check_ids = ?", mustJSONString(*upd.CheckIDs))
		}
		if upd.ServerIDs != nil {
			add("server_ids = ?", mustJSONString(*upd.ServerIDs))
		}
		if upd.Groups != nil {
			add("groups = ?", mustJSONString(*upd.Groups))
		}
		if upd.Tags != nil {
			add("tags = ?", mustJSONString(*upd.Tags))
		}
		if upd.Metric != nil {
			add("metric = ?", nullableStr(emptyToNil(*upd.Metric)))
		}
		if upd.Comparator != nil {
			add("comparator = ?", nullableStr(emptyToNil(*upd.Comparator)))
		}
		if upd.Threshold != nil {
			add("threshold = ?", *upd.Threshold)
		}
		if upd.ForDurationS != nil {
			add("for_duration_s = ?", *upd.ForDurationS)
		}
		if upd.Severity != nil {
			add("severity = ?", *upd.Severity)
		}
		if upd.DedupWindowS != nil {
			add("dedup_window_s = ?", *upd.DedupWindowS)
		}
		if upd.ChannelIDs != nil {
			add("channel_ids = ?", mustJSONString(*upd.ChannelIDs))
		}
		if upd.SilenceUntil != nil {
			add("silence_until = ?", nullableInt(upd.SilenceUntil))
		}
		if upd.ClearSilence != nil && *upd.ClearSilence {
			add("silence_until = ?", nil)
		}
		if len(sets) == 0 {
			return nil
		}

		query := `UPDATE alert_rules SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
		args = append(args, id)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("更新告警规则 %s: %w", id, err)
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM alert_rules WHERE id = ?`, id).Scan(&exists); err != nil {
			return fmt.Errorf("确认告警规则 %s 是否存在: %w", id, err)
		}
		if exists == 0 {
			return fmt.Errorf("更新告警规则 %s: %w", id, ErrNotFound)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetAlertRule(ctx, id)
}

// DeleteAlertRule 删除规则（保留历史告警，便于追溯）。
func (s *Store) DeleteAlertRule(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM alert_rules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除告警规则 %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("删除告警规则 %s 后读取影响行数: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("删除告警规则 %s: %w", id, ErrNotFound)
	}
	return nil
}

// ------------------------------------------------ 通知渠道 ------------------------------------------------

// CreateAlertChannel 插入一个通知渠道（配置已加密）。
func (s *Store) CreateAlertChannel(ctx context.Context, c *AlertChannel) error {
	if c == nil {
		return errors.New("创建通知渠道: 参数为空")
	}
	if len(c.ConfigEnc) == 0 {
		return errors.New("创建通知渠道: 缺少加密配置")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO alert_channels (id, name, kind, config_enc, enabled, last_error, last_ok_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.Kind, c.ConfigEnc, boolToInt(c.Enabled),
		nullableStr(c.LastError), nullableInt(c.LastOKAt), c.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("创建通知渠道 %s: %w", c.Name, ErrConflict)
		}
		return fmt.Errorf("创建通知渠道 %s: %w", c.Name, err)
	}
	return nil
}

// GetAlertChannel 按 ID 查询渠道（含密文，供服务端解密发送）。
func (s *Store) GetAlertChannel(ctx context.Context, id string) (*AlertChannel, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, kind, config_enc, enabled, last_error, last_ok_at, created_at
		   FROM alert_channels WHERE id = ?`, id)
	c, err := scanAlertChannel(row)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ListAlertChannels 返回全部渠道。
func (s *Store) ListAlertChannels(ctx context.Context) ([]AlertChannel, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, kind, config_enc, enabled, last_error, last_ok_at, created_at
		   FROM alert_channels ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("查询通知渠道列表: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]AlertChannel, 0, 8)
	for rows.Next() {
		c, err := scanAlertChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历通知渠道列表: %w", err)
	}
	return out, nil
}

// UpdateAlertChannel 更新渠道的名称/类型/密文/启用状态（nil 表示不改）。
func (s *Store) UpdateAlertChannel(ctx context.Context, id string, name, kind *string, configEnc []byte, enabled *bool) (*AlertChannel, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		sets := make([]string, 0, 4)
		args := make([]any, 0, 5)
		if name != nil {
			sets = append(sets, "name = ?")
			args = append(args, *name)
		}
		if kind != nil {
			sets = append(sets, "kind = ?")
			args = append(args, *kind)
		}
		if len(configEnc) > 0 {
			sets = append(sets, "config_enc = ?")
			args = append(args, configEnc)
		}
		if enabled != nil {
			sets = append(sets, "enabled = ?")
			args = append(args, boolToInt(*enabled))
		}
		if len(sets) == 0 {
			return nil
		}
		query := `UPDATE alert_channels SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
		args = append(args, id)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("更新通知渠道 %s: %w", id, err)
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM alert_channels WHERE id = ?`, id).Scan(&exists); err != nil {
			return fmt.Errorf("确认通知渠道 %s 是否存在: %w", id, err)
		}
		if exists == 0 {
			return fmt.Errorf("更新通知渠道 %s: %w", id, ErrNotFound)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetAlertChannel(ctx, id)
}

// UpdateAlertChannelResult 回写一次发送结果：成功清空 last_error 并刷新 last_ok_at；
// 失败只写 last_error（保留上次成功时间，便于判断「曾经可用」）。
func (s *Store) UpdateAlertChannelResult(ctx context.Context, id string, okAt *int64, lastError *string) error {
	query := `UPDATE alert_channels SET last_error = ?`
	args := []any{nullableStr(emptyToNil(derefOrEmpty(lastError)))}
	if okAt != nil {
		query += `, last_ok_at = ?`
		args = append(args, *okAt)
	}
	query += ` WHERE id = ?`
	args = append(args, id)

	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("回写通知渠道 %s 发送结果: %w", id, err)
	}
	return nil
}

// DeleteAlertChannel 删除渠道。
func (s *Store) DeleteAlertChannel(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM alert_channels WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除通知渠道 %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("删除通知渠道 %s 后读取影响行数: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("删除通知渠道 %s: %w", id, ErrNotFound)
	}
	return nil
}

// ------------------------------------------------ 告警 ------------------------------------------------

// CreateAlert 插入一条告警。
func (s *Store) CreateAlert(ctx context.Context, a Alert) error {
	if a.ID == "" || a.RuleID == "" || a.DedupKey == "" {
		return errors.New("创建告警: id/rule_id/dedup_key 不能为空")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO alerts (`+alertColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.RuleID, a.DedupKey, nullableStr(a.ServerID), nullableStr(a.CheckID),
		a.Status, a.Severity, a.Title, nullableStr(a.Message), nullableFloat(a.Value),
		a.StartedAt, a.UpdatedAt, nullableInt(a.ResolvedAt), nullableInt(a.AckedAt),
		nullableStr(a.AckedBy), a.NotifyCount, nullableInt(a.LastNotifyAt), nullableInt(a.SilenceUntil))
	if err != nil {
		return fmt.Errorf("创建告警 %s: %w", a.DedupKey, err)
	}
	return nil
}

// GetAlert 按 ID 查询告警。
func (s *Store) GetAlert(ctx context.Context, id string) (*Alert, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+alertColumns+` FROM alerts WHERE id = ?`, id)
	return scanAlert(row)
}

// FindActiveAlert 按 dedup_key 查未解决告警（firing 或 acked）；不存在返回 ErrNotFound。
func (s *Store) FindActiveAlert(ctx context.Context, dedupKey string) (*Alert, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+alertColumns+` FROM alerts
		  WHERE dedup_key = ? AND status IN (?, ?)
		  ORDER BY started_at DESC LIMIT 1`,
		dedupKey, AlertStatusFiring, AlertStatusAcked)
	return scanAlert(row)
}

// UpdateAlert 整体更新一条告警的可变字段（引擎负责传完整状态）。
func (s *Store) UpdateAlert(ctx context.Context, a Alert) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE alerts SET status = ?, severity = ?, title = ?, message = ?, value = ?,
		        updated_at = ?, resolved_at = ?, acked_at = ?, acked_by = ?,
		        notify_count = ?, last_notify_at = ?, silence_until = ?
		  WHERE id = ?`,
		a.Status, a.Severity, a.Title, nullableStr(a.Message), nullableFloat(a.Value),
		a.UpdatedAt, nullableInt(a.ResolvedAt), nullableInt(a.AckedAt), nullableStr(a.AckedBy),
		a.NotifyCount, nullableInt(a.LastNotifyAt), nullableInt(a.SilenceUntil), a.ID)
	if err != nil {
		return fmt.Errorf("更新告警 %s: %w", a.ID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("更新告警 %s 后读取影响行数: %w", a.ID, err)
	}
	if affected == 0 {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts WHERE id = ?`, a.ID).Scan(&exists); err != nil {
			return fmt.Errorf("确认告警 %s 是否存在: %w", a.ID, err)
		}
		if exists == 0 {
			return fmt.Errorf("更新告警 %s: %w", a.ID, ErrNotFound)
		}
	}
	return nil
}

// ResolveAlert 把告警置为 resolved。
func (s *Store) ResolveAlert(ctx context.Context, id string, ts int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE alerts SET status = ?, resolved_at = ?, updated_at = ? WHERE id = ?`,
		AlertStatusResolved, ts, ts, id)
	if err != nil {
		return fmt.Errorf("解除告警 %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("解除告警 %s 后读取影响行数: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("解除告警 %s: %w", id, ErrNotFound)
	}
	return nil
}

// AckAlert 人工确认：状态置 acked（不代表恢复）。
func (s *Store) AckAlert(ctx context.Context, id, by string, ts int64) (*Alert, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE alerts SET status = ?, acked_at = ?, acked_by = ?, updated_at = ?
		  WHERE id = ? AND status IN (?, ?)`,
		AlertStatusAcked, ts, nullableStr(emptyToNil(by)), ts, id, AlertStatusFiring, AlertStatusAcked)
	if err != nil {
		return nil, fmt.Errorf("确认告警 %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("确认告警 %s 后读取影响行数: %w", id, err)
	}
	if affected == 0 {
		// 区分「不存在」与「已 resolved 不能确认」
		existing, gerr := s.GetAlert(ctx, id)
		if gerr != nil {
			return nil, gerr
		}
		return existing, fmt.Errorf("告警 %s 当前状态为 %s，不能确认: %w", id, existing.Status, ErrConflict)
	}
	return s.GetAlert(ctx, id)
}

// AlertFilter 是告警列表的过滤条件。
type AlertFilter struct {
	Status   string
	Severity string
	RuleID   string
	ServerID string
	From     int64
	To       int64
	Limit    int
}

// ListAlerts 返回告警列表（按开始时间倒序）。
func (s *Store) ListAlerts(ctx context.Context, f AlertFilter) ([]Alert, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	query := `SELECT ` + alertColumns + ` FROM alerts WHERE 1 = 1`
	args := make([]any, 0, 8)
	if f.Status != "" {
		query += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.Severity != "" {
		query += ` AND severity = ?`
		args = append(args, f.Severity)
	}
	if f.RuleID != "" {
		query += ` AND rule_id = ?`
		args = append(args, f.RuleID)
	}
	if f.ServerID != "" {
		query += ` AND server_id = ?`
		args = append(args, f.ServerID)
	}
	if f.From > 0 {
		query += ` AND started_at >= ?`
		args = append(args, f.From)
	}
	if f.To > 0 {
		query += ` AND started_at <= ?`
		args = append(args, f.To)
	}
	query += ` ORDER BY started_at DESC, id DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询告警列表: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Alert, 0, f.Limit)
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历告警列表: %w", err)
	}
	return out, nil
}

// AlertSummary 统计告警概览：firing/acked 为当前活跃数，resolved_24h 为近 24 小时解除数，
// by_severity 统计当前活跃告警（firing+acked）的严重级别分布。
func (s *Store) AlertSummary(ctx context.Context, nowMS int64) (AlertSummary, error) {
	summary := AlertSummary{BySeverity: map[string]int{"critical": 0, "warning": 0, "info": 0}}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM alerts WHERE status = ?`, AlertStatusFiring).Scan(&summary.Firing); err != nil {
		return summary, fmt.Errorf("统计 firing 告警: %w", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM alerts WHERE status = ?`, AlertStatusAcked).Scan(&summary.Acked); err != nil {
		return summary, fmt.Errorf("统计 acked 告警: %w", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM alerts WHERE status = ? AND resolved_at >= ?`,
		AlertStatusResolved, nowMS-24*3600*1000).Scan(&summary.Resolved24h); err != nil {
		return summary, fmt.Errorf("统计 24h 解除告警: %w", err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT severity, COUNT(*) FROM alerts WHERE status IN (?, ?) GROUP BY severity`,
		AlertStatusFiring, AlertStatusAcked)
	if err != nil {
		return summary, fmt.Errorf("统计活跃告警级别: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var severity string
		var n int
		if err := rows.Scan(&severity, &n); err != nil {
			return summary, fmt.Errorf("扫描活跃告警级别: %w", err)
		}
		summary.BySeverity[severity] = n
	}
	if err := rows.Err(); err != nil {
		return summary, fmt.Errorf("遍历活跃告警级别: %w", err)
	}
	return summary, nil
}

// AppendAlertEvent 追加一条告警事件（派发/恢复/确认/通知/静默）。
func (s *Store) AppendAlertEvent(ctx context.Context, alertID, kind string, ts int64, detail string) error {
	if alertID == "" || kind == "" {
		return errors.New("追加告警事件: alert_id/kind 不能为空")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO alert_events (alert_id, kind, ts, detail) VALUES (?, ?, ?, ?)`,
		alertID, kind, ts, nullableStr(emptyToNil(detail)))
	if err != nil {
		return fmt.Errorf("追加告警事件 %s/%s: %w", alertID, kind, err)
	}
	return nil
}

// ListAlertEvents 返回告警的事件轨迹（按时间升序）。
func (s *Store) ListAlertEvents(ctx context.Context, alertID string, limit int) ([]AlertEvent, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, alert_id, kind, ts, detail FROM alert_events
		  WHERE alert_id = ? ORDER BY ts ASC, id ASC LIMIT ?`, alertID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询告警事件: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]AlertEvent, 0, 16)
	for rows.Next() {
		var (
			e      AlertEvent
			detail sql.NullString
		)
		if err := rows.Scan(&e.ID, &e.AlertID, &e.Kind, &e.TS, &detail); err != nil {
			return nil, fmt.Errorf("扫描告警事件: %w", err)
		}
		e.Detail = strPtr(detail)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历告警事件: %w", err)
	}
	return out, nil
}

// DeleteResolvedAlertsBefore 清理 before 之前解除的告警及其事件，返回删除条数。
func (s *Store) DeleteResolvedAlertsBefore(ctx context.Context, before int64) (int64, error) {
	var deleted int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM alert_events WHERE alert_id IN (SELECT id FROM alerts WHERE status = ? AND resolved_at < ?)`,
			AlertStatusResolved, before); err != nil {
			return fmt.Errorf("清理历史告警事件: %w", err)
		}
		res, err := tx.ExecContext(ctx,
			`DELETE FROM alerts WHERE status = ? AND resolved_at < ?`, AlertStatusResolved, before)
		if err != nil {
			return fmt.Errorf("清理历史告警: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("清理历史告警后读取影响行数: %w", err)
		}
		deleted = n
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// ------------------------------------------------ 扫描辅助 ------------------------------------------------

// ruleJSON 汇总规则里需要 JSON 编码的列。
type ruleJSON struct {
	checkIDs, serverIDs, groups, tags, channelIDs string
}

// encodeRuleJSON 把规则的切片列编码成 JSON 文本。
func (s *Store) encodeRuleJSON(r *AlertRule) (ruleJSON, error) {
	var out ruleJSON
	pairs := []struct {
		dst   *string
		value []string
	}{
		{&out.checkIDs, r.CheckIDs},
		{&out.serverIDs, r.ServerIDs},
		{&out.groups, r.Groups},
		{&out.tags, r.Tags},
		{&out.channelIDs, r.ChannelIDs},
	}
	for _, p := range pairs {
		encoded, err := json.Marshal(nonNilStrings(p.value))
		if err != nil {
			return out, fmt.Errorf("编码规则字段: %w", err)
		}
		*p.dst = string(encoded)
	}
	return out, nil
}

// scanAlertRule 把一行映射为 AlertRule，并把 JSON 列解成切片。
func scanAlertRule(row interface{ Scan(...any) error }) (*AlertRule, error) {
	var (
		r                                     AlertRule
		enabled                               int
		checkIDs, serverIDs, groups, tags, ch string
		metric, comparator                    sql.NullString
		threshold                             sql.NullFloat64
		silence                               sql.NullInt64
	)
	err := row.Scan(&r.ID, &r.Name, &enabled, &r.Source,
		&checkIDs, &serverIDs, &groups, &tags,
		&metric, &comparator, &threshold,
		&r.ForDurationS, &r.Severity, &r.DedupWindowS, &ch,
		&silence, &r.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询告警规则: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描告警规则: %w", err)
	}
	r.Enabled = enabled != 0
	r.CheckIDs = decodeStringList(checkIDs)
	r.ServerIDs = decodeStringList(serverIDs)
	r.Groups = decodeStringList(groups)
	r.Tags = decodeStringList(tags)
	r.ChannelIDs = decodeStringList(ch)
	r.Metric = strPtr(metric)
	r.Comparator = strPtr(comparator)
	if threshold.Valid {
		v := threshold.Float64
		r.Threshold = &v
	}
	r.SilenceUntil = intPtr(silence)
	return &r, nil
}

// scanAlertChannel 把一行映射为 AlertChannel。
func scanAlertChannel(row interface{ Scan(...any) error }) (*AlertChannel, error) {
	var (
		c       AlertChannel
		enabled int
		lastErr sql.NullString
		lastOK  sql.NullInt64
	)
	err := row.Scan(&c.ID, &c.Name, &c.Kind, &c.ConfigEnc, &enabled, &lastErr, &lastOK, &c.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询通知渠道: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描通知渠道: %w", err)
	}
	c.Enabled = enabled != 0
	c.LastError = strPtr(lastErr)
	c.LastOKAt = intPtr(lastOK)
	return &c, nil
}

// scanAlert 把一行映射为 Alert。
func scanAlert(row interface{ Scan(...any) error }) (*Alert, error) {
	var (
		a                               Alert
		serverID, checkID, message      sql.NullString
		value                           sql.NullFloat64
		resolvedAt, ackedAt, lastNotify sql.NullInt64
		ackedBy                         sql.NullString
		silence                         sql.NullInt64
	)
	err := row.Scan(&a.ID, &a.RuleID, &a.DedupKey, &serverID, &checkID,
		&a.Status, &a.Severity, &a.Title, &message, &value,
		&a.StartedAt, &a.UpdatedAt, &resolvedAt, &ackedAt, &ackedBy,
		&a.NotifyCount, &lastNotify, &silence)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询告警: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描告警: %w", err)
	}
	a.ServerID = strPtr(serverID)
	a.CheckID = strPtr(checkID)
	a.Message = strPtr(message)
	if value.Valid {
		v := value.Float64
		a.Value = &v
	}
	a.ResolvedAt = intPtr(resolvedAt)
	a.AckedAt = intPtr(ackedAt)
	a.AckedBy = strPtr(ackedBy)
	a.LastNotifyAt = intPtr(lastNotify)
	a.SilenceUntil = intPtr(silence)
	return &a, nil
}

// decodeStringList 把 JSON 文本解成切片；异常数据按空切片处理。
func decodeStringList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []string{}
	}
	return nonNilStrings(out)
}

// nonNilStrings 保证返回非 nil 切片（JSON 序列化为 []，而非 null）。
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// mustJSONString 把切片编码成 JSON 文本（切片元素为字符串，编码不会失败）。
func mustJSONString(in []string) string {
	encoded, err := json.Marshal(nonNilStrings(in))
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

// nullableFloat 把可选浮点转成可绑定参数。
func nullableFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// derefOrEmpty 解引用字符串指针（nil 返回空串）。
func derefOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
