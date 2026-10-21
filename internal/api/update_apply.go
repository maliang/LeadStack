package api

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"database/sql"

	"github.com/google/uuid"

	_ "modernc.org/sqlite"

	"github.com/maliang/leadstack/internal/store"
)

// upgradeJob 是一次升级任务的进度（内存态；重启后由 state.json 反映）。
type upgradeJob struct {
	JobID         string `json:"job_id"`
	Phase         string `json:"phase"`
	TargetVersion string `json:"target_version"`
	FromVersion   string `json:"from_version"`
	StartedAt     int64  `json:"started_at"`
	FinishedAt    *int64 `json:"finished_at,omitempty"`
	BackupFile    string `json:"backup_file,omitempty"`
	SchemaChanged bool   `json:"schema_changed"`
	Error         string `json:"error,omitempty"`
	Done          bool   `json:"done"`
}

// upgrader 串行化升级执行（并发 apply 返回 409）。
type upgrader struct {
	mu      sync.Mutex
	running bool
	job     *upgradeJob
}

// applyRequest 是 POST /system/update/apply 的请求体。
type applyRequest struct {
	Version    string `json:"version"`
	AllowMajor bool   `json:"allow_major"`
}

// handleApplyUpdate 处理 POST /api/v1/system/update/apply（仅 admin）。
func (a *API) handleApplyUpdate(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.UpgradeEnabled {
		writeError(w, http.StatusForbidden, codeForbidden,
			"控制端自升级已禁用（LEADSTACK_UPGRADE_ENABLED=false）")
		return
	}
	if reason := platformUpgradeUnsupported(); reason != "" {
		writeError(w, http.StatusForbidden, codeForbidden, reason)
		return
	}
	var req applyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	target := strings.TrimSpace(req.Version)
	if target == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "version 不能为空")
		return
	}

	// 并发保护：同一时刻只允许一个升级。
	if !a.upgrader.begin() {
		writeError(w, http.StatusConflict, codeConflict, "已有升级正在进行，请稍后再试")
		return
	}
	job := &upgradeJob{
		JobID: uuid.NewString(), Phase: "validating", TargetVersion: target,
		FromVersion: a.version, StartedAt: a.nowMS(),
	}
	a.upgrader.setJob(job)

	info := a.updates.Get(r.Context(), false)
	if info.Error != "" && info.Latest == "" {
		// 缓存里没有任何可用信息时强制再查一次（避免用户刚点升级就失败）。
		info = a.updates.Get(r.Context(), true)
	}
	asset, err := a.pickAsset(info, target)
	if err != nil {
		a.upgrader.finish(job, err)
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	if err := a.validateTargetVersion(target, req.AllowMajor); err != nil {
		a.upgrader.finish(job, err)
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}

	a.logger.Info("开始升级控制端", "from", a.version, "to", target, "job_id", job.JobID,
		"backup", true, "schema_changed", info.SchemaChanged)
	a.auditManual(r, "system.upgrade_start", "system", "",
		fmt.Sprintf(`{"from":%q,"to":%q,"job_id":%q,"schema_changed":%t}`,
			a.version, target, job.JobID, info.SchemaChanged))

	// 后台执行；HTTP 立即返回 202，让前端能持续轮询 status。
	go a.runUpgrade(job, asset, info.SchemaChanged, os.Args)

	writeData(w, http.StatusAccepted, map[string]any{"job_id": job.JobID, "status": job})
}

// handleUpdateStatus 处理 GET /api/v1/system/update/status。
func (a *API) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	job := a.upgrader.getJob()
	payload := map[string]any{
		"current_version": a.version,
		"upgrade_dir":     a.cfg.UpgradeDir(),
		"running":         a.upgrader.isRunning(),
	}
	if job != nil {
		payload["job"] = job
	}
	if state, err := ReadUpgradeState(upgradeStatePath(a.cfg.DataDir)); err == nil && state != nil {
		payload["pending_state"] = state
	}
	payload["history"] = ReadUpgradeHistory(a.cfg.DataDir, 10)
	writeData(w, http.StatusOK, payload)
}

