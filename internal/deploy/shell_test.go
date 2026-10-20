package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件**真的执行**生成的 shell 片段（本机 sh -c，等价于 agent/ssh 在目标机上的执行方式）。
// 目的：把「命令序列看起来对」升级为「在真实 shell 上确实能跑对」——
// 尤其是 macOS 上符号链接切换的嵌套陷阱与清理步骤的保护语义。

// shRunner 是测试用的真实执行通道（对应 agent 的 job.exec / ssh 的 Run）。
type shRunner struct {
	mu    sync.Mutex
	lines []string
}

func (r *shRunner) Run(ctx context.Context, serverID, command string, timeoutS int, onLog func(string, string)) (string, string, int, error) {
	runCtx := ctx
	if timeoutS > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutS)*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, "sh", "-c", command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r.mu.Lock()
	r.lines = append(r.lines, command)
	r.mu.Unlock()
	if onLog != nil {
		if s := stdout.String(); s != "" {
			onLog("stdout", s)
		}
		if s := stderr.String(); s != "" {
			onLog("stderr", s)
		}
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return stdout.String(), stderr.String(), ee.ExitCode(), nil
		}
		return stdout.String(), stderr.String(), -1, err
	}
	return stdout.String(), stderr.String(), 0, nil
}

func runShell(t *testing.T, command string) (stdout, stderr string, exitCode int) {
	t.Helper()
	r := &shRunner{}
	out, errb, code, err := r.Run(context.Background(), "local", command, 120, nil)
	if err != nil {
		t.Fatalf("执行命令失败: %v\n命令:\n%s", err, command)
	}
	return out, errb, code
}

func mustRunShell(t *testing.T, command string) {
	t.Helper()
	out, errb, code := runShell(t, command)
	if code != 0 {
		t.Fatalf("命令退出码 %d\nstdout=%s\nstderr=%s\n命令:\n%s", code, out, errb, command)
	}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readLinkTarget(t *testing.T, link string) string {
	t.Helper()
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink %s: %v", link, err)
	}
	return target
}

