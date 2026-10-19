package store

import (
	"context"
	"database/sql"
	"fmt"
)

// InsertEvent 写入一条事件记录。
func (s *Store) InsertEvent(ctx context.Context, e *Event) error {
	if e == nil {
		return fmt.Errorf("写入事件: 参数为空")
	}
	if e.Kind == "" {
		return fmt.Errorf("写入事件: kind 为空")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO events (id, server_id, kind, message, ts) VALUES (?, ?, ?, ?, ?)`,
		e.ID, nullableStr(e.ServerID), e.Kind, e.Message, e.TS)
	if err != nil {
		return fmt.Errorf("写入事件 %s: %w", e.Kind, err)
	}
	return nil
}

// ListEvents 返回最近的事件；serverID 为空字符串时返回全部服务器的事件。
func (s *Store) ListEvents(ctx context.Context, serverID string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT id, server_id, kind, COALESCE(message, ''), ts FROM events`
	args := make([]any, 0, 2)
	if serverID != "" {
		query += ` WHERE server_id = ?`
		args = append(args, serverID)
	}
	query += ` ORDER BY ts DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询事件列表: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Event, 0, 16)
	for rows.Next() {
		var (
			e        Event
			serverID sql.NullString
		)
		if err := rows.Scan(&e.ID, &serverID, &e.Kind, &e.Message, &e.TS); err != nil {
			return nil, fmt.Errorf("扫描事件: %w", err)
		}
		e.ServerID = strPtr(serverID)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历事件列表: %w", err)
	}
	return out, nil
}
