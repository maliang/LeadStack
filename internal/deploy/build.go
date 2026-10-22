package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

// 步骤名（稳定标识，会进 release_logs / release_runs.failed_step，不要随意改名）。
const (
	StepPrepare            = "prepare"
	StepDownload           = "download"
	StepVerifySHA256       = "verify-sha256"
	StepUnpack             = "unpack"
	StepWriteRestartScript = "write-restart-script"
	StepWriteCompose       = "write-compose"
	StepComposePull        = "compose-pull"
	StepComposeUp          = "compose-up"
	StepWriteUnit          = "write-unit"
	StepInstallUnit        = "install-unit"
	StepDaemonReload       = "daemon-reload"
	StepSwitchCurrent      = "switch-current"
	StepRestart            = "restart"
	StepHealthCheck        = "health-check"
	StepWriteMetadata      = "write-metadata"
	StepCleanupOldReleases = "cleanup-old-releases"
	// 接管模式（deploy_mode=inplace）专用步骤。
	StepBackup             = "backup-existing"
	StepSync               = "sync-inplace"
	StepWriteEnv           = "write-env"
	StepInit               = "write-init-script"
	StepInitRun            = "run-init-script"
	StepRollbackRestore    = "rollback-restore-backup"
	StepWriteScript        = "write-deploy-script"
	StepRunScript          = "run-deploy-script"
	StepWriteRollback      = "write-rollback-script"
	StepRunRollback        = "run-rollback-script"
	artifactTempName       = ".artifact"
	restartScriptName      = "leadstack-start.sh"
	composeFileName        = "docker-compose.yml"
	systemdUnitDirOnTarget = "/etc/systemd/system"
	systemdUnitSuffix      = ".service"
)

// 各步骤超时（秒）：下载/拉镜像留足时间，清理只做本地删除。
const (
	timeoutPrepare         = 60
	timeoutDownload        = 900
	timeoutVerify          = 300
	timeoutUnpack          = 300
	timeoutWrite           = 30
	timeoutInstall         = 60
	timeoutReload          = 120
	timeoutSwitch          = 60
	timeoutRestart         = 180
	timeoutHealth          = 180
	timeoutPull            = 900
	timeoutUp              = 600
	timeoutCleanup         = 300
	timeoutBackup          = 600
	timeoutSync            = 600
	timeoutInit            = 900
	timeoutRollbackRestore = 600
	timeoutScript          = 1800
)

// releaseMetadata 是写到 <root>/.leadstack/releases/<releaseID>.json 的快照（§24）。
type releaseMetadata struct {
	AppSlug       string `json:"app_slug"`
	Root          string `json:"root"`
	ReleaseID     string `json:"release_id"`
	Version       string `json:"version"`
	PrevReleaseID string `json:"prev_release_id,omitempty"`
	ArtifactKind  string `json:"artifact_kind"`
	ArtifactLoc   string `json:"artifact_location"`
	ArtifactSHA   string `json:"artifact_sha256,omitempty"`
	CreatedAt     int64  `json:"created_at"`
}

