package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// 上传升级包的体积约束。
//
// 用 var 而不是 const：单测要把上限缩到 KB 级（否则每个 413 用例都要真写 200 MiB），
// 默认值就是生产口径，由 TestUpgradeUploadLimitsAreProductionValues 钉住。
var (
	// upgradeUploadMinBytes 是上传二进制的下限：控制端二进制不可能小于 1 MiB，
	// 低于它的输入一定不是「解压后的 leadstack 二进制」。
	upgradeUploadMinBytes = int64(1) << 20
	// upgradeUploadMaxBytes 是上传二进制的上限（200 MiB），防止一次请求把磁盘写满。
	upgradeUploadMaxBytes = int64(200) << 20
)

const (
	// upgradeUploadDrainSlack 是「已知超限」后继续排空请求体的余量：
	// 只为把 413 文案里的实际字节数算准，不落盘。
	upgradeUploadDrainSlack = int64(8) << 20
	// upgradeUploadProbeTimeout 是「执行上传的二进制读版本」的超时。
	upgradeUploadProbeTimeout = 15 * time.Second
	// notExecutableMessage 是魔数不符时的统一文案。
	notExecutableMessage = "不是可执行文件（需要解压后的 leadstack 二进制，不要给 .tar.gz）"
)

// probeUploadedBinaryVersion 是「执行上传的二进制读版本」的注入点（nil = 真实执行）。
//
// 存在的理由：用例必须能构造「魔数正确、但 --version 输出不符合格式」这类输入，
// 而这样的文件不可能真的被 exec 成功（魔数对、内容假）。
// 传 nil 时行为就是真实执行，生产路径不受影响。
var probeUploadedBinaryVersion func(path string) (string, error)

// executableMagics 是允许的上传文件魔数（按文件里的字节顺序）。
//
// 覆盖 ELF 与 Mach-O：
//
//	ELF:          0x7f 'E' 'L' 'F'
//	Mach-O 64 位: 0xfeedfacf（小端落盘 cf fa ed fe）/ 0xcffaedfe（大端落盘 cf fa ed fe）
//	Mach-O 32 位: 0xfeedface（小端落盘 ce fa ed fe）/ 0xcefaedfe（大端落盘 ce fa ed fe）
//
// 后四个常量互为字节反序，落盘后只有两条不同序列，所以表里只有三项。
var executableMagics = [][]byte{
	{0x7f, 'E', 'L', 'F'},
	{0xcf, 0xfa, 0xed, 0xfe},
	{0xce, 0xfa, 0xed, 0xfe},
}

// uploadError 是「接收/校验上传」阶段的拒绝原因（已带 HTTP 状态与错误码）。
type uploadError struct {
	status int
	code   string
	msg    string
}

func (e *uploadError) Error() string { return e.msg }

// receivedUpload 是一次上传成功落盘的结果。
type receivedUpload struct {
	Path       string
	Bytes      int64
	SHA256     string
	AllowMajor bool
}

