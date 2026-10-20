package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/maliang/leadstack/internal/store"
)

// 备份相关常量与存储键。
const (
	// backupSettingsKey 是备份设置（enabled/interval/retention/include_artifacts）在 settings 表的键。
	backupSettingsKey = "backup.settings"
	// backupLastErrorKey 记录最近一次定时备份失败的原因。
	backupLastErrorKey = "backup.last_error"
	// backupLastSuccessKey 记录最近一次成功备份时间（Unix 毫秒）。
	backupLastSuccessKey = "backup.last_success_at"

	// backupUploadMaxBytes 是「上传恢复」接受的最大归档体积（1 GiB）。
	backupUploadMaxBytes = int64(1) << 30
	// backupFileSuffix 是归档文件后缀。
	backupFileSuffix = ".lsbak"
	// restorePendingMetaName 是暂存恢复的元数据文件名。
	restorePendingMetaName = "meta.json"
	// defaultBackupInterval 与 config 的默认值保持一致。
	defaultBackupInterval = 24 * time.Hour
	// defaultBackupRetention 与 config 的默认值保持一致。
	defaultBackupRetention = 7
	// defaultMaxSchemaVersion 是未注入时的兜底最高迁移版本（当前 migrations 最高为 0006）。
	defaultMaxSchemaVersion = 6
)

// BackupInfo 是备份归档的元信息（与 internal/backup.Info 一一对应）。
type BackupInfo struct {
	Name              string `json:"name"`
	SizeBytes         int64  `json:"size_bytes"`
	CreatedAt         int64  `json:"created_at"`
	IncludesArtifacts bool   `json:"includes_artifacts"`
	SchemaVersion     int    `json:"schema_version"`
	SHA256            string `json:"sha256"`
}

// BackupManifest 是归档内 manifest.json 的对外表示。
type BackupManifest struct {
	FormatVersion     int                  `json:"format_version"`
	AppVersion        string               `json:"app_version"`
	SchemaVersion     int                  `json:"schema_version"`
	CreatedAt         int64                `json:"created_at"`
	IncludesArtifacts bool                 `json:"includes_artifacts"`
	Files             []BackupManifestFile `json:"files"`
}

// BackupManifestFile 是 manifest 中的单个文件条目。
type BackupManifestFile struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

// BackupService 是备份能力的端口（由 internal/backup.Manager 适配实现）。
//
// 抽成接口的目的：HTTP 层可以用替身测试路径穿越、错误映射与暂存逻辑，
// 而不依赖真实归档格式；真实实现在 backup_wiring.go 里做一次薄适配。
type BackupService interface {
	Create(ctx context.Context, includeArtifacts bool) (*BackupInfo, error)
	List(ctx context.Context) ([]BackupInfo, error)
	Verify(ctx context.Context, path string) (*BackupManifest, error)
	Delete(ctx context.Context, name string) error
	// StageRestore 校验归档并解压暂存到 pendingDir（含 meta.json）；失败不留半成品。
	StageRestore(ctx context.Context, archivePath, pendingDir string) (*StagedRestore, error)
}

// backupSettings 是 `/backup-settings` 的持久化结构。
type backupSettings struct {
	Enabled          bool   `json:"enabled"`
	Interval         string `json:"interval"`
	Retention        int    `json:"retention"`
	IncludeArtifacts bool   `json:"include_artifacts"`
}

// backupSettingsPayload 是对外的设置结构（附加最近一次结果，便于排障）。
type backupSettingsPayload struct {
	backupSettings
	LastError     string `json:"last_error,omitempty"`
	LastSuccessAt *int64 `json:"last_success_at,omitempty"`
}

// restorePendingMeta 是暂存恢复的元数据（下次启动时据此应用）。
type restorePendingMeta struct {
	ArchiveName   string `json:"archive_name"`
	UploadedAt    int64  `json:"uploaded_at"`
	SHA256        string `json:"sha256,omitempty"`
	SizeBytes     int64  `json:"size_bytes"`
	Origin        string `json:"origin"`
	SchemaVersion int    `json:"schema_version,omitempty"`
}

// restoreResponse 是上传恢复的响应。
type restoreResponse struct {
	RestartRequired bool           `json:"restart_required"`
	Staged          *StagedRestore `json:"staged,omitempty"`
}

// verifyResponse 是校验接口的响应。
type verifyResponse struct {
	OK       bool            `json:"ok"`
	Manifest *BackupManifest `json:"manifest,omitempty"`
}

// ------------------------------------------------ 列表 / 创建 ------------------------------------------------

