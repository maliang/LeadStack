package deploy

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// ScriptParams 是「脚本模式」部署的参数（deploy_mode=script）。
//
// 语义：**不需要制品**，一次发布＝在目标机执行一段用户脚本（内容来自发布的模板快照），
// 脚本负责拉代码（git pull / rsync / 解压…）、装依赖、重启进程等全部动作。
// 平台提供的是：逐台执行、实时日志、失败标记、健康检查、以及（可选的）回滚脚本。
//
// 典型用法（宝塔直装的 Laravel/Swoole 项目）：
//
//	#!/bin/sh
//	set -eu
//	cd /www/wwwroot/tuoluojiang
//	git pull --ff-only
//	composer install --no-dev --no-interaction
//	bash start.sh restart
type ScriptParams struct {
	AppSlug   string
	ReleaseID string
	Version   string
	// WorkDir 是脚本落盘目录（必须绝对路径）：脚本写到 <WorkDir>/.leadstack/scripts/<release>.sh。
	// 一般填站点/项目根目录，方便脚本里 cd 到项目。
	WorkDir       string
	Script        string // 已渲染的更新脚本（必填）
	Rollback      string // 可选回滚脚本（失败或健康检查不通过时执行）
	PrevReleaseID string
	Now           int64
}

// 脚本模式的固定文件名与目录。
const (
	scriptDirOnTarget    = ".leadstack/scripts"
	scriptFileSuffix     = ".sh"
	rollbackScriptSuffix = ".rollback.sh"
)

// BuildScript 编排一次脚本模式发布（纯函数）。
func BuildScript(p ScriptParams) (Plan, error) {
	if err := validateSlug(p.AppSlug); err != nil {
		return Plan{}, fmt.Errorf("deploy: AppSlug 非法: %w", err)
	}
	if err := validateReleaseID(p.ReleaseID); err != nil {
		return Plan{}, fmt.Errorf("deploy: ReleaseID 非法: %w", err)
	}
	if err := validateAbsPath(p.WorkDir); err != nil {
		return Plan{}, fmt.Errorf("deploy: 脚本工作目录非法: %w", err)
	}
	if strings.TrimSpace(p.Script) == "" {
		return Plan{}, errors.New("deploy: 脚本模式需要模板内容作为更新脚本（binary 模板不能为空）")
	}

	scriptDir := path.Join(p.WorkDir, scriptDirOnTarget)
	scriptPath := path.Join(scriptDir, p.ReleaseID+scriptFileSuffix)
	plan := Plan{AppSlug: p.AppSlug, ReleaseID: p.ReleaseID, Version: p.Version, Root: p.WorkDir}

	steps := make([]Step, 0, 4)
	steps = append(steps, Step{
		Name:     StepPrepare,
		Command:  "mkdir -p " + shQuote(scriptDir),
		TimeoutS: timeoutPrepare,
	})
	steps = append(steps, writeFileStep(StepWriteScript, scriptPath, p.Script, modeExecutable))
	// 用 bash 跑（服务器上 sh 可能是 dash，脚本里常见 bash 语义），并显式 cd 到工作目录。
	steps = append(steps, Step{
		Name:     StepRunScript,
		Command:  "cd " + shQuote(p.WorkDir) + " && bash " + shQuote(scriptPath),
		TimeoutS: timeoutScript,
	})
	steps = append(steps, metadataStep(p.WorkDir, p.ReleaseID, releaseMetadata{
		AppSlug: p.AppSlug, Root: p.WorkDir, ReleaseID: p.ReleaseID, Version: p.Version,
		PrevReleaseID: p.PrevReleaseID, ArtifactKind: "script", CreatedAt: p.Now,
	}))
	plan.Steps = steps

	// 执行器按 RollbackSteps 的逆序执行，所以先声明「运行」再声明「写入」，
	// 实际执行顺序是 write-rollback-script → run-rollback-script。
	if rb := strings.TrimSpace(p.Rollback); rb != "" {
		rbPath := path.Join(scriptDir, p.ReleaseID+rollbackScriptSuffix)
		plan.RollbackSteps = []Step{
			{
				Name:           StepRunRollback,
				Command:        "cd " + shQuote(p.WorkDir) + " && bash " + shQuote(rbPath),
				TimeoutS:       timeoutScript,
				RollbackMarker: true,
				IgnoreFailure:  true, // 回滚本身失败不应该掩盖原始失败原因
			},
			writeFileStep(StepWriteRollback, rbPath, rb, modeExecutable),
		}
	}
	return plan, nil
}