// handleUploadUpdate 处理 POST /api/v1/system/update/upload。
//
// 与 POST /system/update/apply（从 GitHub 下载资产）的差别只有「新二进制从哪来」：
// 这里由管理员直接把解压后的二进制上传上来，之后完全复用同一条
// 「备份 → 写标记 → A/B 替换 → spawn watchdog → exec」链路。
// 权限要求与 apply 同格：system.manage + 全局资源范围。
func (a *API) handleUploadUpdate(w http.ResponseWriter, r *http.Request) {
	// 1) 自升级总开关。
	if !a.cfg.UpgradeEnabled {
		writeError(w, http.StatusForbidden, codeForbidden,
			"控制端自升级已禁用（LEADSTACK_UPGRADE_ENABLED=false）")
		return
	}
	// 2) 平台能力（Windows 不支持控制端自升级）。
	if reason := platformUpgradeUnsupported(); reason != "" {
		writeError(w, http.StatusForbidden, codeForbidden, reason)
		return
	}

	uploadDir := a.cfg.UpgradeDir()
	if err := os.MkdirAll(uploadDir, 0o750); err != nil {
		a.internalError(w, r, fmt.Errorf("创建升级目录: %w", err))
		return
	}
	jobID := uuid.NewString()
	dst := filepath.Join(uploadDir, "upload-"+jobID+".bin")

	// 3) 接收文件（multipart 形态与体积在这里把关）。
	upload, err := a.receiveUploadedBinary(w, r, dst)
	if err != nil {
		var reject *uploadError
		if errors.As(err, &reject) {
			writeError(w, reject.status, reject.code, reject.msg)
			return
		}
		a.internalError(w, r, err)
		return
	}

	// 4)~7) 魔数 → 可执行 → 版本。全部在 begin() 之前完成，失败不占坑。
	version, err := validateUploadedBinary(upload.Path, a.version, upload.AllowMajor)
	if err != nil {
		_ = os.Remove(upload.Path)
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}

	// 8) 并发保护：同一时刻只允许一个升级任务。
	// 必须放在所有校验之后——校验失败不能把 upgrader 占住（否则后续合法请求全 409）。
	if !a.upgrader.begin() {
		_ = os.Remove(upload.Path)
		writeError(w, http.StatusConflict, codeConflict, "已有升级正在进行，请稍后再试")
		return
	}

	// 上传来源拿不到 release notes 里的 schema_changed 标记：一律按「含迁移」处理。
	// 宁可回退时多恢复一次数据（watchdog 先 restore 备份再换回旧二进制），
	// 也不能在真的做过 schema 变更时只回退二进制、把旧版本留在新库上。
	const schemaChanged = true

	job := &upgradeJob{
		JobID:         jobID,
		Phase:         "validating",
		TargetVersion: version,
		FromVersion:   a.version,
		StartedAt:     a.nowMS(),
	}
	a.upgrader.setJob(job)
	a.logger.Info("开始升级控制端（上传二进制）",
		"from", a.version, "to", version, "job_id", job.JobID,
		"source", "upload", "upload_bytes", upload.Bytes, "upload_sha256", upload.SHA256)
	a.auditManual(r, "system.upgrade_start", "system", "",
		fmt.Sprintf(`{"from":%q,"to":%q,"job_id":%q,"schema_changed":%t,"source":"upload","upload_sha256":%q,"upload_bytes":%d}`,
			a.version, version, job.JobID, schemaChanged, upload.SHA256, upload.Bytes))

	go a.runUploadUpgrade(job, upload.Path, schemaChanged, os.Args)

	// 响应形状与 /system/update/apply 完全一致（前端复用同一套轮询/展示逻辑）。
	writeData(w, http.StatusAccepted, map[string]any{"job_id": job.JobID, "status": a.upgrader.getJob()})
}

// runUploadUpgrade 用「上传的二进制」执行升级：备份之后与下载路径完全同一条收尾链路。
func (a *API) runUploadUpgrade(job *upgradeJob, newExe string, schemaChanged bool, execArgs []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	backupInfo, err := a.prepareUpgradeBackup(ctx, job)
	if err != nil {
		// 备份阶段就中止：此时还没碰过 swap，上传的二进制已经没用了，
		// 立刻删掉——否则每次失败的尝试都在升级目录里留一个最大 200 MiB 的文件。
		_ = os.Remove(newExe)
		a.failUpgrade(job, "backup", err)
		return
	}
	a.restartUpgrade(job, backupInfo, newExe, schemaChanged, execArgs)
}

