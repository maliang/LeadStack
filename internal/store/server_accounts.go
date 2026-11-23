package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// serverAccountColumns 是 server_accounts 表的固定列顺序，查询与扫描共用。
const serverAccountColumns = `id, server_id, name, address, username, password_enc,
	note, custom_fields, login_script, created_at, updated_at`

// ListServerAccounts 返回某台服务器的全部访问账号，按创建时间正序。
func (s *Store) ListServerAccounts(ctx context.Context, serverID string) ([]ServerAccount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+serverAccountColumns+` FROM server_accounts
		  WHERE server_id = ? ORDER BY created_at ASC, id ASC`, serverID)
	if err != nil {
		return nil, fmt.Errorf("查询服务器 %s 的访问账号: %w", serverID, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ServerAccount, 0, 4)
	for rows.Next() {
		item, err := scanServerAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历服务器 %s 的访问账号: %w", serverID, err)
	}
	return out, nil
}

// GetServerAccount 按「服务器 + 账号」查询，跨服务器访问返回 ErrNotFound。
func (s *Store) GetServerAccount(ctx context.Context, serverID, accountID string) (*ServerAccount, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+serverAccountColumns+` FROM server_accounts
		  WHERE id = ? AND server_id = ?`, accountID, serverID)
	return scanServerAccount(row)
}

// CreateServerAccount 插入一条访问账号。调用方负责填好 ID/CreatedAt/UpdatedAt。
func (s *Store) CreateServerAccount(ctx context.Context, item *ServerAccount) error {
	if item == nil {
		return errors.New("创建访问账号: 参数为空")
	}
	if item.ServerID == "" || item.Name == "" {
		return errors.New("创建访问账号: server_id/name 不能为空")
	}
	if item.CustomFieldsJSON == "" {
		item.CustomFieldsJSON = "{}"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO server_accounts (`+serverAccountColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.ServerID, item.Name, item.Address, item.Username,
		nullableBytes(item.PasswordEnc), item.Note, item.CustomFieldsJSON,
		item.LoginScript, item.CreatedAt, item.UpdatedAt)
	if err != nil {
		return fmt.Errorf("创建访问账号 %s: %w", item.Name, err)
	}
	return nil
}

// ServerAccountUpdate 描述 PATCH 语义的访问账号更新；nil 表示不修改。
//
// PasswordEnc 为 nil 表示不改密码；非 nil 的 nil 字节切片（[]byte{}）表示清空密码。
type ServerAccountUpdate struct {
	Name             *string
	Address          *string
	Username         *string
	PasswordEnc      []byte
	PasswordSet      bool
	Note             *string
	CustomFieldsJSON *string
	LoginScript      *string
	UpdatedAt        int64
}

// UpdateServerAccount 按 PATCH 语义更新账号，返回更新后的记录。
func (s *Store) UpdateServerAccount(ctx context.Context, serverID, accountID string, upd ServerAccountUpdate) (*ServerAccount, error) {
	sets := make([]string, 0, 8)
	args := make([]any, 0, 9)
	if upd.Name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *upd.Name)
	}
	if upd.Address != nil {
		sets = append(sets, "address = ?")
		args = append(args, *upd.Address)
	}
	if upd.Username != nil {
		sets = append(sets, "username = ?")
		args = append(args, *upd.Username)
	}
	if upd.PasswordSet {
		sets = append(sets, "password_enc = ?")
		args = append(args, nullableBytes(upd.PasswordEnc))
	}
	if upd.Note != nil {
		sets = append(sets, "note = ?")
		args = append(args, *upd.Note)
	}
	if upd.CustomFieldsJSON != nil {
		sets = append(sets, "custom_fields = ?")
		args = append(args, *upd.CustomFieldsJSON)
	}
	if upd.LoginScript != nil {
		sets = append(sets, "login_script = ?")
		args = append(args, *upd.LoginScript)
	}
	if len(sets) == 0 {
		return s.GetServerAccount(ctx, serverID, accountID)
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, upd.UpdatedAt)
	args = append(args, accountID, serverID)

	res, err := s.db.ExecContext(ctx,
		`UPDATE server_accounts SET `+strings.Join(sets, ", ")+` WHERE id = ? AND server_id = ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("更新访问账号 %s: %w", accountID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("更新访问账号 %s 后读取影响行数: %w", accountID, err)
	}
	if affected == 0 {
		return nil, fmt.Errorf("更新访问账号 %s: %w", accountID, ErrNotFound)
	}
	return s.GetServerAccount(ctx, serverID, accountID)
}

// DeleteServerAccount 删除一条访问账号；不存在返回 ErrNotFound。
func (s *Store) DeleteServerAccount(ctx context.Context, serverID, accountID string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM server_accounts WHERE id = ? AND server_id = ?`, accountID, serverID)
	if err != nil {
		return fmt.Errorf("删除访问账号 %s: %w", accountID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("删除访问账号 %s 后读取影响行数: %w", accountID, err)
	}
	if affected == 0 {
		return fmt.Errorf("删除访问账号 %s: %w", accountID, ErrNotFound)
	}
	return nil
}

// DeleteServerAccounts 删除某台服务器的全部访问账号（DeleteServer 内使用）。
func DeleteServerAccounts(ctx context.Context, tx *sql.Tx, serverID string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM server_accounts WHERE server_id = ?`, serverID); err != nil {
		return fmt.Errorf("清理服务器 %s 的访问账号: %w", serverID, err)
	}
	return nil
}

func scanServerAccount(row interface{ Scan(...any) error }) (*ServerAccount, error) {
	var (
		item ServerAccount
		enc  []byte
	)
	err := row.Scan(&item.ID, &item.ServerID, &item.Name, &item.Address, &item.Username, &enc,
		&item.Note, &item.CustomFieldsJSON, &item.LoginScript, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询访问账号: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描访问账号: %w", err)
	}
	item.PasswordEnc = enc
	return &item, nil
}
