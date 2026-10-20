package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ------------------------------------------------ 审计日志 ------------------------------------------------

// AuditLog 是一条审计记录（契约第 36 节）。
type AuditLog struct {
	ID           int64   `json:"id"`
	TS           int64   `json:"ts"`
	Actor        string  `json:"actor"`
	ActorRole    *string `json:"actor_role"`
	Action       string  `json:"action"`
	ResourceType *string `json:"resource_type"`
	ResourceID   *string `json:"resource_id"`
	Method       *string `json:"method"`
	Path         *string `json:"path"`
	StatusCode   *int    `json:"status_code"`
	IP           *string `json:"ip"`
	UserAgent    *string `json:"user_agent"`
	Detail       *string `json:"detail"`
}

// InsertAuditLog 写入一条审计记录。
func (s *Store) InsertAuditLog(ctx context.Context, e *AuditLog) error {
	if e == nil {
		return errors.New("写入审计日志: 参数为空")
	}
	if e.Actor == "" {
		e.Actor = "system"
	}
	if e.Action == "" {
		return errors.New("写入审计日志: action 为空")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_logs (ts, actor, actor_role, action, resource_type, resource_id,
		                         method, path, status_code, ip, user_agent, detail)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TS, e.Actor, nullableStr(e.ActorRole), e.Action,
		nullableStr(e.ResourceType), nullableStr(e.ResourceID),
		nullableStr(e.Method), nullableStr(e.Path), nullableInt(statusCodeToPtr(e.StatusCode)),
		nullableStr(e.IP), nullableStr(e.UserAgent), nullableStr(e.Detail))
	if err != nil {
		return fmt.Errorf("写入审计日志 %s: %w", e.Action, err)
	}
	return nil
}

// AuditFilter 是审计列表的过滤与分页条件。
type AuditFilter struct {
	Actor        string
	Action       string
	ResourceType string
	From         int64
	To           int64
	Limit        int
	AfterID      int64 // keyset 分页：只返回 id < AfterID 的记录（0 表示不限）
}

// ListAuditLogs 返回审计记录（按时间/id 倒序）。
func (s *Store) ListAuditLogs(ctx context.Context, f AuditFilter) ([]AuditLog, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	query := `SELECT id, ts, actor, actor_role, action, resource_type, resource_id,
	                 method, path, status_code, ip, user_agent, detail
	            FROM audit_logs WHERE 1 = 1`
	args := make([]any, 0, 8)
	if f.Actor != "" {
		query += ` AND actor = ?`
		args = append(args, f.Actor)
	}
	if f.Action != "" {
		query += ` AND action = ?`
		args = append(args, f.Action)
	}
	if f.ResourceType != "" {
		query += ` AND resource_type = ?`
		args = append(args, f.ResourceType)
	}
	if f.From > 0 {
		query += ` AND ts >= ?`
		args = append(args, f.From)
	}
	if f.To > 0 {
		query += ` AND ts <= ?`
		args = append(args, f.To)
	}
	if f.AfterID > 0 {
		query += ` AND id < ?`
		args = append(args, f.AfterID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询审计日志: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]AuditLog, 0, f.Limit)
	for rows.Next() {
		var (
			item                                           AuditLog
			role, rtype, rid, method, path, ip, ua, detail sql.NullString
			status                                         sql.NullInt64
		)
		if err := rows.Scan(&item.ID, &item.TS, &item.Actor, &role, &item.Action,
			&rtype, &rid, &method, &path, &status, &ip, &ua, &detail); err != nil {
			return nil, fmt.Errorf("扫描审计日志: %w", err)
		}
		item.ActorRole = strPtr(role)
		item.ResourceType = strPtr(rtype)
		item.ResourceID = strPtr(rid)
		item.Method = strPtr(method)
		item.Path = strPtr(path)
		item.StatusCode = int64PtrToIntPtr(status)
		item.IP = strPtr(ip)
		item.UserAgent = strPtr(ua)
		item.Detail = strPtr(detail)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历审计日志: %w", err)
	}
	return out, nil
}

