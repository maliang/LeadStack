package agent

// 自升级，契约见 docs/ARCHITECTURE.md 第 44 节。
//
// 安全约束（硬要求）：
//   - 当前正在运行的可执行文件不能原地覆盖，必须使用
//     「当前文件 rename 成 .old → 新文件 rename 到正式路径」的 A/B 双文件替换；
//   - 下载失败、sha256 校验失败、rename 失败都必须保留旧版本并保持连接；
//   - Windows 上运行中的 exe 不能删除/覆盖，但可以 rename，因此同一套
//     rename 方案在两个平台都成立；遗留的 .old 在下次启动时清理。
//
// 结果回报：协议没有专门的升级回执类型，这里复用 job.log + job.done，
// run_id 使用保留前缀 "agent.upgrade:"，backend 可据此识别并写审计/推送前端。

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/maliang/leadstack/pkg/protocol"
)

const (
	// upgradeRunIDPrefix 是升级结果帧的保留 run_id 前缀。
	upgradeRunIDPrefix = "agent.upgrade:"
	// upgradeMaxBytes 是升级包大小上限。
	upgradeMaxBytes = 256 << 20 // 256 MiB
	// upgradeHTTPTimeout 是下载升级包的整体超时。
	upgradeHTTPTimeout = 5 * time.Minute
	// upgradeRestartDelay 是发出结果帧后、重启前的等待时间，确保帧已刷出。
	upgradeRestartDelay = 300 * time.Millisecond
)

// renameFile 是 os.Rename 的间接引用，单测可替换以模拟 rename 失败。
var renameFile = os.Rename

// upgradeOutcome 描述一次升级尝试的结果。
type upgradeOutcome struct {
	// Skipped 表示当前版本已是目标版本，未做任何文件替换。
	Skipped bool
	// ExePath 是正式可执行文件路径。
	ExePath string
	// OldPath 是旧版本备份路径（exePath + ".old"）。
	OldPath string
}

// upgrader 负责处理 agent.upgrade 指令。
type upgrader struct {
	log *slog.Logger

	mu   sync.Mutex
	busy bool

	// 以下字段可在测试中注入。
	exePath    string             // 为空时运行时通过 os.Executable 解析
	httpClient *http.Client       // 为空时用默认客户端
	restart    func(string) error // 重启实现
}

// newUpgrader 创建升级器。
func newUpgrader(log *slog.Logger) *upgrader {
	if log == nil {
		log = slog.Default()
	}
	u := &upgrader{
		log:        log,
		httpClient: &http.Client{Timeout: upgradeHTTPTimeout},
		restart:    restartSelf,
	}
	// 启动时顺手清理上次升级遗留的 .old（Windows 上旧进程退出后才能删除）。
	u.cleanupOldExecutable()
	return u
}

// cleanupOldExecutable 尽力清理上次升级遗留的 exePath.old 文件。
func (u *upgrader) cleanupOldExecutable() {
	exePath, err := u.resolveExePath()
	if err != nil {
		return
	}
	oldPath := exePath + ".old"
	if err := os.Remove(oldPath); err == nil {
		u.log.Info("已清理上次升级遗留的旧版本文件", "path", oldPath)
	}
}

