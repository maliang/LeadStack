// Package store 提供控制端的 SQLite 持久化：迁移、查询与批量写入。
//
// 设计要点（见 docs/ARCHITECTURE.md 第 2、5 节）：
//   - 单文件 SQLite，WAL 模式；
//   - 只保留一个写连接（SetMaxOpenConns(1)），避免 SQLITE_BUSY；
//   - 所有 SQL 参数化，禁止字符串拼接值；
//   - 时间戳统一使用 Unix 毫秒整数。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	// 纯 Go 的 SQLite 驱动，CGO_ENABLED=0 下可用。
	_ "modernc.org/sqlite"
)

// 驱动名，modernc.org/sqlite 注册的名字为 "sqlite"。
const driverName = "sqlite"

// 通用错误。调用方用 errors.Is 判断。
var (
	// ErrNotFound 表示目标记录不存在。
	ErrNotFound = errors.New("记录不存在")
	// ErrConflict 表示唯一约束冲突（例如用户名重复）。
	ErrConflict = errors.New("记录冲突")
	// ErrExpired 表示接入令牌已过期。
	ErrExpired = errors.New("接入令牌已过期")
)

// Store 封装数据库句柄。
type Store struct {
	db   *sql.DB
	path string
}

// Open 打开（必要时创建）数据库，并设置 WAL、busy_timeout 与单写连接。
func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("打开数据库: 数据库路径为空")
	}
	dsn, err := buildDSN(path)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库 %s: %w", path, err)
	}

	// 单写模型：SQLite 同时只允许一个写事务，串行化连接可避免 SQLITE_BUSY。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	// 立刻验证连接可用，避免把错误推迟到第一次查询。
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("连接数据库 %s: %w", path, err)
	}

	// 再次显式确认关键 PRAGMA（DSN 已设置，这里做防御性校验）。
	if err := verifyPragmas(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Store{db: db, path: path}, nil
}

// buildDSN 构造 modernc.org/sqlite 的 DSN，附带 WAL 与超时参数。
func buildDSN(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("解析数据库路径 %s: %w", path, err)
	}
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	// file: 前缀 + 转义，避免路径中的特殊字符被当作 URI 参数。
	u := url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}
	return u.String(), nil
}

// verifyPragmas 校验 WAL 与 busy_timeout 已生效。
func verifyPragmas(ctx context.Context, db *sql.DB) error {
	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("读取 PRAGMA journal_mode: %w", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("数据库未启用 WAL 模式，当前为 %q", journalMode)
	}
	var busyTimeout int
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		return fmt.Errorf("读取 PRAGMA busy_timeout: %w", err)
	}
	if busyTimeout <= 0 {
		return fmt.Errorf("数据库 busy_timeout 未生效，当前为 %d", busyTimeout)
	}
	return nil
}

// DB 返回底层句柄，仅供同模块的高级用例（例如测试）使用。
func (s *Store) DB() *sql.DB { return s.db }

// Path 返回数据库文件路径。
func (s *Store) Path() string { return s.path }

// Close 关闭数据库。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("关闭数据库 %s: %w", s.path, err)
	}
	return nil
}

// inTx 在单个事务里执行 fn，出错自动回滚。
func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return fmt.Errorf("%w（回滚同样失败: %v）", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务: %w", err)
	}
	return nil
}

// isUniqueViolation 判断错误是否为唯一约束冲突。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "constraint failed")
}

// NowMS 返回当前 Unix 毫秒时间戳。
func NowMS() int64 { return time.Now().UnixMilli() }
