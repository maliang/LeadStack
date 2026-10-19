package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GetSetting 读取配置项；第二个返回值为 false 表示键不存在。
func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("读取配置项 %s: %w", key, err)
	}
	return value, true, nil
}

// SetSetting 写入配置项（存在则覆盖）。
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	if err != nil {
		return fmt.Errorf("写入配置项 %s: %w", key, err)
	}
	return nil
}

// EnsureSetting 保证配置项存在：不存在时调用 gen 生成并落库，随后返回库内最终值。
//
// 采用 INSERT ... ON CONFLICT DO NOTHING 后再读回，避免并发启动时后写者覆盖先写者
// （例如两个进程同时生成 JWT 密钥）。
func (s *Store) EnsureSetting(ctx context.Context, key string, gen func() (string, error)) (string, error) {
	if key == "" {
		return "", errors.New("确保配置项: key 为空")
	}
	if v, ok, err := s.GetSetting(ctx, key); err != nil {
		return "", err
	} else if ok && v != "" {
		return v, nil
	}

	generated, err := gen()
	if err != nil {
		return "", fmt.Errorf("生成配置项 %s: %w", key, err)
	}
	if generated == "" {
		return "", fmt.Errorf("生成配置项 %s: 结果为空", key)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`,
		key, generated); err != nil {
		return "", fmt.Errorf("写入配置项 %s: %w", key, err)
	}

	final, ok, err := s.GetSetting(ctx, key)
	if err != nil {
		return "", err
	}
	if !ok || final == "" {
		return "", fmt.Errorf("配置项 %s 写入后仍不存在", key)
	}
	return final, nil
}