// TestSwitchCurrentIsRealSymlinkAndNoNesting 是本包最关键的实测：
// 连续发布两次后 current 必须仍是符号链接，且指向最新 release 目录（macOS 上不能嵌套）。
func TestSwitchCurrentIsRealSymlinkAndNoNesting(t *testing.T) {
	root := t.TempDir()
	rel1 := filepath.Join(root, "releases", "20261019-160000-aaaaaa")
	rel2 := filepath.Join(root, "releases", "20261019-171500-bbbbbb")
	writeFile(t, filepath.Join(rel1, "marker.txt"), "v1", 0o644)
	writeFile(t, filepath.Join(rel2, "marker.txt"), "v2", 0o644)

	switchCmd := switchCurrentCommand(root, rel1, "20261019-160000-aaaaaa")
	t.Logf("current 切换命令: %s", switchCmd)
	mustRunShell(t, switchCmd)
	cur := filepath.Join(root, "current")
	fi, err := os.Lstat(cur)
	if err != nil {
		t.Fatalf("current 未创建: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("current 必须是真符号链接，实际 mode=%v", fi.Mode())
	}
	if got := readLinkTarget(t, cur); got != rel1 {
		t.Fatalf("current 指向 %q want %q", got, rel1)
	}
	if data, err := os.ReadFile(filepath.Join(cur, "marker.txt")); err != nil || string(data) != "v1" {
		t.Fatalf("通过 current 读取内容失败: %q err=%v", string(data), err)
	}

	// 第二次切换：macOS 的 `ln -sfn` 在 current 是目录时会造嵌套，这里必须换到 rel2
	mustRunShell(t, switchCurrentCommand(root, rel2, "20261019-171500-bbbbbb"))
	if got := readLinkTarget(t, cur); got != rel2 {
		t.Fatalf("第二次切换后 current 指向 %q want %q（疑似嵌套）", got, rel2)
	}
	if fi, err := os.Lstat(cur); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("第二次切换后 current 不再是符号链接: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(cur, "releases")); err == nil {
		t.Fatalf("出现嵌套链接 current/releases")
	}
	if data, _ := os.ReadFile(filepath.Join(cur, "marker.txt")); string(data) != "v2" {
		t.Fatalf("current 未解析到新版本内容: %q", string(data))
	}

	// 幂等：重复执行同一条命令仍然正确
	mustRunShell(t, switchCurrentCommand(root, rel2, "20261019-171500-bbbbbb"))
	if got := readLinkTarget(t, cur); got != rel2 {
		t.Fatalf("重复切换后指向 %q", got)
	}
}

// TestSwitchCurrentMovesAsideRealDir 覆盖「current 曾经是真实目录」的历史遗留情况：
// 必须仍能切成功（current 变符号链接），且旧目录内容不丢。
func TestSwitchCurrentMovesAsideRealDir(t *testing.T) {
	root := t.TempDir()
	rel1 := filepath.Join(root, "releases", "r1")
	writeFile(t, filepath.Join(rel1, "marker.txt"), "v1", 0o644)
	// 人为制造一个真实目录当 current
	writeFile(t, filepath.Join(root, "current", "legacy.txt"), "legacy", 0o644)

	mustRunShell(t, switchCurrentCommand(root, rel1, "r1"))

	cur := filepath.Join(root, "current")
	fi, err := os.Lstat(cur)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("current 应被替换为符号链接: %v mode=%v", err, fi.Mode())
	}
	if got := readLinkTarget(t, cur); got != rel1 {
		t.Fatalf("current 指向 %q want %q", got, rel1)
	}
	// 旧目录被挪到 .current.bak.<releaseID>.$$（带 PID 后缀，避免同一 release 反复部署时互相覆盖）
	backups, err := filepath.Glob(filepath.Join(root, ".current.bak.r1.*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("应有且仅有一个备份目录: %v err=%v", backups, err)
	}
	data, err := os.ReadFile(filepath.Join(backups[0], "legacy.txt"))
	if err != nil || string(data) != "legacy" {
		t.Fatalf("旧目录应被保留为备份而非删除: err=%v data=%q", err, string(data))
	}
}

// TestCleanupKeepsCurrentAndPrevRealShell 实测清理命令：
// 超过保留数的旧目录会被删，但 current 与 PrevReleaseID 指向的目录**绝不删**。
func TestCleanupKeepsCurrentAndPrevRealShell(t *testing.T) {
	newRoot := func(t *testing.T) string {
		root := t.TempDir()
		base := time.Now().Add(-time.Hour)
		for i := 1; i <= 7; i++ {
			dir := filepath.Join(root, "releases", fmt.Sprintf("r%d", i))
			writeFile(t, filepath.Join(dir, "f.txt"), fmt.Sprintf("v%d", i), 0o644)
			ts := base.Add(time.Duration(i) * time.Minute)
			if err := os.Chtimes(dir, ts, ts); err != nil {
				t.Fatalf("chtimes: %v", err)
			}
		}
		writeFile(t, filepath.Join(root, ".leadstack", "keep.txt"), "keep", 0o644)
		return root
	}
	exists := func(path string) bool {
		_, err := os.Lstat(path)
		return err == nil
	}

	t.Run("保护的 prev 比保留窗口更老", func(t *testing.T) {
		root := newRoot(t)
		// current -> r7，本次 release r7，prev r1（比最近 5 个都老）
		mustRunShell(t, switchCurrentCommand(root, filepath.Join(root, "releases", "r7"), "r7"))
		mustRunShell(t, cleanupCommand(root, "r7", "r1", 5))

		for _, keep := range []string{"r1", "r3", "r4", "r5", "r6", "r7"} {
			if !exists(filepath.Join(root, "releases", keep)) {
				t.Fatalf("%s 不应被删除", keep)
			}
		}
		if exists(filepath.Join(root, "releases", "r2")) {
			t.Fatalf("r2 超出保留窗口且未被保护，应被删除")
		}
		if got := readLinkTarget(t, filepath.Join(root, "current")); got != filepath.Join(root, "releases", "r7") {
			t.Fatalf("清理不得动 current: %q", got)
		}
		if !exists(filepath.Join(root, ".leadstack", "keep.txt")) {
			t.Fatalf("清理不得动 .leadstack 元数据")
		}
	})

	t.Run("current 指向老版本也必须保留", func(t *testing.T) {
		root := newRoot(t)
		// current -> r1（最老），本次 release r7，prev r7
		mustRunShell(t, switchCurrentCommand(root, filepath.Join(root, "releases", "r1"), "r1"))
		mustRunShell(t, cleanupCommand(root, "r7", "r7", 5))

		for _, keep := range []string{"r1", "r3", "r4", "r5", "r6", "r7"} {
			if !exists(filepath.Join(root, "releases", keep)) {
				t.Fatalf("%s 不应被删除（current/prev 或最近 5 个）", keep)
			}
		}
		if exists(filepath.Join(root, "releases", "r2")) {
			t.Fatalf("r2 应被删除")
		}
		if got := readLinkTarget(t, filepath.Join(root, "current")); got != filepath.Join(root, "releases", "r1") {
			t.Fatalf("current 被破坏: %q", got)
		}
	})
}

func TestSha256VerifyCommandRealShell(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "artifact.bin")
	writeFile(t, file, "hello deploy\n", 0o644)
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte("hello deploy\n")))

	if _, stderr, code := runShell(t, sha256VerifyCommand(file, sum)); code != 0 {
		t.Fatalf("校验和一致时应通过: code=%d stderr=%s", code, stderr)
	}
	_, stderr, code := runShell(t, sha256VerifyCommand(file, strings.Repeat("f", 64)))
	if code == 0 {
		t.Fatalf("校验和不一致必须失败")
	}
	if !strings.Contains(stderr, "sha256 校验失败") {
		t.Fatalf("失败信息应可读: %q", stderr)
	}
	if !strings.Contains(stderr, sum) {
		t.Fatalf("失败信息应包含实际校验和: %q", stderr)
	}
}

