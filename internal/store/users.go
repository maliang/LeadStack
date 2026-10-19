package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CreateUser 插入一个用户；用户名重复返回 ErrConflict。
func (s *Store) CreateUser(ctx context.Context, u *User) error {
	if u == nil {
		return errors.New("创建用户: 参数为空")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, role, created_at) VALUES (?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, u.Role, u.CreatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("创建用户 %s: %w", u.Username, ErrConflict)
		}
		return fmt.Errorf("创建用户 %s: %w", u.Username, err)
	}
	return nil
}

// GetUserByUsername 按用户名查询用户。
func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, created_at FROM users WHERE username = ?`,
		username,
	))
}

// GetUserByID 按主键查询用户。
func (s *Store) GetUserByID(ctx context.Context, id string) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, created_at FROM users WHERE id = ?`,
		id,
	))
}

// CountUsers 返回用户总数，用于判断是否需要创建初始管理员。
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计用户数: %w", err)
	}
	return n, nil
}

// UpdateUserPassword 更新密码哈希。
func (s *Store) UpdateUserPassword(ctx context.Context, id, passwordHash string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, id)
	if err != nil {
		return fmt.Errorf("更新用户 %s 密码: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("更新用户 %s 密码后读取影响行数: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("更新用户 %s 密码: %w", id, ErrNotFound)
	}
	return nil
}

// scanUser 把一行查询结果映射为 User。
func (s *Store) scanUser(row *sql.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询用户: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描用户记录: %w", err)
	}
	return &u, nil
}