// handleUpgrade 在独立协程中处理一条 agent.upgrade 指令。
// 任何失败都不会影响连接：只回报失败原因并保留旧版本继续运行。
func (a *Agent) handleUpgrade(ctx context.Context, w *wsConn, spec protocol.AgentUpgrade) {
	runID := upgradeRunIDPrefix + spec.Version
	a.log.Info("收到 agent.upgrade 指令",
		"target_version", spec.Version,
		"url", spec.URL,
		"restart", spec.Restart,
		"current_version", a.cfg.Version)

	out, err := a.upgrader.perform(ctx, a.cfg.Version, spec)
	if err != nil {
		a.log.Warn("agent 升级失败，已保留旧版本并继续运行", "err", err, "target_version", spec.Version)
		a.notifyUpgrade(ctx, w, runID, false, fmt.Sprintf("升级失败（已保留旧版本）：%v", err))
		return
	}
	if out.Skipped {
		a.log.Info("agent 已是目标版本，跳过升级", "version", spec.Version)
		a.notifyUpgrade(ctx, w, runID, true, fmt.Sprintf("当前已是版本 %s，无需升级", spec.Version))
		return
	}

	a.log.Info("agent 升级文件已就位",
		"target_version", spec.Version, "exe", out.ExePath, "old", out.OldPath)
	if !spec.Restart {
		a.notifyUpgrade(ctx, w, runID, true,
			fmt.Sprintf("已更新到版本 %s，将在下次重启后生效（旧版本备份：%s）", spec.Version, out.OldPath))
		return
	}

	// 先把结果帧发给控制端，再重启；重启后连接会断开并由 agent 重新 hello。
	a.notifyUpgrade(ctx, w, runID, true,
		fmt.Sprintf("已更新到版本 %s，正在重启", spec.Version))
	time.Sleep(upgradeRestartDelay)
	if err := a.upgrader.restart(out.ExePath); err != nil {
		// 旧版本文件已替换为新版本，但进程未重启：继续以旧代码运行并明确告知。
		a.log.Error("升级后重启失败，请手动重启服务", "err", err, "exe", out.ExePath)
		a.notifyUpgrade(ctx, w, runID, false,
			fmt.Sprintf("升级完成但重启失败，请手动重启服务：%v", err))
	}
}

// notifyUpgrade 用 job.log + job.done 回报升级结果。
// run_id 使用保留前缀 agent.upgrade:<version>，成功时 exit_code=0，失败时 -1 并带 Error。
func (a *Agent) notifyUpgrade(ctx context.Context, w *wsConn, runID string, ok bool, message string) {
	stream := "stdout"
	exitCode := 0
	errMsg := ""
	if !ok {
		stream = "stderr"
		exitCode = -1
		errMsg = message
	}
	_ = w.send(ctx, newEnvelope(protocol.TypeJobLog, protocol.JobLog{
		RunID:  runID,
		Seq:    0,
		Stream: stream,
		Chunk:  message + "\n",
	}))
	_ = w.send(ctx, newEnvelope(protocol.TypeJobDone, protocol.JobDone{
		RunID:    runID,
		ExitCode: exitCode,
		Error:    errMsg,
	}))
}