// handleListBackups 处理 GET /api/v1/backups。
func (a *API) handleListBackups(w http.ResponseWriter, r *http.Request) {
	if a.backups == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "备份模块未启用")
		return
	}
	infos, err := a.backups.List(r.Context())
	if err != nil {
		a.internalError(w, r, fmt.Errorf("读取备份列表: %w", err))
		return
	}
	if infos == nil {
		infos = []BackupInfo{}
	}
	// 响应形状：{data:{backups:[...]}}（契约第 33 节允许；与验收脚本的 `.data.backups` 对齐）。
	writeData(w, http.StatusOK, map[string]any{"backups": infos})
}

// handleCreateBackup 处理 POST /api/v1/backups（立即备份）。
func (a *API) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	if a.backups == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "备份模块未启用")
		return
	}
	var req struct {
		IncludeArtifacts *bool `json:"include_artifacts"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
			return
		}
	}
	settings, err := a.loadBackupSettings(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	includeArtifacts := settings.IncludeArtifacts
	if req.IncludeArtifacts != nil {
		includeArtifacts = *req.IncludeArtifacts
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	info, err := a.backups.Create(ctx, includeArtifacts)
	if err != nil {
		a.writeBackupError(w, r, err, "创建备份")
		return
	}
	a.recordBackupResult(r.Context(), nil)

	// 立即备份也遵守保留策略，避免手工点几次就把磁盘打满。
	a.pruneBackups(r.Context(), settings.Retention)

	a.logger.Info("备份创建成功",
		"name", info.Name, "size_bytes", info.SizeBytes, "includes_artifacts", info.IncludesArtifacts, "sha256", info.SHA256)
	writeData(w, http.StatusCreated, info)
}

// ------------------------------------------------ 下载 / 校验 / 删除 ------------------------------------------------

// handleDownloadBackup 处理 GET /api/v1/backups/{name}/download。
func (a *API) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	path, err := a.resolveBackupPath(w, r)
	if err != nil {
		return // 已写响应
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, codeNotFound, "备份文件不存在")
			return
		}
		a.internalError(w, r, fmt.Errorf("打开备份文件: %w", err))
		return
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			a.logger.Debug("关闭备份文件失败", "err", cerr)
		}
	}()
	info, err := file.Stat()
	if err != nil {
		a.internalError(w, r, fmt.Errorf("读取备份文件信息: %w", err))
		return
	}
	name := filepath.Base(path)
	a.logger.Info("下载备份归档", "name", name, "size_bytes", info.Size())
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, name, info.ModTime(), file)
}

// handleVerifyBackup 处理 POST /api/v1/backups/{name}/verify。
func (a *API) handleVerifyBackup(w http.ResponseWriter, r *http.Request) {
	if a.backups == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "备份模块未启用")
		return
	}
	path, err := a.resolveBackupPath(w, r)
	if err != nil {
		return
	}
	manifest, err := a.backups.Verify(r.Context(), path)
	if err != nil {
		a.writeBackupError(w, r, err, "校验备份")
		return
	}
	// 体检动作也要报版本问题：等到「上传恢复」才发现版本过新体验太差（§33）。
	if status, msg := a.checkSchemaVersion(manifest.SchemaVersion); status != 0 {
		writeError(w, status, codeSchemaTooNew, msg)
		return
	}
	writeData(w, http.StatusOK, verifyResponse{OK: true, Manifest: manifest})
}

// handleDeleteBackup 处理 DELETE /api/v1/backups/{name}。
func (a *API) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	if a.backups == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "备份模块未启用")
		return
	}
	path, err := a.resolveBackupPath(w, r)
	if err != nil {
		return
	}
	name := filepath.Base(path)
	if err := a.backups.Delete(r.Context(), name); err != nil {
		a.writeBackupError(w, r, err, "删除备份")
		return
	}
	a.logger.Info("删除备份归档", "name", name)
	writeData(w, http.StatusOK, map[string]any{"name": name})
}

// ------------------------------------------------ 上传恢复（暂存） ------------------------------------------------

// handleRestoreUpload 处理 POST /api/v1/backups/restore（multipart）。
//
// 流程：临时落盘 → Verify 校验 → 暂存到 <data_dir>/.restore-pending/（含 meta.json）
// → 202 {restart_required:true}。校验失败返回 400 且**不留下任何半成品**。
func (a *API) handleRestoreUpload(w http.ResponseWriter, r *http.Request) {
	if a.backups == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "备份模块未启用")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, backupUploadMaxBytes+(1<<20))
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, codeInvalidArgument,
				fmt.Sprintf("归档超过 %d 字节上限", backupUploadMaxBytes))
			return
		}
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "解析 multipart 失败: "+err.Error())
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			if err := r.MultipartForm.RemoveAll(); err != nil {
				a.logger.Debug("清理上传临时文件失败", "err", err)
			}
		}
	}()

	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少 file 字段（归档文件）")
		return
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			a.logger.Debug("关闭上传归档失败", "err", cerr)
		}
	}()

	// 1) 先落到数据目录内的临时位置（保证后续 rename 不跨文件系统）。
	tmpDir := filepath.Join(a.cfg.DataDir, ".restore-tmp")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		a.internalError(w, r, fmt.Errorf("创建恢复临时目录: %w", err))
		return
	}
	tmpFile, err := os.CreateTemp(tmpDir, "upload-*.lsbak")
	if err != nil {
		a.internalError(w, r, fmt.Errorf("创建恢复临时文件: %w", err))
		return
	}
	tmpPath := tmpFile.Name()
	cleanupTmp := func() {
		if rerr := os.Remove(tmpPath); rerr != nil && !os.IsNotExist(rerr) {
			a.logger.Debug("清理恢复临时文件失败", "err", rerr)
		}
	}
	written, err := io.Copy(tmpFile, io.LimitReader(file, backupUploadMaxBytes+1))
	if err != nil {
		_ = tmpFile.Close()
		cleanupTmp()
		a.internalError(w, r, fmt.Errorf("写入上传归档: %w", err))
		return
	}
	if written > backupUploadMaxBytes {
		_ = tmpFile.Close()
		cleanupTmp()
		writeError(w, http.StatusRequestEntityTooLarge, codeInvalidArgument,
			fmt.Sprintf("归档超过 %d 字节上限", backupUploadMaxBytes))
		return
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		cleanupTmp()
		a.internalError(w, r, fmt.Errorf("同步上传归档: %w", err))
		return
	}
	if err := tmpFile.Close(); err != nil {
		cleanupTmp()
		a.internalError(w, r, fmt.Errorf("关闭上传归档: %w", err))
		return
	}

	// 2) 校验 + 解压暂存：由 internal/backup 完成（口令错/损坏/版本过新分别映射成可读错误，
	//    校验失败不会留下半成品 —— 这是「下次启动才能应用」的安全前提）。
	pendingDir := a.cfg.RestorePendingDir()
	staged, err := a.backups.StageRestore(r.Context(), tmpPath, pendingDir)
	cleanupTmp()
	if err != nil {
		a.writeBackupError(w, r, err, "校验并暂存上传的备份")
		return
	}
	// 版本过新：不要暂存，否则下次启动会因拒绝应用而无法启动（§33）。
	if status, msg := a.checkSchemaVersion(staged.SchemaVersion); status != 0 {
		if rerr := os.RemoveAll(pendingDir); rerr != nil {
			a.logger.Warn("清理版本不兼容的暂存目录失败", "pending_dir", pendingDir, "err", rerr)
		}
		writeError(w, status, codeSchemaTooNew, msg)
		return
	}

	a.logger.Warn("已暂存备份，重启后应用恢复",
		"source", staged.Source, "size_bytes", written,
		"schema_version", staged.SchemaVersion, "staged_dir", pendingDir)
	writeData(w, http.StatusAccepted, restoreResponse{RestartRequired: true, Staged: staged})
}

// ------------------------------------------------ 设置 ------------------------------------------------

// handleGetBackupSettings 处理 GET /api/v1/backup-settings。
func (a *API) handleGetBackupSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := a.loadBackupSettings(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	payload := backupSettingsPayload{backupSettings: settings}
	if raw, ok, err := a.st.GetSetting(r.Context(), backupLastErrorKey); err == nil && ok {
		payload.LastError = raw
	}
	if raw, ok, err := a.st.GetSetting(r.Context(), backupLastSuccessKey); err == nil && ok {
		if ts, perr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); perr == nil && ts > 0 {
			payload.LastSuccessAt = &ts
		}
	}
	writeData(w, http.StatusOK, payload)
}

// handlePatchBackupSettings 处理 PATCH /api/v1/backup-settings。
func (a *API) handlePatchBackupSettings(w http.ResponseWriter, r *http.Request) {
	current, err := a.loadBackupSettings(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	var req struct {
		Enabled          *bool   `json:"enabled"`
		Interval         *string `json:"interval"`
		Retention        *int    `json:"retention"`
		IncludeArtifacts *bool   `json:"include_artifacts"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	if req.Enabled == nil && req.Interval == nil && req.Retention == nil && req.IncludeArtifacts == nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			"至少需要提供 enabled/interval/retention/include_artifacts 之一")
		return
	}
	next := current
	if req.Enabled != nil {
		next.Enabled = *req.Enabled
	}
	if req.Interval != nil {
		raw := strings.TrimSpace(*req.Interval)
		d, perr := time.ParseDuration(raw)
		if perr != nil || d < time.Second || d > 365*24*time.Hour {
			writeError(w, http.StatusBadRequest, codeInvalidArgument,
				"interval 必须是合法的 Go duration（如 24h、30m），且不小于 1s")
			return
		}
		next.Interval = d.String()
	}
	if req.Retention != nil {
		if *req.Retention < 1 || *req.Retention > 1000 {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "retention 必须在 1-1000 之间")
			return
		}
		next.Retention = *req.Retention
	}
	if req.IncludeArtifacts != nil {
		next.IncludeArtifacts = *req.IncludeArtifacts
	}

	if err := a.saveBackupSettings(r.Context(), next); err != nil {
		a.internalError(w, r, err)
		return
	}
	// 通知定时器重排（非阻塞）。
	select {
	case a.backupWake <- struct{}{}:
	default:
	}
	a.logger.Info("备份设置已更新",
		"enabled", next.Enabled, "interval", next.Interval,
		"retention", next.Retention, "include_artifacts", next.IncludeArtifacts)
	a.handleGetBackupSettings(w, r)
}

