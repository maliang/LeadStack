package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ------------------------------------------------ 应用 ------------------------------------------------

// CreateApp 创建应用；slug 重复返回 ErrConflict。
func (s *Store) CreateApp(ctx context.Context, app *App) error {
	if app == nil {
		return errors.New("创建应用: 参数为空")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO apps (id, name, slug, root, description, created_at, provider, site_name, site_port, site_type, healthcheck_path,
		                   deploy_mode, target_dir, restart_cmd, init_script, env_json, keep_paths, healthcheck_port)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		app.ID, app.Name, app.Slug, nullableStr(app.Root), nullableStr(app.Description), app.CreatedAt,
		defaultProvider(app.Provider), nullableStr(app.SiteName), nullableIntFromPtr(app.SitePort),
		nullableStr(app.SiteType), nullableStr(app.HealthcheckPath),
		defaultDeployMode(app.DeployMode), nullableStr(app.TargetDir), nullableStr(app.RestartCmd),
		nullableStr(app.InitScript), nullableStr(app.EnvJSON), nullableStr(app.KeepPaths),
		nullableIntFromPtr(app.HealthcheckPort))
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("创建应用 %s: %w", app.Slug, ErrConflict)
		}
		return fmt.Errorf("创建应用 %s: %w", app.Slug, err)
	}
	return nil
}

// GetApp 按 ID 查询应用。
func (s *Store) GetApp(ctx context.Context, id string) (*App, error) {
	return scanApp(s.db.QueryRowContext(ctx,
		`SELECT id, name, slug, root, description, created_at, provider, site_name, site_port, site_type, healthcheck_path,
		deploy_mode, target_dir, restart_cmd, init_script, env_json, keep_paths, healthcheck_port FROM apps WHERE id = ?`, id))
}

// GetAppBySlug 按 slug 查询应用。
func (s *Store) GetAppBySlug(ctx context.Context, slug string) (*App, error) {
	return scanApp(s.db.QueryRowContext(ctx,
		`SELECT id, name, slug, root, description, created_at, provider, site_name, site_port, site_type, healthcheck_path,
		deploy_mode, target_dir, restart_cmd, init_script, env_json, keep_paths, healthcheck_port FROM apps WHERE slug = ?`, slug))
}

// ListApps 返回全部应用（按创建时间倒序）。
func (s *Store) ListApps(ctx context.Context) ([]App, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, slug, root, description, created_at, provider, site_name, site_port, site_type, healthcheck_path,
		deploy_mode, target_dir, restart_cmd, init_script, env_json, keep_paths, healthcheck_port FROM apps ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("查询应用列表: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]App, 0, 8)
	for rows.Next() {
		app, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *app)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历应用列表: %w", err)
	}
	return out, nil
}

// UpdateApp 更新应用的名称/根目录/描述（nil 表示不改）。
func (s *Store) UpdateApp(ctx context.Context, id string, name, root, description *string) (*App, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		sets := make([]string, 0, 3)
		args := make([]any, 0, 4)
		if name != nil {
			sets = append(sets, "name = ?")
			args = append(args, *name)
		}
		if root != nil {
			sets = append(sets, "root = ?")
			args = append(args, nullableStr(emptyToNil(*root)))
		}
		if description != nil {
			sets = append(sets, "description = ?")
			args = append(args, nullableStr(emptyToNil(*description)))
		}
		if len(sets) == 0 {
			return nil
		}
		query := `UPDATE apps SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
		args = append(args, id)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("更新应用 %s: %w", id, err)
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM apps WHERE id = ?`, id).Scan(&exists); err != nil {
			return fmt.Errorf("确认应用 %s 是否存在: %w", id, err)
		}
		if exists == 0 {
			return fmt.Errorf("更新应用 %s: %w", id, ErrNotFound)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetApp(ctx, id)
}