// perform 执行一次升级：校验参数 → 下载到临时文件 → sha256 校验 → 解包 → A/B 替换。
// 升级包可以是 tar.gz（gzip 魔数）、zip（PK 魔数）或裸二进制；sha256 始终校验下载到的
// 归档本体，解出的 leadstack-agent 再走与裸二进制相同的替换流程。
// 返回 nil error 表示文件替换成功（可能需要重启才生效）。
func (u *upgrader) perform(ctx context.Context, currentVersion string, spec protocol.AgentUpgrade) (*upgradeOutcome, error) {
	// 同一时刻只允许一个升级任务。
	u.mu.Lock()
	if u.busy {
		u.mu.Unlock()
		return nil, errors.New("已有升级任务正在进行")
	}
	u.busy = true
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.busy = false
		u.mu.Unlock()
	}()

	sha, err := normalizeSHA256(spec.SHA256)
	if err != nil {
		return nil, err
	}
	if err := validateUpgradeURL(spec.URL); err != nil {
		return nil, err
	}
	if spec.Version != "" && spec.Version == currentVersion {
		return &upgradeOutcome{Skipped: true}, nil
	}

	exePath, err := u.resolveExePath()
	if err != nil {
		return nil, err
	}

	dir := filepath.Dir(exePath)
	// 升级包先落到与正式文件同目录的临时文件：保证后续 rename 是同一文件系统内的原子操作。
	pkg, err := os.CreateTemp(dir, ".leadstack-agent-pkg-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时文件失败（目录不可写？）: %w", err)
	}
	pkgPath := pkg.Name()
	// 裸二进制会被 rename 走，归档与失败路径则由这里清理（not exist 忽略）。
	defer func() { _ = os.Remove(pkgPath) }()

	if err := u.downloadWithFallback(ctx, spec.URL, spec.FallbackURL, sha, pkg); err != nil {
		_ = pkg.Close()
		return nil, err
	}
	if err := pkg.Sync(); err != nil {
		_ = pkg.Close()
		return nil, fmt.Errorf("刷新临时文件失败: %w", err)
	}
	if err := pkg.Close(); err != nil {
		return nil, fmt.Errorf("关闭临时文件失败: %w", err)
	}

	// 按魔数识别发布包形态：gzip(tar.gz) / zip / 裸二进制。
	// 坏归档或缺条目在这里失败，旧版本与运行中的连接都不受影响。
	binPath, err := extractAgentBinary(pkgPath, dir)
	if err != nil {
		return nil, err
	}
	if binPath != pkgPath {
		// 解出的文件被 rename 成正式文件后这里已不存在；失败路径借此清理残骸。
		defer func() { _ = os.Remove(binPath) }()
	}
	// 新文件需要可执行权限；Windows 上 chmod 无副作用。
	if err := os.Chmod(binPath, 0o755); err != nil {
		return nil, fmt.Errorf("设置新版本可执行权限失败: %w", err)
	}

	oldPath, err := replaceExecutable(exePath, binPath)
	if err != nil {
		return nil, err
	}
	return &upgradeOutcome{ExePath: exePath, OldPath: oldPath}, nil
}

// downloadWithFallback 先试主地址，失败后试备用镜像。
//
// 每次尝试前 truncate+seek，避免主地址留下的半截数据混进备用源的计算。
// 备用地址非法只记 warning，不阻止主地址继续（老控制端下发的自定义地址可能不匹配）。
func (u *upgrader) downloadWithFallback(ctx context.Context, primary, fallback, wantSHA string, dst *os.File) error {
	urls := []string{primary}
	if raw := strings.TrimSpace(fallback); raw != "" && raw != primary {
		if err := validateUpgradeURL(raw); err != nil {
			u.log.Warn("升级备用地址非法，忽略", "url", raw, "err", err)
		} else {
			urls = append(urls, raw)
		}
	}
	var errs []string
	for _, rawURL := range urls {
		if err := dst.Truncate(0); err != nil {
			return fmt.Errorf("重置临时文件失败: %w", err)
		}
		if _, err := dst.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("重置临时文件偏移失败: %w", err)
		}
		if err := u.download(ctx, rawURL, wantSHA, dst); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", rawURL, err))
			if len(urls) > 1 {
				u.log.Warn("升级包下载失败，尝试备用镜像", "url", rawURL, "err", err)
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("下载升级包失败（已尝试 %d 个源）：%s", len(urls), strings.Join(errs, "；"))
}

// download 把升级包下载到 dst，同时计算并校验 sha256（hex 小写）。
func (u *upgrader) download(ctx context.Context, rawURL, wantSHA string, dst *os.File) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("构造下载请求失败: %w", err)
	}
	client := u.httpClient
	if client == nil {
		client = &http.Client{Timeout: upgradeHTTPTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("下载升级包失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载升级包失败: HTTP %d", resp.StatusCode)
	}

	hasher := sha256.New()
	limited := io.LimitReader(resp.Body, upgradeMaxBytes+1)
	n, err := io.Copy(io.MultiWriter(dst, hasher), limited)
	if err != nil {
		return fmt.Errorf("下载升级包中断: %w", err)
	}
	if n > upgradeMaxBytes {
		return fmt.Errorf("升级包超过上限（%d 字节）", upgradeMaxBytes)
	}
	if n == 0 {
		return errors.New("升级包为空")
	}

	got := hasher.Sum(nil)
	want, err := hex.DecodeString(wantSHA)
	if err != nil {
		return fmt.Errorf("sha256 格式非法: %w", err)
	}
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return fmt.Errorf("sha256 校验失败：期望 %s，实际 %s", wantSHA, hex.EncodeToString(got))
	}
	return nil
}

