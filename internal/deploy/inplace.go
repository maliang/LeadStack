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
// 回滚＝把备份归档解到临时目录后再 `rsync -a -I --delete` 回目标目录（与前进同步对称：
// 新版本多出来的文件会被删除、被删的文件会回来），归档里包含 `.leadstack.env`，
// 因此环境变量也一并回到发布前状态；还原后再次执行重启命令。
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
	// inplaceMetaAliasDir 是元数据目录在 SPEC/文档里的写法（代码里实际是 inplaceMetaDir），
	// 两个名字一并纳入回滚的删除保护名单，避免文档与实现措辞差异留下破坏面。
	inplaceMetaAliasDir = ".leadstack-meta"
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

	// 3) 备份当前目录（不含 LeadStack 自己的目录，但**包含 .leadstack.env**：回滚要连环境变量一起还原）
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
	// 注意：执行器按 RollbackSteps 的**逆序**执行（见 deploy.Executor），
	// 所以这里按「期望顺序的倒序」书写：先声明重启、再声明还原，实际执行即 还原 → 重启。
	//
	// 还原 = 解包到系统临时目录后 rsync -a -I --delete 回目标目录，
	// 与前进同步共用 inplaceProtectedExcludes：.leadstack / .leadstack 备份目录 / keep_paths
	// 自身不参与删除；归档里含 .leadstack.env，环境变量一并回到发布前（A5/A6）。
	// 还原不产生新备份（归档只读），A7 的「回滚不额外堆备份」由此保证。
	plan.RollbackSteps = make([]Step, 0, 2)
	if cmd := strings.TrimSpace(p.RestartCmd); cmd != "" {
		plan.RollbackSteps = append(plan.RollbackSteps, Step{
			Name:           "restart",
			Command:        cmd,
			TimeoutS:       timeoutRestart,
			IgnoreFailure:  true,
			RollbackMarker: true,
		})
	}
	plan.RollbackSteps = append(plan.RollbackSteps, Step{
		Name:           StepRollbackRestore,
		Command:        inplaceRestoreCommand(target, backup, p.KeepPaths, false),
		TimeoutS:       timeoutRollbackRestore,
		RollbackMarker: true,
	})
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
//
// **必须包含 `.leadstack.env`**：备份是回滚的唯一依据，若把环境文件排除在外，
// 回滚后就只能得到「旧代码 + 新配置」，A6 永远不可能通过。
func inplaceBackupCommand(target, backup string, keep []string) string {
	excludes := []string{"--exclude=./" + inplaceMetaDir, "--exclude=./" + inplaceBackupDir}
	for _, k := range keep {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		excludes = append(excludes, "--exclude=./"+strings.TrimPrefix(k, "/"))
	}
	return "tar -czf " + shQuote(backup) + " -C " + shQuote(target) + " " + strings.Join(excludes, " ") + " ."
}

// inplaceProtectedExcludes 返回「前进同步」与「回滚还原」必须共用的保护名单。
//
// 两处都排除同一组路径，回滚才不会把 LeadStack 自己的目录与 keep_paths（上传、日志、
// 数据目录）删掉：rsync 的 --exclude 对接收端同样生效，被排除的路径不会被 --delete 删除。
// 由同一个函数生成，而不是两处手写，避免「改了一处忘了另一处」导致的不对称。
func inplaceProtectedExcludes(keep []string) []string {
	// inplaceMetaAliasDir：SPEC/文档里把元数据目录写作 .leadstack-meta，而代码常量
	// inplaceMetaDir 是 .leadstack。两个名字都保护——存在哪个都不会被 --delete 抹掉。
	excludes := []string{"--exclude=" + inplaceMetaDir, "--exclude=" + inplaceMetaAliasDir, "--exclude=" + inplaceBackupDir}
	for _, k := range keep {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		excludes = append(excludes, "--exclude="+strings.TrimPrefix(k, "/"))
	}
	return excludes
}

// inplaceSyncCommand 生成「用 staging 覆盖目标目录」的命令。
func inplaceSyncCommand(staging, target string, keep []string) string {
	args := append([]string{"-a", "--delete"}, inplaceProtectedExcludes(keep)...)
	// 环境文件只在 StepWriteEnv 里按 EnvJSON 写入；同步阶段不得用制品里的同名文件覆盖它。
	args = append(args, "--exclude="+inplaceEnvFileName)
	return "rsync " + strings.Join(args, " ") + " " + shQuote(staging+"/") + " " + shQuote(target+"/")
}

