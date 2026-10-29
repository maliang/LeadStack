package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 真实可执行文件的魔数（ELF / Mach-O 64 位小端 / Mach-O 32 位小端）。
var (
	elfMagic     = []byte{0x7f, 'E', 'L', 'F'}
	macho64Magic = []byte{0xcf, 0xfa, 0xed, 0xfe}
	macho32Magic = []byte{0xce, 0xfa, 0xed, 0xfe}
)

// fakeBinary 构造「魔数正确 + 指定体积」的假二进制：永远不会被真实 exec 成功。
func fakeBinary(magic []byte, size int64) []byte {
	content := make([]byte, size)
	copy(content, magic)
	return content
}

// uploadMultipart 构造 multipart/form-data 请求体（先表单字段，后 file）。
func uploadMultipart(t *testing.T, fields map[string]string, file []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("写表单字段 %s 失败: %v", key, err)
		}
	}
	part, err := writer.CreateFormFile("file", "leadstack")
	if err != nil {
		t.Fatalf("创建 file 字段失败: %v", err)
	}
	if _, err := part.Write(file); err != nil {
		t.Fatalf("写 file 字段失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 multipart writer 失败: %v", err)
	}
	return &buf, writer.FormDataContentType()
}

// postUpload 打上传端点（testEnv.request 只会发 JSON，这里必须自己组 multipart）。
func (e *testEnv) postUpload(token string, fields map[string]string, file []byte) (int, []byte) {
	e.t.Helper()
	body, contentType := uploadMultipart(e.t, fields, file)
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/api/v1/system/update/upload", body)
	if err != nil {
		e.t.Fatalf("构造上传请求失败: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := e.http.Do(req)
	if err != nil {
		e.t.Fatalf("发起上传请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("读取上传响应失败: %v", err)
	}
	return resp.StatusCode, raw
}

// firstAuditDetail 读指定 action 的最新一条审计 detail；没有条目时返回空串（便于轮询，不判败）。
func firstAuditDetail(t *testing.T, env *testEnv, jwt, action string) string {
	t.Helper()
	status, raw := env.request(http.MethodGet, "/api/v1/audit-logs?action="+action+"&limit=20", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询审计失败: status=%d body=%s", status, raw)
	}
	var out struct {
		Data struct {
			Logs []struct {
				Detail *string `json:"detail"`
			} `json:"logs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析审计列表失败: %v（%s）", err, raw)
	}
	if len(out.Data.Logs) == 0 || out.Data.Logs[0].Detail == nil {
		return ""
	}
	return *out.Data.Logs[0].Detail
}

// TestUpgradeUploadLimitsAreProductionValues 钉住上传体积口径：1 MiB ~ 200 MiB。
//
// 两个上下限是 var（单测要能把它们缩小到 KB 级，否则每个 413 用例都要真写 200 MiB），
// 这条用例保证「可变」不会悄悄变成「变小」。
func TestUpgradeUploadLimitsAreProductionValues(t *testing.T) {
	if upgradeUploadMinBytes != 1<<20 {
		t.Fatalf("上传下限应为 1 MiB，实际 %d", upgradeUploadMinBytes)
	}
	if upgradeUploadMaxBytes != 200<<20 {
		t.Fatalf("上传上限应为 200 MiB，实际 %d", upgradeUploadMaxBytes)
	}
	if upgradeUploadDrainSlack <= 0 {
		t.Fatalf("超限排空余量必须为正数，实际 %d", upgradeUploadDrainSlack)
	}
}

// TestValidateUploadedBinaryMatrix 覆盖上传二进制的校验矩阵。
//
// 重点：非可执行文件的用例要求探针「一次都没被调用」——证明魔数校验在执行上传文件之前，
// 否则等于让任意上传内容先跑一遍（契约里校验顺序是硬约束）。
func TestValidateUploadedBinaryMatrix(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name       string
		content    []byte
		probeOut   string
		probeErr   error
		current    string
		allowMajor bool
		wantVer    string
		wantErr    string
		wantProbes int
	}{
		{
			name:       "魔数不是可执行文件（tar.gz）",
			content:    bytes.Repeat([]byte("leadstack.tar.gz"), 512),
			probeOut:   "leadstack v9.9.9\n",
			current:    "v1.0.0",
			wantErr:    "不是可执行文件",
			wantProbes: 0,
		},
		{
			name:       "魔数正确但输出没有 leadstack 前缀",
			content:    fakeBinary(elfMagic, 8192),
			probeOut:   "GNU coreutils 9.1\n",
			current:    "v1.0.0",
			wantErr:    "不是 LeadStack 控制端二进制",
			wantProbes: 1,
		},
		{
			name:       "魔数正确但版本号无法解析",
			content:    fakeBinary(elfMagic, 8192),
			probeOut:   "leadstack nightly (commit none, built none)\n",
			current:    "v1.0.0",
			wantErr:    "不是 LeadStack 控制端二进制",
			wantProbes: 1,
		},
		{
			name:       "执行上传文件失败",
			content:    fakeBinary(elfMagic, 8192),
			probeErr:   errors.New("exec format error"),
			current:    "v1.0.0",
			wantErr:    "无法执行上传的二进制来读取版本",
			wantProbes: 1,
		},
		{
			name:       "版本等于当前版本",
			content:    fakeBinary(elfMagic, 8192),
			probeOut:   "leadstack v1.2.3 (commit abc, built now)\n",
			current:    "v1.2.3",
			wantErr:    "不高于当前版本",
			wantProbes: 1,
		},
		{
			name:       "版本低于当前版本",
			content:    fakeBinary(elfMagic, 8192),
			probeOut:   "leadstack v1.2.2 (commit abc, built now)\n",
			current:    "v1.2.3",
			wantErr:    "不高于当前版本",
			wantProbes: 1,
		},
		{
			name:       "跨大版本但未确认",
			content:    fakeBinary(elfMagic, 8192),
			probeOut:   "leadstack v2.0.0 (commit abc, built now)\n",
			current:    "v1.2.3",
			wantErr:    "不允许跨大版本升级",
			wantProbes: 1,
		},
		{
			name:       "跨大版本且已确认 allow_major",
			content:    fakeBinary(elfMagic, 8192),
			probeOut:   "leadstack v2.0.0 (commit abc, built now)\n",
			current:    "v1.2.3",
			allowMajor: true,
			wantVer:    "v2.0.0",
			wantProbes: 1,
		},
		{
			name:       "Mach-O 64 位小端 + 合法更高版本",
			content:    fakeBinary(macho64Magic, 8192),
			probeOut:   "leadstack v1.3.0 (commit abc, built now)\n",
			current:    "v1.2.3",
			wantVer:    "v1.3.0",
			wantProbes: 1,
		},
		{
			name:       "Mach-O 32 位小端 + 合法更高版本",
			content:    fakeBinary(macho32Magic, 8192),
			probeOut:   "leadstack v1.3.1 (commit abc, built now)\n",
			current:    "v1.2.3",
			wantVer:    "v1.3.1",
			wantProbes: 1,
		},
		{
			// 开发构建（dev）没有语义化版本，不做版本比较，任何合法标签都接受。
			name:       "当前版本是开发构建",
			content:    fakeBinary(elfMagic, 8192),
			probeOut:   "leadstack v0.0.1 (commit none, built none)\n",
			current:    "dev",
			wantVer:    "v0.0.1",
			wantProbes: 1,
		},
	}

	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, fmt.Sprintf("case-%02d.bin", index))
			if err := os.WriteFile(path, tc.content, 0o755); err != nil {
				t.Fatalf("写测试文件失败: %v", err)
			}
			probes := 0
			probeUploadedBinaryVersion = func(string) (string, error) {
				probes++
				return tc.probeOut, tc.probeErr
			}
			t.Cleanup(func() { probeUploadedBinaryVersion = nil })

			version, err := validateUploadedBinary(path, tc.current, tc.allowMajor)
			if probes != tc.wantProbes {
				t.Fatalf("探针应被调用 %d 次，实际 %d 次", tc.wantProbes, probes)
			}
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("期望报错 %q，实际通过（version=%q）", tc.wantErr, version)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("错误文案应含 %q，实际 %q", tc.wantErr, err.Error())
				}
				if version != "" {
					t.Fatalf("报错时不应返回版本号，实际 %q", version)
				}
				return
			}
			if err != nil {
				t.Fatalf("期望通过，实际报错: %v", err)
			}
			if version != tc.wantVer {
				t.Fatalf("版本应为 %q，实际 %q", tc.wantVer, version)
			}
		})
	}
}

// TestUpgradeUploadGuards 覆盖上传端点的路由守卫与校验矩阵（全部在 begin() 之前）。
func TestUpgradeUploadGuards(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	uploadDir := env.cfg.UpgradeDir()
	validBinary := fakeBinary(elfMagic, upgradeUploadMinBytes)

	// 每个失败用例之后：上传目录不留垃圾文件，且 upgrader 没被占住。
	assertClean := func(t *testing.T, label string) {
		t.Helper()
		left, err := filepath.Glob(filepath.Join(uploadDir, "upload-*.bin"))
		if err != nil {
			t.Fatalf("%s：扫描上传目录失败: %v", label, err)
		}
		if len(left) != 0 {
			t.Fatalf("%s：上传目录残留临时文件 %v", label, left)
		}
		if env.api.upgrader.isRunning() {
			t.Fatalf("%s：校验失败却占住了 upgrader，后续合法请求会全部 409", label)
		}
	}

	// 1) 自升级总开关关闭 → 403（与 apply 同文案）
	env.cfg.UpgradeEnabled = false
	status, raw := env.postUpload(jwt, nil, validBinary)
	if status != http.StatusForbidden || !strings.Contains(string(raw), "UPGRADE_ENABLED") {
		t.Fatalf("禁用自升级应 403 且提示 UPGRADE_ENABLED，实际 %d %s", status, raw)
	}
	env.cfg.UpgradeEnabled = true
	assertClean(t, "禁用自升级")

	// 2) 未认证 → 401
	if status, _ = env.postUpload("", nil, validBinary); status != http.StatusUnauthorized {
		t.Fatalf("未认证上传应 401，实际 %d", status)
	}
	assertClean(t, "未认证")

	// 3) 缺 system.manage → 403。
	//
	// 关于「跨范围应 404」：本路由的范围校验与 /system/update/apply 同格，走 RequireGlobalScope，
	// 而它返回的是 403（不是 404）；且 system.manage 属内置 admin 专属权限，自定义角色带上会被 400 拒绝，
	// 因此「有 system.manage 但 scope≠all」的用户不可构造，404 在本路由不可达。
	env.createUserVia(jwt, "vw-up-upload", "viewer-pass-456", "viewer")
	viewer := env.loginAs(t, "vw-up-upload", "viewer-pass-456")
	if status, _ = env.postUpload(viewer, nil, validBinary); status != http.StatusForbidden {
		t.Fatalf("viewer 上传应 403，实际 %d", status)
	}
	assertClean(t, "viewer")

	// 补充证据：上面那句「跨范围 404 不可达」不是推测——自定义角色带 system.manage 会被 400 拒绝
	//（NormalizePermissions 防提权），内置 admin 的 scope 恒为 all，所以根本不存在「有 system.manage
	// 但范围受限」的调用者。
	status, raw = env.request(http.MethodPost, "/api/v1/roles", jwt, map[string]any{
		"name": "scope-limited-system", "permissions": []string{"system.manage"},
		"scope": map[string]any{"mode": "groups", "group_ids": []string{}},
	})
	if status != http.StatusBadRequest || !strings.Contains(string(raw), "system.manage") {
		t.Fatalf("带 system.manage 的自定义角色应被 400 拒绝（防提权），实际 %d %s", status, raw)
	}

	// 4) 不是 multipart（JSON 体）→ 400
	status, raw = env.request(http.MethodPost, "/api/v1/system/update/upload", jwt, map[string]any{"file": "x"})
	if status != http.StatusBadRequest || !strings.Contains(string(raw), "multipart") {
		t.Fatalf("非 multipart 请求应 400 且提示 multipart，实际 %d %s", status, raw)
	}
	assertClean(t, "非 multipart")

	// 5) 魔数不是 ELF/Mach-O → 400（体积先过关，才能走到魔数校验：契约里 3 在 4 之前）
	status, raw = env.postUpload(jwt, nil, bytes.Repeat([]byte("not-a-binary"), 100000))
	if status != http.StatusBadRequest || !strings.Contains(string(raw), "不是可执行文件") {
		t.Fatalf("非可执行文件应 400，实际 %d %s", status, raw)
	}
	assertClean(t, "魔数不符")

	// 6) 小于 1 MiB → 413，且文案带实际字节数
	small := append(append([]byte{}, elfMagic...), make([]byte, 1024)...)
	status, raw = env.postUpload(jwt, nil, small)
	if status != http.StatusRequestEntityTooLarge || !strings.Contains(string(raw), fmt.Sprintf("%d 字节", len(small))) {
		t.Fatalf("过小的上传应 413 且带实际字节数 %d，实际 %d %s", len(small), status, raw)
	}
	assertClean(t, "过小")

	// 7) 超过上限 → 413。把上限临时缩到 64 KiB，避免真的写 200 MiB。
	savedMax := upgradeUploadMaxBytes
	upgradeUploadMaxBytes = 64 << 10
	t.Cleanup(func() { upgradeUploadMaxBytes = savedMax })
	status, raw = env.postUpload(jwt, nil, fakeBinary(elfMagic, 128<<10))
	upgradeUploadMaxBytes = savedMax
	if status != http.StatusRequestEntityTooLarge || !strings.Contains(string(raw), "超过") {
		t.Fatalf("过大的上传应 413，实际 %d %s", status, raw)
	}
	assertClean(t, "过大")

	// 8) 版本不高于当前 → 400；9) 跨大版本未确认 → 400。
	// 当前版本换成语义化版本，才走得到版本比较（dev 会直接放行）。
	savedVersion := env.api.version
	env.api.version = "v1.2.3"
	t.Cleanup(func() { env.api.version = savedVersion })

	probeUploadedBinaryVersion = func(string) (string, error) {
		return "leadstack v1.2.3 (commit abc, built now)\n", nil
	}
	status, raw = env.postUpload(jwt, nil, validBinary)
	if status != http.StatusBadRequest || !strings.Contains(string(raw), "不高于当前版本") {
		t.Fatalf("同版本上传应 400，实际 %d %s", status, raw)
	}
	assertClean(t, "版本不高于当前")

	probeUploadedBinaryVersion = func(string) (string, error) {
		return "leadstack v2.0.0 (commit abc, built now)\n", nil
	}
	status, raw = env.postUpload(jwt, nil, validBinary)
	if status != http.StatusBadRequest || !strings.Contains(string(raw), "不允许跨大版本升级") {
		t.Fatalf("跨大版本上传应 400，实际 %d %s", status, raw)
	}
	assertClean(t, "跨大版本")

	// 10) 已有升级在进行 → 409（能到 409 说明文件与版本校验都过了，占坑是最后一步）
	probeUploadedBinaryVersion = func(string) (string, error) {
		return "leadstack v1.9.9 (commit abc, built now)\n", nil
	}
	t.Cleanup(func() { probeUploadedBinaryVersion = nil })
	env.api.upgrader.mu.Lock()
	env.api.upgrader.running = true
	env.api.upgrader.mu.Unlock()
	status, raw = env.postUpload(jwt, nil, validBinary)
	env.api.upgrader.mu.Lock()
	env.api.upgrader.running = false
	env.api.upgrader.mu.Unlock()
	if status != http.StatusConflict || !strings.Contains(string(raw), "已有升级正在进行") {
		t.Fatalf("并发升级应 409，实际 %d %s", status, raw)
	}
	assertClean(t, "并发 409")
}

// TestUpgradeUploadSuccess 覆盖上传成功路径：202 + 与 apply 同形的响应 + 复用备份/替换链路。
//
// 执行阶段用 a.upgradeRestart 替身截断：真实的 applyAndRestart 会把测试进程自己换成假二进制。
func TestUpgradeUploadSuccess(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	env.api.version = "v1.0.0"
	probeUploadedBinaryVersion = func(string) (string, error) {
		return "leadstack v1.4.0 (commit test, built test)\n", nil
	}
	t.Cleanup(func() { probeUploadedBinaryVersion = nil })

	type restartCall struct {
		backupName    string
		backupSHA     string
		newExe        string
		schemaChanged bool
		execArgs      []string
	}
	calls := make(chan restartCall, 1)
	env.api.upgradeRestart = func(job *upgradeJob, backupInfo *BackupInfo, newExe string, schemaChanged bool, execArgs []string) {
		calls <- restartCall{
			backupName:    backupInfo.Name,
			backupSHA:     backupInfo.SHA256,
			newExe:        newExe,
			schemaChanged: schemaChanged,
			execArgs:      execArgs,
		}
	}
	t.Cleanup(func() { env.api.upgradeRestart = nil })

	content := fakeBinary(elfMagic, upgradeUploadMinBytes)
	digest := sha256.Sum256(content)

	status, raw := env.postUpload(jwt, map[string]string{"allow_major": "true"}, content)
	if status != http.StatusAccepted {
		t.Fatalf("合法上传应 202，实际 %d %s", status, raw)
	}
	var out struct {
		Data struct {
			JobID  string     `json:"job_id"`
			Status upgradeJob `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析响应失败: %v（%s）", err, raw)
	}
	if out.Data.JobID == "" {
		t.Fatalf("响应 data 里必须有 job_id（与 apply 同形）：%s", raw)
	}
	if out.Data.Status.TargetVersion != "v1.4.0" || out.Data.Status.FromVersion != "v1.0.0" {
		t.Fatalf("响应 status 的 from/to 不对：%+v", out.Data.Status)
	}

	// 后台链路必须先做备份，再进入「写标记/替换」阶段。
	var call restartCall
	select {
	case call = <-calls:
	case <-time.After(10 * time.Second):
		t.Fatalf("后台升级没有走到执行阶段：job=%+v", env.api.upgrader.getJob())
	}
	if call.backupName == "" || call.backupSHA == "" {
		t.Fatalf("替换二进制前必须先完成备份，实际 %+v", call)
	}
	if !call.schemaChanged {
		t.Fatalf("上传来源应按「含迁移」处理（schemaChanged=true），否则回退时不会恢复数据")
	}
	wantPrefix := filepath.Join(env.cfg.UpgradeDir(), "upload-")
	if !strings.HasPrefix(call.newExe, wantPrefix) || !strings.HasSuffix(call.newExe, ".bin") {
		t.Fatalf("替换源应是上传落盘文件 %supload-<job>.bin，实际 %s", wantPrefix, call.newExe)
	}
	if len(call.execArgs) == 0 || call.execArgs[0] != os.Args[0] {
		t.Fatalf("execArgs[0] 必须是当前可执行文件路径，实际 %v", call.execArgs)
	}
	info, err := os.Stat(call.newExe)
	if err != nil {
		t.Fatalf("上传落盘文件应存在: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("上传落盘文件必须可执行（后续要 exec 它），实际权限 %v", info.Mode().Perm())
	}

	// 审计：source=upload + 真实 sha256 + 实际字节数（回滚/追责时靠它定位是哪个文件）
	detail := firstAuditDetail(t, env, jwt, "system.upgrade_start")
	for _, want := range []string{
		`"source":"upload"`,
		`"upload_sha256":"` + hex.EncodeToString(digest[:]) + `"`,
		fmt.Sprintf(`"upload_bytes":%d`, len(content)),
		`"from":"v1.0.0"`,
		`"to":"v1.4.0"`,
		`"schema_changed":true`,
	} {
		if !strings.Contains(detail, want) {
			t.Fatalf("审计 detail 缺少 %s：%s", want, detail)
		}
	}

	// job 上要记下备份文件名（watchdog 回退时靠它恢复数据）
	job := env.api.upgrader.getJob()
	if job == nil || job.BackupFile != call.backupName {
		t.Fatalf("job 应记录备份文件名 %q，实际 %+v", call.backupName, job)
	}
	if job.TargetVersion != "v1.4.0" {
		t.Fatalf("job 目标版本应为 v1.4.0，实际 %+v", job)
	}
	// 执行阶段被替身截断，二进制不能被替换过。
	if exe, err := os.Executable(); err == nil {
		if _, err := os.Stat(exe + upgradePrevSuffix); err == nil {
			t.Fatalf("执行阶段被截断，不应留下 %s", exe+upgradePrevSuffix)
		}
	}
	// 替身没结束 job；这里手动收尾，避免 running 泄漏影响同进程的其它用例。
	env.api.upgrader.finish(job, nil)
}

// TestUpgradeUploadBackupFailureKeepsBinary 覆盖上传路径的备份失败：中止升级、不动二进制。
func TestUpgradeUploadBackupFailureKeepsBinary(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	env.api.version = "v1.0.0"
	probeUploadedBinaryVersion = func(string) (string, error) {
		return "leadstack v1.4.0 (commit test, built test)\n", nil
	}
	t.Cleanup(func() { probeUploadedBinaryVersion = nil })

	hookCalls := make(chan struct{}, 1)
	env.api.upgradeRestart = func(job *upgradeJob, backupInfo *BackupInfo, newExe string, schemaChanged bool, execArgs []string) {
		hookCalls <- struct{}{}
	}
	t.Cleanup(func() { env.api.upgradeRestart = nil })

	env.backup.createErr = errors.New("disk full")

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("定位当前可执行文件失败: %v", err)
	}
	before, err := os.Stat(exe)
	if err != nil {
		t.Fatalf("读取当前可执行文件状态失败: %v", err)
	}

	status, raw := env.postUpload(jwt, nil, fakeBinary(elfMagic, upgradeUploadMinBytes))
	if status != http.StatusAccepted {
		t.Fatalf("合法上传应 202（备份在后台做），实际 %d %s", status, raw)
	}

	// 等后台走到终态
	deadline := time.Now().Add(10 * time.Second)
	var job *upgradeJob
	for {
		job = env.api.upgrader.getJob()
		if job != nil && job.Done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("升级没有在 10 秒内进入终态：%+v", job)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(job.Error, "备份失败") {
		t.Fatalf("终态错误应说明备份失败，实际 %+v", job)
	}
	select {
	case <-hookCalls:
		t.Fatalf("备份失败后绝不能进入「替换二进制」阶段")
	default:
	}

	after, err := os.Stat(exe)
	if err != nil {
		t.Fatalf("读取当前可执行文件状态失败: %v", err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("备份失败却动了当前二进制：size %d→%d，mtime %v→%v",
			before.Size(), after.Size(), before.ModTime(), after.ModTime())
	}
	if _, err := os.Stat(exe + upgradePrevSuffix); err == nil {
		t.Fatalf("备份失败却留下了 %s", exe+upgradePrevSuffix)
	}
	// 备份失败后上传的二进制已无用，必须删掉（否则每次失败都留一个最多 200 MiB 的文件）。
	if left, err := filepath.Glob(filepath.Join(env.cfg.UpgradeDir(), "upload-*.bin")); err != nil {
		t.Fatalf("扫描升级目录失败: %v", err)
	} else if len(left) != 0 {
		t.Fatalf("备份失败后上传文件应被删除，实际残留 %v", left)
	}

	// 失败也要留审计（aborted + phase=backup）；finish 先于审计，所以这里轮询。
	deadline = time.Now().Add(5 * time.Second)
	var detail string
	for {
		detail = firstAuditDetail(t, env, jwt, "system.upgrade_start")
		if strings.Contains(detail, `"aborted":true`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("升级中止没有写审计：%q", detail)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(detail, `"phase":"backup"`) || !strings.Contains(detail, `"to":"v1.4.0"`) {
		t.Fatalf("中止审计应记录 phase=backup 与目标版本，实际 %s", detail)
	}
}
