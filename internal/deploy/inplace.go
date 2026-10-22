package deploy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

// InplaceParams 是「接管既有目录」部署的参数（deploy_mode=inplace）。
//
// 适用场景：站点/应用已经在跑，目录里有别人放好的配置、上传文件、数据目录，
// 不能换成 releases/<id> + current 的结构。此时的做法是：
//
//	备份目标目录 → 解包到临时目录 → rsync 覆盖（保留 keep_paths）→ 写环境文件 →
//	（首版）执行初始化脚本 → 执行重启命令
//
// 回滚＝把备份 tar 解回目标目录并再次执行重启命令。
type InplaceParams struct {
	AppSlug       string
	ReleaseID     string
	Version       string
	TargetDir     string // 绝对路径；已有目录（例如 /www/wwwroot/shop 或 /opt/app）
	PrevReleaseID string
	Artifact      Artifact
	// RestartCmd 是发布后执行的重启命令（如 systemctl restart x / docker compose up -d）；
	// 为空表示不需要重启（纯静态站点）。
	RestartCmd string
	// InitScript 只在「该服务器第一次成功部署」时执行（由调用方判定 FirstDeploy）。
	InitScript  string
	FirstDeploy bool
	// EnvJSON 是应用级环境变量（JSON 对象），会写成 <TargetDir>/.leadstack.env（0600）。
	EnvJSON string
	// KeepPaths 是同步时保留、不参与覆盖/删除的路径（相对 TargetDir，支持 * 通配）。
	KeepPaths []string
	Now       int64
}

// 内部布局：所有 LeadStack 自身的文件都收在 .leadstack* 下，避免污染站点目录。
const (
	inplaceMetaDir      = ".leadstack"
	inplaceBackupDir    = ".leadstack-backups"
	inplaceEnvFileName  = ".leadstack.env"
	inplaceBackupSuffix = ".tar.gz"
)

// BuildInplace 编排一次「接管既有目录」的部署计划（纯函数）。
func BuildInplace(p InplaceParams) (Plan, error) {
	if err := validateSlug(p.AppSlug); err != nil {
		return Plan{}, fmt.Errorf("deploy: AppSlug 非法: %w", err)
	}
	if err := validateReleaseID(p.ReleaseID); err != nil {
		return Plan{}, fmt.Errorf("deploy: ReleaseID 非法: %w", err)
	}
	if err := validateAbsPath(p.TargetDir); err != nil {
		return Plan{}, fmt.Errorf("deploy: 接管目录非法: %w", err)
	}
	if err := validateArtifact(p.Artifact, KindBinary); err != nil {
		return Plan{}, err
	}

	target := p.TargetDir
	backup := path.Join(target, inplaceBackupDir, "backup-"+p.ReleaseID+inplaceBackupSuffix)
	staging := path.Join(target, inplaceMetaDir, "staging-"+p.ReleaseID)
	incoming := path.Join(target, inplaceMetaDir, "incoming-"+p.ReleaseID)
	tmpArtifact := path.Join(incoming, artifactTempName)

	plan := Plan{AppSlug: p.AppSlug, ReleaseID: p.ReleaseID, Version: p.Version, Root: target}
	steps := make([]Step, 0, 12)

	// 1) 目录就绪
	steps = append(steps, Step{
		Name:     StepPrepare,
		Command:  "mkdir -p " + shQuote(incoming) + " " + shQuote(staging) + " " + shQuote(path.Join(target, inplaceBackupDir)) + " " + shQuote(path.Join(target, inplaceMetaDir, "releases")),
		TimeoutS: timeoutPrepare,
	})

	// 2) 下载 + 校验 + 解包到临时目录（不碰现有文件）
	if p.Artifact.Kind == ArtifactURL || p.Artifact.Kind == ArtifactUpload {
		steps = append(steps, Step{
			Name: StepDownload,
			Command: "curl -fsSL --retry 3 --retry-delay 2 --connect-timeout 30 -o " +
				shQuote(tmpArtifact) + " " + shQuote(p.Artifact.Location),
			TimeoutS: timeoutDownload,
		})
		steps = append(steps, Step{Name: StepVerifySHA256, Command: sha256VerifyCommand(tmpArtifact, p.Artifact.SHA256), TimeoutS: timeoutVerify})
		steps = append(steps, Step{Name: StepUnpack, Command: unpackCommand(staging, tmpArtifact, p.Artifact.Location), TimeoutS: timeoutUnpack})
	}

	// 3) 备份当前目录（不含 LeadStack 自己的目录）
	steps = append(steps, Step{
		Name:     StepBackup,
		Command:  inplaceBackupCommand(target, backup, p.KeepPaths),
		TimeoutS: timeoutBackup,
	})

	// 4) 同步：rsync 覆盖（--delete 清掉旧版本残留的静态资源）
	steps = append(steps, Step{
		Name:     StepSync,
		Command:  inplaceSyncCommand(staging, target, p.KeepPaths),
		TimeoutS: timeoutSync,
	})

	// 5) 环境变量文件（0600；命令里是 base64，日志层会按 secret 脱敏）
	if env := strings.TrimSpace(p.EnvJSON); env != "" {
		content, err := envFileContent(env)
		if err != nil {
			return Plan{}, err
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(content))
		steps = append(steps, Step{
			Name:     StepWriteEnv,
			Command:  "printf %s " + shQuote(encoded) + " | base64 -d > " + shQuote(path.Join(target, inplaceEnvFileName)) + " && chmod 0600 " + shQuote(path.Join(target, inplaceEnvFileName)),
			TimeoutS: timeoutWrite,
			Secret:   true,
		})
	}

	// 6) 首版初始化（只执行一次）
	if p.FirstDeploy && strings.TrimSpace(p.InitScript) != "" {
		scriptPath := path.Join(target, inplaceMetaDir, "init-"+p.ReleaseID+".sh")
		step := writeFileStep(StepInit, scriptPath, p.InitScript, modeExecutable)
		steps = append(steps, step)
		steps = append(steps, Step{Name: StepInitRun, Command: "sh " + shQuote(scriptPath), TimeoutS: timeoutInit})
	}

	// 7) 重启
	if cmd := strings.TrimSpace(p.RestartCmd); cmd != "" {
		steps = append(steps, Step{Name: StepRestart, Command: cmd, TimeoutS: timeoutRestart})
	}

	// 8) 元数据（便于排障；失败不影响发布）
	steps = append(steps, metadataStep(target, p.ReleaseID, releaseMetadata{
		AppSlug: p.AppSlug, Root: target, ReleaseID: p.ReleaseID, Version: p.Version,
		PrevReleaseID: p.PrevReleaseID, ArtifactKind: p.Artifact.Kind,
		ArtifactLoc: p.Artifact.Location, ArtifactSHA: p.Artifact.SHA256, CreatedAt: p.Now,
	}))

	plan.Steps = steps
	plan.RollbackSteps = []Step{
		{
			Name:           StepRollbackRestore,
			Command:        "tar -xzf " + shQuote(backup) + " -C " + shQuote(target),
			TimeoutS:       timeoutRollbackRestore,
			IgnoreFailure:  false,
			RollbackMarker: true,
		},
	}
	if cmd := strings.TrimSpace(p.RestartCmd); cmd != "" {
		plan.RollbackSteps = append(plan.RollbackSteps, Step{
			Name:           "restart",
			Command:        cmd,
			TimeoutS:       timeoutRestart,
			IgnoreFailure:  true,
			RollbackMarker: true,
		})
	}
	return plan, nil
}