// DeleteApp 删除应用；已有发布记录时返回 ErrConflict（409）。
func (s *Store) DeleteApp(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var releases int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM releases WHERE app_id = ?`, id).Scan(&releases); err != nil {
			return fmt.Errorf("统计应用 %s 的发布记录: %w", id, err)
		}
		if releases > 0 {
			return fmt.Errorf("应用 %s 仍有 %d 条发布记录，禁止删除: %w", id, releases, ErrConflict)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM apps WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("删除应用 %s: %w", id, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("删除应用 %s 后读取影响行数: %w", id, err)
		}
		if affected == 0 {
			return fmt.Errorf("删除应用 %s: %w", id, ErrNotFound)
		}
		return nil
	})
}

// CountAppReleases 统计应用的发布数量。
func (s *Store) CountAppReleases(ctx context.Context, appID string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM releases WHERE app_id = ?`, appID).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计应用 %s 的发布数: %w", appID, err)
	}
	return n, nil
}

// scanApp 把一行映射为 App。
func scanApp(row interface{ Scan(...any) error }) (*App, error) {
	var (
		app        App
		root, desc sql.NullString
		provider   sql.NullString
		siteName   sql.NullString
		sitePort   sql.NullInt64
		siteType   sql.NullString
		health     sql.NullString
		deployMode sql.NullString
		targetDir  sql.NullString
		restartCmd sql.NullString
		initScript sql.NullString
		envJSON    sql.NullString
		keepPaths  sql.NullString
		healthPort sql.NullInt64
	)
	err := row.Scan(&app.ID, &app.Name, &app.Slug, &root, &desc, &app.CreatedAt,
		&provider, &siteName, &sitePort, &siteType, &health,
		&deployMode, &targetDir, &restartCmd, &initScript, &envJSON, &keepPaths, &healthPort)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询应用: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描应用: %w", err)
	}
	app.Root = strPtr(root)
	app.Description = strPtr(desc)
	app.Provider = strings.TrimSpace(provider.String)
	if app.Provider == "" {
		app.Provider = AppProviderBuiltin
	}
	app.SiteName = strPtr(siteName)
	if sitePort.Valid {
		v := int(sitePort.Int64)
		app.SitePort = &v
	}
	app.SiteType = strPtr(siteType)
	app.HealthcheckPath = strPtr(health)
	app.DeployMode = strings.TrimSpace(deployMode.String)
	if app.DeployMode == "" {
		app.DeployMode = DeployModeRelease
	}
	app.TargetDir = strPtr(targetDir)
	app.RestartCmd = strPtr(restartCmd)
	app.InitScript = strPtr(initScript)
	app.EnvJSON = strPtr(envJSON)
	app.KeepPaths = strPtr(keepPaths)
	if healthPort.Valid {
		v := int(healthPort.Int64)
		app.HealthcheckPort = &v
	}
	return &app, nil
}

// 应用部署方式：builtin（SSH/agent 执行 shell 计划）与 baota（宝塔面板 API）。
const (
	AppProviderBuiltin = "builtin"
	AppProviderBaota   = "baota"
)

// 部署模式：release（默认，releases/<id> + current 原子切换）与 inplace（接管既有目录）。
const (
	DeployModeRelease = "release"
	DeployModeInplace = "inplace"
)

// defaultDeployMode 把空值归一为 release。
func defaultDeployMode(m string) string {
	if v := strings.TrimSpace(m); v != "" {
		return v
	}
	return DeployModeRelease
}

// defaultProvider 把空值归一为 builtin。
func defaultProvider(p string) string {
	if v := strings.TrimSpace(p); v != "" {
		return v
	}
	return AppProviderBuiltin
}

// nullableIntFromPtr 把 *int 转成可空 sql 值。
func nullableIntFromPtr(p *int) *int64 {
	if p == nil {
		return nil
	}
	v := int64(*p)
	return &v
}

