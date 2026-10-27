package deploy

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func inplaceParams() InplaceParams {
	return InplaceParams{
		AppSlug:     "shop",
		ReleaseID:   "rel-1",
		Version:     "1.0.0",
		TargetDir:   "/www/wwwroot/shop",
		Artifact:    Artifact{Kind: ArtifactURL, Location: "https://example.com/shop.zip", SHA256: strings.Repeat("a", 64)},
		RestartCmd:  "systemctl restart shop",
		InitScript:  "echo init",
		FirstDeploy: true,
		EnvJSON:     `{"DB_HOST":"127.0.0.1","DB_PASSWORD":"s3cret"}`,
		KeepPaths:   []string{"uploads", "help/docker-compose/*/data"},
		Now:         1000,
	}
}

func TestBuildInplaceStepOrder(t *testing.T) {
	plan, err := BuildInplace(inplaceParams())
	if err != nil {
		t.Fatalf("BuildInplace 失败: %v", err)
	}
	got := make([]string, 0, len(plan.Steps))
	for _, s := range plan.Steps {
		got = append(got, s.Name)
	}
	want := []string{
		StepPrepare, StepDownload, StepVerifySHA256, StepUnpack,
		StepBackup, StepSync, StepWriteEnv, StepInit, StepInitRun, StepRestart, StepWriteMetadata,
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("步骤顺序不符\n got: %v\nwant: %v", got, want)
	}
}

func TestBuildInplaceSkipsInitAfterFirstDeploy(t *testing.T) {
	p := inplaceParams()
	p.FirstDeploy = false
	plan, err := BuildInplace(p)
	if err != nil {
		t.Fatalf("BuildInplace 失败: %v", err)
	}
	for _, s := range plan.Steps {
		if s.Name == StepInit || s.Name == StepInitRun {
			t.Fatalf("非首版不应包含初始化步骤: %v", s.Name)
		}
	}
}

func TestBuildInplaceBackupAndSyncKeepDataDirs(t *testing.T) {
	plan, err := BuildInplace(inplaceParams())
	if err != nil {
		t.Fatalf("BuildInplace 失败: %v", err)
	}
	var backup, sync string
	for _, s := range plan.Steps {
		switch s.Name {
		case StepBackup:
			backup = s.Command
		case StepSync:
			sync = s.Command
		}
	}
	// 备份：不含 .leadstack 自身目录，但包含目标目录全部内容
	if !strings.Contains(backup, "tar -czf") || !strings.Contains(backup, "/www/wwwroot/shop") {
		t.Fatalf("备份命令不符: %s", backup)
	}
	if !strings.Contains(backup, "--exclude=./.leadstack") {
		t.Fatalf("备份应排除 .leadstack: %s", backup)
	}
	// 同步：--delete 覆盖，但保留数据目录
	if !strings.Contains(sync, "rsync -a --delete") {
		t.Fatalf("同步命令不符: %s", sync)
	}
	for _, keep := range []string{"--exclude=uploads", "--exclude=help/docker-compose/*/data"} {
		if !strings.Contains(sync, keep) {
			t.Fatalf("同步应保留 %s: %s", keep, sync)
		}
	}
	if !strings.Contains(sync, "staging-rel-1/") {
		t.Fatalf("同步源应为本次 staging: %s", sync)
	}
}

func TestInplaceEnvFileIsRedactedAndBase64(t *testing.T) {
	plan, err := BuildInplace(inplaceParams())
	if err != nil {
		t.Fatalf("BuildInplace 失败: %v", err)
	}
	var envStep *Step
	for i := range plan.Steps {
		if plan.Steps[i].Name == StepWriteEnv {
			envStep = &plan.Steps[i]
		}
	}
	if envStep == nil {
		t.Fatal("缺少写环境变量步骤")
	}
	if !envStep.Secret {
		t.Fatal("写环境变量步骤必须标记 Secret（日志脱敏）")
	}
	if strings.Contains(envStep.Command, "s3cret") {
		t.Fatal("命令里不应出现明文口令")
	}
	// 从命令里还原出内容，确认是合法 .env 且 KEY 排序稳定
	idx := strings.Index(envStep.Command, "'")
	end := strings.Index(envStep.Command[idx+1:], "'") + idx + 1
	raw, err := base64.StdEncoding.DecodeString(envStep.Command[idx+1 : end])
	if err != nil {
		t.Fatalf("环境变量内容不是 base64: %v", err)
	}
	if string(raw) != "DB_HOST=127.0.0.1\nDB_PASSWORD=s3cret\n" {
		t.Fatalf("环境文件内容不符: %q", string(raw))
	}
}

