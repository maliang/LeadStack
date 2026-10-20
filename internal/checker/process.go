package checker

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// processGOOS 是生成进程查询命令时使用的目标机 OS。
//
// 契约里 Executor 只给出「在目标机上执行命令」的能力，没有携带目标机 OS，
// 因此 OS 来源按优先级取：
//  1. spec.Params["os"]（"linux" / "darwin" / "windows"，大小写不敏感）—— 由 backend 按
//     servers.os 传下来，跨 OS 纳管（控制端 Linux 管 Windows 机器）时必须用它；
//  2. 缺省沿用控制端 GOOS（本变量），兼容 M2 已有检测项。
//
// 注意：这是**不改 Executor 接口**的前提下实现的（改了会打挂 backend 与 internal/jobs）。
var processGOOS = runtime.GOOS

// Windows 下 tasklist 的 IMAGENAME 过滤器不接受任意字符；与其做易错的 cmd 转义，
// 不如只放行安全字符集，其余直接拒绝（安全优先）。
var winProcessNameRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,255}$`)

// POSIX 下的用户名同样是白名单（用于 pgrep -u / ps -o user= 的比较）。
var posixUserNameRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,64}$`)

// probeProcess 在目标机上统计匹配的进程数，与 params.min_count（默认 1）比较。
//
// 命令生成见 processCommand：target 做严格转义，杜绝命令注入。
func probeProcess(ctx context.Context, spec Spec, exec Executor, serverID string, timeout time.Duration) Result {
	start := time.Now()
	if exec == nil {
		return failResult(0, "process 检测需要目标机执行通道（Executor 为 nil）")
	}
	name := strings.TrimSpace(spec.Target)
	if name == "" {
		return failResult(0, "进程名（target）不能为空")
	}
	minCount := paramInt(spec.Params, "min_count", DefaultMinCount)
	if minCount < 1 {
		minCount = 1
	}
	user := strings.TrimSpace(paramString(spec.Params, "user"))

	goos, err := targetOS(spec.Params)
	if err != nil {
		return failResult(0, "process 检测配置错误: %s", err.Error())
	}
	command, err := processCommand(goos, name, user)
	if err != nil {
		return failResult(0, "生成进程查询命令失败: %s", err.Error())
	}

	stdout, stderr, exitCode, err := exec.Exec(ctx, serverID, command, timeoutSecondsOf(spec))
	lat := elapsedMS(start)
	if err != nil {
		if ctx.Err() != nil {
			return failResult(lat, "目标机执行被取消: %s", ctx.Err().Error())
		}
		return failResult(lat, "目标机执行失败: %s", err.Error())
	}
	if exitCode != 0 {
		return failResult(lat, "进程查询命令退出码 %d%s", exitCode, stderrSuffix(stderr))
	}
	count, perr := parseProcessCount(goos, stdout)
	if perr != nil {
		return failResult(lat, "解析进程数失败: %s（stdout=%q stderr=%q）",
			perr.Error(), snippet(stdout, 120), snippet(stderr, 120))
	}
	if count < minCount {
		return failResult(lat, "匹配进程数 %d < 期望最少 %d%s", count, minCount, stderrSuffix(stderr))
	}
	return okResult(lat, 0, "匹配进程数 %d（≥%d）", count, minCount)
}

// targetOS 解析 process 检测的目标机 OS：params["os"] 优先，缺省沿用控制端 GOOS。
func targetOS(params map[string]any) (string, error) {
	raw := strings.TrimSpace(paramString(params, "os"))
	if raw == "" {
		return processGOOS, nil
	}
	switch strings.ToLower(raw) {
	case "linux":
		return "linux", nil
	case "darwin", "mac", "macos", "osx":
		return "darwin", nil
	case "windows", "win":
		return "windows", nil
	default:
		return "", fmt.Errorf("os 参数非法: %q（支持 linux/darwin/windows）", raw)
	}
}