// inplaceRestoreScript 是「用备份归档还原目标目录」的 POSIX sh 脚本正文。
//
// 位置参数由 inplaceRestoreCommand 传入：$1=备份归档 $2=目标目录，
// $3.. = rsync 的 --exclude 列表（与前进同步同一组）。
//
// 为什么不再直接 `tar -xzf <backup> -C <target>`：tar 只覆盖、不删除，
// 失败版本新加的文件会留在目录里，回滚后与发布前不一致（A5）。
// 改为「先解到临时目录，再 rsync -a -I --delete」——与前进方向完全同一组 exclude。
//
// 必须带 -I（--ignore-times）：rsync 默认的 quick check 只比「大小 + 秒级 mtime」，
// 回滚时备份里的文件与新版本同名同大小且 mtime 落在同一秒是常态（.leadstack.env 的
// ENV_MARK=v1/v2 就是等长），不带 -I 会出现「rsync 退出 0、文件却还是新版本」的假成功。
//
// 临时目录为什么放在系统临时目录而不是 <target>/.leadstack 下：把 rsync 的源目录
// 放在目标目录之内属于病态用法，不同实现行为不一致（macOS 自带 openrsync 实测会
// 静默一个文件都不拷、还把源目录删掉），回滚就变成「假成功」。大站点可用
// LEADSTACK_RESTORE_TMPDIR 指定到空间充足的盘。
const inplaceRestoreScript = `set -e
backup="$1"
target="$2"
shift 2
staging=$(mktemp -d "${LEADSTACK_RESTORE_TMPDIR:-${TMPDIR:-/tmp}}/leadstack-restore.XXXXXX")
trap 'rm -rf "$staging"' EXIT
tar -xzf "$backup" -C "$staging"
if [ -f "$staging/` + inplaceEnvFileName + `" ]; then
    rsync -a -I --delete "$@" "$staging"/ "$target"/
    echo "已回滚到发布前状态（含环境变量）"
else
    echo "归档内无 ` + inplaceEnvFileName + `，保留当前环境变量"
    rsync -a -I --delete "$@" --exclude=` + inplaceEnvFileName + ` "$staging"/ "$target"/
    echo "已回滚到发布前状态（环境变量保留当前值）"
fi
`

// inplaceRestoreCommand 生成「用备份归档还原目标目录」的完整命令。
//
// 还原失败时向 stderr 写「回滚不完整，请人工核对：<目标目录>」并以非 0 退出，
// 这样执行器会把它计入回滚失败（RunOutcome.Message 带「回滚 n 步中有 m 步失败」），
// 命令日志里也留下可直接照做的提示。
//
// requireArchive 为 true 时先 `test -f` 校验归档存在（手动回滚到指定版本用），
// 缺失时同样走「回滚不完整」的提示，而不是 rsync 的原生报错。
func inplaceRestoreCommand(targetDir, backup string, keep []string, requireArchive bool) string {
	args := []string{shQuote(backup), shQuote(targetDir)}
	for _, e := range inplaceProtectedExcludes(keep) {
		args = append(args, shQuote(e))
	}
	cmd := "sh -c " + shQuote(inplaceRestoreScript) + " leadstack-inplace-restore " + strings.Join(args, " ")
	if requireArchive {
		cmd = "test -f " + shQuote(backup) + " && " + cmd
	}
	fail := "echo " + shQuote("回滚不完整，请人工核对："+targetDir) + " >&2; exit 1"
	return cmd + " || { " + fail + "; }"
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

// BuildRestoreInplace 生成「把某次发布的备份恢复回去」的计划（正向步骤：恢复 → 重启）。
//
// 用于回滚到指定历史版本（inplace 模式）：每次发布的备份文件名里带 releaseID，
// 因此可以精确恢复任意一版；恢复后执行 restart_cmd。
//
// 恢复与自动回滚走**同一个生成器**（inplaceRestoreCommand）：rsync -a -I --delete 还原、
// 归档含 .leadstack.env 时一并还原，避免两条回滚路径的行为漂移。
// keepPaths 为可变参数只是为了不破坏既有三参数调用方；调用方应传入 app.KeepPaths，
// 否则默认只保护 .leadstack / .leadstack-backups。
func BuildRestoreInplace(targetDir, releaseID, restartCmd string, keepPaths ...string) (Plan, error) {
	if err := validateAbsPath(targetDir); err != nil {
		return Plan{}, fmt.Errorf("deploy: 接管目录非法: %w", err)
	}
	if err := validateReleaseID(releaseID); err != nil {
		return Plan{}, fmt.Errorf("deploy: ReleaseID 非法: %w", err)
	}
	backup := path.Join(targetDir, inplaceBackupDir, "backup-"+releaseID+inplaceBackupSuffix)
	plan := Plan{ReleaseID: releaseID, Root: targetDir}
	plan.Steps = []Step{
		{
			Name:     StepRollbackRestore,
			Command:  inplaceRestoreCommand(targetDir, backup, keepPaths, true),
			TimeoutS: timeoutRollbackRestore,
		},
	}
	if cmd := strings.TrimSpace(restartCmd); cmd != "" {
		plan.Steps = append(plan.Steps, Step{Name: StepRestart, Command: cmd, TimeoutS: timeoutRestart})
	}
	return plan, nil
}