func TestInplaceRollbackRestoresBackup(t *testing.T) {
	plan, err := BuildInplace(inplaceParams())
	if err != nil {
		t.Fatalf("BuildInplace 失败: %v", err)
	}
	if len(plan.RollbackSteps) < 2 {
		t.Fatalf("回滚步骤不足: %v", plan.RollbackSteps)
	}
	// 执行器逆序执行：RollbackSteps[1] 先跑（恢复备份），RollbackSteps[0] 后跑（重启）
	restore := plan.RollbackSteps[len(plan.RollbackSteps)-1]
	if restore.Name != StepRollbackRestore {
		t.Fatalf("逆序执行时应先恢复备份，最后一步应为此: %s", restore.Name)
	}
	if !strings.Contains(restore.Command, "backup-rel-1.tar.gz") || !strings.Contains(restore.Command, "/www/wwwroot/shop") {
		t.Fatalf("恢复命令不符: %s", restore.Command)
	}
	if plan.RollbackSteps[0].Name != "restart" || !plan.RollbackSteps[0].IgnoreFailure {
		t.Fatalf("回滚应（在逆序执行时最后）重启服务且允许失败: %+v", plan.RollbackSteps[0])
	}
}

func TestBuildInplaceRejectsRelativeTarget(t *testing.T) {
	p := inplaceParams()
	p.TargetDir = "relative/dir"
	if _, err := BuildInplace(p); err == nil {
		t.Fatal("相对路径应被拒绝")
	}
}

// --- T-02：回滚与前进对称（A5/A6/A7）-------------------------------------------

func TestInplaceBackupIncludesEnvFile(t *testing.T) {
	plan, err := BuildInplace(inplaceParams())
	if err != nil {
		t.Fatalf("BuildInplace 失败: %v", err)
	}
	backup := stepByName(t, plan.Steps, StepBackup).Command
	if strings.Contains(backup, "--exclude=./"+inplaceEnvFileName) {
		t.Fatalf("备份归档必须包含 %s（否则回滚只能得到「旧代码 + 新配置」）: %s", inplaceEnvFileName, backup)
	}
	// 仍然排除 LeadStack 自己的目录（避免备份里套备份）与 keep_paths（运行期数据）
	for _, want := range []string{
		"--exclude=./" + inplaceMetaDir,
		"--exclude=./" + inplaceBackupDir,
		"--exclude=./uploads",
		"--exclude=./help/docker-compose/*/data",
	} {
		if !strings.Contains(backup, want) {
			t.Fatalf("备份命令缺少保护项 %q: %s", want, backup)
		}
	}
}