// runUpgrade 执行契约 51.3 的严格流程：校验 → 备份 → 下载 → 校验和 → 解压 → 标记 → A/B 替换 → spawn watchdog → exec。
func (a *API) runUpgrade(job *upgradeJob, asset UpdateAsset, schemaChanged bool, execArgs []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	fail := func(phase string, err error) {
		a.upgrader.setPhase(job, phase)
		a.upgrader.finish(job, err)
		a.logger.Warn("升级中止", "phase", phase, "err", err, "job_id", job.JobID)
		a.auditSystem("system.upgrade_start",
			fmt.Sprintf(`{"from":%q,"to":%q,"job_id":%q,"aborted":true,"phase":%q,"error":%q}`,
				job.FromVersion, job.TargetVersion, job.JobID, phase, err.Error()))
	}

	// 1) 强制备份：失败即中止，绝不替换二进制（硬约束）。
	a.upgrader.setPhase(job, "backup")
	if a.backups == nil {
		fail("backup", errors.New("备份模块未启用，无法在升级前备份"))
		return
	}
	backupInfo, err := a.backups.Create(ctx, true)
	if err != nil {
		fail("backup", fmt.Errorf("升级前备份失败（已中止升级）：%w", err))
		return
	}
	a.upgrader.setBackupFile(job, backupInfo.Name)
	a.logger.Info("升级前备份完成", "backup", backupInfo.Name, "size_bytes", backupInfo.SizeBytes)

	// 2) 下载 + 校验 sha256。
	a.upgrader.setPhase(job, "download")
	archivePath, err := a.downloadReleaseAsset(ctx, asset, job.TargetVersion)
	if err != nil {
		fail("download", err)
		return
	}
	a.upgrader.setPhase(job, "verify_checksum")
	if err := a.verifyAssetChecksum(ctx, asset, archivePath); err != nil {
		fail("verify_checksum", err)
		return
	}

	// 3) 解压取二进制。
	a.upgrader.setPhase(job, "extract")
	newExe, err := a.extractBinary(archivePath, job.TargetVersion)
	if err != nil {
		fail("extract", err)
		return
	}

	// 4) 写升级标记（watchdog 与新进程自检共用）。
	a.upgrader.setPhase(job, "prepare")
	exe, err := os.Executable()
	if err != nil {
		fail("prepare", fmt.Errorf("定位当前可执行文件失败: %w", err))
		return
	}
	timeout := a.cfg.UpgradeRollbackTimeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	state := &UpgradeState{
		TargetVersion: job.TargetVersion,
		FromVersion:   job.FromVersion,
		PrevExe:       exe + upgradePrevSuffix,
		NewExe:        exe,
		BackupFile:    upgradeBackupPath(a.cfg.BackupDir, backupInfo.Name),
		SchemaChanged: schemaChanged,
		StartedAt:     a.nowMS(),
		Deadline:      a.nowMS() + timeout.Milliseconds(),
		Args:          execArgs,
		DataDir:       a.cfg.DataDir,
		ActiveListen:  a.activeListen,
	}
	statePath := upgradeStatePath(a.cfg.DataDir)
	if err := WriteUpgradeState(statePath, state); err != nil {
		fail("prepare", err)
		return
	}
	a.upgrader.setSchemaChanged(job, schemaChanged)

	// 5) A/B 替换（同一文件系统 rename，原子）。
	a.upgrader.setPhase(job, "swap")
	if err := swapExecutable(exe, newExe); err != nil {
		_ = RemoveUpgradeState(statePath)
		fail("swap", err)
		return
	}

	// 6) spawn 独立 watchdog，然后 exec。
	//
	// 关键细节：watchdog **必须从 <exe>.prev（旧版本）启动**，
	// 不能用 os.Executable() —— 此时 <exe> 已经是新版本，若子进程在 exec 前
	// 恰逢文件被 rename，它会 exec 到"新版本"（可能就是那个会崩溃的二进制），
	// watchdog 一起死掉，回退就没人做了。用 .prev 是确定性的。
	a.upgrader.setPhase(job, "spawn_watchdog")
	pid, err := spawnUpgradeWatchdog(exe+upgradePrevSuffix, a.cfg.DataDir, statePath, state)
	if err != nil {
		// watchdog 没起来：立刻把二进制换回去，避免无人看管的新版本在跑。
		_ = rollbackExecutable(exe)
		_ = RemoveUpgradeState(statePath)
		fail("spawn_watchdog", fmt.Errorf("启动升级 watchdog 失败（已回滚二进制）：%w", err))
		return
	}
	state.WatchdogPID = pid
	_ = WriteUpgradeState(statePath, state)
	a.logger.Info("升级 watchdog 已启动，即将切换到新版本",
		"watchdog_pid", pid, "target", job.TargetVersion, "deadline_ms", timeout.Milliseconds())

	a.upgrader.setPhase(job, "exec")
	a.logger.Warn("控制端即将切换到新版本", "from", job.FromVersion, "to", job.TargetVersion,
		"backup", backupInfo.Name, "watchdog_pid", pid)

	// 给 HTTP 响应/日志一点落盘时间再 exec。
	time.Sleep(500 * time.Millisecond)
	// argv[0] 必须是可执行文件路径，否则新进程的 os.Args[0] 会变成别的参数（导致启动异常）。
	argv := []string{exe}
	if len(execArgs) > 1 {
		argv = append(argv, execArgs[1:]...)
	}
	if err := execSelf(exe, argv); err != nil {
		_ = rollbackExecutable(exe)
		_ = RemoveUpgradeState(statePath)
		fail("exec", fmt.Errorf("切换可执行文件失败（已回滚）：%w", err))
	}
	// exec 成功时不会走到这里。
}