// UpdateAppSite 更新应用的部署方式与面板站点配置（PATCH 语义，nil 表示不改）。
func (s *Store) UpdateAppSite(ctx context.Context, id string, provider, siteName, siteType, healthcheckPath *string, sitePort *int) error {
	sets := make([]string, 0, 6)
	args := make([]any, 0, 7)
	if provider != nil {
		sets = append(sets, "provider = ?")
		args = append(args, defaultProvider(*provider))
	}
	if siteName != nil {
		sets = append(sets, "site_name = ?")
		args = append(args, nullableStr(emptyToNil(*siteName)))
	}
	if sitePort != nil {
		sets = append(sets, "site_port = ?")
		args = append(args, *sitePort)
	}
	if siteType != nil {
		sets = append(sets, "site_type = ?")
		args = append(args, nullableStr(emptyToNil(*siteType)))
	}
	if healthcheckPath != nil {
		sets = append(sets, "healthcheck_path = ?")
		args = append(args, nullableStr(emptyToNil(*healthcheckPath)))
	}
	if len(sets) == 0 {
		return nil
	}
	query := `UPDATE apps SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
	args = append(args, id)
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("更新应用 %s 部署配置: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("更新应用 %s 后读取影响行数: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("更新应用 %s: %w", id, ErrNotFound)
	}
	return nil
}

// UpdateAppDeploy 更新部署 v2 字段（PATCH 语义，nil 表示不改）。
func (s *Store) UpdateAppDeploy(ctx context.Context, id string,
	deployMode, targetDir, restartCmd, initScript, envJSON, keepPaths *string, healthPort *int) error {
	sets := make([]string, 0, 7)
	args := make([]any, 0, 8)
	add := func(col string, v any) { sets = append(sets, col+" = ?"); args = append(args, v) }
	if deployMode != nil {
		add("deploy_mode", defaultDeployMode(*deployMode))
	}
	if targetDir != nil {
		add("target_dir", nullableStr(emptyToNil(*targetDir)))
	}
	if restartCmd != nil {
		add("restart_cmd", nullableStr(emptyToNil(*restartCmd)))
	}
	if initScript != nil {
		add("init_script", nullableStr(emptyToNil(*initScript)))
	}
	if envJSON != nil {
		add("env_json", nullableStr(emptyToNil(*envJSON)))
	}
	if keepPaths != nil {
		add("keep_paths", nullableStr(emptyToNil(*keepPaths)))
	}
	if healthPort != nil {
		add("healthcheck_port", *healthPort)
	}
	if len(sets) == 0 {
		return nil
	}
	query := `UPDATE apps SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
	args = append(args, id)
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("更新应用 %s 部署参数: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("更新应用 %s 后读取影响行数: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("更新应用 %s: %w", id, ErrNotFound)
	}
	return nil
}

// ------------------------------------------------ 模板 ------------------------------------------------