// Build 根据模板 + 制品 + 目标信息编排执行计划（**纯函数**：不触网、不读文件、不执行命令）。
//
// 步骤顺序（§24 的安全前提：校验一定在切 current 之前）：
//
//	prepare → download → verify-sha256 → unpack →（按类型写文件）→ write-metadata
//	→ switch-current → restart/health → cleanup-old-releases
func Build(tmpl Template, art Artifact, p BuildParams) (Plan, error) {
	if err := validateBuildParams(p); err != nil {
		return Plan{}, err
	}
	kind := tmpl.Kind
	switch kind {
	case KindBinary, KindCompose, KindSystemd:
	default:
		return Plan{}, fmt.Errorf("deploy: 未知模板类型 %q（支持 %s/%s/%s）", string(kind), KindBinary, KindCompose, KindSystemd)
	}
	if err := validateArtifact(art, kind); err != nil {
		return Plan{}, err
	}
	if strings.TrimSpace(tmpl.Content) == "" && kind != KindBinary {
		return Plan{}, fmt.Errorf("deploy: %s 模板内容不能为空", kind)
	}

	appRoot := path.Join(p.Root, p.AppSlug)
	releaseDir := path.Join(appRoot, "releases", p.ReleaseID)
	rendered, err := Render(tmpl.Content, map[string]string{
		VarAppSlug:        p.AppSlug,
		VarVersion:        p.Version,
		VarReleaseID:      p.ReleaseID,
		VarRoot:           appRoot,
		VarReleaseDir:     releaseDir,
		VarArtifactURL:    art.Location,
		VarArtifactSHA256: art.SHA256,
		VarPrevReleaseID:  p.PrevReleaseID,
	})
	if err != nil {
		return Plan{}, fmt.Errorf("deploy: 渲染模板失败: %w", err)
	}

	plan := Plan{
		AppSlug:   p.AppSlug,
		ReleaseID: p.ReleaseID,
		Version:   p.Version,
		Root:      appRoot,
	}
	meta := releaseMetadata{
		AppSlug:       p.AppSlug,
		Root:          appRoot,
		ReleaseID:     p.ReleaseID,
		Version:       p.Version,
		PrevReleaseID: p.PrevReleaseID,
		ArtifactKind:  art.Kind,
		ArtifactLoc:   art.Location,
		ArtifactSHA:   art.SHA256,
		CreatedAt:     p.Now,
	}
	steps := make([]Step, 0, 16)
	steps = append(steps, prepareStep(appRoot, releaseDir))

	// url/upload 制品需要下载 + 校验；image 制品由容器运行时拉取。
	if art.Kind == ArtifactURL || art.Kind == ArtifactUpload {
		steps = append(steps, downloadStep(releaseDir, art))
		steps = append(steps, verifyStep(releaseDir, art))
		steps = append(steps, unpackStep(releaseDir, art))
	}

	switch kind {
	case KindBinary:
		hasRestart := strings.TrimSpace(rendered) != ""
		if hasRestart {
			steps = append(steps, writeFileStep(StepWriteRestartScript, path.Join(releaseDir, restartScriptName), rendered, modeExecutable))
		}
		steps = append(steps, metadataStep(appRoot, p.ReleaseID, meta))
		steps = append(steps, switchStep(appRoot, releaseDir, p.ReleaseID))
		if hasRestart {
			steps = append(steps, restartStep(path.Join(releaseDir, restartScriptName), timeoutRestart))
		}
		steps = append(steps, cleanupStep(appRoot, p.ReleaseID, p.PrevReleaseID))
		plan.RollbackSteps = binaryRollbackSteps(appRoot, releaseDir, p.ReleaseID, p.PrevReleaseID)

	case KindCompose:
		composeFile := path.Join(releaseDir, composeFileName)
		steps = append(steps, writeFileStep(StepWriteCompose, composeFile, rendered, modeRegular))
		steps = append(steps, metadataStep(appRoot, p.ReleaseID, meta))
		steps = append(steps, Step{Name: StepComposePull, Command: composeCommand(composeFile, "pull"), TimeoutS: timeoutPull})
		steps = append(steps, Step{Name: StepComposeUp, Command: composeCommand(composeFile, "up -d"), TimeoutS: timeoutUp})
		steps = append(steps, Step{Name: StepHealthCheck, Command: composeHealthCommand(composeFile), TimeoutS: timeoutHealth})
		steps = append(steps, switchStep(appRoot, releaseDir, p.ReleaseID))
		steps = append(steps, cleanupStep(appRoot, p.ReleaseID, p.PrevReleaseID))
		plan.RollbackSteps = composeRollbackSteps(appRoot, releaseDir, p.ReleaseID, p.PrevReleaseID)

	case KindSystemd:
		unitInRelease := path.Join(releaseDir, p.AppSlug+systemdUnitSuffix)
		unitInstalled := path.Join(systemdUnitDirOnTarget, p.AppSlug+systemdUnitSuffix)
		steps = append(steps, writeFileStep(StepWriteUnit, unitInRelease, rendered, modeRegular))
		steps = append(steps, Step{
			Name:     StepInstallUnit,
			Command:  "install -m 0644 " + shQuote(unitInRelease) + " " + shQuote(unitInstalled),
			TimeoutS: timeoutInstall,
		})
		steps = append(steps, Step{Name: StepDaemonReload, Command: "systemctl daemon-reload", TimeoutS: timeoutReload})
		steps = append(steps, switchStep(appRoot, releaseDir, p.ReleaseID))
		steps = append(steps, systemdRestartStep(p.AppSlug))
		steps = append(steps, systemdHealthStep(p.AppSlug))
		steps = append(steps, metadataStep(appRoot, p.ReleaseID, meta))
		steps = append(steps, cleanupStep(appRoot, p.ReleaseID, p.PrevReleaseID))
		plan.RollbackSteps = systemdRollbackSteps(appRoot, releaseDir, p.ReleaseID, p.PrevReleaseID, p.AppSlug)
	}

	plan.Steps = steps
	return plan, nil
}

const (
	modeRegular    = 0
	modeExecutable = 1
)