// pickAsset 为当前平台挑选下载产物。
func (a *API) pickAsset(info UpdateInfo, target string) (UpdateAsset, error) {
	if info.Latest != "" && info.Latest != target {
		return UpdateAsset{}, fmt.Errorf("目标版本 %s 与最新版本 %s 不一致，请先刷新更新信息", target, info.Latest)
	}
	for _, asset := range info.Assets {
		if asset.OS == runtime.GOOS && asset.Arch == runtime.GOARCH {
			return asset, nil
		}
	}
	return UpdateAsset{}, fmt.Errorf("发行版中没有适用于当前平台 %s/%s 的产物", runtime.GOOS, runtime.GOARCH)
}

// validateTargetVersion 校验版本可升级性（含大版本跳跃限制）。
func (a *API) validateTargetVersion(target string, allowMajor bool) error {
	cand, err := ParseSemver(target)
	if err != nil {
		return err
	}
	cur, err := ParseSemver(a.version)
	if err != nil {
		return nil // 当前版本非语义化（dev）：允许升级
	}
	if cand.Compare(cur) <= 0 {
		return fmt.Errorf("目标版本 %s 不高于当前版本 %s", target, a.version)
	}
	if cand.Major != cur.Major && !allowMajor {
		return fmt.Errorf("不允许跨大版本升级（%d → %d）：如确认需要，请在请求中显式传 allow_major=true",
			cur.Major, cand.Major)
	}
	return nil
}

