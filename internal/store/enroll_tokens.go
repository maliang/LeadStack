package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CreateEnrollToken 插入一枚接入令牌（只存哈希）。
func (s *Store) CreateEnrollToken(ctx context.Context, t *EnrollToken) error {
	if t == nil {
		return errors.New("创建接入令牌: 参数为空")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO enroll_tokens (id, token_hash, note, expires_at, used_at, server_id)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		t.ID, t.TokenHash, t.Note, t.ExpiresAt, nullableInt(t.UsedAt), nullableStr(t.ServerID),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("创建接入令牌: %w", ErrConflict)
		}
		return fmt.Errorf("创建接入令牌: %w", err)
	}
	return nil
}

// ListEnrollTokens 返回最近创建的接入令牌列表。
func (s *Store) ListEnrollTokens(ctx context.Context, limit int) ([]EnrollToken, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, token_hash, COALESCE(note, ''), expires_at, used_at, server_id
		   FROM enroll_tokens ORDER BY expires_at DESC, id DESC LIMIT ?`,
		limit)
	if err != nil {
		return nil, fmt.Errorf("查询接入令牌列表: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]EnrollToken, 0, 8)
	for rows.Next() {
		var (
			t        EnrollToken
			usedAt   sql.NullInt64
			serverID sql.NullString
		)
		if err := rows.Scan(&t.ID, &t.TokenHash, &t.Note, &t.ExpiresAt, &usedAt, &serverID); err != nil {
			return nil, fmt.Errorf("扫描接入令牌: %w", err)
		}
		t.UsedAt = intPtr(usedAt)
		t.ServerID = strPtr(serverID)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历接入令牌列表: %w", err)
	}
	return out, nil
}

// FindEnrollTokenByHash 按哈希查询令牌；不存在返回 ErrNotFound，已过期返回 ErrExpired。
func (s *Store) FindEnrollTokenByHash(ctx context.Context, tokenHash string, nowMS int64) (*EnrollToken, error) {
	var (
		t        EnrollToken
		usedAt   sql.NullInt64
		serverID sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, token_hash, COALESCE(note, ''), expires_at, used_at, server_id
		   FROM enroll_tokens WHERE token_hash = ?`,
		tokenHash,
	).Scan(&t.ID, &t.TokenHash, &t.Note, &t.ExpiresAt, &usedAt, &serverID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询接入令牌: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("查询接入令牌: %w", err)
	}
	t.UsedAt = intPtr(usedAt)
	t.ServerID = strPtr(serverID)
	if t.ExpiresAt <= nowMS {
		return nil, fmt.Errorf("接入令牌 %s: %w", t.ID, ErrExpired)
	}
	return &t, nil
}

// BindEnrollToken 记录令牌首次使用时间并绑定服务器。
//
// used_at 只在首次写入（COALESCE 保留原值），这样 agent 重连时仍可用同一令牌回到原服务器。
func (s *Store) BindEnrollToken(ctx context.Context, tokenID, serverID string, usedAt int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE enroll_tokens
		    SET used_at = COALESCE(used_at, ?), server_id = ?
		  WHERE id = ?`,
		usedAt, serverID, tokenID)
	if err != nil {
		return fmt.Errorf("绑定接入令牌 %s: %w", tokenID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("绑定接入令牌 %s 后读取影响行数: %w", tokenID, err)
	}
	if affected == 0 {
		return fmt.Errorf("绑定接入令牌 %s: %w", tokenID, ErrNotFound)
	}
	return nil
}

// DeleteExpiredEnrollTokens 清理已过期且未被使用的令牌，返回删除条数。
func (s *Store) DeleteExpiredEnrollTokens(ctx context.Context, nowMS int64) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM enroll_tokens WHERE expires_at <= ? AND used_at IS NULL`, nowMS)
	if err != nil {
		return 0, fmt.Errorf("清理过期接入令牌: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("清理过期接入令牌后读取影响行数: %w", err)
	}
	return n, nil
}
