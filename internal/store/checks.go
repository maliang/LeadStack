package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// checkColumns 是 checks 表的固定列顺序。
const checkColumns = `id, name, kind, target, interval_s, timeout_s, params_json, enabled, created_at`

// CheckUpdate 描述 PATCH 语义的检测项更新；nil 表示不修改。
type CheckUpdate struct {
	Name       *string
	Target     *string
	IntervalS  *int
	TimeoutS   *int
	ParamsJSON *string
	Enabled    *bool
}

// CreateCheck 插入一个检测项。
func (s *Store) CreateCheck(ctx context.Context, c *Check) error {
	if c == nil {
		return errors.New("创建检测项: 参数为空")
	}
	if c.IntervalS <= 0 {
		return errors.New("创建检测项: interval_s 必须为正数")
	}
	if c.TimeoutS <= 0 {
		c.TimeoutS = 10
	}
	if strings.TrimSpace(c.ParamsJSON) == "" {
		c.ParamsJSON = "{}"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO checks (`+checkColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.Kind, c.Target, c.IntervalS, c.TimeoutS, c.ParamsJSON, boolToInt(c.Enabled), c.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("创建检测项 %s: %w", c.Name, ErrConflict)
		}
		return fmt.Errorf("创建检测项 %s: %w", c.Name, err)
	}
	return nil
}

// GetCheck 按 ID 查询检测项；不存在返回 ErrNotFound。
func (s *Store) GetCheck(ctx context.Context, id string) (*Check, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+checkColumns+` FROM checks WHERE id = ?`, id)
	c, err := scanCheck(row)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ListChecks 返回全部检测项，按创建时间倒序。
func (s *Store) ListChecks(ctx context.Context) ([]Check, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+checkColumns+` FROM checks ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("查询检测项列表: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Check, 0, 8)
	for rows.Next() {
		c, err := scanCheck(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历检测项列表: %w", err)
	}
	return out, nil
}

// UpdateCheck 按 PATCH 语义更新检测项，返回更新后的记录。
func (s *Store) UpdateCheck(ctx context.Context, id string, upd CheckUpdate) (*Check, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		sets := make([]string, 0, 6)
		args := make([]any, 0, 7)
		if upd.Name != nil {
			sets = append(sets, "name = ?")
			args = append(args, *upd.Name)
		}
		if upd.Target != nil {
			sets = append(sets, "target = ?")
			args = append(args, *upd.Target)
		}
		if upd.IntervalS != nil {
			sets = append(sets, "interval_s = ?")
			args = append(args, *upd.IntervalS)
		}
		if upd.TimeoutS != nil {
			sets = append(sets, "timeout_s = ?")
			args = append(args, *upd.TimeoutS)
		}
		if upd.ParamsJSON != nil {
			sets = append(sets, "params_json = ?")
			args = append(args, *upd.ParamsJSON)
		}
		if upd.Enabled != nil {
			sets = append(sets, "enabled = ?")
			args = append(args, boolToInt(*upd.Enabled))
		}
		if len(sets) == 0 {
			return nil
		}

		query := `UPDATE checks SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
		args = append(args, id)
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("更新检测项 %s: %w", id, err)
		}
		if _, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("更新检测项 %s 后读取影响行数: %w", id, err)
		}
		// RowsAffected 为 0 也可能是值未变化，这里显式确认存在性。
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM checks WHERE id = ?`, id).Scan(&exists); err != nil {
			return fmt.Errorf("确认检测项 %s 是否存在: %w", id, err)
		}
		if exists == 0 {
			return fmt.Errorf("更新检测项 %s: %w", id, ErrNotFound)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetCheck(ctx, id)
}

// DeleteCheck 删除检测项及其目标、结果与状态。
func (s *Store) DeleteCheck(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM checks WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("删除检测项 %s: %w", id, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("删除检测项 %s 后读取影响行数: %w", id, err)
		}
		if affected == 0 {
			return fmt.Errorf("删除检测项 %s: %w", id, ErrNotFound)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM check_targets WHERE check_id = ?`, id); err != nil {
			return fmt.Errorf("清理检测项 %s 的目标: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM check_results WHERE check_id = ?`, id); err != nil {
			return fmt.Errorf("清理检测项 %s 的结果: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM check_state WHERE check_id = ?`, id); err != nil {
			return fmt.Errorf("清理检测项 %s 的状态: %w", id, err)
		}
		return nil
	})
}

// SetCheckTargets 覆盖式设置检测项的适用服务器（单事务内先删后插）。
func (s *Store) SetCheckTargets(ctx context.Context, checkID string, serverIDs []string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM checks WHERE id = ?`, checkID).Scan(&exists); err != nil {
			return fmt.Errorf("确认检测项 %s 是否存在: %w", checkID, err)
		}
		if exists == 0 {
			return fmt.Errorf("设置检测项 %s 的目标: %w", checkID, ErrNotFound)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM check_targets WHERE check_id = ?`, checkID); err != nil {
			return fmt.Errorf("清理检测项 %s 的旧目标: %w", checkID, err)
		}
		seen := make(map[string]struct{}, len(serverIDs))
		for _, serverID := range serverIDs {
			if serverID == "" {
				continue
			}
			if _, dup := seen[serverID]; dup {
				continue
			}
			seen[serverID] = struct{}{}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO check_targets (check_id, server_id) VALUES (?, ?)`, checkID, serverID); err != nil {
				return fmt.Errorf("绑定检测项 %s 到服务器 %s: %w", checkID, serverID, err)
			}
		}
		// 目标被移除后，其历史状态也一并清理，避免 /servers/{id}/checks 出现幽灵项。
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM check_state WHERE check_id = ? AND server_id NOT IN (SELECT server_id FROM check_targets WHERE check_id = ?)`,
			checkID, checkID); err != nil {
			return fmt.Errorf("清理检测项 %s 的失效状态: %w", checkID, err)
		}
		return nil
	})
}

