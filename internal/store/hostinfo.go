package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// UpsertServerHostInfo 写入/覆盖服务器的主机信息快照。
func (s *Store) UpsertServerHostInfo(ctx context.Context, serverID, infoJSON string, collectedAt int64) error {
	if strings.TrimSpace(serverID) == "" {
		return errors.New("写入主机信息: server_id 为空")
	}
	if strings.TrimSpace(infoJSON) == "" {
		return errors.New("写入主机信息: info_json 为空")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO server_host_info (server_id, info_json, collected_at) VALUES (?, ?, ?)
		 ON CONFLICT(server_id) DO UPDATE SET info_json = excluded.info_json,
		                                      collected_at = excluded.collected_at`,
		serverID, infoJSON, collectedAt)
	if err != nil {
		return fmt.Errorf("写入服务器 %s 的主机信息: %w", serverID, err)
	}
	return nil
}

// GetServerHostInfo 读取服务器的主机信息快照；不存在返回 ErrNotFound。
func (s *Store) GetServerHostInfo(ctx context.Context, serverID string) (*HostInfoSnapshot, error) {
	var snap HostInfoSnapshot
	err := s.db.QueryRowContext(ctx,
		`SELECT server_id, info_json, collected_at FROM server_host_info WHERE server_id = ?`,
		serverID).Scan(&snap.ServerID, &snap.InfoJSON, &snap.CollectedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询服务器 %s 的主机信息: %w", serverID, ErrNotFound)
		}
		return nil, fmt.Errorf("查询服务器 %s 的主机信息: %w", serverID, err)
	}
	return &snap, nil
}

// ListServerHostInfo 批量读取主机信息；serverIDs 为空时返回全部。
func (s *Store) ListServerHostInfo(ctx context.Context, serverIDs []string) (map[string]HostInfoSnapshot, error) {
	query := `SELECT server_id, info_json, collected_at FROM server_host_info`
	args := make([]any, 0, len(serverIDs))
	if len(serverIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(serverIDs)), ",")
		query += ` WHERE server_id IN (` + placeholders + `)`
		for _, id := range serverIDs {
			args = append(args, id)
		}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("批量查询主机信息: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]HostInfoSnapshot, len(serverIDs))
	for rows.Next() {
		var snap HostInfoSnapshot
		if err := rows.Scan(&snap.ServerID, &snap.InfoJSON, &snap.CollectedAt); err != nil {
			return nil, fmt.Errorf("扫描主机信息: %w", err)
		}
		out[snap.ServerID] = snap
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历主机信息: %w", err)
	}
	return out, nil
}