// downloadReleaseAsset 下载产物到 <data_dir>/upgrade/<version>.tar.gz（临时文件 + 原子 rename）。
func (a *API) downloadReleaseAsset(ctx context.Context, asset UpdateAsset, version string) (string, error) {
	dir := a.cfg.UpgradeDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("创建升级目录失败: %w", err)
	}
	safeVersion := strings.NewReplacer("/", "_", "..", "_").Replace(version)
	dst := filepath.Join(dir, safeVersion+".tar.gz")
	tmp := dst + ".tmp"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return "", fmt.Errorf("构造下载请求失败: %w", err)
	}
	req.Header.Set("User-Agent", "leadstack-updater")
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return "", fmt.Errorf("下载升级包失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载升级包失败：HTTP %d", resp.StatusCode)
	}
	file, err := os.Create(tmp)
	if err != nil {
		return "", fmt.Errorf("创建临时文件失败: %w", err)
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(tmp)
	}()
	written, err := io.Copy(file, resp.Body)
	if err != nil {
		return "", fmt.Errorf("写入升级包失败: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("同步升级包失败: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("关闭升级包失败: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", fmt.Errorf("落盘升级包失败: %w", err)
	}
	a.logger.Info("升级包已下载", "path", dst, "bytes", written)
	return dst, nil
}

// verifyAssetChecksum 下载 checksums.txt 并校验升级包的 sha256。
func (a *API) verifyAssetChecksum(ctx context.Context, asset UpdateAsset, archivePath string) error {
	checksumsURL := checksumsURLFor(asset)
	if checksumsURL == "" {
		return errors.New("发行版缺少 checksums.txt，无法校验升级包完整性（已中止）")
	}
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumsURL, nil)
	if err != nil {
		return fmt.Errorf("构造校验和请求失败: %w", err)
	}
	req.Header.Set("User-Agent", "leadstack-updater")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("下载 checksums.txt 失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 checksums.txt 失败：HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("读取 checksums.txt 失败: %w", err)
	}
	want := lookupChecksum(string(raw), filepath.Base(asset.URL))
	if want == "" {
		// 有些项目用 asset.Name 作为键。
		if asset.Name != "" {
			want = lookupChecksum(string(raw), asset.Name)
		}
	}
	if want == "" {
		return fmt.Errorf("checksums.txt 中找不到 %s 的校验和（已中止）", filepath.Base(asset.URL))
	}
	got, err := fileSHA256(archivePath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("升级包校验失败：期望 %s，实际 %s（文件可能被篡改或下载不完整）", want, got)
	}
	return nil
}

// checksumsURLFor 推导 checksums.txt 的地址（与 asset 同目录）。
func checksumsURLFor(asset UpdateAsset) string {
	idx := strings.LastIndex(asset.URL, "/")
	if idx <= 0 {
		return ""
	}
	return asset.URL[:idx+1] + upgradeChecksumsName
}

// lookupChecksum 在 checksums.txt 里查找某个文件名的 sha256。
func lookupChecksum(content, filename string) string {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == filename || filepath.Base(name) == filename {
			return fields[0]
		}
	}
	return ""
}

// fileSHA256 计算文件 sha256（十六进制小写）。
func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开文件 %s 失败: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("读取文件 %s 失败: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// extractBinary 从 tar.gz 中解出 leadstack 可执行文件。
func (a *API) extractBinary(archivePath, version string) (string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("打开升级包失败: %w", err)
	}
	defer func() { _ = file.Close() }()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("解压升级包失败: %w", err)
	}
	defer func() { _ = gz.Close() }()

	if err := os.MkdirAll(a.cfg.UpgradeDir(), 0o750); err != nil {
		return "", fmt.Errorf("创建升级目录失败: %w", err)
	}
	safeVersion := strings.NewReplacer("/", "_", "..", "_").Replace(version)
	dst := filepath.Join(a.cfg.UpgradeDir(), "leadstack-"+safeVersion)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", fmt.Errorf("创建新二进制失败: %w", err)
	}
	succeeded := false
	defer func() {
		_ = out.Close()
		if !succeeded {
			_ = os.Remove(dst) // 失败路径清理；成功则由调用方持有该文件
		}
	}()

	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("遍历升级包失败: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.Base(header.Name)
		if name != "leadstack" && name != "leadstack.exe" {
			continue
		}
		if _, err := io.Copy(out, io.LimitReader(reader, 200<<20)); err != nil {
			return "", fmt.Errorf("写出新二进制失败: %w", err)
		}
		if err := out.Sync(); err != nil {
			return "", fmt.Errorf("同步新二进制失败: %w", err)
		}
		if err := out.Close(); err != nil {
			return "", fmt.Errorf("关闭新二进制失败: %w", err)
		}
		if err := os.Chmod(dst, 0o755); err != nil {
			return "", fmt.Errorf("设置可执行权限失败: %w", err)
		}
		if !isExecutable(dst) {
			return "", fmt.Errorf("升级包中的 leadstack 不是可执行文件（已中止）")
		}
		succeeded = true
		return dst, nil
	}
	return "", errors.New("升级包里没有找到 leadstack 可执行文件（已中止）")
}

