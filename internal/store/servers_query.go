package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/maliang/leadstack/pkg/protocol"
)

// ServerFilter 描述 GET /servers 的过滤条件；空字符串表示不过滤。
type ServerFilter struct {
	Group  string
	Tag    string
	Status string
	Mode   string
}

// ListServersFiltered 按过滤条件返回服务器。
//
// group/mode 在 SQL 层过滤；tag（JSON 数组）与 status（需按模式实时推导）在 Go 层过滤，
// 保证过滤口径与接口返回的状态完全一致。
func (s *Store) ListServersFiltered(ctx context.Context, f ServerFilter, nowMS int64) ([]Server, error) {
	query := `SELECT ` + serverColumns + ` FROM servers`
	where := make([]string, 0, 2)
	args := make([]any, 0, 2)
	if strings.TrimSpace(f.Mode) != "" {
		where = append(where, `mode = ?`)
		args = append(args, strings.TrimSpace(f.Mode))
	}
	if strings.TrimSpace(f.Group) != "" {
		where = append(where, `"group" = ?`)
		args = append(args, strings.TrimSpace(f.Group))
	}
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY created_at DESC, id DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询服务器列表: %w", err)
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
		return nil, fmt.Errorf("遍历服务器列表: %w", err)
	}

	return filterServers(out, f, nowMS), nil
}

// filterServers 应用 tag 与 status 过滤。
func filterServers(in []Server, f ServerFilter, nowMS int64) []Server {
	tag := strings.TrimSpace(f.Tag)
	status := strings.TrimSpace(f.Status)
	if tag == "" && status == "" {
		return in
	}
	out := make([]Server, 0, len(in))
	for _, srv := range in {
		if tag != "" && !hasTag(srv.TagsJSON, tag) {
			continue
		}
		if status != "" && EffectiveStatus(srv, nowMS) != status {
			continue
		}
		out = append(out, srv)
	}
	return out
}

// hasTag 判断服务器的标签 JSON 中是否包含指定标签。
func hasTag(tagsJSON, tag string) bool {
	for _, t := range parseTagsJSON(tagsJSON) {
		if t == tag {
			return true
		}
	}
	return false
}

// parseTagsJSON 解析标签 JSON；异常数据按空标签处理。
func parseTagsJSON(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return nil
	}
	return tags
}

// EffectiveStatus 计算服务器的对外状态。
//
//   - mode=ssh：以采集器维护的 status 列为准（连续失败 3 次才判 offline，见契约第 10 节）；
//   - mode=agent：由 last_seen_at 按 45 秒阈值实时推导（基础 行为，保持兼容）。
func EffectiveStatus(srv Server, nowMS int64) string {
	if srv.Mode == ModeSSH {
		switch srv.Status {
		case protocol.StatusOnline, protocol.StatusOffline, protocol.StatusUnknown:
			return srv.Status
		default:
			return protocol.StatusUnknown
		}
	}
	if srv.LastSeenAt == nil {
		return protocol.StatusUnknown
	}
	if nowMS-*srv.LastSeenAt <= int64(protocol.OfflineAfterS)*1000 {
		return protocol.StatusOnline
	}
	return protocol.StatusOffline
}

// ListGroups 按 servers.group 聚合出分组名与数量（不含未分组）。
func (s *Store) ListGroups(ctx context.Context) ([]GroupCount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT "group" AS g, COUNT(*) FROM servers
		  WHERE "group" IS NOT NULL AND TRIM("group") <> ''
		  GROUP BY g ORDER BY COUNT(*) DESC, g ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询分组聚合: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]GroupCount, 0, 8)
	for rows.Next() {
		var g GroupCount
		if err := rows.Scan(&g.Name, &g.Count); err != nil {
			return nil, fmt.Errorf("扫描分组聚合: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历分组聚合: %w", err)
	}
	return out, nil
}

// UpdateServerCollectionState 记录一次 agentless 采集结果。
//
// 三个字段都是「可选更新」：status/lastSeenAt/lastError 传 nil 表示不改动该列。
// 这样失败次数尚未达到阈值时可以只更新 last_error，而不破坏当前状态。
//
// lastError 传「非 nil 的空串」表示**清空**该列（写 NULL）：采集恢复成功时用它抹掉过期故障。
func (s *Store) UpdateServerCollectionState(ctx context.Context, id string, status *string, lastSeenAt *int64, lastError *string) error {
	sets := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if status != nil {
		sets = append(sets, `status = ?`)
		args = append(args, *status)
	}
	if lastSeenAt != nil {
		sets = append(sets, `last_seen_at = ?`)
		args = append(args, *lastSeenAt)
	}
	if lastError != nil {
		sets = append(sets, `last_error = ?`)
		// 空串 → NULL：表示「恢复正常，没有错误可展示」。
		args = append(args, nullableStr(emptyToNil(*lastError)))
	}
	if len(sets) == 0 {
		return nil
	}
	query := `UPDATE servers SET ` + strings.Join(sets, `, `)
	query += ` WHERE id = ?`
	args = append(args, id)

	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("更新服务器 %s 采集状态: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("更新服务器 %s 采集状态后读取影响行数: %w", id, err)
	}
	if affected == 0 {
		// 服务器可能已被删除：返回 ErrNotFound，让采集器安静地跳过。
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers WHERE id = ?`, id).Scan(&exists); err != nil {
			return fmt.Errorf("确认服务器 %s 是否存在: %w", id, err)
		}
		if exists == 0 {
			return fmt.Errorf("更新服务器 %s 采集状态: %w", id, ErrNotFound)
		}
	}
	return nil
}

// UpdateServerSystemInfo 把主机信息里的系统字段同步到 servers 行（ssh 模式下 os/arch/kernel 来源）。
func (s *Store) UpdateServerSystemInfo(ctx context.Context, id, osName, arch, kernel string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE servers SET os = ?, arch = ?, kernel = ? WHERE id = ?`,
		nullableStr(emptyToNil(osName)), nullableStr(emptyToNil(arch)), nullableStr(emptyToNil(kernel)), id)
	if err != nil {
		return fmt.Errorf("更新服务器 %s 系统信息: %w", id, err)
	}
	if _, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("更新服务器 %s 系统信息后读取影响行数: %w", id, err)
	}
	return nil
}

// ListServersByMode 返回指定模式的全部服务器（采集循环每轮调用）。
func (s *Store) ListServersByMode(ctx context.Context, mode string) ([]Server, error) {
	return s.ListServersFiltered(ctx, ServerFilter{Mode: mode}, NowMS())
}