func validateBuildParams(p BuildParams) error {
	if err := validateSlug(p.AppSlug); err != nil {
		return fmt.Errorf("deploy: AppSlug 非法: %w", err)
	}
	if err := validateReleaseID(p.ReleaseID); err != nil {
		return fmt.Errorf("deploy: ReleaseID 非法: %w", err)
	}
	if err := validateVersion(p.Version); err != nil {
		return fmt.Errorf("deploy: Version 非法: %w", err)
	}
	if err := validateAbsPath(p.Root); err != nil {
		return fmt.Errorf("deploy: Root 非法: %w", err)
	}
	if p.PrevReleaseID != "" {
		if err := validateReleaseID(p.PrevReleaseID); err != nil {
			return fmt.Errorf("deploy: PrevReleaseID 非法: %w", err)
		}
		if p.PrevReleaseID == p.ReleaseID {
			return errors.New("deploy: PrevReleaseID 不能等于本次 ReleaseID")
		}
	}
	if p.Now < 0 {
		return errors.New("deploy: Now 不能为负数")
	}
	return nil
}

// validateArtifact 按模板类型校验制品（安全边界：Location/SHA256 不合法就不生成任何计划）。
func validateArtifact(art Artifact, kind Kind) error {
	if art.SizeBytes < 0 {
		return errors.New("deploy: 制品 size_bytes 不能为负")
	}
	switch art.Kind {
	case ArtifactURL, ArtifactUpload:
		if err := validateURL(art.Location); err != nil {
			if art.Kind == ArtifactUpload {
				return fmt.Errorf("deploy: upload 制品的 location 必须是控制端可下载的**绝对 http(s) URL**（由 backend 把存储 key 解析成地址）: %w", err)
			}
			return fmt.Errorf("deploy: 制品 location 非法: %w", err)
		}
		if art.SHA256 == "" {
			return errors.New("deploy: url/upload 制品必须提供 sha256（否则无法校验，回滚就没有保障）")
		}
		if err := validateSHA256(art.SHA256); err != nil {
			return fmt.Errorf("deploy: 制品 sha256 非法: %w", err)
		}
	case ArtifactImage:
		if err := validateImageRef(art.Location); err != nil {
			return fmt.Errorf("deploy: 镜像引用非法: %w", err)
		}
		if err := validateSHA256(art.SHA256); err != nil {
			return fmt.Errorf("deploy: 制品 sha256 非法: %w", err)
		}
		if kind == KindBinary || kind == KindSystemd {
			return fmt.Errorf("deploy: %s 模板需要 url/upload 制品，不支持 image", kind)
		}
	default:
		return fmt.Errorf("deploy: 未知制品类型 %q（支持 %s/%s/%s）", art.Kind, ArtifactURL, ArtifactUpload, ArtifactImage)
	}
	return nil
}

func prepareStep(appRoot, releaseDir string) Step {
	return Step{
		Name:     StepPrepare,
		Command:  "mkdir -p " + shQuote(releaseDir) + " " + shQuote(path.Join(appRoot, ".leadstack", "releases")),
		TimeoutS: timeoutPrepare,
	}
}

func downloadStep(releaseDir string, art Artifact) Step {
	return Step{
		Name: StepDownload,
		Command: "curl -fsSL --retry 3 --retry-delay 2 --connect-timeout 30 -o " +
			shQuote(path.Join(releaseDir, artifactTempName)) + " " + shQuote(art.Location),
		TimeoutS: timeoutDownload,
	}
}

func verifyStep(releaseDir string, art Artifact) Step {
	return Step{
		Name:     StepVerifySHA256,
		Command:  sha256VerifyCommand(path.Join(releaseDir, artifactTempName), art.SHA256),
		TimeoutS: timeoutVerify,
	}
}

func unpackStep(releaseDir string, art Artifact) Step {
	return Step{
		Name:     StepUnpack,
		Command:  unpackCommand(releaseDir, path.Join(releaseDir, artifactTempName), art.Location),
		TimeoutS: timeoutUnpack,
	}
}

// writeFileStep 用 quoted heredoc 落文件；modeExecutable 时追加 chmod 0755。
func writeFileStep(name, filePath, content string, mode int) Step {
	cmd := heredocWrite(filePath, content)
	if mode == modeExecutable {
		cmd += "chmod 0755 " + shQuote(filePath) + "\n"
	}
	return Step{Name: name, Command: strings.TrimRight(cmd, "\n"), TimeoutS: timeoutWrite}
}

func metadataStep(appRoot, releaseID string, meta releaseMetadata) Step {
	blob, err := json.MarshalIndent(meta, "", "  ")
	if err != nil { // 不可能发生：字段都是基本类型
		blob = []byte("{}")
	}
	return Step{
		Name:          StepWriteMetadata,
		Command:       heredocWrite(path.Join(appRoot, ".leadstack", "releases", releaseID+".json"), string(blob)),
		TimeoutS:      timeoutWrite,
		IgnoreFailure: true, // 元数据写失败不该让发布失败
	}
}