// isExecutable 判断文件是否可执行。
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// upgradeBackupPath 把备份名解析成绝对路径。
func upgradeBackupPath(backupDir, name string) string {
	if name == "" {
		return ""
	}
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(backupDir, name)
}

// ------------------------------------------------ upgrader 状态机 ------------------------------------------------

// begin 标记开始（已有升级在跑时返回 false）。
func (u *upgrader) begin() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running {
		return false
	}
	u.running = true
	return true
}

// setJob 记录当前任务。
func (u *upgrader) setJob(job *upgradeJob) {
	u.mu.Lock()
	u.job = job
	u.mu.Unlock()
}

// setPhase 更新阶段。
// setBackupFile 在锁内更新升级前备份文件名。
// 注意：job 指针同时被状态接口读取（getJob 在锁内做值拷贝），
// 因此所有对 job 字段的写入都必须走这些 setter，不能直接赋值。
func (u *upgrader) setBackupFile(job *upgradeJob, name string) {
	u.mu.Lock()
	if u.job != nil && u.job.JobID == job.JobID {
		u.job.BackupFile = name
	}
	u.mu.Unlock()
}

// setSchemaChanged 在锁内更新「本次升级是否包含数据库迁移」。
func (u *upgrader) setSchemaChanged(job *upgradeJob, changed bool) {
	u.mu.Lock()
	if u.job != nil && u.job.JobID == job.JobID {
		u.job.SchemaChanged = changed
	}
	u.mu.Unlock()
}

func (u *upgrader) setPhase(job *upgradeJob, phase string) {
	u.mu.Lock()
	if u.job != nil && u.job.JobID == job.JobID {
		u.job.Phase = phase
	}
	u.mu.Unlock()
}

// finish 结束任务（置 done 并记录错误）。
func (u *upgrader) finish(job *upgradeJob, cause error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.job != nil && u.job.JobID == job.JobID {
		now := time.Now().UnixMilli()
		u.job.FinishedAt = &now
		u.job.Done = true
		if cause != nil {
			u.job.Error = cause.Error()
		}
	}
	u.running = false
}

// getJob 返回当前任务快照。
func (u *upgrader) getJob() *upgradeJob {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.job == nil {
		return nil
	}
	copied := *u.job
	return &copied
}

// isRunning 返回是否有升级在跑。
func (u *upgrader) isRunning() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.running
}

// ------------------------------------------------ 新进程自检 ------------------------------------------------

// SelfCheckResult 是新进程自检的结果。
type SelfCheckResult struct {
	Passed          bool   `json:"passed"`
	IntegrityOK     bool   `json:"integrity_ok"`
	SchemaVersion   int    `json:"schema_version"`
	SchemaOK        bool   `json:"schema_ok"`
	DataDirWritable bool   `json:"data_dir_writable"`
	Message         string `json:"message"`
}