// loadBackupSettings 读取备份设置（缺省回落到配置）。
func (a *API) loadBackupSettings(ctx context.Context) (backupSettings, error) {
	settings := backupSettings{
		Enabled:          true,
		Interval:         a.cfg.BackupInterval.String(),
		Retention:        a.cfg.BackupRetention,
		IncludeArtifacts: a.cfg.BackupIncludeArtifacts,
	}
	if settings.Interval == "" || settings.Interval == "0s" {
		settings.Interval = defaultBackupInterval.String()
	}
	if settings.Retention <= 0 {
		settings.Retention = defaultBackupRetention
	}
	raw, ok, err := a.st.GetSetting(ctx, backupSettingsKey)
	if err != nil {
		return settings, err
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return settings, nil
	}
	var stored backupSettings
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		a.logger.Warn("备份设置解析失败，使用默认值", "err", err)
		return settings, nil
	}
	if stored.Interval == "" {
		stored.Interval = settings.Interval
	}
	if stored.Retention <= 0 {
		stored.Retention = settings.Retention
	}
	return stored, nil
}

// saveBackupSettings 持久化备份设置。
func (a *API) saveBackupSettings(ctx context.Context, settings backupSettings) error {
	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("序列化备份设置: %w", err)
	}
	return a.st.SetSetting(ctx, backupSettingsKey, string(raw))
}

