package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// credentialColumns 是 credentials 表的固定列顺序。
const credentialColumns = `id, name, kind, username, secret_enc, fingerprint, created_at`

// CreateCredential 插入一条凭证（只存密文）；名称重复返回 ErrConflict。
func (s *Store) CreateCredential(ctx context.Context, c *Credential) error {
	if c == nil {
		return errors.New("创建凭证: 参数为空")
	}
	if c.Name == "" || c.Kind == "" || c.Username == "" {
		return errors.New("创建凭证: name/kind/username 不能为空")
	}
	if len(c.SecretEnc) == 0 {
		return errors.New("创建凭证: 缺少密文")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO credentials (`+credentialColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.Kind, c.Username, c.SecretEnc, nullableStr(c.Fingerprint), c.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("创建凭证 %s: %w", c.Name, ErrConflict)
		}
		return fmt.Errorf("创建凭证 %s: %w", c.Name, err)
	}
	return nil
}

// ListCredentials 返回全部凭证（不含明文）及其被引用次数。
func (s *Store) ListCredentials(ctx context.Context) ([]CredentialSummary, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.name, c.kind, c.username, c.secret_enc, c.fingerprint, c.created_at,
		        (SELECT COUNT(*) FROM servers srv WHERE srv.credential_id = c.id) AS server_count
		   FROM credentials c
		  ORDER BY c.created_at DESC, c.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("查询凭证列表: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]CredentialSummary, 0, 8)
	for rows.Next() {
		var (
			item CredentialSummary
			fp   sql.NullString
		)
		if err := rows.Scan(&item.ID, &item.Name, &item.Kind, &item.Username,
			&item.SecretEnc, &fp, &item.CreatedAt, &item.ServerCount); err != nil {
			return nil, fmt.Errorf("扫描凭证: %w", err)
		}
		item.Fingerprint = strPtr(fp)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历凭证列表: %w", err)
	}
	return out, nil
}

// GetCredential 按 ID 查询凭证（含密文，供服务端解密使用）。
func (s *Store) GetCredential(ctx context.Context, id string) (*Credential, error) {
	var (
		c  Credential
		fp sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT `+credentialColumns+` FROM credentials WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.Kind, &c.Username, &c.SecretEnc, &fp, &c.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询凭证 %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("查询凭证 %s: %w", id, err)
	}
	c.Fingerprint = strPtr(fp)
	return &c, nil
}

// CountServersUsingCredential 统计引用该凭证的服务器数量。
func (s *Store) CountServersUsingCredential(ctx context.Context, id string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM servers WHERE credential_id = ?`, id).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计凭证 %s 的引用数: %w", id, err)
	}
	return n, nil
}

// DeleteCredential 删除凭证；仍被服务器引用时返回 ErrConflict。
func (s *Store) DeleteCredential(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var inUse int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM servers WHERE credential_id = ?`, id).Scan(&inUse); err != nil {
			return fmt.Errorf("检查凭证 %s 的引用: %w", id, err)
		}
		if inUse > 0 {
			return fmt.Errorf("凭证 %s 仍被 %d 台服务器引用: %w", id, inUse, ErrConflict)
		}

		res, err := tx.ExecContext(ctx, `DELETE FROM credentials WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("删除凭证 %s: %w", id, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("删除凭证 %s 后读取影响行数: %w", id, err)
		}
		if affected == 0 {
			return fmt.Errorf("删除凭证 %s: %w", id, ErrNotFound)
		}
		return nil
	})
}