// ListCheckTargets 返回检测项适用的服务器 ID 列表。
func (s *Store) ListCheckTargets(ctx context.Context, checkID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT server_id FROM check_targets WHERE check_id = ? ORDER BY server_id`, checkID)
	if err != nil {
		return nil, fmt.Errorf("查询检测项 %s 的目标: %w", checkID, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]string, 0, 4)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("扫描检测项目标: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历检测项目标: %w", err)
	}
	return out, nil
}

// ListAllCheckTargets 一次性返回 check_id → server_id 列表（列表页避免 N+1 查询）。
func (s *Store) ListAllCheckTargets(ctx context.Context) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT check_id, server_id FROM check_targets ORDER BY check_id ASC, server_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("批量查询检测项目标: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string][]string, 8)
	for rows.Next() {
		var checkID, serverID string
		if err := rows.Scan(&checkID, &serverID); err != nil {
			return nil, fmt.Errorf("扫描检测项目标: %w", err)
		}
		out[checkID] = append(out[checkID], serverID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历检测项目标: %w", err)
	}
	return out, nil
}

// ListCheckIDsForServer 返回适用于某台服务器的检测项 ID 列表。
func (s *Store) ListCheckIDsForServer(ctx context.Context, serverID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT check_id FROM check_targets WHERE server_id = ? ORDER BY check_id`, serverID)
	if err != nil {
		return nil, fmt.Errorf("查询服务器 %s 的检测项: %w", serverID, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]string, 0, 4)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("扫描服务器检测项: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历服务器检测项: %w", err)
	}
	return out, nil
}