// recordBackupResult 记录一次备份结果（last_error 为空表示成功）。
func (a *API) recordBackupResult(ctx context.Context, cause error) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if cause != nil {
		if err := a.st.SetSetting(writeCtx, backupLastErrorKey, cause.Error()); err != nil {
			a.logger.Warn("记录备份失败原因出错", "err", err)
		}
		return
	}
	if err := a.st.SetSetting(writeCtx, backupLastErrorKey, ""); err != nil {
		a.logger.Warn("清空备份失败原因出错", "err", err)
	}
	ts := strconv.FormatInt(a.nowMS(), 10)
	if err := a.st.SetSetting(writeCtx, backupLastSuccessKey, ts); err != nil {
		a.logger.Warn("记录备份成功时间出错", "err", err)
	}
}

// pruneBackups 按保留份数删除最旧的备份（失败只记日志）。
func (a *API) pruneBackups(ctx context.Context, retention int) {
	if a.backups == nil {
		return
	}
	if retention <= 0 {
		retention = defaultBackupRetention
	}
	infos, err := a.backups.List(ctx)
	if err != nil {
		a.logger.Warn("保留策略读取备份列表失败", "err", err)
		return
	}
	if len(infos) <= retention {
		return
	}
	// List 已按创建时间倒序：从尾部删。
	for i := len(infos) - 1; i >= retention; i-- {
		if err := a.backups.Delete(ctx, infos[i].Name); err != nil {
			a.logger.Warn("保留策略删除旧备份失败", "name", infos[i].Name, "err", err)
			continue
		}
		a.logger.Info("保留策略已删除旧备份", "name", infos[i].Name)
	}
}

// ------------------------------------------------ 路径安全 ------------------------------------------------

