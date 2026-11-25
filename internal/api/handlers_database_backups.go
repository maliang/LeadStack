package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/store"
)

type databaseBackupConfigRequest struct {
	ID            string   `json:"id"`
	Databases     []string `json:"databases"`
	Host          string   `json:"host"`
	Port          int      `json:"port"`
	Username      string   `json:"username"`
	Password      string   `json:"password"`
	IntervalHours int      `json:"interval_hours"`
	Retention     int      `json:"retention"`
	Enabled       *bool    `json:"enabled"`
}

type databaseBackupConfigPayload struct {
	ID            string   `json:"id"`
	ServerID      string   `json:"server_id"`
	Databases     []string `json:"databases"`
	Host          string   `json:"host"`
	Port          int      `json:"port"`
	Username      string   `json:"username"`
	IntervalHours int      `json:"interval_hours"`
	Retention     int      `json:"retention"`
	Enabled       bool     `json:"enabled"`
	LastRunAt     *int64   `json:"last_run_at"`
	NextRunAt     int64    `json:"next_run_at"`
	LastError     *string  `json:"last_error"`
	CreatedAt     int64    `json:"created_at"`
	UpdatedAt     int64    `json:"updated_at"`
}

func toDatabaseBackupConfigPayload(cfg store.DatabaseBackupConfig) databaseBackupConfigPayload {
	return databaseBackupConfigPayload{
		ID: cfg.ID, ServerID: cfg.ServerID, Databases: cfg.Databases, Host: cfg.Host, Port: cfg.Port,
		Username: cfg.Username, IntervalHours: cfg.IntervalHours, Retention: cfg.Retention,
		Enabled: cfg.Enabled, LastRunAt: cfg.LastRunAt, NextRunAt: cfg.NextRunAt,
		LastError: cfg.LastError, CreatedAt: cfg.CreatedAt, UpdatedAt: cfg.UpdatedAt,
	}
}

// handleListDatabaseBackups 处理 GET /servers/{id}/database-backups。
func (a *API) handleListDatabaseBackups(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	configs, err := a.st.ListDatabaseBackupConfigs(r.Context(), serverID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]databaseBackupConfigPayload, 0, len(configs))
	for _, cfg := range configs {
		out = append(out, toDatabaseBackupConfigPayload(cfg))
	}
	writeData(w, http.StatusOK, out)
}