func TestUnpackCommandRealShell(t *testing.T) {
	t.Run("tar.gz", func(t *testing.T) {
		releaseDir := t.TempDir()
		tmp := filepath.Join(releaseDir, artifactTempName)
		if err := os.WriteFile(tmp, makeTarGz(t, map[string]string{"app.txt": "payload"}), 0o644); err != nil {
			t.Fatalf("写制品: %v", err)
		}
		mustRunShell(t, unpackCommand(releaseDir, tmp, "https://example.com/a/app-1.0.0.tar.gz"))
		data, err := os.ReadFile(filepath.Join(releaseDir, "app.txt"))
		if err != nil || string(data) != "payload" {
			t.Fatalf("解包结果不符: %q err=%v", string(data), err)
		}
		if _, err := os.Stat(tmp); err == nil {
			t.Fatalf("解包后应删除临时制品")
		}
	})

	t.Run("单文件", func(t *testing.T) {
		releaseDir := t.TempDir()
		tmp := filepath.Join(releaseDir, artifactTempName)
		writeFile(t, tmp, "#!/bin/sh\necho hi\n", 0o644)
		mustRunShell(t, unpackCommand(releaseDir, tmp, "https://example.com/a/mytool"))
		fi, err := os.Stat(filepath.Join(releaseDir, "mytool"))
		if err != nil {
			t.Fatalf("单文件未落位: %v", err)
		}
		if fi.Mode().Perm() != 0o755 {
			t.Fatalf("单文件应为可执行 0755，实际 %v", fi.Mode().Perm())
		}
		if _, err := os.Stat(tmp); err == nil {
			t.Fatalf("落位后应删除临时制品")
		}
	})
}