func TestInplaceRollbackRestoreIsSymmetricRsync(t *testing.T) {
	plan, err := BuildInplace(inplaceParams())
	if err != nil {
		t.Fatalf("BuildInplace 失败: %v", err)
	}
	restore := plan.RollbackSteps[len(plan.RollbackSteps)-1]
	if restore.Name != StepRollbackRestore {
		t.Fatalf("逆序执行第一步应为还原，实际 %q", restore.Name)
	}
	if restore.IgnoreFailure {
		t.Fatal("还原失败不能被忽略（否则 run 看不出回滚不完整）")
	}
	cmd := restore.Command
	for _, want := range []string{
		"rsync -a -I --delete", // 与前进同步对称：删除新版本多出来的文件；-I 见下

		"backup-rel-1.tar.gz",                       // 用的是本次发布的备份
		shQuote("--exclude=" + inplaceMetaDir),      // .leadstack 不参与删除
		shQuote("--exclude=" + inplaceBackupDir),    // 备份目录不参与删除（A7：不额外堆备份也不会被清掉）
		shQuote("--exclude=" + inplaceMetaAliasDir), // SPEC 措辞里的 .leadstack-meta 同样保护
		shQuote("--exclude=uploads"),                // keep_paths 不参与删除
		shQuote("--exclude=help/docker-compose/*/data"),
		"已回滚到发布前状态（含环境变量）",                         // B-05 完整还原时的事件行
		"回滚不完整，请人工核对：" + inplaceParams().TargetDir, // B-05 失败提示
		"归档内无 " + inplaceEnvFileName,               // 旧备份（不含 env）时保留当前环境变量
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("还原命令缺少 %q:\n%s", want, cmd)
		}
	}
	// -I（--ignore-times）不能省：rsync 默认 quick check 只比「大小 + 秒级 mtime」，
	// 回滚时同名同大小、mtime 落在同一秒是常态（ENV_MARK=v1/v2 等长），
	// 省掉 -I 会得到「rsync 退出 0、文件仍是新版本」的假成功。
	if !strings.Contains(cmd, "--ignore-times") && !strings.Contains(cmd, " -I ") {
		t.Fatalf("还原必须忽略 mtime 强制覆盖: %s", cmd)
	}
	// tar 只解到临时目录：直接解回目标目录无法删除新版本残留文件
	if !strings.Contains(cmd, `tar -xzf "$backup" -C "$staging"`) {
		t.Fatalf("应解包到临时目录再 rsync: %s", cmd)
	}
	// 临时目录必须在目标目录之外：源目录嵌在目标目录里属于病态用法，
	// 不同 rsync 实现行为不一致（实测 macOS 自带 openrsync 会把源目录当重复项删掉）。
	if !strings.Contains(cmd, "mktemp -d") {
		t.Fatalf("临时目录应用 mktemp 建在目标目录之外: %s", cmd)
	}
	if strings.Contains(cmd, inplaceParams().TargetDir+"/.leadstack/restore") {
		t.Fatalf("临时目录不得放在目标目录内: %s", cmd)
	}
}

func TestBuildRestoreInplaceReusesRestoreCommand(t *testing.T) {
	plan, err := BuildRestoreInplace("/www/wwwroot/shop", "rel-7", "systemctl restart shop", "uploads")
	if err != nil {
		t.Fatalf("BuildRestoreInplace 失败: %v", err)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("应为 还原 → 重启 两步，实际 %v", stepNamesOf(plan.Steps))
	}
	restore := plan.Steps[0]
	if restore.Name != StepRollbackRestore {
		t.Fatalf("首步应为还原，实际 %q", restore.Name)
	}
	for _, want := range []string{
		"rsync -a -I --delete",
		"backup-rel-7.tar.gz",
		"test -f " + shQuote("/www/wwwroot/shop/.leadstack-backups/backup-rel-7.tar.gz"),
		shQuote("--exclude=uploads"),
		"回滚不完整，请人工核对：/www/wwwroot/shop",
	} {
		if !strings.Contains(restore.Command, want) {
			t.Fatalf("手动回滚的还原命令缺少 %q:\n%s", want, restore.Command)
		}
	}
	if plan.Steps[1].Name != StepRestart || plan.Steps[1].Command != "systemctl restart shop" {
		t.Fatalf("还原后应重启: %+v", plan.Steps[1])
	}
}