// 归档魔数：gzip 为 1f 8b；zip 以 "PK" 开头（本地文件头/空归档/分卷标记都是 PK 前缀）。
var (
	archiveGzipMagic = []byte{0x1f, 0x8b}
	archiveZipMagic  = []byte("PK")
)

// errAgentBinaryMissing 表示归档本身可读，但里面没有 agent 可执行文件。
var errAgentBinaryMissing = errors.New("升级包中未找到 leadstack-agent 可执行文件（已中止）")

// extractAgentBinary 按魔数识别升级包形态，返回可直接交给 A/B 替换的可执行文件路径：
//   - gzip（.tar.gz）：解压后遍历 tar，取 basename 为 leadstack-agent[/.exe] 的常规文件；
//   - zip（Windows 发布包）：取同名条目；
//   - 其它：视为裸二进制，原样返回 pkgPath，保持旧的直传升级方式。
//
// 解出的文件写在 dir 下的临时文件里（与正式文件同目录，保证 rename 原子），权限 0755。
// 坏归档与缺条目都返回可定位的错误，调用方据此保留旧版本、不换文件。
func extractAgentBinary(pkgPath, dir string) (string, error) {
	file, err := os.Open(pkgPath)
	if err != nil {
		return "", fmt.Errorf("打开升级包失败: %w", err)
	}
	head := make([]byte, 4)
	n, err := io.ReadFull(file, head)
	_ = file.Close()
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", fmt.Errorf("读取升级包失败: %w", err)
	}
	head = head[:n]

	switch {
	case bytes.HasPrefix(head, archiveGzipMagic):
		return extractAgentFromTarGz(pkgPath, dir)
	case bytes.HasPrefix(head, archiveZipMagic):
		return extractAgentFromZip(pkgPath, dir)
	default:
		return pkgPath, nil
	}
}

// extractAgentFromTarGz 从 gzip 压缩的 tar 包中解出 agent 可执行文件。
func extractAgentFromTarGz(pkgPath, dir string) (string, error) {
	file, err := os.Open(pkgPath)
	if err != nil {
		return "", fmt.Errorf("打开升级包失败: %w", err)
	}
	defer func() { _ = file.Close() }()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("解压升级包失败（gzip 头非法）: %w", err)
	}
	defer func() { _ = gz.Close() }()

	reader := tar.NewReader(gz)
	for {
		hdr, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return "", errAgentBinaryMissing
		}
		if err != nil {
			return "", fmt.Errorf("遍历升级包失败（tar 已损坏）: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || !isAgentBinaryName(hdr.Name) {
			continue
		}
		if hdr.Size <= 0 || hdr.Size > upgradeMaxBytes {
			return "", fmt.Errorf("升级包内 leadstack-agent 大小非法（%d 字节）", hdr.Size)
		}
		return writeExtractedAgent(dir, reader, hdr.Size)
	}
}

// extractAgentFromZip 从 zip 包中解出 agent 可执行文件。
func extractAgentFromZip(pkgPath, dir string) (string, error) {
	zr, err := zip.OpenReader(pkgPath)
	if err != nil {
		return "", fmt.Errorf("解压升级包失败（zip 结构非法）: %w", err)
	}
	defer func() { _ = zr.Close() }()

	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !isAgentBinaryName(f.Name) {
			continue
		}
		if f.UncompressedSize64 == 0 || f.UncompressedSize64 > uint64(upgradeMaxBytes) {
			return "", fmt.Errorf("升级包内 leadstack-agent 大小非法（%d 字节）", f.UncompressedSize64)
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("解压升级包内 leadstack-agent 失败: %w", err)
		}
		path, err := writeExtractedAgent(dir, rc, int64(f.UncompressedSize64))
		_ = rc.Close()
		return path, err
	}
	return "", errAgentBinaryMissing
}