// TestHeredocWriteIsInjectionSafe 验证写文件路径不会被内容注入：
// 内容里带 heredoc 分隔符与各类 shell 元字符，写入结果必须逐字节一致且无副作用。
func TestHeredocWriteIsInjectionSafe(t *testing.T) {
	dir := t.TempDir()
	canary := filepath.Join(dir, "pwned")
	target := filepath.Join(dir, "out", "compose.yml")
	content := strings.Join([]string{
		"services:",
		"  app:",
		"    image: x",
		heredocDelimiter,
		heredocDelimiter + "_X",
		"'; rm -rf /tmp/nothing",
		"$(touch " + canary + ")",
		"`touch " + canary + "`",
		"line with 'single' and \"double\" quotes",
	}, "\n") + "\n"

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mustRunShell(t, heredocWrite(target, content))

	if _, err := os.Stat(canary); err == nil {
		t.Fatalf("内容里的命令替换被执行了（注入成功）")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取写入结果: %v", err)
	}
	if string(data) != content {
		t.Fatalf("写入内容不一致:\n got=%q\nwant=%q", string(data), content)
	}
}

func TestWriteFileStepExecutableRealShell(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "leadstack-start.sh")
	content := "#!/bin/sh\necho '{{VERSION}}-started'\n"
	rendered, err := Render(content, validVars())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	step := writeFileStep(StepWriteRestartScript, target, rendered, modeExecutable)
	mustRunShell(t, step.Command)
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("脚本未写入: %v", err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("脚本应为 0755，实际 %v", fi.Mode().Perm())
	}
	out, _, code := runShell(t, "sh "+shQuote(target))
	if code != 0 || strings.TrimSpace(out) != "1.2.3-rc.1-started" {
		t.Fatalf("脚本执行结果不符: out=%q code=%d", out, code)
	}
}

