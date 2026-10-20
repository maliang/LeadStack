package deploy

import (
	"path"
	"strconv"
	"strings"
)

// 本文件集中生成**目标机上真正执行**的 shell 片段。所有路径都经过 Build 的白名单校验，
// 但这里仍然统一走 shQuote / heredoc，避免任何值直接裸拼进命令。

// heredocDelimiter 是写文件用的 quoted heredoc 分隔符基串；
// 若内容里出现它，会不断加后缀直到不冲突（防止内容提前终止 heredoc 造成命令注入）。
const heredocDelimiter = "LEADSTACK_HEREDOC_EOF"

// shQuote 把值包成 POSIX 单引号字面量（内部单引号用 '\” 转义），杜绝命令注入。
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// heredocWrite 生成「把 content 原样写入 path」的命令。
//
// 用 `<<'DELIM'`（quoted heredoc）：内容不做变量展开/命令替换，
// 因此模板内容里出现 $、反引号、引号都不会执行 —— 这是写 compose/unit/脚本文件的安全路径。
func heredocWrite(filePath, content string) string {
	delim := heredocDelimiter
	for strings.Contains(content, delim) {
		delim += "_X"
	}
	var b strings.Builder
	b.WriteString("cat > ")
	b.WriteString(shQuote(filePath))
	b.WriteString(" <<'")
	b.WriteString(delim)
	b.WriteString("'\n")
	b.WriteString(content)
	if content != "" && !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(delim)
	b.WriteString("\n")
	return b.String()
}

// sha256VerifyCommand 生成跨平台（Linux/macOS）的 sha256 校验命令：
// sha256sum → shasum -a 256 → openssl dgst -sha256 逐级回退，校验失败打印原因并非 0 退出。
func sha256VerifyCommand(file, want string) string {
	f := shQuote(file)
	return "f=" + f + "; want=" + shQuote(want) + "; " +
		"if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum \"$f\" | awk '{print $1}'); " +
		"elif command -v shasum >/dev/null 2>&1; then got=$(shasum -a 256 \"$f\" | awk '{print $1}'); " +
		"else got=$(openssl dgst -sha256 \"$f\" | awk '{print $NF}'); fi; " +
		"[ -n \"$got\" ] || { echo 'sha256 校验失败：缺少 sha256sum/shasum/openssl' >&2; exit 1; }; " +
		"[ \"$got\" = \"$want\" ] || { echo \"sha256 校验失败：期望 $want 实际 $got\" >&2; exit 1; }"
}

// switchCurrentCommand 生成「把 <root>/current 切换到 releaseDir」的命令。
//
// 这是回滚能不能工作的前提，写法是在 macOS（Darwin 27）上逐个实测后选定的：
//
//   - 用 `ln -sfn <releaseDir> current`：current 是**符号链接**时，BSD/GNU 的 -n/-h
//     都不跟随链接，直接替换链接本身（实测 readlink 变成新目标，不产生嵌套）；
//   - 不能用 `mv -f <临时链接> current`：BSD `mv` 遇到「指向目录的符号链接」会跟随，
//     把源移进那个目录里（实测 current 仍指向旧版本，切换静默失败），所以不用 mv；
//   - current 是**真实目录**（历史遗留的错误部署）时，`ln -sfn` 会在目录里造嵌套链接
//     （实测生成 current/<releaseID>），所以先把真实目录原样挪到
//     `.current.bak.<releaseID>.$$` 保留数据，再创建符号链接 —— 绝不 rm -rf 可能正在运行的目录。
func switchCurrentCommand(root, releaseDir, releaseID string) string {
	cur := path.Join(root, "current")
	return "cd " + shQuote(root) + " && " +
		"if [ -e " + shQuote(cur) + " ] && [ ! -L " + shQuote(cur) + " ]; then mv -f " + shQuote(cur) +
		" \".current.bak." + releaseID + ".$$\"; fi && " +
		"ln -sfn " + shQuote(releaseDir) + " " + shQuote(cur)
}

// unlinkCurrentIfPointsToCommand 生成「只有当 current 指向本次 release 时才删除它」的命令，
// 用于首次发布（没有上一版本）时的回滚：不会误删别人的 current。
func unlinkCurrentIfPointsToCommand(root, releaseDir string) string {
	cur := path.Join(root, "current")
	return "if [ -L " + shQuote(cur) + " ] && [ \"$(readlink " + shQuote(cur) + ")\" = " + shQuote(releaseDir) + " ]; then rm -f " + shQuote(cur) + "; fi"
}

