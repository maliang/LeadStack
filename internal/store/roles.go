package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Role 是一个自定义角色（内置角色不落库，由 auth 包常量提供）。
type Role struct {
	Key         string
	Name        string
	Description string
	Permissions []string
	Builtin     bool
	ScopeMode   string
	GroupIDs    []string
	ServerIDs   []string
	CreatedAt   int64
	UpdatedAt   int64
}

// roleColumns 是 roles 表的读取列。
const roleColumns = `key, name, description, permissions, builtin, scope_mode, created_at, updated_at`

// scanRole 把一行角色记录映射为 Role（不含范围，范围由 loadRoleScopes 补齐）。
func scanRole(scan func(dest ...any) error) (*Role, error) {
	var (
		r         Role
		permsJSON string
		builtin   int
		scopeMode string
		createdAt int64
		updatedAt int64
	)
	if err := scan(&r.Key, &r.Name, &r.Description, &permsJSON, &builtin, &scopeMode, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	r.Builtin = builtin != 0
	r.ScopeMode = scopeMode
	if r.ScopeMode == "" {
		r.ScopeMode = "all"
	}
	r.CreatedAt = createdAt
	r.UpdatedAt = updatedAt
	perms := make([]string, 0, 8)
	if strings.TrimSpace(permsJSON) != "" {
		if err := json.Unmarshal([]byte(permsJSON), &perms); err != nil {
			return nil, fmt.Errorf("解析角色 %s 权限: %w", r.Key, err)
		}
	}
	r.Permissions = perms
	r.GroupIDs = []string{}
	r.ServerIDs = []string{}
	return &r, nil
}

// loadRoleScopes 读取一个角色的范围目标（按 kind 分流）。
func (s *Store) loadRoleScopes(ctx context.Context, r *Role) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT kind, target_id FROM role_scopes WHERE role_key = ? ORDER BY kind ASC, target_id ASC`, r.Key)
	if err != nil {
		return fmt.Errorf("查询角色 %s 范围: %w", r.Key, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind, target string
		if err := rows.Scan(&kind, &target); err != nil {
			return fmt.Errorf("扫描角色 %s 范围: %w", r.Key, err)
		}
		switch kind {
		case "group":
			r.GroupIDs = append(r.GroupIDs, target)
		case "server":
			r.ServerIDs = append(r.ServerIDs, target)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历角色 %s 范围: %w", r.Key, err)
	}
	return nil
}

// ListRoles 返回全部自定义角色（含范围）。
func (s *Store) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+roleColumns+` FROM roles ORDER BY created_at ASC, key ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询角色列表: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Role, 0, 8)
	for rows.Next() {
		r, err := scanRole(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("扫描角色: %w", err)
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历角色列表: %w", err)
	}
	for i := range out {
		if err := s.loadRoleScopes(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// GetRole 按 key 查询自定义角色（含范围）；不存在返回 ErrNotFound。
func (s *Store) GetRole(ctx context.Context, key string) (*Role, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+roleColumns+` FROM roles WHERE key = ?`, key)
	r, err := scanRole(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询角色 %s: %w", key, ErrNotFound)
		}
		return nil, fmt.Errorf("扫描角色 %s: %w", key, err)
	}
	if err := s.loadRoleScopes(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

// CreateRole 插入一个自定义角色及其范围。
func (s *Store) CreateRole(ctx context.Context, r *Role) error {
	if r == nil {
		return errors.New("创建角色: 参数为空")
	}
	permsJSON, err := json.Marshal(r.Permissions)
	if err != nil {
		return fmt.Errorf("序列化角色权限: %w", err)
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO roles (key, name, description, permissions, builtin, scope_mode, created_at, updated_at)
			 VALUES (?, ?, ?, ?, 0, ?, ?, ?)`,
			r.Key, r.Name, r.Description, string(permsJSON), r.ScopeMode, r.CreatedAt, r.UpdatedAt)
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("角色 %s 已存在: %w", r.Name, ErrConflict)
			}
			return fmt.Errorf("创建角色 %s: %w", r.Name, err)
		}
		return replaceRoleScopesTx(ctx, tx, r.Key, r.ScopeMode, r.GroupIDs, r.ServerIDs)
	})
	if err != nil {
		return err
	}
	return nil
}

// UpdateRole 更新角色字段与范围（nil 表示不改）。返回更新后的角色。
func (s *Store) UpdateRole(ctx context.Context, key string, upd RoleUpdate, nowMS int64) (*Role, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		sets := make([]string, 0, 4)
		args := make([]any, 0, 6)
		if upd.Name != nil {
			sets = append(sets, "name = ?")
			args = append(args, *upd.Name)
		}
		if upd.Description != nil {
			sets = append(sets, "description = ?")
			args = append(args, *upd.Description)
		}
		if upd.Permissions != nil {
			permsJSON, err := json.Marshal(upd.Permissions)
			if err != nil {
				return fmt.Errorf("序列化角色权限: %w", err)
			}
			sets = append(sets, "permissions = ?")
			args = append(args, string(permsJSON))
		}
		if upd.ScopeMode != nil {
			sets = append(sets, "scope_mode = ?")
			args = append(args, *upd.ScopeMode)
		}
		if len(sets) > 0 {
			sets = append(sets, "updated_at = ?")
			args = append(args, nowMS)
			query := `UPDATE roles SET ` + strings.Join(sets, ", ") + ` WHERE key = ?`
			args = append(args, key)
			if _, err := tx.ExecContext(ctx, query, args...); err != nil {
				if isUniqueViolation(err) {
					return fmt.Errorf("角色 %s 的新名称已被占用: %w", key, ErrConflict)
				}
				return fmt.Errorf("更新角色 %s: %w", key, err)
			}
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM roles WHERE key = ?`, key).Scan(&exists); err != nil {
			return fmt.Errorf("确认角色 %s 是否存在: %w", key, err)
		}
		if exists == 0 {
			return fmt.Errorf("更新角色 %s: %w", key, ErrNotFound)
		}
		if upd.ScopeMode != nil || upd.GroupIDs != nil || upd.ServerIDs != nil {
			scopeMode := ""
			if upd.ScopeMode != nil {
				scopeMode = *upd.ScopeMode
			} else if err := tx.QueryRowContext(ctx,
				`SELECT scope_mode FROM roles WHERE key = ?`, key).Scan(&scopeMode); err != nil {
				return fmt.Errorf("读取角色 %s 范围模式: %w", key, err)
			}
			groupIDs := upd.GroupIDs
			serverIDs := upd.ServerIDs
			if groupIDs == nil || serverIDs == nil {
				var err error
				groupIDs, serverIDs, err = loadRoleScopesTx(ctx, tx, key)
				if err != nil {
					return err
				}
				if upd.GroupIDs != nil {
					groupIDs = upd.GroupIDs
				}
				if upd.ServerIDs != nil {
					serverIDs = upd.ServerIDs
				}
			}
			if err := replaceRoleScopesTx(ctx, tx, key, scopeMode, groupIDs, serverIDs); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetRole(ctx, key)
}

// RoleUpdate 是 PATCH 语义的角色更新（nil 表示不改）。
type RoleUpdate struct {
	Name        *string
	Description *string
	Permissions []string
	ScopeMode   *string
	GroupIDs    []string
	ServerIDs   []string
}

// DeleteRole 删除自定义角色；不存在返回 ErrNotFound。
func (s *Store) DeleteRole(ctx context.Context, key string) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM role_scopes WHERE role_key = ?`, key); err != nil {
			return fmt.Errorf("清理角色 %s 范围: %w", key, err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM roles WHERE key = ?`, key)
		if err != nil {
			return fmt.Errorf("删除角色 %s: %w", key, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("删除角色 %s 后读取影响行数: %w", key, err)
		}
		if affected == 0 {
			return fmt.Errorf("删除角色 %s: %w", key, ErrNotFound)
		}
		return nil
	})
	return err
}

// CountUsersByRole 统计引用该角色的用户数（删除保护用）。
func (s *Store) CountUsersByRole(ctx context.Context, key string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = ?`, key).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计角色 %s 用户数: %w", key, err)
	}
	return n, nil
}

// loadRoleScopesTx 在事务内读取角色范围（用于 PATCH 时只改一半字段的场景）。
func loadRoleScopesTx(ctx context.Context, tx *sql.Tx, key string) ([]string, []string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT kind, target_id FROM role_scopes WHERE role_key = ? ORDER BY kind ASC, target_id ASC`, key)
	if err != nil {
		return nil, nil, fmt.Errorf("查询角色 %s 范围: %w", key, err)
	}
	defer func() { _ = rows.Close() }()
	groupIDs := make([]string, 0, 4)
	serverIDs := make([]string, 0, 4)
	for rows.Next() {
		var kind, target string
		if err := rows.Scan(&kind, &target); err != nil {
			return nil, nil, fmt.Errorf("扫描角色 %s 范围: %w", key, err)
		}
		switch kind {
		case "group":
			groupIDs = append(groupIDs, target)
		case "server":
			serverIDs = append(serverIDs, target)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("遍历角色 %s 范围: %w", key, err)
	}
	return groupIDs, serverIDs, nil
}

// replaceRoleScopesTx 用给定集合整体替换角色范围；mode=all 时清空。
func replaceRoleScopesTx(ctx context.Context, tx *sql.Tx, key, scopeMode string, groupIDs, serverIDs []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM role_scopes WHERE role_key = ?`, key); err != nil {
		return fmt.Errorf("清理角色 %s 范围: %w", key, err)
	}
	if scopeMode == "all" {
		return nil
	}
	for _, gid := range groupIDs {
		if strings.TrimSpace(gid) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO role_scopes (role_key, kind, target_id) VALUES (?, 'group', ?)`, key, gid); err != nil {
			return fmt.Errorf("写入角色 %s 分组范围: %w", key, err)
		}
	}
	for _, sid := range serverIDs {
		if strings.TrimSpace(sid) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO role_scopes (role_key, kind, target_id) VALUES (?, 'server', ?)`, key, sid); err != nil {
			return fmt.Errorf("写入角色 %s 服务器范围: %w", key, err)
		}
	}
	return nil
}