// handleSaveDatabaseBackup 处理 POST /servers/{id}/database-backups（新建/更新）。
func (a *API) handleSaveDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	if a.cipher == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "凭证加密未启用（缺少主密钥）")
		return
	}
	serverID := chi.URLParam(r, "serverID")
	if _, err := a.st.GetServer(r.Context(), serverID); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	var req databaseBackupConfigRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	databases, err := normalizeDatabaseNames(req.Databases)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	host := strings.TrimSpace(req.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	user := strings.TrimSpace(req.Username)
	if !dbHostRe.MatchString(host) || !dbIdentifierRe.MatchString(user) {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "数据库地址或用户名格式不合法")
		return
	}
	port := req.Port
	if port == 0 {
		port = 3306
	}
	if port < 1 || port > 65535 {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "数据库端口必须在 1-65535 之间")
		return
	}
	interval := req.IntervalHours
	if interval == 0 {
		interval = 24
	}
	if interval < 1 || interval > 24*30 {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "备份周期必须在 1-720 小时之间")
		return
	}
	retention := req.Retention
	if retention == 0 {
		retention = 7
	}
	if retention < 1 || retention > 365 {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "保留份数必须在 1-365 之间")
		return
	}

	now := a.nowMS()
	id := strings.TrimSpace(req.ID)
	var existing *store.DatabaseBackupConfig
	if id != "" {
		existing, err = a.st.GetDatabaseBackupConfig(r.Context(), id)
		if err != nil {
			a.writeStoreError(w, r, err)
			return
		}
		if existing.ServerID != serverID {
			writeError(w, http.StatusConflict, codeConflict, "备份配置不属于这台服务器")
			return
		}
	} else {
		id = uuid.NewString()
		existing = &store.DatabaseBackupConfig{CreatedAt: now}
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	} else if existing.ID != "" {
		enabled = existing.Enabled
	}

	var encrypted []byte
	if req.Password != "" {
		encrypted, err = a.cipher.Encrypt([]byte(req.Password))
		if err != nil {
			a.internalError(w, r, fmt.Errorf("加密数据库密码: %w", err))
			return
		}
	} else if existing.ID != "" {
		encrypted = existing.PasswordEnc
	}
	if len(encrypted) == 0 {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "首次创建备份配置必须填写数据库密码")
		return
	}
	cfg := &store.DatabaseBackupConfig{
		ID: id, ServerID: serverID, Databases: databases, Host: host, Port: port,
		Username: user, PasswordEnc: encrypted, IntervalHours: interval, Retention: retention,
		Enabled: enabled, LastRunAt: existing.LastRunAt,
		NextRunAt: now + int64(interval)*int64(time.Hour/time.Millisecond), LastError: existing.LastError,
		CreatedAt: existing.CreatedAt, UpdatedAt: now,
	}
	if existing.ID != "" && existing.NextRunAt > 0 && existing.IntervalHours == interval {
		cfg.NextRunAt = existing.NextRunAt
	}
	if cfg.CreatedAt == 0 {
		cfg.CreatedAt = now
	}
	if err := a.st.UpsertDatabaseBackupConfig(r.Context(), cfg); err != nil {
		a.internalError(w, r, err)
		return
	}
	a.auditManual(r, "server.database_backup_config", "server", serverID,
		fmt.Sprintf(`{"config_id":%q,"databases":%q,"interval_hours":%d}`, id, strings.Join(databases, ","), interval))
	writeData(w, http.StatusOK, toDatabaseBackupConfigPayload(*cfg))
}

func (a *API) handleDeleteDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	id := chi.URLParam(r, "configID")
	cfg, err := a.st.GetDatabaseBackupConfig(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if cfg.ServerID != serverID {
		writeError(w, http.StatusConflict, codeConflict, "备份配置不属于这台服务器")
		return
	}
	if err := a.st.DeleteDatabaseBackupConfig(r.Context(), id); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.auditManual(r, "server.database_backup_config_delete", "server", serverID,
		fmt.Sprintf(`{"config_id":%q}`, id))
	writeData(w, http.StatusOK, map[string]any{"id": id})
}

// handleRunDatabaseBackup 立即执行一次配置并返回生成的文件名。
func (a *API) handleRunDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	id := chi.URLParam(r, "configID")
	cfg, err := a.st.GetDatabaseBackupConfig(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if cfg.ServerID != serverID {
		writeError(w, http.StatusConflict, codeConflict, "备份配置不属于这台服务器")
		return
	}
	files, err := a.executeDatabaseBackup(r.Context(), cfg)
	now := a.nowMS()
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	next := now + int64(cfg.IntervalHours)*int64(time.Hour/time.Millisecond)
	_ = a.st.MarkDatabaseBackupRun(r.Context(), cfg.ID, now, next, errText)
	if err != nil {
		writeError(w, http.StatusBadGateway, codeInternal, "数据库备份失败: "+err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"files": files})
}

func normalizeDatabaseNames(in []string) ([]string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		for _, name := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t' }) {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if !dbIdentifierRe.MatchString(name) {
				return nil, fmt.Errorf("数据库名 %q 格式不合法", name)
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("至少填写一个数据库名")
	}
	if len(out) > 32 {
		return nil, errors.New("单个配置最多 32 个数据库")
	}
	return out, nil
}

func (a *API) databaseBackupDir(serverID, configID string) string {
	return filepath.Join(a.cfg.DataDir, "db-backups", serverID, configID)
}