// CreateDeployTemplate 创建部署模板。
func (s *Store) CreateDeployTemplate(ctx context.Context, t *DeployTemplate) error {
	if t == nil {
		return errors.New("创建模板: 参数为空")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO deploy_templates (id, name, kind, content, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.Kind, t.Content, t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return fmt.Errorf("创建模板 %s: %w", t.Name, err)
	}
	return nil
}

// GetDeployTemplate 按 ID 查询模板。
func (s *Store) GetDeployTemplate(ctx context.Context, id string) (*DeployTemplate, error) {
	return scanTemplate(s.db.QueryRowContext(ctx,
		`SELECT id, name, kind, content, created_at, updated_at FROM deploy_templates WHERE id = ?`, id))
}

// ListDeployTemplates 返回全部模板（按更新时间倒序）。
func (s *Store) ListDeployTemplates(ctx context.Context) ([]DeployTemplate, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, kind, content, created_at, updated_at FROM deploy_templates
		  ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("查询模板列表: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]DeployTemplate, 0, 8)
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历模板列表: %w", err)
	}
	return out, nil
}

// UpdateDeployTemplate 更新模板的名称/类型/内容（nil 表示不改）。
//
// 已发布的历史快照存在 releases.template_snapshot 里，不受这里的修改影响。
func (s *Store) UpdateDeployTemplate(ctx context.Context, id string, name, kind, content *string, nowMS int64) (*DeployTemplate, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		sets := make([]string, 0, 4)
		args := make([]any, 0, 5)
		if name != nil {
			sets = append(sets, "name = ?")
			args = append(args, *name)
		}
		if kind != nil {
			sets = append(sets, "kind = ?")
			args = append(args, *kind)
		}
		if content != nil {
			sets = append(sets, "content = ?")
			args = append(args, *content)
		}
		if len(sets) == 0 {
			return nil
		}
		sets = append(sets, "updated_at = ?")
		args = append(args, nowMS)
		query := `UPDATE deploy_templates SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
		args = append(args, id)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("更新模板 %s: %w", id, err)
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM deploy_templates WHERE id = ?`, id).Scan(&exists); err != nil {
			return fmt.Errorf("确认模板 %s 是否存在: %w", id, err)
		}
		if exists == 0 {
			return fmt.Errorf("更新模板 %s: %w", id, ErrNotFound)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetDeployTemplate(ctx, id)
}

// DeleteDeployTemplate 删除模板（历史快照保留在 releases 里）。
func (s *Store) DeleteDeployTemplate(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM deploy_templates WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除模板 %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("删除模板 %s 后读取影响行数: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("删除模板 %s: %w", id, ErrNotFound)
	}
	return nil
}

// scanTemplate 把一行映射为 DeployTemplate。
func scanTemplate(row interface{ Scan(...any) error }) (*DeployTemplate, error) {
	var t DeployTemplate
	err := row.Scan(&t.ID, &t.Name, &t.Kind, &t.Content, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询模板: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描模板: %w", err)
	}
	return &t, nil
}

// ------------------------------------------------ 制品 ------------------------------------------------

// CreateArtifact 登记一个制品。
func (s *Store) CreateArtifact(ctx context.Context, a *Artifact) error {
	if a == nil {
		return errors.New("创建制品: 参数为空")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO artifacts (id, kind, location, sha256, size_bytes, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		a.ID, a.Kind, a.Location, nullableStr(a.SHA256), a.SizeBytes, a.CreatedAt)
	if err != nil {
		return fmt.Errorf("创建制品 %s: %w", a.ID, err)
	}
	return nil
}

// GetArtifact 按 ID 查询制品。
func (s *Store) GetArtifact(ctx context.Context, id string) (*Artifact, error) {
	var (
		a   Artifact
		sha sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, kind, location, sha256, size_bytes, created_at FROM artifacts WHERE id = ?`, id).
		Scan(&a.ID, &a.Kind, &a.Location, &sha, &a.SizeBytes, &a.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询制品: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描制品: %w", err)
	}
	a.SHA256 = strPtr(sha)
	return &a, nil
}

// ------------------------------------------------ 发布 ------------------------------------------------

// CreateRelease 在一个事务里写入发布记录与各服务器的 run。
func (s *Store) CreateRelease(ctx context.Context, rel *Release, runs []ReleaseRun) error {
	if rel == nil {
		return errors.New("创建发布: 参数为空")
	}
	if len(runs) == 0 {
		return errors.New("创建发布: 目标服务器不能为空")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO releases (id, app_id, template_id, version, artifact_id, template_snapshot,
			                       status, created_by, created_at, started_at, finished_at, concurrency)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?)`,
			rel.ID, rel.AppID, rel.TemplateID, rel.Version, nullableStr(rel.ArtifactID),
			rel.TemplateSnapshot, rel.Status, nullableStr(rel.CreatedBy), rel.CreatedAt, rel.Concurrency); err != nil {
			return fmt.Errorf("创建发布 %s: %w", rel.ID, err)
		}
		for _, run := range runs {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO release_runs (id, release_id, server_id, status, prev_release_id)
				 VALUES (?, ?, ?, ?, ?)`,
				run.ID, run.ReleaseID, run.ServerID, run.Status, nullableStr(run.PrevReleaseID)); err != nil {
				return fmt.Errorf("创建发布任务 %s/%s: %w", rel.ID, run.ServerID, err)
			}
		}
		return nil
	})
}

// GetRelease 按 ID 查询发布。
func (s *Store) GetRelease(ctx context.Context, id string) (*Release, error) {
	return scanRelease(s.db.QueryRowContext(ctx, `SELECT `+releaseColumns+` FROM releases WHERE id = ?`, id))
}

// releaseColumns 是 releases 表的固定列顺序。
const releaseColumns = `id, app_id, template_id, version, artifact_id, template_snapshot,
	status, created_by, created_at, started_at, finished_at, concurrency`

// ReleaseFilter 是发布列表的过滤条件。
type ReleaseFilter struct {
	AppID  string
	Status string
	Limit  int
}

// ListReleases 返回发布列表（按创建时间倒序）。
func (s *Store) ListReleases(ctx context.Context, f ReleaseFilter) ([]Release, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	query := `SELECT ` + releaseColumns + ` FROM releases WHERE 1 = 1`
	args := make([]any, 0, 3)
	if f.AppID != "" {
		query += ` AND app_id = ?`
		args = append(args, f.AppID)
	}
	if f.Status != "" {
		query += ` AND status = ?`
		args = append(args, f.Status)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询发布列表: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Release, 0, f.Limit)
	for rows.Next() {
		rel, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历发布列表: %w", err)
	}
	return out, nil
}

// UpdateReleaseStatus 更新发布状态与起止时间（nil 表示不改）。
func (s *Store) UpdateReleaseStatus(ctx context.Context, id, status string, startedAt, finishedAt *int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE releases SET status = ?,
		        started_at = COALESCE(?, started_at),
		        finished_at = ?
		  WHERE id = ?`,
		status, nullableInt(startedAt), nullableInt(finishedAt), id); err != nil {
		return fmt.Errorf("更新发布 %s 状态: %w", id, err)
	}
	return nil
}

// SummarizeReleaseStatus 依据各 run 的状态计算发布整体状态（契约第 23 节）。
func (s *Store) SummarizeReleaseStatus(ctx context.Context, releaseID string) (string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT status, COUNT(*) FROM release_runs WHERE release_id = ? GROUP BY status`, releaseID)
	if err != nil {
		return "", fmt.Errorf("统计发布 %s 的 run 状态: %w", releaseID, err)
	}
	defer func() { _ = rows.Close() }()

	counts := map[string]int{}
	total := 0
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return "", fmt.Errorf("扫描 run 状态: %w", err)
		}
		counts[status] = n
		total += n
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("遍历 run 状态: %w", err)
	}
	if total == 0 {
		return ReleaseStatusPending, nil
	}

	switch {
	case counts[ReleaseStatusRunning] > 0:
		return ReleaseStatusRunning, nil
	case counts[ReleaseStatusPending] > 0:
		return ReleaseStatusPending, nil
	case counts[ReleaseStatusFailed] > 0:
		return ReleaseStatusFailed, nil
	case counts[ReleaseStatusCanceled] > 0 && counts[ReleaseStatusSuccess] == 0:
		return ReleaseStatusCanceled, nil
	case counts[ReleaseStatusSuccess] == total:
		return ReleaseStatusSuccess, nil
	default:
		return ReleaseStatusFailed, nil
	}
}

// scanRelease 把一行映射为 Release。
func scanRelease(row interface{ Scan(...any) error }) (*Release, error) {
	var (
		rel                   Release
		artifactID, createdBy sql.NullString
		startedAt, finishedAt sql.NullInt64
		concurrency           int
	)
	err := row.Scan(&rel.ID, &rel.AppID, &rel.TemplateID, &rel.Version, &artifactID,
		&rel.TemplateSnapshot, &rel.Status, &createdBy, &rel.CreatedAt, &startedAt, &finishedAt, &concurrency)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询发布: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描发布: %w", err)
	}
	rel.ArtifactID = strPtr(artifactID)
	rel.CreatedBy = strPtr(createdBy)
	rel.StartedAt = intPtr(startedAt)
	rel.FinishedAt = intPtr(finishedAt)
	rel.Concurrency = concurrency
	return &rel, nil
}

// ------------------------------------------------ 发布任务 ------------------------------------------------

// releaseRunColumns 是 release_runs 表的固定列顺序。
const releaseRunColumns = `id, release_id, server_id, status, exit_code, message, failed_step,
	prev_release_id, started_at, finished_at`

// GetReleaseRun 按 (发布, 服务器) 查询执行单元。
func (s *Store) GetReleaseRun(ctx context.Context, releaseID, serverID string) (*ReleaseRun, error) {
	return scanReleaseRun(s.db.QueryRowContext(ctx,
		`SELECT `+releaseRunColumns+` FROM release_runs WHERE release_id = ? AND server_id = ?`,
		releaseID, serverID))
}

// ListReleaseRuns 返回一次发布的全部执行单元（稳定按 server_id 排序）。
func (s *Store) ListReleaseRuns(ctx context.Context, releaseID string) ([]ReleaseRun, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+releaseRunColumns+` FROM release_runs WHERE release_id = ? ORDER BY server_id ASC`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("查询发布任务: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ReleaseRun, 0, 4)
	for rows.Next() {
		run, err := scanReleaseRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历发布任务: %w", err)
	}
	return out, nil
}

// ListReleaseRunsByStatus 返回指定状态的执行单元（并发调度与重试用）。
func (s *Store) ListReleaseRunsByStatus(ctx context.Context, releaseID, status string) ([]ReleaseRun, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+releaseRunColumns+` FROM release_runs WHERE release_id = ? AND status = ? ORDER BY server_id ASC`,
		releaseID, status)
	if err != nil {
		return nil, fmt.Errorf("按状态查询发布任务: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ReleaseRun, 0, 4)
	for rows.Next() {
		run, err := scanReleaseRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历发布任务: %w", err)
	}
	return out, nil
}

// UpdateReleaseRun 更新执行单元的状态、退出码、消息、失败步骤与起止时间。
func (s *Store) UpdateReleaseRun(ctx context.Context, run ReleaseRun) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE release_runs SET status = ?, exit_code = ?, message = ?, failed_step = ?,
		        prev_release_id = COALESCE(?, prev_release_id), started_at = COALESCE(?, started_at),
		        finished_at = ?
		  WHERE id = ?`,
		run.Status, nullableInt(statusCodeToPtr(run.ExitCode)), nullableStr(run.Message), nullableStr(run.FailedStep),
		nullableStr(run.PrevReleaseID), nullableInt(run.StartedAt), nullableInt(run.FinishedAt), run.ID)
	if err != nil {
		return fmt.Errorf("更新发布任务 %s: %w", run.ID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("更新发布任务 %s 后读取影响行数: %w", run.ID, err)
	}
	if affected == 0 {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM release_runs WHERE id = ?`, run.ID).Scan(&exists); err != nil {
			return fmt.Errorf("确认发布任务 %s 是否存在: %w", run.ID, err)
		}
		if exists == 0 {
			return fmt.Errorf("更新发布任务 %s: %w", run.ID, ErrNotFound)
		}
	}
	return nil
}

// ResetReleaseRunsForRetry 把指定的执行单元重置为 pending 并清空上次结果与日志。
//
// 清空日志是为了让重试的 seq 空间从 1 开始，避免与上次尝试混在一起（前端按 seq 增量读）。
func (s *Store) ResetReleaseRunsForRetry(ctx context.Context, releaseID string, serverIDs []string) (int, error) {
	if len(serverIDs) == 0 {
		return 0, nil
	}
	reset := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		for _, serverID := range serverIDs {
			var runID string
			err := tx.QueryRowContext(ctx,
				`SELECT id FROM release_runs WHERE release_id = ? AND server_id = ? AND status = ?`,
				releaseID, serverID, ReleaseStatusFailed).Scan(&runID)
			if errors.Is(err, sql.ErrNoRows) {
				continue // 非 failed 的服务器不重跑
			}
			if err != nil {
				return fmt.Errorf("查询待重试任务 %s/%s: %w", releaseID, serverID, err)
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE release_runs SET status = ?, exit_code = NULL, message = NULL, failed_step = NULL,
				        started_at = NULL, finished_at = NULL WHERE id = ?`,
				ReleaseStatusPending, runID); err != nil {
				return fmt.Errorf("重置发布任务 %s: %w", runID, err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM release_logs WHERE release_run_id = ?`, runID); err != nil {
				return fmt.Errorf("清理重试任务的旧日志 %s: %w", runID, err)
			}
			reset++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return reset, nil
}

// CancelPendingRuns 把仍处于 pending/running 的执行单元置为 canceled，返回受影响数量。
func (s *Store) CancelPendingRuns(ctx context.Context, releaseID string, serverIDs []string, ts int64) (int, error) {
	canceled := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		query := `UPDATE release_runs SET status = ?, message = ?, finished_at = ?
		           WHERE release_id = ? AND status IN (?, ?)`
		args := []any{ReleaseStatusCanceled, "已被用户取消", ts, releaseID, ReleaseStatusPending, ReleaseStatusRunning}
		if len(serverIDs) > 0 {
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(serverIDs)), ",")
			query += ` AND server_id IN (` + placeholders + `)`
			for _, id := range serverIDs {
				args = append(args, id)
			}
		}
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("取消发布任务: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("取消发布任务后读取影响行数: %w", err)
		}
		canceled = int(n)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return canceled, nil
}

// scanReleaseRun 把一行映射为 ReleaseRun。
func scanReleaseRun(row interface{ Scan(...any) error }) (*ReleaseRun, error) {
	var (
		run                       ReleaseRun
		exitCode                  sql.NullInt64
		message, failedStep, prev sql.NullString
		startedAt, finishedAt     sql.NullInt64
	)
	err := row.Scan(&run.ID, &run.ReleaseID, &run.ServerID, &run.Status, &exitCode,
		&message, &failedStep, &prev, &startedAt, &finishedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("查询发布任务: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("扫描发布任务: %w", err)
	}
	run.ExitCode = int64PtrToIntPtr(exitCode)
	run.Message = strPtr(message)
	run.FailedStep = strPtr(failedStep)
	run.PrevReleaseID = strPtr(prev)
	run.StartedAt = intPtr(startedAt)
	run.FinishedAt = intPtr(finishedAt)
	return &run, nil
}

// ------------------------------------------------ 发布日志 ------------------------------------------------

// AppendReleaseLog 追加一条发布日志，返回分配的 seq。
//
// seq 在事务内按 `MAX(seq)+1` 分配，保证同一 run 内**严格单调递增且并发安全**
// （SQLite 单写连接 + 事务），与 agent 侧 seq 无关，避免两条通道混用时序号冲突。
func (s *Store) AppendReleaseLog(ctx context.Context, runID, stream, chunk string, ts int64) (int64, error) {
	if runID == "" {
		return 0, errors.New("追加发布日志: release_run_id 为空")
	}
	if stream != "stdout" && stream != "stderr" && stream != "system" {
		stream = "stdout"
	}
	if chunk == "" {
		return 0, nil // 空分块不入库，避免噪声
	}
	var seq int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), 0) + 1 FROM release_logs WHERE release_run_id = ?`, runID).Scan(&seq); err != nil {
			return fmt.Errorf("分配日志序号 %s: %w", runID, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO release_logs (release_run_id, seq, ts, stream, chunk) VALUES (?, ?, ?, ?, ?)`,
			runID, seq, ts, stream, chunk); err != nil {
			return fmt.Errorf("写入发布日志 %s#%d: %w", runID, seq, err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// ListReleaseLogs 按 seq 增量返回日志（after_seq 之前的已读部分不再返回）。
func (s *Store) ListReleaseLogs(ctx context.Context, runID string, afterSeq int64, limit int) ([]ReleaseLog, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT release_run_id, seq, ts, stream, chunk FROM release_logs
		  WHERE release_run_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?`,
		runID, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("查询发布日志: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ReleaseLog, 0, 64)
	for rows.Next() {
		var item ReleaseLog
		if err := rows.Scan(&item.ReleaseRunID, &item.Seq, &item.TS, &item.Stream, &item.Chunk); err != nil {
			return nil, fmt.Errorf("扫描发布日志: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历发布日志: %w", err)
	}
	return out, nil
}

// CountReleaseLogs 统计某次执行的日志条数。
func (s *Store) CountReleaseLogs(ctx context.Context, runID string) (int64, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM release_logs WHERE release_run_id = ?`, runID).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计发布日志: %w", err)
	}
	return n, nil
}

// DeleteReleaseLogsBefore 清理 before（Unix 毫秒）之前的发布日志，返回删除条数。
func (s *Store) DeleteReleaseLogsBefore(ctx context.Context, before int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM release_logs WHERE ts < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("清理历史发布日志: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("清理历史发布日志后读取影响行数: %w", err)
	}
	return n, nil
}

// ReleaseServerIDs 返回发布 -> 目标服务器 ID 集合（来自 release_runs）。
//
// 资源级授权用它判定「该发布是否与调用者范围有交集」。
func (s *Store) ReleaseServerIDs(ctx context.Context, releaseIDs []string) (map[string][]string, error) {
	out := make(map[string][]string, len(releaseIDs))
	if len(releaseIDs) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(releaseIDs)), ",")
	args := make([]any, 0, len(releaseIDs))
	for _, id := range releaseIDs {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT release_id, server_id FROM release_runs WHERE release_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("查询发布目标服务器: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var releaseID, serverID string
		if err := rows.Scan(&releaseID, &serverID); err != nil {
			return nil, fmt.Errorf("扫描发布目标服务器: %w", err)
		}
		out[releaseID] = append(out[releaseID], serverID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历发布目标服务器: %w", err)
	}
	return out, nil
}
