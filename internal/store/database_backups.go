package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// DatabaseBackupConfig 是服务器上一个 MySQL/MariaDB 自动备份配置。
// Databases 为库名数组；PasswordEnc 是控制端主密钥加密后的密文。
type DatabaseBackupConfig struct {
	ID            string   `json:"id"`
	ServerID      string   `json:"server_id"`
	Databases     []string `json:"databases"`
	Host          string   `json:"host"`
	Port          int      `json:"port"`
	Username      string   `json:"username"`
	PasswordEnc   []byte   `json:"-"`
	IntervalHours int      `json:"interval_hours"`
	Retention     int      `json:"retention"`
	Enabled       bool     `json:"enabled"`
	LastRunAt     *int64   `json:"last_run_at"`
	NextRunAt     int64    `json:"next_run_at"`
	LastError     *string  `json:"last_error"`
	CreatedAt     int64    `json:"created_at"`
	UpdatedAt     int64    `json:"updated_at"`
}

func (s *Store) ListDatabaseBackupConfigs(ctx context.Context, serverID string) ([]DatabaseBackupConfig, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, server_id, databases, host, port, username, password_enc,
		        interval_hours, retention, enabled, last_run_at, next_run_at, last_error, created_at, updated_at
		   FROM database_backup_configs WHERE server_id = ? ORDER BY created_at ASC`, serverID)
	if err != nil {
		return nil, fmt.Errorf("查询数据库备份配置: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]DatabaseBackupConfig, 0, 4)
	for rows.Next() {
		cfg, err := scanDatabaseBackupConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *cfg)
	}
	return out, rows.Err()
}

func (s *Store) GetDatabaseBackupConfig(ctx context.Context, id string) (*DatabaseBackupConfig, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, server_id, databases, host, port, username, password_enc,
		        interval_hours, retention, enabled, last_run_at, next_run_at, last_error, created_at, updated_at
		   FROM database_backup_configs WHERE id = ?`, id)
	cfg, err := scanDatabaseBackupConfig(row)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func (s *Store) UpsertDatabaseBackupConfig(ctx context.Context, cfg *DatabaseBackupConfig) error {
	databases, err := json.Marshal(cfg.Databases)
	if err != nil {
		return fmt.Errorf("编码数据库列表: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO database_backup_configs
		 (id, server_id, databases, host, port, username, password_enc, interval_hours, retention,
		  enabled, last_run_at, next_run_at, last_error, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		  databases=excluded.databases, host=excluded.host, port=excluded.port,
		  username=excluded.username, password_enc=excluded.password_enc,
		  interval_hours=excluded.interval_hours, retention=excluded.retention,
		  enabled=excluded.enabled, next_run_at=excluded.next_run_at,
		  last_error=excluded.last_error, updated_at=excluded.updated_at`,
		cfg.ID, cfg.ServerID, string(databases), cfg.Host, cfg.Port, cfg.Username, cfg.PasswordEnc,
		cfg.IntervalHours, cfg.Retention, boolInt(cfg.Enabled), nullableIntValue(cfg.LastRunAt),
		cfg.NextRunAt, nullableStr(cfg.LastError), cfg.CreatedAt, cfg.UpdatedAt)
	if err != nil {
		return fmt.Errorf("保存数据库备份配置: %w", err)
	}
	return nil
}

func (s *Store) DeleteDatabaseBackupConfig(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM database_backup_configs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除数据库备份配置: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListDueDatabaseBackupConfigs(ctx context.Context, nowMS int64) ([]DatabaseBackupConfig, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, server_id, databases, host, port, username, password_enc,
		        interval_hours, retention, enabled, last_run_at, next_run_at, last_error, created_at, updated_at
		   FROM database_backup_configs
		  WHERE enabled = 1 AND next_run_at <= ? ORDER BY next_run_at ASC`, nowMS)
	if err != nil {
		return nil, fmt.Errorf("查询到期数据库备份: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]DatabaseBackupConfig, 0, 4)
	for rows.Next() {
		cfg, err := scanDatabaseBackupConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *cfg)
	}
	return out, rows.Err()
}

func (s *Store) MarkDatabaseBackupRun(ctx context.Context, id string, lastRunAt, nextRunAt int64, lastError string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE database_backup_configs SET last_run_at=?, next_run_at=?, last_error=?, updated_at=? WHERE id=?`,
		lastRunAt, nextRunAt, nullableStr(&lastError), NowMS(), id)
	return err
}

func scanDatabaseBackupConfig(row interface{ Scan(...any) error }) (*DatabaseBackupConfig, error) {
	var (
		cfg       DatabaseBackupConfig
		databases string
		enabled   int
		lastRun   sql.NullInt64
		lastErr   sql.NullString
	)
	if err := row.Scan(&cfg.ID, &cfg.ServerID, &databases, &cfg.Host, &cfg.Port, &cfg.Username,
		&cfg.PasswordEnc, &cfg.IntervalHours, &cfg.Retention, &enabled, &lastRun,
		&cfg.NextRunAt, &lastErr, &cfg.CreatedAt, &cfg.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("扫描数据库备份配置: %w", err)
	}
	_ = json.Unmarshal([]byte(databases), &cfg.Databases)
	cfg.Enabled = enabled == 1
	cfg.LastRunAt = intPtr(lastRun)
	cfg.LastError = strPtr(lastErr)
	return &cfg, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullableIntValue(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