// writeExtractedAgent 把归档里的可执行文件写成 dir 下的临时文件（0755），返回其路径。
func writeExtractedAgent(dir string, r io.Reader, size int64) (string, error) {
	out, err := os.CreateTemp(dir, ".leadstack-agent-new-*")
	if err != nil {
		return "", fmt.Errorf("创建临时文件失败（目录不可写？）: %w", err)
	}
	path := out.Name()
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()

	if err := out.Chmod(0o755); err != nil {
		return "", fmt.Errorf("设置临时文件权限失败: %w", err)
	}
	n, err := io.Copy(out, io.LimitReader(r, size))
	if err != nil {
		return "", fmt.Errorf("解出 leadstack-agent 失败: %w", err)
	}
	if n != size {
		return "", fmt.Errorf("解出 leadstack-agent 失败：内容不完整（%d/%d 字节）", n, size)
	}
	if err := out.Sync(); err != nil {
		return "", fmt.Errorf("刷新新版本文件失败: %w", err)
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("关闭新版本文件失败: %w", err)
	}
	ok = true
	return path, nil
}

// isAgentBinaryName 判断归档条目是否为 agent 可执行文件（允许目录前缀与 Windows 的 .exe 后缀）。
func isAgentBinaryName(name string) bool {
	base := filepath.Base(name)
	return base == "leadstack-agent" || base == "leadstack-agent.exe"
}

// resolveExePath 返回当前可执行文件的真实路径（解析符号链接，替换真实文件）。
func (u *upgrader) resolveExePath() (string, error) {
	if u.exePath != "" {
		return u.exePath, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("获取当前可执行文件路径失败: %w", err)
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe, nil
}

// replaceExecutable 执行 A/B 双文件替换：exePath → exePath.old，tmpPath → exePath。
//
// 这是「永不弄坏 agent」的核心：
//   - 两次 rename 都是同目录下的原子操作；
//   - 如果第二次 rename 失败，立刻把 .old 回滚成正式文件，保证 exePath 仍指向旧版本；
//   - 只有全部成功才返回，此时旧版本完整保留在 .old 中。
func replaceExecutable(exePath, tmpPath string) (string, error) {
	oldPath := exePath + ".old"

	// 清理上次遗留的 .old（Windows 上上次运行的 exe 退出后才能删除）。
	if err := os.Remove(oldPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("清理旧备份 %s 失败: %w", oldPath, err)
	}

	if err := renameFile(exePath, oldPath); err != nil {
		return "", fmt.Errorf("备份当前可执行文件失败（未做任何修改）: %w", err)
	}
	if err := renameFile(tmpPath, exePath); err != nil {
		// 回滚：把旧文件 rename 回正式路径，保证 agent 仍可执行旧版本。
		if rbErr := renameFile(oldPath, exePath); rbErr != nil {
			return "", fmt.Errorf("替换可执行文件失败且回滚失败，需人工恢复（%s -> %s）: %v / %v",
				oldPath, exePath, err, rbErr)
		}
		return "", fmt.Errorf("替换可执行文件失败，已回滚到旧版本: %w", err)
	}
	return oldPath, nil
}

// normalizeSHA256 校验并归一化 sha256：必须为 64 位 hex，返回小写形式。
func normalizeSHA256(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", errors.New("缺少 sha256 校验值，拒绝升级")
	}
	if len(s) != 64 {
		return "", fmt.Errorf("sha256 长度非法（%d，期望 64）", len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", fmt.Errorf("sha256 不是合法 hex: %w", err)
	}
	return s, nil
}

// validateUpgradeURL 只允许 http/https 下载地址。
func validateUpgradeURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("缺少下载地址")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("下载地址非法: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("不支持的下载协议 %q（仅允许 http/https）", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("下载地址缺少主机名")
	}
	return nil
}