// DeleteAuditLogsBefore 清理 before（Unix 毫秒）之前的审计日志，返回删除条数。
func (s *Store) DeleteAuditLogsBefore(ctx context.Context, before int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM audit_logs WHERE ts < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("清理历史审计日志: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("清理历史审计日志后读取影响行数: %w", err)
	}
	return n, nil
}

// ------------------------------------------------ 用户（M5 扩展） ------------------------------------------------

// UserRecord 是带禁用状态与更新时间的用户（契约第 37 节）。
type UserRecord struct {
	ID           string
	Username     string
	PasswordHash string
	Role         string
	Disabled     bool
	CreatedAt    int64
	UpdatedAt    *int64
}

// ListUsers 返回全部用户（按创建时间升序）。
func (s *Store) ListUsers(ctx context.Context) ([]UserRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, password_hash, role, disabled, created_at, updated_at
		   FROM users ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询用户列表: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]UserRecord, 0, 8)
	for rows.Next() {
		var (
			u        UserRecord
			disabled int
			updated  sql.NullInt64
		)
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &disabled, &u.CreatedAt, &updated); err != nil {
			return nil, fmt.Errorf("扫描用户: %w", err)
		}
		u.Disabled = disabled != 0
		u.UpdatedAt = intPtr(updated)
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历用户: %w", err)
	}
	return out, nil
}

// GetUserRecord 按 ID 查询用户（含禁用状态）。
func (s *Store) GetUserRecord(ctx context.Context, id string) (*UserRecord, error) {
	var (
		u        UserRecord
		disabled int
		updated  sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, disabled, created_at, updated_at FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &disabled, &u.CreatedAt, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询用户: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描用户: %w", err)
	}
	u.Disabled = disabled != 0
	u.UpdatedAt = intPtr(updated)
	return &u, nil
}

// GetUserRecordByUsername 按用户名查询用户（含禁用状态）。
func (s *Store) GetUserRecordByUsername(ctx context.Context, username string) (*UserRecord, error) {
	var (
		u        UserRecord
		disabled int
		updated  sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, disabled, created_at, updated_at FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &disabled, &u.CreatedAt, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询用户: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描用户: %w", err)
	}
	u.Disabled = disabled != 0
	u.UpdatedAt = intPtr(updated)
	return &u, nil
}

// UserUpdate 是 PATCH 语义的用户更新（nil 表示不改）。
type UserUpdate struct {
	Role         *string
	Disabled     *bool
	PasswordHash *string
}

// UpdateUser 更新用户角色/禁用状态/密码。
func (s *Store) UpdateUser(ctx context.Context, id string, upd UserUpdate, nowMS int64) (*UserRecord, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		sets := make([]string, 0, 4)
		args := make([]any, 0, 5)
		if upd.Role != nil {
			sets = append(sets, "role = ?")
			args = append(args, *upd.Role)
		}
		if upd.Disabled != nil {
			sets = append(sets, "disabled = ?")
			args = append(args, boolToInt(*upd.Disabled))
		}
		if upd.PasswordHash != nil {
			sets = append(sets, "password_hash = ?")
			args = append(args, *upd.PasswordHash)
		}
		if len(sets) == 0 {
			return nil
		}
		sets = append(sets, "updated_at = ?")
		args = append(args, nowMS)
		query := `UPDATE users SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
		args = append(args, id)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("更新用户 %s: %w", id, err)
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, id).Scan(&exists); err != nil {
			return fmt.Errorf("确认用户 %s 是否存在: %w", id, err)
		}
		if exists == 0 {
			return fmt.Errorf("更新用户 %s: %w", id, ErrNotFound)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetUserRecord(ctx, id)
}

// CountActiveAdmins 统计未禁用的 admin 数量（删除保护用）。
func (s *Store) CountActiveAdmins(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled = 0`).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计管理员数量: %w", err)
	}
	return n, nil
}

