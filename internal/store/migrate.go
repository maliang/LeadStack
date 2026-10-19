package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migration 是一条待执行的迁移。
type migration struct {
	version int
	name    string
	sql     string
}

// Migrate 按 version 升序执行尚未应用的迁移。
//
// 每条迁移在**单独一个事务**内执行：迁移脚本与 schema_migrations 的写入同生共死，
// 保证「要么整体生效、要么完全没发生」。
func (s *Store) Migrate(ctx context.Context) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if len(migrations) == 0 {
		return nil
	}

	applied, err := s.appliedVersions(ctx)
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if _, ok := applied[m.version]; ok {
			continue
		}
		if err := s.applyMigration(ctx, m); err != nil {
			return err
		}
		slog.Info("数据库迁移已应用", "version", m.version, "name", m.name)
	}
	return nil
}

// applyMigration 在单事务内执行一条迁移并登记版本号。
func (s *Store) applyMigration(ctx context.Context, m migration) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("执行迁移 %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			m.version, NowMS(),
		); err != nil {
			return fmt.Errorf("登记迁移版本 %d: %w", m.version, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("应用迁移 %s: %w", m.name, err)
	}
	return nil
}

// appliedVersions 读取已应用的迁移版本集合。
func (s *Store) appliedVersions(ctx context.Context) (map[int]struct{}, error) {
	// schema_migrations 由 0001 自身创建，因此首次运行时表可能还不存在。
	var exists int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`,
	).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("检查 schema_migrations 表: %w", err)
	}
	if exists == 0 {
		return map[int]struct{}{}, nil
	}

	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("读取已应用迁移: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			slog.Warn("关闭查询结果失败", "err", cerr)
		}
	}()

	applied := make(map[int]struct{})
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("扫描迁移版本号: %w", err)
		}
		applied[v] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历迁移版本号: %w", err)
	}
	return applied, nil
}

// loadMigrations 从内嵌文件系统读取并解析全部迁移。
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("读取内嵌迁移目录: %w", err)
	}

	out := make([]migration, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, err := parseVersion(e.Name())
		if err != nil {
			return nil, err
		}
		content, err := migrationsFS.ReadFile(path.Join("migrations", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("读取迁移 %s: %w", e.Name(), err)
		}
		out = append(out, migration{version: version, name: e.Name(), sql: string(content)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("迁移版本号重复: %d", out[i].version)
		}
	}
	return out, nil
}

// parseVersion 从 "0001_init.sql" 解析出 1。
func parseVersion(name string) (int, error) {
	base := strings.TrimSuffix(name, ".sql")
	idx := strings.Index(base, "_")
	if idx <= 0 {
		return 0, fmt.Errorf("迁移文件名不符合 <version>_<name>.sql 规范: %s", name)
	}
	v, err := strconv.Atoi(base[:idx])
	if err != nil {
		return 0, fmt.Errorf("解析迁移版本号 %s: %w", name, err)
	}
	if v <= 0 {
		return 0, fmt.Errorf("迁移版本号必须为正数: %s", name)
	}
	return v, nil
}