// receiveUploadedBinary 把 multipart/form-data 里的 file 字段流式落盘到 dst，并解析 allow_major。
//
// 用 MultipartReader 而不是 ParseMultipartForm：后者会把整个请求体读进内存（上限 200 MiB）。
// 超限时把剩余字节排空，只为让 413 文案报出真实大小。
func (a *API) receiveUploadedBinary(w http.ResponseWriter, r *http.Request, dst string) (out *receivedUpload, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, upgradeUploadMaxBytes+upgradeUploadDrainSlack+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, &uploadError{http.StatusBadRequest, codeInvalidArgument,
			"请求必须是 multipart/form-data（file 字段为解压后的 leadstack 二进制）"}
	}

	file, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("创建上传文件 %s: %w", filepath.Base(dst), err)
	}
	closed := false
	// 任何失败都不给上传目录留垃圾文件。
	defer func() {
		if !closed {
			_ = file.Close()
		}
		if err != nil {
			_ = os.Remove(dst)
		}
	}()

	hash := sha256.New()
	upload := &receivedUpload{Path: dst}
	hasFile := false
	tooLarge := false
	tooLargeMessage := ""
	for {
		part, perr := reader.NextPart()
		if errors.Is(perr, io.EOF) {
			break
		}
		if perr != nil {
			if isBodyTooLarge(perr) {
				return nil, &uploadError{http.StatusRequestEntityTooLarge, codeInvalidArgument,
					fmt.Sprintf("上传文件超过 %d 字节上限（请求体在传输中被截断）", upgradeUploadMaxBytes)}
			}
			return nil, &uploadError{http.StatusBadRequest, codeInvalidArgument,
				"解析 multipart 请求失败: " + perr.Error()}
		}
		switch part.FormName() {
		case "file":
			if hasFile {
				_ = part.Close()
				return nil, &uploadError{http.StatusBadRequest, codeInvalidArgument, "file 字段只能出现一次"}
			}
			hasFile = true
			written, werr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(part, upgradeUploadMaxBytes+1))
			upload.Bytes += written
			if werr != nil {
				_ = part.Close()
				if isBodyTooLarge(werr) {
					return nil, &uploadError{http.StatusRequestEntityTooLarge, codeInvalidArgument,
						fmt.Sprintf("上传文件超过 %d 字节上限（请求体在传输中被截断，已读取 %d 字节）",
							upgradeUploadMaxBytes, upload.Bytes)}
				}
				return nil, &uploadError{http.StatusBadRequest, codeInvalidArgument,
					"接收上传文件失败: " + werr.Error()}
			}
			if upload.Bytes > upgradeUploadMaxBytes {
				// 超限：剩余字节只数不写，把真实大小算进 413 文案。
				tooLarge = true
				rest, derr := io.Copy(io.Discard, part)
				upload.Bytes += rest
				tooLargeMessage = fmt.Sprintf("上传文件 %d 字节，超过 %d 字节上限", upload.Bytes, upgradeUploadMaxBytes)
				if derr != nil {
					_ = part.Close()
					return nil, &uploadError{http.StatusRequestEntityTooLarge, codeInvalidArgument,
						fmt.Sprintf("上传文件超过 %d 字节上限（已读取 %d 字节，实际更大）",
							upgradeUploadMaxBytes, upload.Bytes)}
				}
			}
		case "allow_major":
			raw, rerr := io.ReadAll(io.LimitReader(part, 16))
			if rerr != nil {
				_ = part.Close()
				return nil, &uploadError{http.StatusBadRequest, codeInvalidArgument,
					"读取 allow_major 字段失败: " + rerr.Error()}
			}
			upload.AllowMajor = isTrueFormValue(string(raw))
		default:
			// 未知字段直接丢弃，避免未知字段把请求体撑大。
			_, _ = io.Copy(io.Discard, io.LimitReader(part, 1<<20))
		}
		_ = part.Close()
	}

	cerr := file.Close()
	closed = true
	if cerr != nil {
		return nil, fmt.Errorf("写入上传文件失败: %w", cerr)
	}

	if !hasFile {
		return nil, &uploadError{http.StatusBadRequest, codeInvalidArgument,
			"缺少 file 字段（需要上传解压后的 leadstack 二进制）"}
	}
	if tooLarge {
		return nil, &uploadError{http.StatusRequestEntityTooLarge, codeInvalidArgument, tooLargeMessage}
	}
	if upload.Bytes < upgradeUploadMinBytes {
		return nil, &uploadError{http.StatusRequestEntityTooLarge, codeInvalidArgument,
			fmt.Sprintf("上传文件只有 %d 字节，小于 %d 字节下限（这不是解压后的 leadstack 二进制）",
				upload.Bytes, upgradeUploadMinBytes)}
	}
	// 后续要 exec 这个文件读版本，必须有可执行位。
	if cerr := os.Chmod(dst, 0o755); cerr != nil {
		return nil, fmt.Errorf("设置上传文件权限: %w", cerr)
	}
	upload.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return upload, nil
}

