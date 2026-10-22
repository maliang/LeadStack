package deploy

import (
	"encoding/base64"
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
