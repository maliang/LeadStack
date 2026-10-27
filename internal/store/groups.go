package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ServerGroup 是一等公民的服务器分组（授权单元；与 servers.tags 的筛选口径互不影响）。
type ServerGroup struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MemberCount int    `json:"member_count"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// ListServerGroups 返回全部分组（含成员数）。
func (s *Store) ListServerGroups(ctx context.Context) ([]ServerGroup, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT g.id, g.name, g.description, g.created_at, g.updated_at,
		        (SELECT COUNT(*) FROM server_group_members m WHERE m.group_id = g.id) AS members
		   FROM server_groups g
		  ORDER BY g.name ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询分组列表: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ServerGroup, 0, 8)
	for rows.Next() {
		var g ServerGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt, &g.MemberCount); err != nil {
			return nil, fmt.Errorf("扫描分组: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历分组列表: %w", err)
	}
	return out, nil
}

// GetServerGroup 按 ID 查询分组；不存在返回 ErrNotFound。
func (s *Store) GetServerGroup(ctx context.Context, id string) (*ServerGroup, error) {
	var g ServerGroup
	err := s.db.QueryRowContext(ctx,
		`SELECT g.id, g.name, g.description, g.created_at, g.updated_at,
		        (SELECT COUNT(*) FROM server_group_members m WHERE m.group_id = g.id)
		   FROM server_groups g WHERE g.id = ?`, id).
		Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt, &g.MemberCount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询分组 %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("扫描分组 %s: %w", id, err)
	}
	return &g, nil
}

// CreateServerGroup 插入分组；重名返回 ErrConflict。
func (s *Store) CreateServerGroup(ctx context.Context, g *ServerGroup) error {
	if g == nil {
		return errors.New("创建分组: 参数为空")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO server_groups (id, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		g.ID, g.Name, g.Description, g.CreatedAt, g.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("分组 %s 已存在: %w", g.Name, ErrConflict)
		}
		return fmt.Errorf("创建分组 %s: %w", g.Name, err)
	}
	return nil
}

// GroupUpdate 是 PATCH 语义的分组更新（nil 表示不改）。
type GroupUpdate struct {
	Name        *string
	Description *string
}

// UpdateServerGroup 更新分组名称/描述。
func (s *Store) UpdateServerGroup(ctx context.Context, id string, upd GroupUpdate, nowMS int64) (*ServerGroup, error) {
	sets := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if upd.Name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *upd.Name)
	}
	if upd.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *upd.Description)
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at = ?")
		args = append(args, nowMS)
		query := `UPDATE server_groups SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
		args = append(args, id)
		if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
			if isUniqueViolation(err) {
				return nil, fmt.Errorf("分组 %s 的新名称已被占用: %w", id, ErrConflict)
			}
			return nil, fmt.Errorf("更新分组 %s: %w", id, err)
		}
	}
	return s.GetServerGroup(ctx, id)
}

// DeleteServerGroup 删除分组，并清理成员关系与角色范围引用。
func (s *Store) DeleteServerGroup(ctx context.Context, id string) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM server_group_members WHERE group_id = ?`, id); err != nil {
			return fmt.Errorf("清理分组 %s 成员: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM role_scopes WHERE kind = 'group' AND target_id = ?`, id); err != nil {
			return fmt.Errorf("清理分组 %s 的角色范围引用: %w", id, err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM server_groups WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("删除分组 %s: %w", id, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("删除分组 %s 后读取影响行数: %w", id, err)
		}
		if affected == 0 {
			return fmt.Errorf("删除分组 %s: %w", id, ErrNotFound)
		}
		return nil
	})
	return err
}