// ListCheckTargetCounts 返回每个检测项的目标数量。
func (s *Store) ListCheckTargetCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT check_id, COUNT(*) FROM check_targets GROUP BY check_id`)
	if err != nil {
		return nil, fmt.Errorf("统计检测项目标数: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]int, 8)
	for rows.Next() {
		var checkID string
		var n int
		if err := rows.Scan(&checkID, &n); err != nil {
			return nil, fmt.Errorf("扫描检测项目标数: %w", err)
		}
		out[checkID] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历检测项目标数: %w", err)
	}
	return out, nil
}

// InsertCheckResult 写入一条检测结果。
func (s *Store) InsertCheckResult(ctx context.Context, r *CheckResult) error {
	if r == nil {
		return errors.New("写入检测结果: 参数为空")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO check_results (check_id, server_id, ts, ok, latency_ms, status_code, message)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.CheckID, r.ServerID, r.TS, boolToInt(r.OK), r.LatencyMS,
		nullableInt(statusCodeToPtr(r.StatusCode)), nullableStr(r.Message))
	if err != nil {
		return fmt.Errorf("写入检测结果 %s/%s: %w", r.CheckID, r.ServerID, err)
	}
	return nil
}

// CheckResultFilter 是结果时间线的过滤条件。
type CheckResultFilter struct {
	CheckID  string
	ServerID string
	From     int64
	To       int64
	Limit    int
}

// ListCheckResults 返回结果时间线（按时间倒序）。
func (s *Store) ListCheckResults(ctx context.Context, f CheckResultFilter) ([]CheckResult, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	query := `SELECT id, check_id, server_id, ts, ok, latency_ms, status_code, message
	            FROM check_results WHERE check_id = ?`
	args := []any{f.CheckID}
	if f.ServerID != "" {
		query += ` AND server_id = ?`
		args = append(args, f.ServerID)
	}
	if f.From > 0 {
		query += ` AND ts >= ?`
		args = append(args, f.From)
	}
	if f.To > 0 {
		query += ` AND ts <= ?`
		args = append(args, f.To)
	}
	query += ` ORDER BY ts DESC, id DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询检测结果: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]CheckResult, 0, f.Limit)
	for rows.Next() {
		r, err := scanCheckResult(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历检测结果: %w", err)
	}
	return out, nil
}

// ListLatestCheckResults 返回每个检测项最近的一条结果；checkIDs 为空表示全部。
func (s *Store) ListLatestCheckResults(ctx context.Context, checkIDs []string) (map[string]CheckResult, error) {
	query := `SELECT r.id, r.check_id, r.server_id, r.ts, r.ok, r.latency_ms, r.status_code, r.message
	            FROM check_results r
	           WHERE r.ts = (SELECT MAX(ts) FROM check_results WHERE check_id = r.check_id)`
	args := make([]any, 0, len(checkIDs))
	if len(checkIDs) > 0 {
		query += ` AND r.check_id IN (` + placeholders(len(checkIDs)) + `)`
		for _, id := range checkIDs {
			args = append(args, id)
		}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询最近检测结果: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]CheckResult, 8)
	for rows.Next() {
		r, err := scanCheckResult(rows)
		if err != nil {
			return nil, err
		}
		out[r.CheckID] = *r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历最近检测结果: %w", err)
	}
	return out, nil
}

// ListCheckStates 返回检测项状态；checkIDs 为空表示全部。
func (s *Store) ListCheckStates(ctx context.Context, checkIDs []string) (map[string][]CheckState, error) {
	query := `SELECT check_id, server_id, ok, consecutive_fail, since, last_ts, last_message FROM check_state`
	args := make([]any, 0, len(checkIDs))
	if len(checkIDs) > 0 {
		query += ` WHERE check_id IN (` + placeholders(len(checkIDs)) + `)`
		for _, id := range checkIDs {
			args = append(args, id)
		}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询检测项状态: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string][]CheckState, 8)
	for rows.Next() {
		st, err := scanCheckState(rows)
		if err != nil {
			return nil, err
		}
		out[st.CheckID] = append(out[st.CheckID], *st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历检测项状态: %w", err)
	}
	return out, nil
}