// envFileContent 把 JSON 对象转成 KEY=VALUE 行（.env 风格）。
func envFileContent(raw string) (string, error) {
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return "", fmt.Errorf("deploy: 环境变量必须是 {\"KEY\":\"VALUE\"} 形式: %w", err)
	}
	var b strings.Builder
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// 稳定输出，便于 diff 与排障
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, k := range keys {
		v := strings.ReplaceAll(m[k], "\n", " ")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(v)
		b.WriteString("\n")
	}
	return b.String(), nil
}

// inplaceBackupCommand 生成「打包目标目录」的命令（排除 LeadStack 目录与保留路径）。
func inplaceBackupCommand(target, backup string, keep []string) string {
	excludes := []string{"--exclude=./" + inplaceMetaDir, "--exclude=./" + inplaceBackupDir, "--exclude=./" + inplaceEnvFileName}
	for _, k := range keep {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		excludes = append(excludes, "--exclude=./"+strings.TrimPrefix(k, "/"))
	}
	return "tar -czf " + shQuote(backup) + " -C " + shQuote(target) + " " + strings.Join(excludes, " ") + " ."
}

// inplaceSyncCommand 生成「用 staging 覆盖目标目录」的命令。
func inplaceSyncCommand(staging, target string, keep []string) string {
	args := []string{"-a", "--delete", "--exclude=" + inplaceMetaDir, "--exclude=" + inplaceBackupDir, "--exclude=" + inplaceEnvFileName}
	for _, k := range keep {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		args = append(args, "--exclude="+strings.TrimPrefix(k, "/"))
	}
	return "rsync " + strings.Join(args, " ") + " " + shQuote(staging+"/") + " " + shQuote(target+"/")
}

var errNoTarget = errors.New("deploy: 接管模式必须提供目标目录")

// ValidateInplaceParams 供 API 层复用的参数校验。
func ValidateInplaceParams(targetDir string) error {
	if strings.TrimSpace(targetDir) == "" {
		return errNoTarget
	}
	if !strings.HasPrefix(targetDir, "/") {
		return errors.New("deploy: 接管目录必须是绝对路径")
	}
	return nil
}