// SetServerGroupMembers 用给定服务器集合整体替换分组成员。
func (s *Store) SetServerGroupMembers(ctx context.Context, groupID string, serverIDs []string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM server_groups WHERE id = ?`, groupID).Scan(&exists); err != nil {
			return fmt.Errorf("确认分组 %s 是否存在: %w", groupID, err)
		}
		if exists == 0 {
			return fmt.Errorf("分组 %s: %w", groupID, ErrNotFound)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM server_group_members WHERE group_id = ?`, groupID); err != nil {
			return fmt.Errorf("清空分组 %s 成员: %w", groupID, err)
		}
		for _, sid := range serverIDs {
			if strings.TrimSpace(sid) == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO server_group_members (group_id, server_id) VALUES (?, ?)`, groupID, sid); err != nil {
				return fmt.Errorf("写入分组 %s 成员 %s: %w", groupID, sid, err)
			}
		}
		return nil
	})
}

// ListServerGroupMembers 返回分组内的服务器（按名称排序）。
func (s *Store) ListServerGroupMembers(ctx context.Context, groupID string) ([]Server, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+serverColumns+` FROM servers
		  WHERE id IN (SELECT server_id FROM server_group_members WHERE group_id = ?)
		  ORDER BY name ASC, id ASC`, groupID)
	if err != nil {
		return nil, fmt.Errorf("查询分组 %s 成员: %w", groupID, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Server, 0, 8)
	for rows.Next() {
		srv, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *srv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历分组 %s 成员: %w", groupID, err)
	}
	return out, nil
}

// SetServerGroups 用给定分组集合整体替换某台服务器的分组归属。
func (s *Store) SetServerGroups(ctx context.Context, serverID string, groupIDs []string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers WHERE id = ?`, serverID).Scan(&exists); err != nil {
			return fmt.Errorf("确认服务器 %s 是否存在: %w", serverID, err)
		}
		if exists == 0 {
			return fmt.Errorf("服务器 %s: %w", serverID, ErrNotFound)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM server_group_members WHERE server_id = ?`, serverID); err != nil {
			return fmt.Errorf("清空服务器 %s 分组归属: %w", serverID, err)
		}
		for _, gid := range groupIDs {
			if strings.TrimSpace(gid) == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO server_group_members (group_id, server_id) VALUES (?, ?)`, gid, serverID); err != nil {
				return fmt.Errorf("写入服务器 %s 分组 %s: %w", serverID, gid, err)
			}
		}
		return nil
	})
}

// ServerGroupRefs 返回服务器 -> 所属分组（ID 与名称）映射，用于列表载荷。
func (s *Store) ServerGroupRefs(ctx context.Context, serverIDs []string) (map[string][]string, map[string][]string, error) {
	ids := make(map[string][]string, len(serverIDs))
	names := make(map[string][]string, len(serverIDs))
	if len(serverIDs) == 0 {
		return ids, names, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(serverIDs)), ",")
	args := make([]any, 0, len(serverIDs))
	for _, id := range serverIDs {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.server_id, g.id, g.name
		   FROM server_group_members m JOIN server_groups g ON g.id = m.group_id
		  WHERE m.server_id IN (`+placeholders+`)
		  ORDER BY g.name ASC`, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("查询服务器分组归属: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var serverID, groupID, groupName string
		if err := rows.Scan(&serverID, &groupID, &groupName); err != nil {
			return nil, nil, fmt.Errorf("扫描服务器分组归属: %w", err)
		}
		ids[serverID] = append(ids[serverID], groupID)
		names[serverID] = append(names[serverID], groupName)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("遍历服务器分组归属: %w", err)
	}
	return ids, names, nil
}

// ServerIDsInScope 解析「授权分组 ∪ 直接授权服务器」得到可见服务器 ID 集合。
//
// 返回空切片表示范围内没有任何服务器；调用方用 all=true 表示不过滤。
func (s *Store) ServerIDsInScope(ctx context.Context, groupIDs, serverIDs []string) ([]string, error) {
	seen := make(map[string]struct{}, len(serverIDs))
	out := make([]string, 0, len(serverIDs))
	for _, id := range serverIDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(groupIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(groupIDs)), ",")
		args := make([]any, 0, len(groupIDs))
		for _, id := range groupIDs {
			args = append(args, id)
		}
		rows, err := s.db.QueryContext(ctx,
			`SELECT DISTINCT server_id FROM server_group_members WHERE group_id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("查询分组内服务器: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var sid string
			if err := rows.Scan(&sid); err != nil {
				return nil, fmt.Errorf("扫描分组内服务器: %w", err)
			}
			if _, ok := seen[sid]; ok {
				continue
			}
			seen[sid] = struct{}{}
			out = append(out, sid)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("遍历分组内服务器: %w", err)
		}
	}
	return out, nil
}

// GroupsExist 返回不存在的分组 ID（创建/更新角色范围时校验用）。
func (s *Store) GroupsExist(ctx context.Context, groupIDs []string) ([]string, error) {
	missing := make([]string, 0, 2)
	for _, gid := range groupIDs {
		if strings.TrimSpace(gid) == "" {
			missing = append(missing, gid)
			continue
		}
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM server_groups WHERE id = ?`, gid).Scan(&n); err != nil {
			return nil, fmt.Errorf("校验分组 %s: %w", gid, err)
		}
		if n == 0 {
			missing = append(missing, gid)
		}
	}
	return missing, nil
}

// ServersExist 返回不存在的服务器 ID（角色范围校验用）。
func (s *Store) ServersExist(ctx context.Context, serverIDs []string) ([]string, error) {
	missing := make([]string, 0, 2)
	for _, sid := range serverIDs {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers WHERE id = ?`, sid).Scan(&n); err != nil {
			return nil, fmt.Errorf("校验服务器 %s: %w", sid, err)
		}
		if n == 0 {
			missing = append(missing, sid)
		}
	}
	return missing, nil
}