// GetCheckState 读取一个 (检测项, 服务器) 的状态；不存在返回 ErrNotFound。
func (s *Store) GetCheckState(ctx context.Context, checkID, serverID string) (*CheckState, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT check_id, server_id, ok, consecutive_fail, since, last_ts, last_message
		   FROM check_state WHERE check_id = ? AND server_id = ?`, checkID, serverID)
	st, err := scanCheckState(row)
	if err != nil {
		return nil, err
	}
	return st, nil
}

// ListAvailability 统计 since（Unix 毫秒）以来每个检测项的可用率。
func (s *Store) ListAvailability(ctx context.Context, since int64) (map[string]AvailabilityStat, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT check_id, COUNT(*), COALESCE(SUM(ok), 0) FROM check_results
		  WHERE ts >= ? GROUP BY check_id`, since)
	if err != nil {
		return nil, fmt.Errorf("统计可用率: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]AvailabilityStat, 8)
	for rows.Next() {
		var checkID string
		var stat AvailabilityStat
		if err := rows.Scan(&checkID, &stat.Total, &stat.Up); err != nil {
			return nil, fmt.Errorf("扫描可用率: %w", err)
		}
		out[checkID] = stat
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历可用率: %w", err)
	}
	return out, nil
}

// Availability 统计单个检测项自 since 以来的可用率；serverID 为空表示全部目标。
func (s *Store) Availability(ctx context.Context, checkID, serverID string, since int64) (AvailabilityStat, error) {
	query := `SELECT COUNT(*), COALESCE(SUM(ok), 0) FROM check_results WHERE check_id = ? AND ts >= ?`
	args := []any{checkID, since}
	if serverID != "" {
		query += ` AND server_id = ?`
		args = append(args, serverID)
	}
	var stat AvailabilityStat
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&stat.Total, &stat.Up); err != nil {
		return AvailabilityStat{}, fmt.Errorf("统计检测项 %s 可用率: %w", checkID, err)
	}
	return stat, nil
}

// DeleteCheckResultsBefore 清理 before（Unix 毫秒）之前的检测结果，返回删除条数。
func (s *Store) DeleteCheckResultsBefore(ctx context.Context, before int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM check_results WHERE ts < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("清理历史检测结果: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("清理历史检测结果后读取影响行数: %w", err)
	}
	return n, nil
}

// scanCheck 把一行映射为 Check。
func scanCheck(row interface{ Scan(...any) error }) (*Check, error) {
	var (
		c       Check
		enabled int
	)
	err := row.Scan(&c.ID, &c.Name, &c.Kind, &c.Target, &c.IntervalS, &c.TimeoutS,
		&c.ParamsJSON, &enabled, &c.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询检测项: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描检测项: %w", err)
	}
	c.Enabled = enabled != 0
	if strings.TrimSpace(c.ParamsJSON) == "" {
		c.ParamsJSON = "{}"
	}
	return &c, nil
}

// scanCheckResult 把一行映射为 CheckResult。
func scanCheckResult(row interface{ Scan(...any) error }) (*CheckResult, error) {
	var (
		r       CheckResult
		okInt   int
		status  sql.NullInt64
		message sql.NullString
	)
	err := row.Scan(&r.ID, &r.CheckID, &r.ServerID, &r.TS, &okInt, &r.LatencyMS, &status, &message)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询检测结果: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描检测结果: %w", err)
	}
	r.OK = okInt != 0
	r.StatusCode = int64PtrToIntPtr(status)
	r.Message = strPtr(message)
	return &r, nil
}

// scanCheckState 把一行映射为 CheckState。
func scanCheckState(row interface{ Scan(...any) error }) (*CheckState, error) {
	var (
		st      CheckState
		okInt   int
		message sql.NullString
	)
	err := row.Scan(&st.CheckID, &st.ServerID, &okInt, &st.ConsecutiveFail,
		&st.Since, &st.LastTS, &message)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询检测状态: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描检测状态: %w", err)
	}
	st.OK = okInt != 0
	st.LastMessage = strPtr(message)
	return &st, nil
}

// boolToInt 把 bool 转成 SQLite 的 0/1。
func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// int64PtrToIntPtr 把 sql.NullInt64 转成 *int。
func int64PtrToIntPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

// statusCodeToPtr 把可空状态码转成 *int64，便于绑定参数。
func statusCodeToPtr(v *int) *int64 {
	if v == nil {
		return nil
	}
	n := int64(*v)
	return &n
}

// placeholders 生成 "?,?,?" 形式的占位符串。
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// EnabledCheckTarget 是「启用中的检测项 × 适用服务器」，附带服务器模式供检测层选 Executor。
type EnabledCheckTarget struct {
	Check    Check
	ServerID string
	Mode     string
}