// TestInplaceRollbackRestoreRoundTrip 在临时目录里真跑一遍「备份 → 模拟坏发布 → 还原」，
// 断言目录与 .leadstack.env 都回到发布前，且 keep_paths / .leadstack / 备份目录不被删（A5/A6/A7）。
func TestInplaceRollbackRestoreRoundTrip(t *testing.T) {
	for _, bin := range []string{"tar", "rsync"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("环境缺少 %s，跳过回滚还原的端到端校验", bin)
		}
	}

	target := t.TempDir()
	// 还原脚本把解包目录放在系统临时目录下，这里指到自己的临时根以便断言「用完即清」
	tmpRoot := t.TempDir()
	t.Setenv("LEADSTACK_RESTORE_TMPDIR", tmpRoot)
	backupDir := filepath.Join(target, inplaceBackupDir)
	mustMkdirInplace(t, backupDir)
	mustWriteInplace(t, filepath.Join(target, "app.txt"), "v1")
	mustWriteInplace(t, filepath.Join(target, "only-in-v1.txt"), "old")
	mustWriteInplace(t, filepath.Join(target, "uploads", "keep.db"), "keep-v1")
	mustWriteInplace(t, filepath.Join(target, inplaceEnvFileName), "ENV_MARK=v1\n")
	mustWriteInplace(t, filepath.Join(target, inplaceMetaDir, "state.json"), `{"release":"rel-0"}`)

	p := inplaceParams()
	p.TargetDir = target
	plan, err := BuildInplace(p)
	if err != nil {
		t.Fatalf("BuildInplace 失败: %v", err)
	}
	runInplaceShell(t, stepByName(t, plan.Steps, StepBackup).Command)

	backup := filepath.Join(backupDir, "backup-rel-1"+inplaceBackupSuffix)
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("备份归档未生成: %v", err)
	}
	if listing := runInplaceShell(t, "tar -tzf "+shQuote(backup)); !strings.Contains(listing, "./"+inplaceEnvFileName) {
		t.Fatalf("备份归档里没有 %s:\n%s", inplaceEnvFileName, listing)
	}

	// 模拟一次「部署成功但健康检查失败」的坏发布：改文件、加文件、删文件、换 env，
	// 并动过运行期目录（keep_paths 与 .leadstack），这些都不该被回滚破坏。
	mustWriteInplace(t, filepath.Join(target, "app.txt"), "v2")
	mustWriteInplace(t, filepath.Join(target, "only-in-v2.txt"), "new")
	if err := os.Remove(filepath.Join(target, "only-in-v1.txt")); err != nil {
		t.Fatalf("删除 v1 文件失败: %v", err)
	}
	mustWriteInplace(t, filepath.Join(target, inplaceEnvFileName), "ENV_MARK=v2\n")
	mustWriteInplace(t, filepath.Join(target, "uploads", "keep.db"), "keep-v2")
	mustWriteInplace(t, filepath.Join(target, "uploads", "runtime.log"), "runtime")
	mustWriteInplace(t, filepath.Join(target, inplaceMetaDir, "state.json"), `{"release":"rel-1"}`)

	restore := plan.RollbackSteps[len(plan.RollbackSteps)-1]
	out := runInplaceShell(t, restore.Command)
	if !strings.Contains(out, "已回滚到发布前状态（含环境变量）") {
		t.Fatalf("回滚应输出完成事件: %s", out)
	}

	if got := readInplace(t, filepath.Join(target, "app.txt")); got != "v1" {
		t.Fatalf("app.txt 未回到发布前: %q", got)
	}
	if got := readInplace(t, filepath.Join(target, "only-in-v1.txt")); got != "old" {
		t.Fatalf("被删的旧文件未恢复: %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, "only-in-v2.txt")); !os.IsNotExist(err) {
		t.Fatalf("新版本新增的文件应被删除，实际 err=%v", err)
	}
	if got := readInplace(t, filepath.Join(target, inplaceEnvFileName)); got != "ENV_MARK=v1\n" {
		t.Fatalf("环境变量未回到发布前（A6）: %q", got)
	}
	if got := readInplace(t, filepath.Join(target, "uploads", "keep.db")); got != "keep-v2" {
		t.Fatalf("keep_paths 不应被回滚覆盖: %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, "uploads", "runtime.log")); err != nil {
		t.Fatalf("keep_paths 下的运行期文件不应被删除: %v", err)
	}
	if got := readInplace(t, filepath.Join(target, inplaceMetaDir, "state.json")); got != `{"release":"rel-1"}` {
		t.Fatalf(".leadstack 下的元数据不应被回滚覆盖: %q", got)
	}
	if entries, err := os.ReadDir(backupDir); err != nil {
		t.Fatalf("读取备份目录失败: %v", err)
	} else if len(entries) != 1 {
		t.Fatalf("回滚不得新增备份（A7），实际 %d 个: %v", len(entries), entries)
	}
	if leftover, err := os.ReadDir(tmpRoot); err != nil {
		t.Fatalf("读取临时目录失败: %v", err)
	} else if len(leftover) != 0 {
		t.Fatalf("临时解包目录应被清理，实际残留 %v", leftover)
	}
}

// TestInplaceRestoreLegacyBackupKeepsEnv 覆盖升级前的旧备份：归档里没有 .leadstack.env 时
// 保留当前环境变量并留日志，而不是把 env 一起 --delete 掉（PLAN 取舍 2）。
func TestInplaceRestoreLegacyBackupKeepsEnv(t *testing.T) {
	for _, bin := range []string{"tar", "rsync"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("环境缺少 %s，跳过旧备份还原校验", bin)
		}
	}
	target := t.TempDir()
	mustMkdirInplace(t, filepath.Join(target, inplaceBackupDir))
	mustWriteInplace(t, filepath.Join(target, "app.txt"), "v1")
	legacy := filepath.Join(target, inplaceBackupDir, "backup-rel-0"+inplaceBackupSuffix)
	runInplaceShell(t, "tar -czf "+shQuote(legacy)+" -C "+shQuote(target)+
		" --exclude=./"+inplaceMetaDir+" --exclude=./"+inplaceBackupDir+" --exclude=./"+inplaceEnvFileName+" .")

	mustWriteInplace(t, filepath.Join(target, "app.txt"), "v2")
	mustWriteInplace(t, filepath.Join(target, "only-in-v2.txt"), "new")
	mustWriteInplace(t, filepath.Join(target, inplaceEnvFileName), "ENV_MARK=v2\n")

	plan, err := BuildRestoreInplace(target, "rel-0", "", "uploads")
	if err != nil {
		t.Fatalf("BuildRestoreInplace 失败: %v", err)
	}
	out := runInplaceShell(t, plan.Steps[0].Command)
	if !strings.Contains(out, "归档内无 "+inplaceEnvFileName+"，保留当前环境变量") {
		t.Fatalf("旧备份应说明环境变量保留原因: %s", out)
	}
	if got := readInplace(t, filepath.Join(target, "app.txt")); got != "v1" {
		t.Fatalf("旧备份目录未还原: %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, "only-in-v2.txt")); !os.IsNotExist(err) {
		t.Fatalf("新版本新增的文件应被删除，实际 err=%v", err)
	}
	if got := readInplace(t, filepath.Join(target, inplaceEnvFileName)); got != "ENV_MARK=v2\n" {
		t.Fatalf("旧归档缺 env 时应保留当前环境变量，实际 %q", got)
	}
}

// TestInplaceRestoreMissingBackupFails 校验手动回滚到指定版本时，备份缺失必须失败并给出提示。
func TestInplaceRestoreMissingBackupFails(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("环境缺少 rsync，跳过")
	}
	target := t.TempDir()
	mustWriteInplace(t, filepath.Join(target, "app.txt"), "v1")
	plan, err := BuildRestoreInplace(target, "rel-9", "")
	if err != nil {
		t.Fatalf("BuildRestoreInplace 失败: %v", err)
	}
	out, cmdErr := exec.Command("sh", "-c", plan.Steps[0].Command).CombinedOutput()
	if cmdErr == nil {
		t.Fatalf("备份缺失时还原必须失败，输出: %s", out)
	}
	if !strings.Contains(string(out), "回滚不完整，请人工核对："+target) {
		t.Fatalf("失败提示不符: %s", out)
	}
	if got := readInplace(t, filepath.Join(target, "app.txt")); got != "v1" {
		t.Fatalf("失败路径不应改动目录: %q", got)
	}
}

func runInplaceShell(t *testing.T, command string) string {
	t.Helper()
	out, err := exec.Command("sh", "-c", command).CombinedOutput()
	if err != nil {
		t.Fatalf("命令执行失败: %v\n命令: %s\n输出: %s", err, command, out)
	}
	return string(out)
}

func mustMkdirInplace(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建目录 %s 失败: %v", dir, err)
	}
}

func mustWriteInplace(t *testing.T, file, content string) {
	t.Helper()
	if dir := filepath.Dir(file); dir != "" {
		mustMkdirInplace(t, dir)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatalf("写入 %s 失败: %v", file, err)
	}
}

func readInplace(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", file, err)
	}
	return string(b)
}