// RunUpgradeSelfCheck 在新进程启动并监听成功后执行自检（契约 51.3）。
//
// 全部通过 → 删除 state.json（watchdog 据此认定成功）；任一失败 → 返回 Passed=false，
// 调用方应退出进程，让 watchdog 回退到旧版本（含数据恢复）。
//
// 这里用独立只读连接查库（不依赖调用方已打开的 store），因此可以在"开库之前"或"开库之后"安全调用。
func RunUpgradeSelfCheck(ctx context.Context, dbPath, dataDir string, supportedSchema int) (*SelfCheckResult, *UpgradeState, error) {
	statePath := upgradeStatePath(dataDir)
	state, err := ReadUpgradeState(statePath)
	if err != nil {
		return nil, nil, err
	}
	if state == nil {
		return nil, nil, nil // 没有待确认的升级
	}
	result := &SelfCheckResult{}

	db, derr := sql.Open("sqlite", dbPath)
	if derr != nil {
		result.Message = fmt.Sprintf("打开数据库失败: %v", derr)
	} else {
		defer func() { _ = db.Close() }()
		checkCtx, cancel := context.WithTimeout(ctx, upgradeSelfCheckTimeout)
		defer cancel()

		// 1) PRAGMA integrity_check
		var integrity string
		if qerr := db.QueryRowContext(checkCtx, "PRAGMA integrity_check").Scan(&integrity); qerr != nil {
			result.Message = fmt.Sprintf("数据库完整性检查执行失败: %v", qerr)
		} else if !strings.EqualFold(strings.TrimSpace(integrity), "ok") {
			result.Message = fmt.Sprintf("数据库完整性检查未通过: %s", integrity)
		} else {
			result.IntegrityOK = true
		}

		// 2) schema 版本 == 本二进制支持版本
		var version int
		if qerr := db.QueryRowContext(checkCtx,
			"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); qerr != nil {
			if result.Message == "" {
				result.Message = fmt.Sprintf("读取 schema 版本失败: %v", qerr)
			}
		} else {
			result.SchemaVersion = version
			if supportedSchema > 0 && version != supportedSchema {
				if result.Message == "" {
					result.Message = fmt.Sprintf("schema 版本不匹配：库里 %d，本二进制支持 %d", version, supportedSchema)
				}
			} else {
				result.SchemaOK = true
			}
		}
	}

	// 3) 数据目录可写
	probe := filepath.Join(dataDir, ".upgrade-selfcheck")
	if werr := os.WriteFile(probe, []byte("ok"), 0o600); werr != nil {
		if result.Message == "" {
			result.Message = fmt.Sprintf("数据目录不可写: %v", werr)
		}
	} else {
		_ = os.Remove(probe)
		result.DataDirWritable = true
	}

	result.Passed = result.IntegrityOK && result.SchemaOK && result.DataDirWritable
	if result.Passed {
		if rerr := RemoveUpgradeState(statePath); rerr != nil {
			return result, state, rerr
		}
	}
	return result, state, nil
}

// auditSystem 记录一条系统动作审计（无 HTTP 请求上下文时使用）。
func (a *API) auditSystem(action, detail string) {
	if a == nil {
		return
	}
	resourceType := "system"
	a.writeAudit(&store.AuditLog{
		TS: a.nowMS(), Actor: "system", Action: action,
		ResourceType: &resourceType, Detail: &detail,
	})
}

// AuditUpgradeSuccess 记录升级成功审计（新进程自检通过后调用）。
func (a *API) AuditUpgradeSuccess(from, to string) {
	if a == nil {
		return
	}
	detail := fmt.Sprintf(`{"from":%q,"to":%q,"confirmed":true}`, from, to)
	resourceType := "system"
	a.writeAudit(&store.AuditLog{
		TS: a.nowMS(), Actor: "system", Action: "system.upgrade_success",
		ResourceType: &resourceType, Detail: &detail,
	})
}

// 保证 encoding/json 与 strconv 的引用（供未来的状态序列化扩展）。
var _ = json.Marshal
var _ = strconv.Itoa