func (a *API) executeDatabaseBackup(ctx context.Context, cfg *store.DatabaseBackupConfig) ([]string, error) {
	if a.remoteExec == nil {
		return nil, errors.New("远程执行通道未启用")
	}
	plain, err := a.cipher.Decrypt(cfg.PasswordEnc)
	if err != nil {
		return nil, fmt.Errorf("解密数据库密码失败: %w", err)
	}
	dir := a.databaseBackupDir(cfg.ServerID, cfg.ID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("创建备份目录失败: %w", err)
	}
	ts := time.Now().Format("20060102-150405")
	files := make([]string, 0, len(cfg.Databases))
	for _, database := range cfg.Databases {
		command := databaseDumpCommand(cfg.Host, cfg.Port, cfg.Username, string(plain), database)
		stdout, stderr, code, err := a.remoteExec.Exec(ctx, cfg.ServerID, command, 300)
		if err != nil {
			return files, err
		}
		if code != 0 {
			msg := strings.TrimSpace(stderr)
			if msg == "" {
				msg = fmt.Sprintf("mysqldump 退出码 %d", code)
			}
			return files, fmt.Errorf("%s: %s", database, msg)
		}
		name := database + "-" + ts + ".sql"
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(stdout), 0o600); err != nil {
			return files, fmt.Errorf("写入备份文件失败: %w", err)
		}
		files = append(files, name)
	}
	a.pruneDatabaseBackups(dir, cfg.Retention)
	return files, nil
}

func (a *API) pruneDatabaseBackups(dir string, retention int) {
	if retention <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	if len(names) <= retention*4 { // 每个周期可能多个库，粗略保留
		return
	}
	sort.Strings(names)
	for _, name := range names[:len(names)-retention*4] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// RunDatabaseBackupScheduler 每分钟检查到期配置并执行。
func (a *API) RunDatabaseBackupScheduler(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			configs, err := a.st.ListDueDatabaseBackupConfigs(ctx, a.nowMS())
			if err != nil {
				continue
			}
			for _, cfg := range configs {
				files, runErr := a.executeDatabaseBackup(ctx, &cfg)
				errText := ""
				if runErr != nil {
					errText = fmt.Sprintf("备份失败: %v", runErr)
					a.logger.Warn("数据库自动备份失败", "server_id", cfg.ServerID, "config_id", cfg.ID, "err", runErr)
				} else {
					a.logger.Info("数据库自动备份完成", "server_id", cfg.ServerID, "config_id", cfg.ID, "files", files)
				}
				now := a.nowMS()
				next := now + int64(cfg.IntervalHours)*int64(time.Hour/time.Millisecond)
				_ = a.st.MarkDatabaseBackupRun(ctx, cfg.ID, now, next, errText)
			}
		}
	}
}

func databaseDumpCommand(host string, port int, user, password, database string) string {
	escaped := strings.ReplaceAll(password, "'", `'\''`)
	return fmt.Sprintf("MYSQL_PWD='%s' mysqldump --single-transaction --quick --skip-lock-tables --default-character-set=utf8mb4 -h '%s' -P %d -u '%s' --databases '%s'",
		escaped, host, port, user, database)
}

func (a *API) handleListDatabaseBackupFiles(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	id := chi.URLParam(r, "configID")
	cfg, err := a.st.GetDatabaseBackupConfig(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if cfg.ServerID != serverID {
		writeError(w, http.StatusConflict, codeConflict, "备份配置不属于这台服务器")
		return
	}
	dir := a.databaseBackupDir(serverID, id)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		a.internalError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		info, ierr := entry.Info()
		if ierr != nil {
			continue
		}
		out = append(out, map[string]any{"name": entry.Name(), "size": info.Size(), "mod_time": info.ModTime().UnixMilli()})
	}
	writeData(w, http.StatusOK, out)
}

func (a *API) handleDownloadDatabaseBackupFile(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	id := chi.URLParam(r, "configID")
	name := chi.URLParam(r, "name")
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".sql") {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "备份文件名不合法")
		return
	}
	cfg, err := a.st.GetDatabaseBackupConfig(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if cfg.ServerID != serverID {
		writeError(w, http.StatusConflict, codeConflict, "备份配置不属于这台服务器")
		return
	}
	path := filepath.Join(a.databaseBackupDir(serverID, id), name)
	data, err := os.ReadFile(path)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/sql; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}