// ListEnabledCheckTargets 返回所有启用检测项的目标（含服务器 mode）。
func (s *Store) ListEnabledCheckTargets(ctx context.Context) ([]EnabledCheckTarget, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.name, c.kind, c.target, c.interval_s, c.timeout_s, c.params_json, c.enabled, c.created_at,
		        ct.server_id, s.mode
		   FROM checks c
		   JOIN check_targets ct ON ct.check_id = c.id
		   JOIN servers s ON s.id = ct.server_id
		  WHERE c.enabled = 1
		  ORDER BY c.id ASC, ct.server_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询启用的检测目标: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]EnabledCheckTarget, 0, 8)
	for rows.Next() {
		var (
			t       EnabledCheckTarget
			enabled int
		)
		if err := rows.Scan(&t.Check.ID, &t.Check.Name, &t.Check.Kind, &t.Check.Target,
			&t.Check.IntervalS, &t.Check.TimeoutS, &t.Check.ParamsJSON, &enabled, &t.Check.CreatedAt,
			&t.ServerID, &t.Mode); err != nil {
			return nil, fmt.Errorf("扫描启用的检测目标: %w", err)
		}
		t.Check.Enabled = enabled != 0
		if strings.TrimSpace(t.Check.ParamsJSON) == "" {
			t.Check.ParamsJSON = "{}"
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历启用的检测目标: %w", err)
	}
	return out, nil
}

// UpsertCheckState 按一次检测结果推进状态机：
//   - 成功：ok=1、连续失败清零；状态由失败转成功时刷新 since；
//   - 失败：ok=0、连续失败 +1；状态由成功转失败或首次失败时刷新 since；
//   - last_ts 始终更新，last_message 由 SaveResult 写入（这里不改）。
func (s *Store) UpsertCheckState(ctx context.Context, checkID, serverID string, ok bool, ts int64) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var (
			prevOK   int
			prevFail int
			since    int64
			found    bool
		)
		err := tx.QueryRowContext(ctx,
			`SELECT ok, consecutive_fail, since FROM check_state WHERE check_id = ? AND server_id = ?`,
			checkID, serverID).Scan(&prevOK, &prevFail, &since)
		switch {
		case err == nil:
			found = true
		case errors.Is(err, sql.ErrNoRows):
			found = false
		default:
			return fmt.Errorf("读取检测状态 %s/%s: %w", checkID, serverID, err)
		}

		// 摘要直接取该 (检测项, 服务器) 最新结果，避免 SaveResult/UpdateState 的调用顺序影响展示。
		// 恢复成功（ok=true）时强制清空：过期的失败文案不应继续展示给用户。
		var lastMessage sql.NullString
		if ok {
			lastMessage = sql.NullString{}
		} else if err := tx.QueryRowContext(ctx,
			`SELECT message FROM check_results WHERE check_id = ? AND server_id = ?
			  ORDER BY ts DESC, id DESC LIMIT 1`, checkID, serverID).Scan(&lastMessage); err != nil &&
			!errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("读取最近检测摘要 %s/%s: %w", checkID, serverID, err)
		}

		newOK := boolToInt(ok)
		newFail := 0
		newSince := ts
		if found {
			newSince = since
			if ok {
				newFail = 0
				if prevOK == 0 || since == 0 {
					newSince = ts // 失败 → 成功：状态起点刷新
				}
			} else {
				newFail = prevFail + 1
				if prevOK != 0 || since == 0 {
					newSince = ts // 成功 → 失败：状态起点刷新
				}
			}
		} else if !ok {
			newFail = 1
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO check_state (check_id, server_id, ok, consecutive_fail, since, last_ts, last_message)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(check_id, server_id) DO UPDATE SET
			     ok = excluded.ok,
			     consecutive_fail = excluded.consecutive_fail,
			     since = excluded.since,
			     last_ts = excluded.last_ts,
			     last_message = excluded.last_message`,
			checkID, serverID, newOK, newFail, newSince, ts, nullableStr(strPtr(lastMessage))); err != nil {
			return fmt.Errorf("写入检测状态 %s/%s: %w", checkID, serverID, err)
		}
		return nil
	})
}