// TestBinaryPlanRealEndToEnd 用真实 HTTP 制品源 + 真实 sh 执行整条 binary 计划，
// 并复现验收第 5/6 条：sha256 失败时 current 不被破坏；回滚把 current 切回上一版本。
func TestBinaryPlanRealEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("真实执行用例在 -short 下跳过")
	}
	v1 := makeTarGz(t, map[string]string{"app.txt": "v1"})
	v2 := makeTarGz(t, map[string]string{"app.txt": "v2"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app-1.0.0.tar.gz":
			_, _ = w.Write(v1)
		case "/app-2.0.0.tar.gz":
			_, _ = w.Write(v2)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	appRoot := filepath.Join(root, "my-app")
	tmpl := Template{Kind: KindBinary, Content: "#!/bin/sh\necho \"started {{VERSION}}\" > {{ROOT}}/started.txt\n"}
	out := t.TempDir()
	logFile, err := os.Create(filepath.Join(out, "deploy.log"))
	if err != nil {
		t.Fatalf("日志文件: %v", err)
	}
	defer logFile.Close()
	exec := NewExecutor(&shRunner{}, ExecutorOptions{Logger: slog.New(slog.NewTextHandler(logFile, nil))})

	plan1, err := Build(tmpl, Artifact{
		Kind: ArtifactURL, Location: srv.URL + "/app-1.0.0.tar.gz",
		SHA256: fmt.Sprintf("%x", sha256.Sum256(v1)),
	}, BuildParams{AppSlug: "my-app", ReleaseID: "r1", Version: "1.0.0", Root: root, Now: 1})
	if err != nil {
		t.Fatalf("Build r1: %v", err)
	}
	res1, err := exec.RunPlan(context.Background(), "srv-local", plan1, Hooks{})
	if err != nil || res1.Status != StatusSuccess {
		t.Fatalf("首次发布应成功: %+v err=%v", res1, err)
	}

	cur := filepath.Join(appRoot, "current")
	if fi, err := os.Lstat(cur); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("current 必须是真符号链接: err=%v", err)
	}
	if got, want := readLinkTarget(t, cur), filepath.Join(appRoot, "releases", "r1"); got != want {
		t.Fatalf("current 指向 %q want %q", got, want)
	}
	data, err := os.ReadFile(filepath.Join(cur, "app.txt"))
	if err != nil || string(data) != "v1" {
		t.Fatalf("产物应落在 current 指向的目录里: %q err=%v", string(data), err)
	}
	if _, err := os.Stat(filepath.Join(appRoot, ".leadstack", "releases", "r1.json")); err != nil {
		t.Fatalf("缺少发布元数据: %v", err)
	}
	if started, err := os.ReadFile(filepath.Join(appRoot, "started.txt")); err != nil || !strings.Contains(string(started), "1.0.0") {
		t.Fatalf("重启脚本未按新版本执行: %q err=%v", string(started), err)
	}

	// 第二次发布：故意给错误 sha256 → 必须失败在 verify-sha256，且 current 保持 r1
	badPlan, err := Build(tmpl, Artifact{
		Kind: ArtifactURL, Location: srv.URL + "/app-2.0.0.tar.gz",
		SHA256: strings.Repeat("f", 64),
	}, BuildParams{AppSlug: "my-app", ReleaseID: "r2bad", Version: "2.0.0", Root: root, PrevReleaseID: "r1", Now: 2})
	if err != nil {
		t.Fatalf("Build r2bad: %v", err)
	}
	resBad, err := exec.RunPlan(context.Background(), "srv-local", badPlan, Hooks{})
	if err != nil {
		t.Fatalf("步骤失败应通过 outcome 表达: %v", err)
	}
	if resBad.Status != StatusFailed || resBad.FailedStep != StepVerifySHA256 {
		t.Fatalf("应失败在 %s，实际 %+v", StepVerifySHA256, resBad)
	}
	if got := readLinkTarget(t, cur); got != filepath.Join(appRoot, "releases", "r1") {
		t.Fatalf("校验失败后 current 被破坏: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(cur, "app.txt")); string(got) != "v1" {
		t.Fatalf("current 内容被破坏: %q", string(got))
	}

	// 修正后第二次发布成功 → current 指向 r2
	plan2, err := Build(tmpl, Artifact{
		Kind: ArtifactURL, Location: srv.URL + "/app-2.0.0.tar.gz",
		SHA256: fmt.Sprintf("%x", sha256.Sum256(v2)),
	}, BuildParams{AppSlug: "my-app", ReleaseID: "r2", Version: "2.0.0", Root: root, PrevReleaseID: "r1", Now: 3})
	if err != nil {
		t.Fatalf("Build r2: %v", err)
	}
	res2, err := exec.RunPlan(context.Background(), "srv-local", plan2, Hooks{})
	if err != nil || res2.Status != StatusSuccess {
		t.Fatalf("第二次发布应成功: %+v err=%v", res2, err)
	}
	if got := readLinkTarget(t, cur); got != filepath.Join(appRoot, "releases", "r2") {
		t.Fatalf("current 应指向 r2: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(cur, "app.txt")); string(got) != "v2" {
		t.Fatalf("r2 内容不符: %q", string(got))
	}

	// 回滚 plan2 → current 回到 r1，且不重新下载
	rb, err := Rollback(plan2)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	resRb, err := exec.RunPlan(context.Background(), "srv-local", rb, Hooks{})
	if err != nil || resRb.Status != StatusSuccess {
		t.Fatalf("回滚应成功: %+v err=%v", resRb, err)
	}
	if got := readLinkTarget(t, cur); got != filepath.Join(appRoot, "releases", "r1") {
		t.Fatalf("回滚后 current 应指向 r1: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(cur, "app.txt")); string(got) != "v1" {
		t.Fatalf("回滚后内容应为 v1: %q", string(got))
	}
	if started, _ := os.ReadFile(filepath.Join(appRoot, "started.txt")); !strings.Contains(string(started), "1.0.0") {
		t.Fatalf("回滚应重启旧版本脚本: %q", string(started))
	}
	if _, err := os.Stat(filepath.Join(appRoot, "releases", "r1")); err != nil {
		t.Fatalf("回滚目标目录必须仍在: %v", err)
	}
}

func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		content := files[name]
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}