// cleanupCommand 生成「保留最近 keep 个 release 目录」的清理命令。
//
// 硬要求：**绝不删除 current 与 prevReleaseID 指向的目录**（否则回滚目标就没了），
// 因此除了「最近 keep 个」之外，这三个名字一律跳过；删除范围严格限制在 <root>/releases 内。
func cleanupCommand(root, releaseID, prevReleaseID string, keep int) string {
	if keep < 1 {
		keep = DefaultKeepReleases
	}
	releasesDir := path.Join(root, "releases")
	cur := path.Join(root, "current")
	var b strings.Builder
	b.WriteString("cd " + shQuote(releasesDir) + " || exit 1\n")
	b.WriteString("cur=$(basename \"$(readlink " + shQuote(cur) + " 2>/dev/null)\" 2>/dev/null || true)\n")
	b.WriteString("n=0\n")
	b.WriteString("ls -1dt -- */ 2>/dev/null | while IFS= read -r d; do\n")
	b.WriteString("  d=${d%/}\n")
	b.WriteString("  n=$((n+1))\n")
	b.WriteString("  [ \"$n\" -le " + strconv.Itoa(keep) + " ] && continue\n")
	b.WriteString("  [ \"$d\" = " + shQuote(releaseID) + " ] && continue\n")
	b.WriteString("  [ \"$d\" = " + shQuote(prevReleaseID) + " ] && continue\n")
	b.WriteString("  [ -n \"$cur\" ] && [ \"$d\" = \"$cur\" ] && continue\n")
	b.WriteString("  rm -rf -- \"$d\"\n")
	b.WriteString("done")
	return b.String()
}

// unpackCommand 生成「把下载好的制品落位到 release 目录」的命令。
// 按扩展名识别 tar.gz/tgz/tar/zip，其余按单文件安装（可执行）。
func unpackCommand(releaseDir, tmpFile, location string) string {
	lower := strings.ToLower(location)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return "tar -xzf " + shQuote(tmpFile) + " -C " + shQuote(releaseDir) + " && rm -f " + shQuote(tmpFile)
	case strings.HasSuffix(lower, ".tar"):
		return "tar -xf " + shQuote(tmpFile) + " -C " + shQuote(releaseDir) + " && rm -f " + shQuote(tmpFile)
	case strings.HasSuffix(lower, ".zip"):
		return "unzip -oq " + shQuote(tmpFile) + " -d " + shQuote(releaseDir) + " && rm -f " + shQuote(tmpFile)
	default:
		return "install -m 0755 " + shQuote(tmpFile) + " " + shQuote(path.Join(releaseDir, artifactFileName(location))) +
			" && rm -f " + shQuote(tmpFile)
	}
}

// artifactFileName 取制品文件名（去掉 query/fragment），并做字符白名单收敛。
func artifactFileName(location string) string {
	s := location
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	name := strings.Trim(b.String(), ".")
	if name == "" || name == "." || name == ".." {
		return "artifact.bin"
	}
	return name
}

// composePrefix 生成 docker compose 的兼容前缀（v2 的 `docker compose` 优先，回退 `docker-compose`）。
func composePrefix() string {
	return `if docker compose version >/dev/null 2>&1; then dc="docker compose"; else dc="docker-compose"; fi`
}

// composeCommand 生成一条 compose 子命令（含兼容前缀）。
func composeCommand(composeFile, args string) string {
	return composePrefix() + "; $dc -f " + shQuote(composeFile) + " " + args
}

// composeHealthCommand 生成健康检查：至少有一个 running 容器，否则非 0 退出。
func composeHealthCommand(composeFile string) string {
	return composePrefix() + "; [ -n \"$($dc -f " + shQuote(composeFile) + " ps --status running -q)\" ]"
}

// composeUpRollbackCommand 生成回滚时的 compose 重启命令（只在旧 release 的 compose 文件存在时执行）。
func composeUpRollbackCommand(composeFile string) string {
	return "if [ -f " + shQuote(composeFile) + " ]; then " + composeCommand(composeFile, "up -d") + "; fi"
}

// binaryRestartRollbackCommand 生成回滚时的重启命令（只在旧 release 的启动脚本存在时执行）。
func binaryRestartRollbackCommand(scriptPath string) string {
	return "if [ -f " + shQuote(scriptPath) + " ]; then sh " + shQuote(scriptPath) + "; fi"
}