// resolveBackupPath 把 URL 上的 {name} 解析成备份目录内的绝对路径。
//
// 安全边界：只允许备份目录内的合法文件名 —— 拒绝 ../、绝对路径、分隔符、
// URL 编码穿越（chi 已解码，这里按解码后的值判断）、以及符号链接逃逸。
func (a *API) resolveBackupPath(w http.ResponseWriter, r *http.Request) (string, error) {
	raw := chi.URLParam(r, "name")
	name, err := validateBackupName(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return "", err
	}
	dir := filepath.Clean(a.cfg.BackupDir)
	full := filepath.Join(dir, name)
	// 二次确认：Join 之后必须仍在备份目录内（防御性双保险）。
	if full != filepath.Join(dir, filepath.Base(full)) {
		err := errors.New("非法的备份文件名")
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return "", err
	}
	// 符号链接逃逸：文件必须存在于备份目录内且不是软链。
	info, statErr := os.Lstat(full)
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		err := errors.New("备份路径不允许指向符号链接")
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return "", err
	}
	return full, nil
}

// validateBackupName 校验备份文件名：必须是 .lsbak 结尾的纯文件名。
func validateBackupName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("缺少备份文件名")
	}
	// 拒绝任何路径成分（含 Windows 分隔符）。
	if name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return "", errors.New("备份文件名不能包含路径分隔符")
	}
	if name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return "", errors.New("非法的备份文件名")
	}
	if !strings.HasSuffix(name, backupFileSuffix) {
		return "", fmt.Errorf("备份文件名必须以 %s 结尾", backupFileSuffix)
	}
	if len(name) > 255 {
		return "", errors.New("备份文件名过长")
	}
	return name, nil
}

// ------------------------------------------------ 错误映射 ------------------------------------------------

// writeBackupError 把备份层错误映射成可指导用户行动的中文提示。
//
// 三类失败必须可区分（契约第 31 节）：
//   - 口令错 → 请检查 backup.pass 或 LEADSTACK_BACKUP_PASSPHRASE；
//   - 归档损坏 → 文件不完整或被篡改（建议重新下载/重新备份）；
//   - 版本过新 → 该备份由更新版本创建，请先升级。
func (a *API) writeBackupError(w http.ResponseWriter, r *http.Request, err error, action string) {
	msg, status := mapBackupError(err)
	a.logger.Warn("备份操作失败", "action", action, "status", status, "err", err)
	writeError(w, status, backupErrorCode(status), msg)
}

// checkSchemaVersion 校验备份的 schema 版本是否可被当前程序接受。
//
// 返回 (0, "") 表示通过；否则返回 HTTP 状态码与可执行提示。
func (a *API) checkSchemaVersion(schemaVersion int) (int, string) {
	if schemaVersion <= 0 || schemaVersion <= a.maxSchemaVersion {
		return 0, ""
	}
	return http.StatusBadRequest,
		fmt.Sprintf("该备份由更新版本的 LeadStack 创建（备份 schema_version=%d，当前程序最高支持 %d）：请先升级当前程序，再执行校验或恢复",
			schemaVersion, a.maxSchemaVersion)
}

// mapBackupError 把备份错误映射成「状态码 + 可执行提示」。
func mapBackupError(err error) (string, int) {
	if err == nil {
		return "", http.StatusOK
	}
	var msg string
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, errBackupBadPassphrase):
		msg = "备份口令错误：请检查数据目录下的 backup.pass，或环境变量 LEADSTACK_BACKUP_PASSPHRASE 是否与备份时一致"
	case errors.Is(err, errBackupCorrupt):
		msg = "备份归档损坏或格式不符：文件可能不完整或被篡改，建议重新获取该备份文件"
	case errors.Is(err, errBackupSchemaTooNew):
		msg = "该备份由更新版本的 LeadStack 创建：请先升级当前程序，再执行恢复"
	case errors.Is(err, os.ErrNotExist):
		msg = "备份文件不存在"
		status = http.StatusNotFound
	default:
		msg = "备份操作失败：" + err.Error()
	}
	return msg, status
}

// backupErrorCode 依据状态码返回契约错误码。
func backupErrorCode(status int) string {
	switch status {
	case http.StatusNotFound:
		return codeNotFound
	case http.StatusInternalServerError:
		return codeInternal
	default:
		return codeInvalidArgument
	}
}

// 备份层哨兵错误（与 internal/backup 的 ErrBadPassphrase/ErrCorrupt/ErrSchemaTooNew 对应）。
//
// 这里用 api 自己的变量，适配层负责 errors.Is 转换；这样 HTTP 层不依赖备份包的实现细节。
var (
	errBackupBadPassphrase = errors.New("backup: 口令错误")
	errBackupCorrupt       = errors.New("backup: 归档损坏或格式不符")
	errBackupSchemaTooNew  = errors.New("backup: 备份来自更新的版本")
)

var _ = store.ErrNotFound