// validateUploadedBinary 校验上传的文件确实是「更高版本的 LeadStack 控制端二进制」，返回其版本号。
//
// 校验顺序固定：魔数 → 执行 --version → 解析版本 → 与当前版本比较。
// 当前版本不是语义化版本（开发构建）时，版本比较环节直接放行。
func validateUploadedBinary(path, currentVersion string, allowMajor bool) (string, error) {
	if err := checkExecutableMagic(path); err != nil {
		return "", err
	}
	output, err := probeBinaryVersion(path)
	if err != nil {
		return "", fmt.Errorf("无法执行上传的二进制来读取版本：%w", err)
	}
	version, err := parseUploadedVersion(output)
	if err != nil {
		return "", err
	}
	if err := checkUploadedVersion(version, currentVersion, allowMajor); err != nil {
		return "", err
	}
	return version, nil
}

// checkExecutableMagic 校验文件头是 ELF 或 Mach-O。
func checkExecutableMagic(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开上传文件失败: %w", err)
	}
	defer file.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(file, head); err != nil {
		return errors.New(notExecutableMessage)
	}
	for _, magic := range executableMagics {
		if bytes.Equal(head, magic) {
			return nil
		}
	}
	return errors.New(notExecutableMessage)
}

// probeBinaryVersion 执行上传的二进制读版本；测试可用 probeUploadedBinaryVersion 替换。
func probeBinaryVersion(path string) (string, error) {
	if probeUploadedBinaryVersion != nil {
		return probeUploadedBinaryVersion(path)
	}
	return runUploadedBinaryVersion(path)
}

// runUploadedBinaryVersion 真实执行 `<path> --version`（15 秒超时），返回 stdout+stderr。
func runUploadedBinaryVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), upgradeUploadProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	text := string(out)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return text, fmt.Errorf("执行 %s --version 超时（%s）", filepath.Base(path), upgradeUploadProbeTimeout)
		}
		return text, fmt.Errorf("执行 %s --version 失败: %v（输出：%s）",
			filepath.Base(path), err, truncateNotes(strings.TrimSpace(text)))
	}
	return text, nil
}

// parseUploadedVersion 从 `leadstack <semver> (commit …, built …)` 输出里取版本号。
func parseUploadedVersion(output string) (string, error) {
	line := ""
	for _, candidate := range strings.Split(output, "\n") {
		if strings.TrimSpace(candidate) != "" {
			line = strings.TrimSpace(candidate)
			break
		}
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", errors.New("上传的文件不是 LeadStack 控制端二进制（--version 输出里没有 `leadstack <版本>`）")
	}
	if fields[0] != "leadstack" {
		return "", fmt.Errorf("上传的文件不是 LeadStack 控制端二进制（--version 输出以 %q 开头，不是 leadstack）", fields[0])
	}
	if _, err := ParseSemver(fields[1]); err != nil {
		return "", fmt.Errorf("上传的文件不是 LeadStack 控制端二进制（--version 输出的版本号 %q 无法解析）", fields[1])
	}
	return fields[1], nil
}

// checkUploadedVersion 判断上传版本是否可升级：口径与 validateTargetVersion 一致。
//
// 当前版本不是语义化版本（开发构建，如 "dev"/"test"）时不参与比较，直接放行。
func checkUploadedVersion(version, currentVersion string, allowMajor bool) error {
	cur, err := ParseSemver(currentVersion)
	if err != nil {
		return nil
	}
	cand, err := ParseSemver(version)
	if err != nil {
		return fmt.Errorf("上传的文件不是 LeadStack 控制端二进制（版本号 %q 无法解析）", version)
	}
	if cand.Compare(cur) <= 0 {
		return fmt.Errorf("上传版本 %s 不高于当前版本 %s，无需升级", version, currentVersion)
	}
	if cand.Major != cur.Major && !allowMajor {
		return fmt.Errorf("不允许跨大版本升级（%d → %d）：如确认需要，请在请求中显式传 allow_major=true",
			cur.Major, cand.Major)
	}
	return nil
}

// isTrueFormValue 判断表单里的布尔字段是否为真（"true"/"1"/"yes"/"on"，大小写不敏感）。
func isTrueFormValue(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}
