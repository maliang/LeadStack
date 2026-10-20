package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
)

// sqliteDriverName 是 modernc.org/sqlite 注册的驱动名。
const sqliteDriverName = "sqlite"

// errOnlineBackupUnsupported 表示驱动未暴露 SQLite Online Backup API，需要回退到 VACUUM INTO。
var errOnlineBackupUnsupported = errors.New("backup: 驱动未暴露 SQLite 在线备份 API")

// sqliteRODSN 构造只读 DSN（带 busy_timeout 与 query_only），避免备份过程意外写库。
func sqliteRODSN(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("backup: 解析数据库路径 %s 失败: %w", path, err)
	}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "query_only(1)")
	u := url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}
	return u.String(), nil
}

// sqliteRWDSN 构造仅用于 VACUUM INTO 回退路径的读写 DSN。
func sqliteRWDSN(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("backup: 解析数据库路径 %s 失败: %w", path, err)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	u := url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}
	return u.String(), nil
}

// sqlQuote 把字符串转成 SQL 单引号字面量（VACUUM INTO 的目标路径无法参数化）。
func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// onlineBackupConn 是 modernc.org/sqlite 驱动连接暴露的在线备份入口。
// modernc 的 *conn 未导出，但实现了该方法；通过 Raw + 接口断言使用。
type onlineBackupConn interface {
	NewBackup(dstURI string) (*sqlite.Backup, error)
}

// snapshotDatabase 把在线数据库导出为一致快照，返回快照中的最大迁移版本。
//
// 这是本包最关键的约束：WAL 模式下直接拷贝 .db 文件会得到不一致的库。
// 实现优先使用 SQLite Online Backup API（sqlite3_backup_*）：
//   - 源连接严格只读（mode=ro + query_only），不会写业务库；
//   - 分页逐步复制，可响应 context 取消；
//   - 与 `VACUUM INTO` 同样保证快照一致，但不要求源连接可写
//     （实测 `VACUUM INTO` 在只读连接上会报 "attempt to write a readonly database"）。
//
// 仅当驱动版本不暴露 Backup API 时才回退到读写连接上的 `VACUUM INTO`。
func snapshotDatabase(ctx context.Context, dbPath, destPath string) (int, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return 0, fmt.Errorf("backup: 读取数据库 %s 失败（数据库不存在或不可读）: %w", dbPath, err)
	}
	if err := snapshotViaOnlineBackup(ctx, dbPath, destPath); err != nil {
		if !errors.Is(err, errOnlineBackupUnsupported) {
			return 0, err
		}
		if fallbackErr := snapshotViaVacuumInto(ctx, dbPath, destPath); fallbackErr != nil {
			return 0, fallbackErr
		}
	}
	if _, err := os.Stat(destPath); err != nil {
		return 0, fmt.Errorf("backup: 快照文件不存在: %w", err)
	}
	return readSchemaVersion(ctx, destPath)
}

// snapshotViaOnlineBackup 用 SQLite Online Backup API 把只读源库复制到 destPath。
func snapshotViaOnlineBackup(ctx context.Context, dbPath, destPath string) error {
	dsn, err := sqliteRODSN(dbPath)
	if err != nil {
		return err
	}
	db, err := sql.Open(sqliteDriverName, dsn)
	if err != nil {
		return fmt.Errorf("backup: 打开数据库 %s 失败: %w", dbPath, err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("backup: 获取数据库连接失败: %w", err)
	}
	defer conn.Close()

	var backup *sqlite.Backup
	if err := conn.Raw(func(driverConn any) error {
		c, ok := driverConn.(onlineBackupConn)
		if !ok {
			return errOnlineBackupUnsupported
		}
		b, berr := c.NewBackup(destPath)
		if berr != nil {
			return fmt.Errorf("backup: 初始化 SQLite 在线备份失败: %w", berr)
		}
		backup = b
		return nil
	}); err != nil {
		return err
	}
	if backup == nil {
		return errOnlineBackupUnsupported
	}

	finished := false
	defer func() {
		if !finished {
			_ = backup.Finish()
		}
	}()

	// 每步最多复制 512 页（4KiB 页约 2MiB），既限制单次持锁时间，也能及时响应取消。
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		more, err := backup.Step(512)
		if err != nil {
			return fmt.Errorf("backup: SQLite 在线备份失败: %w", err)
		}
		if !more {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	finished = true
	if err := backup.Finish(); err != nil {
		return fmt.Errorf("backup: 完成 SQLite 在线备份失败: %w", err)
	}
	if _, err := os.Stat(destPath); err != nil {
		return fmt.Errorf("backup: 在线备份后快照文件不存在: %w", err)
	}
	return nil
}

// snapshotViaVacuumInto 是驱动不提供 Backup API 时的回退路径。
func snapshotViaVacuumInto(ctx context.Context, dbPath, destPath string) error {
	dsn, err := sqliteRWDSN(dbPath)
	if err != nil {
		return err
	}
	db, err := sql.Open(sqliteDriverName, dsn)
	if err != nil {
		return fmt.Errorf("backup: 打开数据库 %s 失败: %w", dbPath, err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "VACUUM INTO "+sqlQuote(destPath)); err != nil {
		return fmt.Errorf("backup: VACUUM INTO 快照失败: %w", err)
	}
	return nil
}

// readSchemaVersion 读取 schema_migrations 的最大版本；老库没有该表时返回 0。
func readSchemaVersion(ctx context.Context, dbPath string) (int, error) {
	dsn, err := sqliteRODSN(dbPath)
	if err != nil {
		return 0, err
	}
	db, err := sql.Open(sqliteDriverName, dsn)
	if err != nil {
		return 0, fmt.Errorf("backup: 打开快照 %s 失败: %w", dbPath, err)
	}
	defer db.Close()

	var version int
	err = db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version)
	if err != nil {
		if isNoSuchTable(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("backup: 读取 schema 版本失败: %w", err)
	}
	return version, nil
}

// checkDatabaseIntegrity 对数据库执行 `PRAGMA integrity_check`。
func checkDatabaseIntegrity(ctx context.Context, dbPath string) error {
	dsn, err := sqliteRODSN(dbPath)
	if err != nil {
		return err
	}
	db, err := sql.Open(sqliteDriverName, dsn)
	if err != nil {
		return fmt.Errorf("backup: 打开待校验数据库 %s 失败: %w", dbPath, err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("%w: SQLite integrity_check 执行失败: %v", ErrCorrupt, err)
	}
	defer rows.Close()

	var problems []string
	for rows.Next() {
		var msg string
		if err := rows.Scan(&msg); err != nil {
			return fmt.Errorf("%w: 读取 integrity_check 结果失败: %v", ErrCorrupt, err)
		}
		if !strings.EqualFold(strings.TrimSpace(msg), "ok") {
			problems = append(problems, msg)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: integrity_check 中断: %v", ErrCorrupt, err)
	}
	if len(problems) > 0 {
		if len(problems) > 3 {
			problems = problems[:3]
		}
		return fmt.Errorf("%w: SQLite integrity_check 失败: %s", ErrCorrupt, strings.Join(problems, "; "))
	}
	return nil
}

func isNoSuchTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// dbFileName 返回数据目录中的数据库路径。
func dbFileName(dataDir string) string {
	return filepath.Join(dataDir, DBFileName)
}

// mustAbs 返回绝对路径；空字符串返回空。
func mustAbs(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("backup: 路径为空")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("backup: 解析路径 %s 失败: %w", path, err)
	}
	return abs, nil
}
