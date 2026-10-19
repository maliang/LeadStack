package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// serverColumns 是 servers 表的固定列顺序，查询与扫描共用，避免两边字段错位。
// "group" 是 SQLite 关键字，必须加双引号。
const serverColumns = `id, name, host, port, mode, tags, status, last_seen_at,
	agent_version, os, arch, kernel, created_at, credential_id, "group", host_key_policy, last_error`

// CreateServer 插入一台服务器。调用方负责填好 ID/CreatedAt。
func (s *Store) CreateServer(ctx context.Context, srv *Server) error {
	if srv == nil {
		return errors.New("创建服务器: 参数为空")
	}
	if srv.TagsJSON == "" {
		srv.TagsJSON = "[]"
	}
	if srv.Mode == "" {
		srv.Mode = "agent"
	}
	if srv.Status == "" {
		srv.Status = "unknown"
	}
	if srv.HostKeyPolicy == "" {
		srv.HostKeyPolicy = HostKeyPolicyAcceptNew
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO servers (`+serverColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		srv.ID, srv.Name, srv.Host, srv.Port, srv.Mode, srv.TagsJSON, srv.Status,
		nullableInt(srv.LastSeenAt), nullableStr(srv.AgentVersion), nullableStr(srv.OS),
		nullableStr(srv.Arch), nullableStr(srv.Kernel), srv.CreatedAt,
		nullableStr(srv.CredentialID), nullableStr(srv.Group), srv.HostKeyPolicy,
		nullableStr(srv.LastError),
	)
	if err != nil {
		return fmt.Errorf("创建服务器 %s: %w", srv.Name, err)
	}
	return nil
}

// ListServers 返回全部服务器，按创建时间倒序。
func (s *Store) ListServers(ctx context.Context) ([]Server, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+serverColumns+` FROM servers ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("查询服务器列表: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Server, 0, 8)
	for rows.Next() {
		srv, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *srv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历服务器列表: %w", err)
	}
	return out, nil
}

// GetServer 按 ID 查询服务器，不存在返回 ErrNotFound。
func (s *Store) GetServer(ctx context.Context, id string) (*Server, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+serverColumns+` FROM servers WHERE id = ?`, id)
	srv, err := scanServer(row)
	if err != nil {
		return nil, err
	}
	return srv, nil
}

// ServerUpdate 描述 PATCH 语义的服务器字段更新；nil 表示不修改。
//
// Group/CredentialID 传空字符串表示清空该字段（写 NULL）。
type ServerUpdate struct {
	Name          *string
	TagsJSON      *string
	Group         *string
	HostKeyPolicy *string
	CredentialID  *string
}

// UpdateServerFields 按 PATCH 语义更新服务器字段，返回更新后的记录。
func (s *Store) UpdateServerFields(ctx context.Context, id string, upd ServerUpdate) (*Server, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		type setClause struct {
			sql   string
			value any
		}
		sets := make([]setClause, 0, 5)
		if upd.Name != nil {
			sets = append(sets, setClause{`name = ?`, *upd.Name})
		}
		if upd.TagsJSON != nil {
			sets = append(sets, setClause{`tags = ?`, *upd.TagsJSON})
		}
		if upd.Group != nil {
			sets = append(sets, setClause{`"group" = ?`, nullableStr(emptyToNil(*upd.Group))})
		}
		if upd.HostKeyPolicy != nil {
			sets = append(sets, setClause{`host_key_policy = ?`, *upd.HostKeyPolicy})
		}
		if upd.CredentialID != nil {
			sets = append(sets, setClause{`credential_id = ?`, nullableStr(emptyToNil(*upd.CredentialID))})
		}
		if len(sets) == 0 {
			return nil
		}

		query := `UPDATE servers SET `
		args := make([]any, 0, len(sets)+1)
		for i, sc := range sets {
			if i > 0 {
				query += `, `
			}
			query += sc.sql
			args = append(args, sc.value)
		}
		query += ` WHERE id = ?`
		args = append(args, id)

		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("更新服务器 %s: %w", id, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("更新服务器 %s 后读取影响行数: %w", id, err)
		}
		if affected == 0 {
			// 字段值与旧值相同时也可能为 0，这里再确认一次记录是否存在。
			var exists int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers WHERE id = ?`, id).Scan(&exists); err != nil {
				return fmt.Errorf("确认服务器 %s 是否存在: %w", id, err)
			}
			if exists == 0 {
				return fmt.Errorf("更新服务器 %s: %w", id, ErrNotFound)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetServer(ctx, id)
}

// DeleteServer 删除服务器，并清理其指标、事件、主机信息与汇总，同时解绑接入令牌。
func (s *Store) DeleteServer(ctx context.Context, id string) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM servers WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("删除服务器 %s: %w", id, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("删除服务器 %s 后读取影响行数: %w", id, err)
		}
		if affected == 0 {
			return fmt.Errorf("删除服务器 %s: %w", id, ErrNotFound)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM metrics_samples WHERE server_id = ?`, id); err != nil {
			return fmt.Errorf("清理服务器 %s 的指标: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM metrics_rollup WHERE server_id = ?`, id); err != nil {
			return fmt.Errorf("清理服务器 %s 的汇总指标: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM server_host_info WHERE server_id = ?`, id); err != nil {
			return fmt.Errorf("清理服务器 %s 的主机信息: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE server_id = ?`, id); err != nil {
			return fmt.Errorf("清理服务器 %s 的事件: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE enroll_tokens SET server_id = NULL WHERE server_id = ?`, id); err != nil {
			return fmt.Errorf("解绑服务器 %s 的接入令牌: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("删除服务器 %s: %w", id, err)
	}
	return nil
}

// ServerIdentity 描述 agent hello 上报的身份信息。
type ServerIdentity struct {
	AgentVersion string
	OS           string
	Arch         string
	Kernel       string
	// Hostname 仅在服务器名为空时用于兜底。
	Hostname string
}

// ApplyServerIdentity 写入 agent 上报的身份信息，并把 host 兜底填成来源地址。
func (s *Store) ApplyServerIdentity(ctx context.Context, id, remoteHost string, ident ServerIdentity) error {
	err := s.db.QueryRowContext(ctx,
		`UPDATE servers
		    SET agent_version = ?,
		        os = ?,
		        arch = ?,
		        kernel = ?,
		        name = CASE WHEN name IS NULL OR name = '' THEN ? ELSE name END,
		        host = CASE WHEN host IS NULL OR host = '' THEN ? ELSE host END
		  WHERE id = ?
		  RETURNING id`,
		ident.AgentVersion, ident.OS, ident.Arch, ident.Kernel, ident.Hostname, remoteHost, id,
	).Scan(new(string))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("更新服务器 %s 身份信息: %w", id, ErrNotFound)
		}
		return fmt.Errorf("更新服务器 %s 身份信息: %w", id, err)
	}
	return nil
}

// TouchServerHeartbeat 更新最后心跳时间与状态，返回更新前的状态。
//
// 返回旧状态是为了让调用方只在「离线/未知 → 在线」的跃迁上写事件，避免每 20 秒刷一条事件。
func (s *Store) TouchServerHeartbeat(ctx context.Context, id string, ts int64) (string, error) {
	var prev string
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT status FROM servers WHERE id = ?`, id).Scan(&prev); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("查询服务器 %s 状态: %w", id, ErrNotFound)
			}
			return fmt.Errorf("查询服务器 %s 状态: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE servers SET last_seen_at = ?, status = ? WHERE id = ?`, ts, "online", id); err != nil {
			return fmt.Errorf("更新服务器 %s 心跳: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("更新服务器 %s 心跳: %w", id, err)
	}
	return prev, nil
}

// MarkStaleServersOffline 把超过 offlineBefore（Unix 毫秒）未心跳的在线服务器标记为离线，
// 返回被标记的服务器 ID 列表。
func (s *Store) MarkStaleServersOffline(ctx context.Context, offlineBefore int64) ([]string, error) {
	var ids []string
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		// 只对 agent 模式生效：ssh 模式服务器的在线状态由 collector 按「连续 3 次失败」裁决。
		rows, err := tx.QueryContext(ctx,
			`SELECT id FROM servers WHERE status = ? AND mode = ? AND (last_seen_at IS NULL OR last_seen_at < ?)`,
			"online", ModeAgent, offlineBefore)
		if err != nil {
			return fmt.Errorf("查询超时未心跳的服务器: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return fmt.Errorf("扫描超时服务器 ID: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("遍历超时服务器: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("关闭超时服务器查询: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE servers SET status = ? WHERE id = ?`, "offline", id); err != nil {
				return fmt.Errorf("标记服务器 %s 离线: %w", id, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// CountServers 返回服务器总数与在线数。
//
// 在线判定用「最后心跳时间 >= onlineSince」实时推导，不依赖后台清扫的写库延迟。
func (s *Store) CountServers(ctx context.Context, onlineSince int64) (total, online int, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*),
		        COALESCE(SUM(CASE WHEN last_seen_at IS NOT NULL AND last_seen_at >= ? THEN 1 ELSE 0 END), 0)
		   FROM servers`,
		onlineSince,
	).Scan(&total, &online)
	if err != nil {
		return 0, 0, fmt.Errorf("统计服务器数量: %w", err)
	}
	return total, online, nil
}

// LatestMetrics 返回每台服务器每个指标的最新值：map[server_id]map[metric]value。
func (s *Store) LatestMetrics(ctx context.Context, serverIDs []string) (map[string]map[string]float64, error) {
	out := make(map[string]map[string]float64, len(serverIDs))
	if len(serverIDs) == 0 {
		return out, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(serverIDs)), ",")
	args := make([]any, 0, len(serverIDs))
	for _, id := range serverIDs {
		args = append(args, id)
	}

	query := `SELECT m.server_id, m.metric, m.value
	            FROM metrics_samples m
	           WHERE m.server_id IN (` + placeholders + `)
	             AND m.ts = (SELECT MAX(ts) FROM metrics_samples
	                          WHERE server_id = m.server_id AND metric = m.metric)`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询最新指标: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var serverID, metric string
		var value float64
		if err := rows.Scan(&serverID, &metric, &value); err != nil {
			return nil, fmt.Errorf("扫描最新指标: %w", err)
		}
		if out[serverID] == nil {
			out[serverID] = make(map[string]float64, 5)
		}
		out[serverID][metric] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历最新指标: %w", err)
	}
	return out, nil
}

// scanServer 把一行结果映射成 Server，兼容 *sql.Row 与 *sql.Rows。
func scanServer(row interface{ Scan(...any) error }) (*Server, error) {
	var (
		srv      Server
		lastSeen sql.NullInt64
		ver      sql.NullString
		osName   sql.NullString
		arch     sql.NullString
		kernel   sql.NullString
		credID   sql.NullString
		group    sql.NullString
		policy   sql.NullString
		lastErr  sql.NullString
	)
	err := row.Scan(&srv.ID, &srv.Name, &srv.Host, &srv.Port, &srv.Mode, &srv.TagsJSON,
		&srv.Status, &lastSeen, &ver, &osName, &arch, &kernel, &srv.CreatedAt,
		&credID, &group, &policy, &lastErr)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询服务器: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描服务器记录: %w", err)
	}
	srv.LastSeenAt = intPtr(lastSeen)
	srv.AgentVersion = strPtr(ver)
	srv.OS = strPtr(osName)
	srv.Arch = strPtr(arch)
	srv.Kernel = strPtr(kernel)
	srv.CredentialID = strPtr(credID)
	srv.Group = strPtr(group)
	srv.LastError = strPtr(lastErr)
	srv.HostKeyPolicy = HostKeyPolicyAcceptNew
	if policy.Valid && policy.String != "" {
		srv.HostKeyPolicy = policy.String
	}
	return &srv, nil
}

// nullableInt 把可选时间戳转成可绑定的参数。
func nullableInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// nullableStr 把可选字符串转成可绑定的参数。
func nullableStr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// emptyToNil 把空字符串（含纯空白）转成 nil，用于「清空该字段」的语义。
func emptyToNil(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	v := strings.TrimSpace(s)
	return &v
}

// intPtr 把 sql.NullInt64 转成 *int64。
func intPtr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// strPtr 把 sql.NullString 转成 *string。
func strPtr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}
