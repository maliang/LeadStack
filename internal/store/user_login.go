package store

import (
	"context"
	"database/sql"
	"fmt"
)

// RecordLoginFailure 记一次登录失败，必要时锁定账号（契约 §54）。
//
//   - 已锁定且未到期：不改动，直接返回当前状态；
//   - 锁定期已过：失败计数从 0 重新算（冷却后重新给机会）；
//   - 达到阈值：写入 locked_until（保留失败计数，便于 UI 展示）。
func (s *Store) RecordLoginFailure(ctx context.Context, userID string, nowMS int64, maxAttempts int, lockDurationMS int64) (int, int64, error) {
	var (
		failed      int
		lockedUntil int64
	)
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx,
			`SELECT failed_attempts, locked_until FROM users WHERE id = ?`, userID).
			Scan(&failed, &lockedUntil); err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("用户 %s: %w", userID, ErrNotFound)
			}
			return fmt.Errorf("读取用户 %s 登录状态: %w", userID, err)
		}
		if lockedUntil > nowMS {
			return nil
		}
		if lockedUntil != 0 && lockedUntil <= nowMS {
			failed = 0
			lockedUntil = 0
		}
		if maxAttempts <= 0 {
			// 保护关闭：只记录时间戳，不累加锁定。
			if _, err := tx.ExecContext(ctx,
				`UPDATE users SET last_failed_at = ? WHERE id = ?`, nowMS, userID); err != nil {
				return fmt.Errorf("记录用户 %s 登录失败时间: %w", userID, err)
			}
			return nil
		}
		failed++
		if failed >= maxAttempts {
			lockedUntil = nowMS + lockDurationMS
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET failed_attempts = ?, locked_until = ?, last_failed_at = ? WHERE id = ?`,
			failed, lockedUntil, nowMS, userID); err != nil {
			return fmt.Errorf("更新用户 %s 登录失败状态: %w", userID, err)
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return failed, lockedUntil, nil
}

// ResetLoginState 登录成功后清零失败计数与锁定。
func (s *Store) ResetLoginState(ctx context.Context, userID string, nowMS int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET failed_attempts = 0, locked_until = 0, updated_at = ? WHERE id = ?`,
		nowMS, userID); err != nil {
		return fmt.Errorf("重置用户 %s 登录状态: %w", userID, err)
	}
	return nil
}

// UnlockUser 管理员解锁：清零失败计数与锁定时间；返回是否命中用户。
func (s *Store) UnlockUser(ctx context.Context, userID string, nowMS int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET failed_attempts = 0, locked_until = 0, updated_at = ? WHERE id = ?`,
		nowMS, userID)
	if err != nil {
		return fmt.Errorf("解锁用户 %s: %w", userID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("解锁用户 %s 后读取影响行数: %w", userID, err)
	}
	if affected == 0 {
		return fmt.Errorf("解锁用户 %s: %w", userID, ErrNotFound)
	}
	return nil
}

// SetUserRole 更新用户角色（角色 key 由调用方校验）。
func (s *Store) SetUserRole(ctx context.Context, userID, role string, nowMS int64) error {
	_, err := s.UpdateUser(ctx, userID, UserUpdate{Role: &role}, nowMS)
	return err
}
