package deploy

import (
	"strings"
	"testing"
)

func scriptParams() ScriptParams {
	return ScriptParams{
		AppSlug:   "tuoluojiang",
		ReleaseID: "rel-s1",
		Version:   "2.0.0",
		WorkDir:   "/www/wwwroot/tuoluojiang",
		Script:    "#!/bin/sh\nset -eu\ngit pull --ff-only\nbash start.sh restart\n",
		Rollback:  "#!/bin/sh\nset -eu\ngit reset --hard HEAD@{1}\nbash start.sh restart\n",
		Now:       1000,
	}
}

func TestBuildScriptStepOrder(t *testing.T) {
	plan, err := BuildScript(scriptParams())
	if err != nil {
		t.Fatalf("BuildScript 失败: %v", err)
	}
	got := make([]string, 0, len(plan.Steps))
	for _, s := range plan.Steps {
		got = append(got, s.Name)
	}
	want := []string{StepPrepare, StepWriteScript, StepRunScript, StepWriteMetadata}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("步骤顺序不符\n got: %v\nwant: %v", got, want)
	}
	// 脚本落在 <WorkDir>/.leadstack/scripts/<release>.sh，并在工作目录里执行
	for _, s := range plan.Steps {
		switch s.Name {
		case StepWriteScript:
			if !strings.Contains(s.Command, "/www/wwwroot/tuoluojiang/.leadstack/scripts/rel-s1.sh") {
				t.Fatalf("脚本路径不符: %s", s.Command)
			}
		case StepRunScript:
			if !strings.Contains(s.Command, "cd '/www/wwwroot/tuoluojiang'") || !strings.Contains(s.Command, "bash") {
				t.Fatalf("执行命令不符: %s", s.Command)
			}
		}
	}
}

func TestBuildScriptRequiresScript(t *testing.T) {
	p := scriptParams()
	p.Script = "   \n"
	if _, err := BuildScript(p); err == nil {
		t.Fatal("空脚本应被拒绝")
	}
}

func TestBuildScriptRejectsRelativeWorkDir(t *testing.T) {
	p := scriptParams()
	p.WorkDir = "relative/dir"
	if _, err := BuildScript(p); err == nil {
		t.Fatal("相对工作目录应被拒绝")
	}
}

func TestBuildScriptRollbackOnlyWhenProvided(t *testing.T) {
	plan, err := BuildScript(scriptParams())
	if err != nil {
		t.Fatalf("BuildScript 失败: %v", err)
	}
	if len(plan.RollbackSteps) != 2 {
		t.Fatalf("提供回滚脚本时应有两步，实际 %d", len(plan.RollbackSteps))
	}
	// 逆序执行：先 write（列表末尾），再 run（列表开头）
	if plan.RollbackSteps[0].Name != StepRunRollback || plan.RollbackSteps[len(plan.RollbackSteps)-1].Name != StepWriteRollback {
		t.Fatalf("回滚步骤不符（应按逆序执行）: %+v", plan.RollbackSteps)
	}
	if !plan.RollbackSteps[0].IgnoreFailure {
		t.Fatal("回滚脚本失败不应覆盖原始失败原因")
	}

	noRollback := scriptParams()
	noRollback.Rollback = ""
	plan2, err := BuildScript(noRollback)
	if err != nil {
		t.Fatalf("BuildScript 失败: %v", err)
	}
	if len(plan2.RollbackSteps) != 0 {
		t.Fatalf("未提供回滚脚本时不应生成回滚步骤: %+v", plan2.RollbackSteps)
	}
}