// processCommand 生成「统计匹配进程数」的目标机命令。
//
// 安全设计（红线：target 必须严格转义，防命令注入）：
//
//   - POSIX：target 逐字节转成 printf 的八进制转义 `\NNN`，作为单引号字面量交给
//     `printf`，运行时才还原成真值。这样命令行里**永远不出现** target 原文，
//     既杜绝注入（`;`、反引号、`$()` 都只是被还原的数据），
//     又避免 pgrep/ps 把执行命令的 shell 自身当成匹配目标。
//   - 匹配用 `ps -ww -eo pid=,ppid=,command=` + awk，按 `$1!=me && $2!=me`
//     排除本命令自身的进程树（shell/ps/awk 都是它的子进程），
//     因此不存在自匹配导致的假 up（实测 macOS 上 `pgrep -f` 在 Linux 上会把
//     执行该命令的 sh 计入，故不采用）。
//   - 进程名通过导出的环境变量传给 awk（ENVIRON["PAT"]），
//     不进 awk 的 argv，进一步避免自匹配。
//   - Windows：tasklist 的 IMAGENAME 只接受 `[A-Za-z0-9_.-]`，其余一律拒绝。
func processCommand(goos, name, user string) (string, error) {
	if name == "" {
		return "", errors.New("进程名不能为空")
	}
	if strings.ContainsRune(name, 0) {
		return "", errors.New("进程名不能包含 NUL 字节")
	}
	if len(name) > 1024 {
		return "", fmt.Errorf("进程名过长（%d 字节，上限 1024）", len(name))
	}
	if goos == "windows" {
		if !winProcessNameRe.MatchString(name) {
			return "", fmt.Errorf("进程名含非法字符（Windows 仅允许字母/数字/下划线/点/短横线）: %q", name)
		}
		image := name
		if !strings.Contains(image, ".") {
			image += ".exe" // tasklist 的 IMAGENAME 是映像名，通常是 xxx.exe
		}
		return fmt.Sprintf(`tasklist /FI "IMAGENAME eq %s" /FO CSV /NH`, image), nil
	}

	pat := posixOctalPrintf(name)
	userFilter := ""
	cmdStart := 3
	if user != "" {
		if !posixUserNameRe.MatchString(user) {
			return "", fmt.Errorf("user 参数含非法字符（仅允许字母/数字/下划线/点/短横线）: %q", user)
		}
		userFilter = fmt.Sprintf(` && $3==u`)
		cmdStart = 4
	}
	awkProg := fmt.Sprintf(`'$1!=me && $2!=me%s { line=$%d; for(i=%d;i<=NF;i++) line=line" "$i; if (index(line, ENVIRON["PAT"])>0) n++ } END{print n+0}'`,
		userFilter, cmdStart, cmdStart+1)

	// linux(procps) 用 `-eo`；darwin(BSD ps) 用 `-ww -axo`：
	// 两者都关掉表头（`=` 后缀）并输出 pid/ppid/command 三列，便于同一套 awk 过滤自身进程树。
	psCmd := "ps -ww -eo pid=,ppid=,command="
	if goos == "darwin" {
		psCmd = "ps -ww -axo pid=,ppid=,command="
	}

	var b strings.Builder
	b.WriteString("P=$(printf '")
	b.WriteString(pat)
	b.WriteString("'); PAT=\"$P\"; export PAT; ")
	if user != "" {
		if goos == "darwin" {
			fmt.Fprintf(&b, `ps -ww -axo pid=,ppid=,user=,command= | awk -v me=$$ -v u="%s" %s`, user, awkProg)
		} else {
			fmt.Fprintf(&b, `ps -ww -eo pid=,ppid=,user=,command= | awk -v me=$$ -v u="%s" %s`, user, awkProg)
		}
	} else {
		fmt.Fprintf(&b, `%s | awk -v me=$$ %s`, psCmd, awkProg)
	}
	return b.String(), nil
}

// posixOctalPrintf 把字符串转成 printf 可还原的八进制转义序列（逐字节）。
func posixOctalPrintf(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 4)
	for i := 0; i < len(s); i++ {
		fmt.Fprintf(&b, `\%03o`, s[i])
	}
	return b.String()
}

// parseProcessCount 解析命令输出得到进程数。
func parseProcessCount(goos, stdout string) (int, error) {
	if goos == "windows" {
		// tasklist CSV 每行以 " 开头；无匹配时输出 "INFO: No tasks are running..."
		n := 0
		for _, line := range strings.Split(stdout, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), `"`) {
				n++
			}
		}
		return n, nil
	}
	s := strings.TrimSpace(stdout)
	if s == "" {
		return 0, errors.New("命令无输出")
	}
	lines := strings.Split(s, "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	n, err := strconv.Atoi(last)
	if err != nil {
		return 0, fmt.Errorf("输出不是数字: %q", last)
	}
	if n < 0 {
		return 0, fmt.Errorf("输出为负数: %d", n)
	}
	return n, nil
}