func switchStep(appRoot, releaseDir, releaseID string) Step {
	return Step{
		Name:     StepSwitchCurrent,
		Command:  switchCurrentCommand(appRoot, releaseDir, releaseID),
		TimeoutS: timeoutSwitch,
	}
}

func restartStep(scriptPath string, timeoutS int) Step {
	return Step{Name: StepRestart, Command: "sh " + shQuote(scriptPath), TimeoutS: timeoutS}
}

func systemdRestartStep(slug string) Step {
	return Step{Name: StepRestart, Command: "systemctl restart " + shQuote(slug), TimeoutS: timeoutRestart}
}

func systemdHealthStep(slug string) Step {
	return Step{Name: StepHealthCheck, Command: "systemctl is-active --quiet " + shQuote(slug), TimeoutS: timeoutHealth}
}

func cleanupStep(appRoot, releaseID, prevReleaseID string) Step {
	return Step{
		Name:          StepCleanupOldReleases,
		Command:       cleanupCommand(appRoot, releaseID, prevReleaseID, DefaultKeepReleases),
		TimeoutS:      timeoutCleanup,
		IgnoreFailure: true, // 清理失败不影响发布结果
	}
}

// rollbackSwitchStep 生成「把 current 切回上一版本」的回滚步骤；
// 首次发布（没有上一版本）时只把自己刚创建的链接撤掉，绝不误删别人的 current。
func rollbackSwitchStep(appRoot, releaseDir, prevReleaseID string) Step {
	if prevReleaseID == "" {
		return Step{
			Name:          StepSwitchCurrent,
			Command:       unlinkCurrentIfPointsToCommand(appRoot, releaseDir),
			TimeoutS:      timeoutSwitch,
			IgnoreFailure: true,
		}
	}
	return Step{
		Name:          StepSwitchCurrent,
		Command:       switchCurrentCommand(appRoot, path.Join(appRoot, "releases", prevReleaseID), prevReleaseID),
		TimeoutS:      timeoutSwitch,
		IgnoreFailure: true,
	}
}

// 下面三个函数返回的切片按「逆执行顺序」排列（Executor 从后往前执行）。
func binaryRollbackSteps(appRoot, releaseDir, releaseID, prevReleaseID string) []Step {
	steps := make([]Step, 0, 2)
	if prevReleaseID != "" {
		steps = append(steps, Step{
			Name:          StepRestart,
			Command:       binaryRestartRollbackCommand(path.Join(appRoot, "releases", prevReleaseID, restartScriptName)),
			TimeoutS:      timeoutRestart,
			IgnoreFailure: true,
		})
	}
	return append(steps, rollbackSwitchStep(appRoot, releaseDir, prevReleaseID))
}

func composeRollbackSteps(appRoot, releaseDir, releaseID, prevReleaseID string) []Step {
	steps := make([]Step, 0, 2)
	if prevReleaseID != "" {
		steps = append(steps, Step{
			Name:          StepComposeUp,
			Command:       composeUpRollbackCommand(path.Join(appRoot, "releases", prevReleaseID, composeFileName)),
			TimeoutS:      timeoutUp,
			IgnoreFailure: true,
		})
	}
	return append(steps, rollbackSwitchStep(appRoot, releaseDir, prevReleaseID))
}

func systemdRollbackSteps(appRoot, releaseDir, releaseID, prevReleaseID, slug string) []Step {
	steps := make([]Step, 0, 4)
	steps = append(steps, Step{
		Name:          StepRestart,
		Command:       "systemctl restart " + shQuote(slug),
		TimeoutS:      timeoutRestart,
		IgnoreFailure: true,
	})
	steps = append(steps, Step{
		Name:          StepDaemonReload,
		Command:       "systemctl daemon-reload",
		TimeoutS:      timeoutReload,
		IgnoreFailure: true,
	})
	if prevReleaseID != "" {
		prevUnit := path.Join(appRoot, "releases", prevReleaseID, slug+systemdUnitSuffix)
		steps = append(steps, Step{
			Name: StepInstallUnit,
			Command: "if [ -f " + shQuote(prevUnit) + " ]; then install -m 0644 " + shQuote(prevUnit) + " " +
				shQuote(path.Join(systemdUnitDirOnTarget, slug+systemdUnitSuffix)) + "; fi",
			TimeoutS:      timeoutInstall,
			IgnoreFailure: true,
		})
	}
	return append(steps, rollbackSwitchStep(appRoot, releaseDir, prevReleaseID))
}