// DeleteUser 删除用户；不存在返回 ErrNotFound。
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除用户 %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("删除用户 %s 后读取影响行数: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("删除用户 %s: %w", id, ErrNotFound)
	}
	return nil
}

// ------------------------------------------------ API Token ------------------------------------------------

// APIToken 是一条 API Token（库内只存哈希）。
type APIToken struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	TokenHash  string  `json:"-"`
	Role       string  `json:"role"`
	CreatedBy  *string `json:"created_by"`
	CreatedAt  int64   `json:"created_at"`
	LastUsedAt *int64  `json:"last_used_at"`
	ExpiresAt  *int64  `json:"expires_at"`
	RevokedAt  *int64  `json:"revoked_at"`
}

// CreateAPIToken 插入一条 token。
func (s *Store) CreateAPIToken(ctx context.Context, t *APIToken) error {
	if t == nil {
		return errors.New("创建 API Token: 参数为空")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_tokens (id, name, token_hash, role, created_by, created_at, last_used_at, expires_at, revoked_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.TokenHash, t.Role, nullableStr(t.CreatedBy), t.CreatedAt,
		nullableInt(t.LastUsedAt), nullableInt(t.ExpiresAt), nullableInt(t.RevokedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("创建 API Token %s: %w", t.Name, ErrConflict)
		}
		return fmt.Errorf("创建 API Token %s: %w", t.Name, err)
	}
	return nil
}

// ListAPITokens 返回全部 token（不含哈希）。
func (s *Store) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, token_hash, role, created_by, created_at, last_used_at, expires_at, revoked_at
		   FROM api_tokens ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("查询 API Token 列表: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]APIToken, 0, 8)
	for rows.Next() {
		var (
			t                          APIToken
			createdBy                  sql.NullString
			lastUsed, expires, revoked sql.NullInt64
		)
		if err := rows.Scan(&t.ID, &t.Name, &t.TokenHash, &t.Role, &createdBy, &t.CreatedAt,
			&lastUsed, &expires, &revoked); err != nil {
			return nil, fmt.Errorf("扫描 API Token: %w", err)
		}
		t.CreatedBy = strPtr(createdBy)
		t.LastUsedAt = intPtr(lastUsed)
		t.ExpiresAt = intPtr(expires)
		t.RevokedAt = intPtr(revoked)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 API Token: %w", err)
	}
	return out, nil
}

// FindAPITokenByHash 按哈希查询 token（含已撤销/过期，由调用方判定）。
func (s *Store) FindAPITokenByHash(ctx context.Context, hash string) (*APIToken, error) {
	var (
		t                          APIToken
		createdBy                  sql.NullString
		lastUsed, expires, revoked sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, token_hash, role, created_by, created_at, last_used_at, expires_at, revoked_at
		   FROM api_tokens WHERE token_hash = ?`, hash).
		Scan(&t.ID, &t.Name, &t.TokenHash, &t.Role, &createdBy, &t.CreatedAt, &lastUsed, &expires, &revoked)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询 API Token: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描 API Token: %w", err)
	}
	t.CreatedBy = strPtr(createdBy)
	t.LastUsedAt = intPtr(lastUsed)
	t.ExpiresAt = intPtr(expires)
	t.RevokedAt = intPtr(revoked)
	return &t, nil
}

// TouchAPIToken 更新 last_used_at。
func (s *Store) TouchAPIToken(ctx context.Context, id string, ts int64) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, ts, id); err != nil {
		return fmt.Errorf("更新 API Token %s 使用时间: %w", id, err)
	}
	return nil
}

// RevokeAPIToken 撤销 token（软删除）。
func (s *Store) RevokeAPIToken(ctx context.Context, id string, ts int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?`, ts, id)
	if err != nil {
		return fmt.Errorf("撤销 API Token %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("撤销 API Token %s 后读取影响行数: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("撤销 API Token %s: %w", id, ErrNotFound)
	}
	return nil
}

// ------------------------------------------------ 定时任务 ------------------------------------------------

// ScheduledTask 是一个定时任务（契约第 42 节）。
type ScheduledTask struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Cron        string   `json:"cron"`
	Command     string   `json:"command"`
	TimeoutS    int      `json:"timeout_s"`
	Enabled     bool     `json:"enabled"`
	Concurrency int      `json:"concurrency"`
	CreatedAt   int64    `json:"created_at"`
	LastRunAt   *int64   `json:"last_run_at"`
	NextRunAt   *int64   `json:"next_run_at"`
	ServerIDs   []string `json:"server_ids,omitempty"`
}

// ScheduledTaskRun 是一次任务执行记录。
type ScheduledTaskRun struct {
	ID         int64   `json:"id"`
	TaskID     string  `json:"task_id"`
	ServerID   string  `json:"server_id"`
	TS         int64   `json:"ts"`
	Status     string  `json:"status"`
	ExitCode   *int    `json:"exit_code"`
	DurationMS *int64  `json:"duration_ms"`
	Stdout     *string `json:"stdout"`
	Stderr     *string `json:"stderr"`
	Message    *string `json:"message"`
}

// CreateScheduledTask 在单事务里写任务与目标。
func (s *Store) CreateScheduledTask(ctx context.Context, t *ScheduledTask) error {
	if t == nil {
		return errors.New("创建定时任务: 参数为空")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO scheduled_tasks (id, name, cron, command, timeout_s, enabled, concurrency,
			                              created_at, last_run_at, next_run_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.Name, t.Cron, t.Command, t.TimeoutS, boolToInt(t.Enabled), t.Concurrency,
			t.CreatedAt, nullableInt(t.LastRunAt), nullableInt(t.NextRunAt)); err != nil {
			return fmt.Errorf("创建定时任务 %s: %w", t.Name, err)
		}
		for _, serverID := range t.ServerIDs {
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO scheduled_task_targets (task_id, server_id) VALUES (?, ?)`,
				t.ID, serverID); err != nil {
				return fmt.Errorf("绑定定时任务 %s 到服务器 %s: %w", t.ID, serverID, err)
			}
		}
		return nil
	})
}

// scheduledTaskColumns 是 scheduled_tasks 表的固定列顺序。
const scheduledTaskColumns = `id, name, cron, command, timeout_s, enabled, concurrency,
	created_at, last_run_at, next_run_at`

// GetScheduledTask 按 ID 查询任务（含目标）。
func (s *Store) GetScheduledTask(ctx context.Context, id string) (*ScheduledTask, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+scheduledTaskColumns+` FROM scheduled_tasks WHERE id = ?`, id)
	t, err := scanScheduledTask(row)
	if err != nil {
		return nil, err
	}
	targets, err := s.ListScheduledTaskTargets(ctx, id)
	if err != nil {
		return nil, err
	}
	t.ServerIDs = targets
	return t, nil
}

// ListScheduledTasks 返回全部任务（含目标）。
func (s *Store) ListScheduledTasks(ctx context.Context) ([]ScheduledTask, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+scheduledTaskColumns+` FROM scheduled_tasks ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("查询定时任务列表: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ScheduledTask, 0, 8)
	for rows.Next() {
		t, err := scanScheduledTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历定时任务: %w", err)
	}
	for i := range out {
		targets, err := s.ListScheduledTaskTargets(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].ServerIDs = targets
	}
	return out, nil
}

// ListEnabledScheduledTasks 返回启用中的任务（引擎用）。
func (s *Store) ListEnabledScheduledTasks(ctx context.Context) ([]ScheduledTask, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+scheduledTaskColumns+` FROM scheduled_tasks WHERE enabled = 1 ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询启用定时任务: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ScheduledTask, 0, 8)
	for rows.Next() {
		t, err := scanScheduledTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历启用定时任务: %w", err)
	}
	for i := range out {
		targets, err := s.ListScheduledTaskTargets(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].ServerIDs = targets
	}
	return out, nil
}

// UpdateScheduledTask 更新任务字段与目标（nil 表示不改；ServerIDs 非 nil 时覆盖目标）。
func (s *Store) UpdateScheduledTask(ctx context.Context, id string, upd ScheduledTaskUpdate) (*ScheduledTask, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		sets := make([]string, 0, 8)
		args := make([]any, 0, 9)
		if upd.Name != nil {
			sets = append(sets, "name = ?")
			args = append(args, *upd.Name)
		}
		if upd.Cron != nil {
			sets = append(sets, "cron = ?")
			args = append(args, *upd.Cron)
		}
		if upd.Command != nil {
			sets = append(sets, "command = ?")
			args = append(args, *upd.Command)
		}
		if upd.TimeoutS != nil {
			sets = append(sets, "timeout_s = ?")
			args = append(args, *upd.TimeoutS)
		}
		if upd.Enabled != nil {
			sets = append(sets, "enabled = ?")
			args = append(args, boolToInt(*upd.Enabled))
		}
		if upd.Concurrency != nil {
			sets = append(sets, "concurrency = ?")
			args = append(args, *upd.Concurrency)
		}
		if upd.LastRunAt != nil {
			sets = append(sets, "last_run_at = ?")
			args = append(args, nullableInt(upd.LastRunAt))
		}
		if upd.NextRunAt != nil {
			sets = append(sets, "next_run_at = ?")
			args = append(args, nullableInt(upd.NextRunAt))
		}
		if len(sets) > 0 {
			query := `UPDATE scheduled_tasks SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
			args = append(args, id)
			if _, err := tx.ExecContext(ctx, query, args...); err != nil {
				return fmt.Errorf("更新定时任务 %s: %w", id, err)
			}
		}
		if upd.ServerIDs != nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM scheduled_task_targets WHERE task_id = ?`, id); err != nil {
				return fmt.Errorf("清理定时任务 %s 的目标: %w", id, err)
			}
			for _, serverID := range *upd.ServerIDs {
				if _, err := tx.ExecContext(ctx,
					`INSERT OR IGNORE INTO scheduled_task_targets (task_id, server_id) VALUES (?, ?)`,
					id, serverID); err != nil {
					return fmt.Errorf("绑定定时任务 %s 到服务器 %s: %w", id, serverID, err)
				}
			}
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduled_tasks WHERE id = ?`, id).Scan(&exists); err != nil {
			return fmt.Errorf("确认定时任务 %s 是否存在: %w", id, err)
		}
		if exists == 0 {
			return fmt.Errorf("更新定时任务 %s: %w", id, ErrNotFound)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetScheduledTask(ctx, id)
}

// ScheduledTaskUpdate 是 PATCH 语义的任务更新。
type ScheduledTaskUpdate struct {
	Name        *string
	Cron        *string
	Command     *string
	TimeoutS    *int
	Enabled     *bool
	Concurrency *int
	LastRunAt   *int64
	NextRunAt   *int64
	ServerIDs   *[]string
}

// DeleteScheduledTask 删除任务及目标与历史执行记录。
func (s *Store) DeleteScheduledTask(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM scheduled_tasks WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("删除定时任务 %s: %w", id, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("删除定时任务 %s 后读取影响行数: %w", id, err)
		}
		if affected == 0 {
			return fmt.Errorf("删除定时任务 %s: %w", id, ErrNotFound)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM scheduled_task_targets WHERE task_id = ?`, id); err != nil {
			return fmt.Errorf("清理定时任务 %s 的目标: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM scheduled_task_runs WHERE task_id = ?`, id); err != nil {
			return fmt.Errorf("清理定时任务 %s 的执行记录: %w", id, err)
		}
		return nil
	})
}

// ListScheduledTaskTargets 返回任务的目标服务器。
func (s *Store) ListScheduledTaskTargets(ctx context.Context, taskID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT server_id FROM scheduled_task_targets WHERE task_id = ? ORDER BY server_id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("查询定时任务目标: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]string, 0, 4)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("扫描定时任务目标: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历定时任务目标: %w", err)
	}
	return out, nil
}

// InsertScheduledTaskRun 写入一条执行记录。
func (s *Store) InsertScheduledTaskRun(ctx context.Context, r *ScheduledTaskRun) error {
	if r == nil {
		return errors.New("写入定时任务执行记录: 参数为空")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO scheduled_task_runs (task_id, server_id, ts, status, exit_code, duration_ms,
		                                  stdout, stderr, message)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TaskID, r.ServerID, r.TS, r.Status, nullableInt(statusCodeToPtr(r.ExitCode)),
		nullableInt(r.DurationMS), nullableStr(r.Stdout), nullableStr(r.Stderr), nullableStr(r.Message))
	if err != nil {
		return fmt.Errorf("写入定时任务执行记录 %s/%s: %w", r.TaskID, r.ServerID, err)
	}
	return nil
}

// ListScheduledTaskRuns 返回任务的执行记录（倒序，可选 afterID 分页）。
func (s *Store) ListScheduledTaskRuns(ctx context.Context, taskID string, afterID int64, limit int) ([]ScheduledTaskRun, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT id, task_id, server_id, ts, status, exit_code, duration_ms, stdout, stderr, message
	            FROM scheduled_task_runs WHERE task_id = ?`
	args := []any{taskID}
	if afterID > 0 {
		query += ` AND id < ?`
		args = append(args, afterID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询定时任务执行记录: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ScheduledTaskRun, 0, limit)
	for rows.Next() {
		var (
			r                       ScheduledTaskRun
			exitCode                sql.NullInt64
			duration                sql.NullInt64
			stdout, stderr, message sql.NullString
		)
		if err := rows.Scan(&r.ID, &r.TaskID, &r.ServerID, &r.TS, &r.Status, &exitCode,
			&duration, &stdout, &stderr, &message); err != nil {
			return nil, fmt.Errorf("扫描定时任务执行记录: %w", err)
		}
		r.ExitCode = int64PtrToIntPtr(exitCode)
		if duration.Valid {
			v := duration.Int64
			r.DurationMS = &v
		}
		r.Stdout = strPtr(stdout)
		r.Stderr = strPtr(stderr)
		r.Message = strPtr(message)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历定时任务执行记录: %w", err)
	}
	return out, nil
}

// DeleteScheduledTaskRunsBefore 清理 before 之前的执行记录。
func (s *Store) DeleteScheduledTaskRunsBefore(ctx context.Context, before int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM scheduled_task_runs WHERE ts < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("清理历史定时任务记录: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("清理历史定时任务记录后读取影响行数: %w", err)
	}
	return n, nil
}

// scanScheduledTask 把一行映射为 ScheduledTask。
func scanScheduledTask(row interface{ Scan(...any) error }) (*ScheduledTask, error) {
	var (
		t                ScheduledTask
		enabled          int
		lastRun, nextRun sql.NullInt64
	)
	err := row.Scan(&t.ID, &t.Name, &t.Cron, &t.Command, &t.TimeoutS, &enabled, &t.Concurrency,
		&t.CreatedAt, &lastRun, &nextRun)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询定时任务: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描定时任务: %w", err)
	}
	t.Enabled = enabled != 0
	t.LastRunAt = intPtr(lastRun)
	t.NextRunAt = intPtr(nextRun)
	return &t, nil
}

// UpdateServerAgentVersion 更新服务器上记录的 agent 版本。
func (s *Store) UpdateServerAgentVersion(ctx context.Context, serverID, version string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE servers SET agent_version = ?, updated_at = ? WHERE id = ?`, version, NowMS(), serverID)
	if err != nil {
		return fmt.Errorf("更新服务器 %s 的 agent 版本: %w", serverID, err)
	}
	if _, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("更新 agent 版本后读取影响行数: %w", err)
	}
	return nil
}
