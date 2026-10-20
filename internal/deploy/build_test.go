package deploy

import (
	"reflect"
	"strings"
	"testing"
)

func sha(seed string) string {
	return strings.Repeat(seed, 64)[:64]
}

func baseParams() BuildParams {
	return BuildParams{
		AppSlug:       "my-app",
		ReleaseID:     "20261019-171500-a1b2c3",
		Version:       "1.2.3",
		Root:          "/opt/leadstack/apps",
		PrevReleaseID: "20261019-160000-d4e5f6",
		Now:           1792396500000,
	}
}

func urlArtifact() Artifact {
	return Artifact{Kind: ArtifactURL, Location: "https://example.com/app-1.2.3.tar.gz", SHA256: sha("a"), SizeBytes: 1024}
}

func TestBuildBinaryStepSequence(t *testing.T) {
	tmpl := Template{ID: "t1", Kind: KindBinary, Content: "#!/bin/sh\nsystemctl --user restart dummy\n"}
	p, err := Build(tmpl, urlArtifact(), baseParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Root != "/opt/leadstack/apps/my-app" {
		t.Fatalf("Plan.Root 应为应用根: %q", p.Root)
	}
	want := []string{
		StepPrepare, StepDownload, StepVerifySHA256, StepUnpack,
		StepWriteRestartScript, StepWriteMetadata, StepSwitchCurrent, StepRestart, StepCleanupOldReleases,
	}
	if got := stepNamesOf(p.Steps); !reflect.DeepEqual(got, want) {
		t.Fatalf("binary 步骤序列不符:\n got=%v\nwant=%v", got, want)
	}
	// 目录布局：<root>/<slug>/releases/<releaseID>
	releaseDir := "/opt/leadstack/apps/my-app/releases/20261019-171500-a1b2c3"
	if !commandsContain(p.Steps, releaseDir) {
		t.Fatalf("步骤里应出现 release 目录 %s", releaseDir)
	}
	if !commandsContain(p.Steps, "/opt/leadstack/apps/my-app/.leadstack/releases/20261019-171500-a1b2c3.json") {
		t.Fatalf("应有元数据落盘步骤")
	}
	// 校验必须发生在切换 current 之前（否则失败会破坏上一版本）
	if idxOf(p.Steps, StepVerifySHA256) > idxOf(p.Steps, StepSwitchCurrent) {
		t.Fatalf("verify-sha256 必须在 switch-current 之前")
	}
	if idxOf(p.Steps, StepDownload) > idxOf(p.Steps, StepVerifySHA256) {
		t.Fatalf("download 必须在 verify 之前")
	}
	// 元数据/清理是 best-effort
	if s := stepByName(t, p.Steps, StepWriteMetadata); !s.IgnoreFailure {
		t.Fatalf("元数据步骤应为 IgnoreFailure")
	}
	if s := stepByName(t, p.Steps, StepCleanupOldReleases); !s.IgnoreFailure {
		t.Fatalf("清理步骤应为 IgnoreFailure")
	}
	if s := stepByName(t, p.Steps, StepVerifySHA256); s.TimeoutS <= 0 {
		t.Fatalf("步骤超时未设置")
	}
	// 下载命令必须是 curl + 单引号包住的 URL
	dl := stepByName(t, p.Steps, StepDownload)
	if !strings.HasPrefix(dl.Command, "curl -fsSL ") || !strings.Contains(dl.Command, "'https://example.com/app-1.2.3.tar.gz'") {
		t.Fatalf("下载命令不符: %q", dl.Command)
	}
	// 重启脚本要落盘并 chmod，再 sh 执行
	wr := stepByName(t, p.Steps, StepWriteRestartScript)
	if !strings.Contains(wr.Command, "chmod 0755") || !strings.Contains(wr.Command, "leadstack-start.sh") {
		t.Fatalf("启动脚本步骤不符: %q", wr.Command)
	}
	rs := stepByName(t, p.Steps, StepRestart)
	if !strings.Contains(rs.Command, "sh '/opt/leadstack/apps/my-app/releases/20261019-171500-a1b2c3/leadstack-start.sh'") {
		t.Fatalf("重启命令不符: %q", rs.Command)
	}
	// 清理必须保护 current 与 prev
	cl := stepByName(t, p.Steps, StepCleanupOldReleases)
	for _, must := range []string{
		"readlink '/opt/leadstack/apps/my-app/current'",
		"[ \"$d\" = '20261019-171500-a1b2c3' ] && continue",
		"[ \"$d\" = '20261019-160000-d4e5f6' ] && continue",
		"[ \"$d\" = \"$cur\" ] && continue",
		"n\" -le 5",
	} {
		if !strings.Contains(cl.Command, must) {
			t.Fatalf("清理命令缺少保护片段 %q:\n%s", must, cl.Command)
		}
	}
}

func TestBuildBinaryWithoutRestartScript(t *testing.T) {
	p, err := Build(Template{Kind: KindBinary, Content: "   \n"}, urlArtifact(), baseParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, name := range []string{StepWriteRestartScript, StepRestart} {
		if idxOf(p.Steps, name) >= 0 {
			t.Fatalf("模板内容为空时不应有 %s 步骤", name)
		}
	}
	if idxOf(p.Steps, StepSwitchCurrent) < 0 {
		t.Fatalf("仍应切换 current")
	}
}

func TestBuildComposeStepSequence(t *testing.T) {
	tmpl := Template{Kind: KindCompose, Content: "services:\n  app:\n    image: {{ARTIFACT_URL}}\n"}
	art := Artifact{Kind: ArtifactImage, Location: "registry.example.com/app:1.2.3"}
	p, err := Build(tmpl, art, baseParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []string{StepPrepare, StepWriteCompose, StepWriteMetadata, StepComposePull, StepComposeUp, StepHealthCheck, StepSwitchCurrent, StepCleanupOldReleases}
	if got := stepNamesOf(p.Steps); !reflect.DeepEqual(got, want) {
		t.Fatalf("compose 步骤序列不符:\n got=%v\nwant=%v", got, want)
	}
	if idxOf(p.Steps, StepHealthCheck) > idxOf(p.Steps, StepSwitchCurrent) {
		t.Fatalf("健康检查应在切换 current 之前（up 失败不得改变 current）")
	}
	// image 制品不下载
	for _, name := range []string{StepDownload, StepVerifySHA256, StepUnpack} {
		if idxOf(p.Steps, name) >= 0 {
			t.Fatalf("image 制品不应有 %s 步骤", name)
		}
	}
	wc := stepByName(t, p.Steps, StepWriteCompose)
	if !strings.Contains(wc.Command, "image: registry.example.com/app:1.2.3") {
		t.Fatalf("compose 文件应渲染出镜像引用:\n%s", wc.Command)
	}
	if !strings.Contains(wc.Command, "docker-compose.yml") {
		t.Fatalf("compose 文件路径不符: %q", wc.Command)
	}
	up := stepByName(t, p.Steps, StepComposeUp)
	if !strings.Contains(up.Command, "up -d") || !strings.Contains(up.Command, `dc="docker compose"`) {
		t.Fatalf("compose up 命令不符: %q", up.Command)
	}
	hc := stepByName(t, p.Steps, StepHealthCheck)
	if !strings.Contains(hc.Command, "--status running") {
		t.Fatalf("健康检查命令不符: %q", hc.Command)
	}
}

func TestBuildComposeWithURLArtifactDownloadsFirst(t *testing.T) {
	tmpl := Template{Kind: KindCompose, Content: "services: {}\n"}
	p, err := Build(tmpl, urlArtifact(), baseParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []string{StepPrepare, StepDownload, StepVerifySHA256, StepUnpack, StepWriteCompose, StepWriteMetadata, StepComposePull, StepComposeUp, StepHealthCheck, StepSwitchCurrent, StepCleanupOldReleases}
	if got := stepNamesOf(p.Steps); !reflect.DeepEqual(got, want) {
		t.Fatalf("compose(url) 步骤序列不符:\n got=%v\nwant=%v", got, want)
	}
}

func TestBuildSystemdStepSequence(t *testing.T) {
	tmpl := Template{Kind: KindSystemd, Content: "[Unit]\nDescription={{APP_SLUG}}\nExecStart={{RELEASE_DIR}}/app\n"}
	p, err := Build(tmpl, urlArtifact(), baseParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []string{
		StepPrepare, StepDownload, StepVerifySHA256, StepUnpack, StepWriteUnit, StepInstallUnit,
		StepDaemonReload, StepSwitchCurrent, StepRestart, StepHealthCheck, StepWriteMetadata, StepCleanupOldReleases,
	}
	if got := stepNamesOf(p.Steps); !reflect.DeepEqual(got, want) {
		t.Fatalf("systemd 步骤序列不符:\n got=%v\nwant=%v", got, want)
	}
	wl := stepByName(t, p.Steps, StepWriteUnit)
	if !strings.Contains(wl.Command, "my-app.service") || !strings.Contains(wl.Command, "/releases/20261019-171500-a1b2c3/my-app.service") {
		t.Fatalf("unit 应落在 release 目录: %q", wl.Command)
	}
	iu := stepByName(t, p.Steps, StepInstallUnit)
	if !strings.Contains(iu.Command, "install -m 0644") || !strings.Contains(iu.Command, "/etc/systemd/system/my-app.service") {
		t.Fatalf("安装 unit 命令不符: %q", iu.Command)
	}
	if !strings.Contains(stepByName(t, p.Steps, StepDaemonReload).Command, "systemctl daemon-reload") {
		t.Fatalf("daemon-reload 命令不符")
	}
	if !strings.Contains(stepByName(t, p.Steps, StepHealthCheck).Command, "systemctl is-active --quiet 'my-app'") {
		t.Fatalf("systemd 健康检查不符")
	}
}

func TestBuildFirstDeployHasNoPrev(t *testing.T) {
	params := baseParams()
	params.PrevReleaseID = ""
	p, err := Build(Template{Kind: KindBinary, Content: "#!/bin/sh\ntrue\n"}, urlArtifact(), params)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(p.RollbackSteps) != 1 {
		t.Fatalf("首次发布回滚应只有撤销 current 一步，得到 %v", stepNamesOf(p.RollbackSteps))
	}
	if !strings.Contains(p.RollbackSteps[0].Command, "readlink") || !strings.Contains(p.RollbackSteps[0].Command, "rm -f") {
		t.Fatalf("首次发布回滚应只在 current 指向自己时删除: %q", p.RollbackSteps[0].Command)
	}
}

// TestBuildStepOrderIsVerifyBeforeSwitch 是回滚保障的核心断言：
// 任何模板的 switch-current 都必须排在 download/verify 之后，这样校验失败不会破坏 current。
func TestBuildStepOrderIsVerifyBeforeSwitch(t *testing.T) {
	cases := []struct {
		kind Kind
		tmpl Template
		art  Artifact
	}{
		{KindBinary, Template{Kind: KindBinary, Content: "#!/bin/sh\ntrue\n"}, urlArtifact()},
		{KindCompose, Template{Kind: KindCompose, Content: "services: {}\n"}, urlArtifact()},
		{KindSystemd, Template{Kind: KindSystemd, Content: "[Unit]\n"}, urlArtifact()},
	}
	for _, tc := range cases {
		p, err := Build(tc.tmpl, tc.art, baseParams())
		if err != nil {
			t.Fatalf("%s Build: %v", tc.kind, err)
		}
		verify := idxOf(p.Steps, StepVerifySHA256)
		unpack := idxOf(p.Steps, StepUnpack)
		sw := idxOf(p.Steps, StepSwitchCurrent)
		if verify < 0 || sw < 0 || verify > sw || unpack > sw {
			t.Fatalf("%s 顺序错误: verify=%d unpack=%d switch=%d", tc.kind, verify, unpack, sw)
		}
	}
}

func TestBuildArtifactValidation(t *testing.T) {
	cases := []struct {
		name string
		kind Kind
		art  Artifact
		err  string
	}{
		{"url 缺 sha", KindBinary, Artifact{Kind: ArtifactURL, Location: "https://e/a.tgz"}, "必须提供 sha256"},
		{"url 非法(注入)", KindBinary, Artifact{Kind: ArtifactURL, Location: "https://e/a;rm -rf /", SHA256: sha("a")}, "location 非法"},
		{"url 相对路径", KindBinary, Artifact{Kind: ArtifactURL, Location: "/local/a.tgz", SHA256: sha("a")}, "location 非法"},
		{"sha 长度不对", KindBinary, Artifact{Kind: ArtifactURL, Location: "https://e/a.tgz", SHA256: "abc"}, "sha256 非法"},
		{"upload 必须是 URL", KindBinary, Artifact{Kind: ArtifactUpload, Location: "uploads/abc.tgz", SHA256: sha("a")}, "upload 制品的 location 必须是"},
		{"image 不支持 binary", KindBinary, Artifact{Kind: ArtifactImage, Location: "reg/app:1"}, "不支持 image"},
		{"image 不支持 systemd", KindSystemd, Artifact{Kind: ArtifactImage, Location: "reg/app:1"}, "不支持 image"},
		{"未知制品类型", KindBinary, Artifact{Kind: "ftp", Location: "https://e/a", SHA256: sha("a")}, "未知制品类型"},
		{"size 为负", KindBinary, Artifact{Kind: ArtifactURL, Location: "https://e/a", SHA256: sha("a"), SizeBytes: -1}, "不能为负"},
		{"compose image 合法", KindCompose, Artifact{Kind: ArtifactImage, Location: "reg/app:1"}, ""},
		{"image 带 sha 合法", KindCompose, Artifact{Kind: ArtifactImage, Location: "reg/app:1", SHA256: sha("b")}, ""},
		{"image 引用非法", KindCompose, Artifact{Kind: ArtifactImage, Location: "reg/app:1;rm"}, "镜像引用非法"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := "#!/bin/sh\ntrue\n"
			if tc.kind != KindBinary {
				content = "services: {}\n"
			}
			_, err := Build(Template{Kind: tc.kind, Content: content}, tc.art, baseParams())
			if tc.err == "" {
				if err != nil {
					t.Fatalf("应通过，得到 %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("错误应包含 %q，得到 %v", tc.err, err)
			}
		})
	}
}

func TestBuildParamsValidation(t *testing.T) {
	urlArt := urlArtifact()
	cases := []struct {
		name  string
		mut   func(*BuildParams)
		kind  Kind
		art   Artifact
		cmt   string
		isErr bool
	}{
		{name: "slug 注入", mut: func(p *BuildParams) { p.AppSlug = "app;rm -rf /" }, isErr: true},
		{name: "version 注入", mut: func(p *BuildParams) { p.Version = "1.0; rm -rf /" }, isErr: true},
		{name: "releaseID 非法", mut: func(p *BuildParams) { p.ReleaseID = "../etc" }, isErr: true},
		{name: "root 相对", mut: func(p *BuildParams) { p.Root = "opt/apps" }, isErr: true},
		{name: "root 含 ..", mut: func(p *BuildParams) { p.Root = "/opt/../etc" }, isErr: true},
		{name: "prev 等于本次", mut: func(p *BuildParams) { p.PrevReleaseID = p.ReleaseID }, isErr: true},
		{name: "now 为负", mut: func(p *BuildParams) { p.Now = -1 }, isErr: true},
		{name: "未知模板类型", kind: Kind("helm"), isErr: true},
		{name: "systemd 空模板", kind: KindSystemd, cmt: "  ", isErr: true},
		{name: "compose 空模板", kind: KindCompose, cmt: "\n", isErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := baseParams()
			if tc.mut != nil {
				tc.mut(&params)
			}
			kind := tc.kind
			if kind == "" {
				kind = KindBinary
			}
			content := tc.cmt
			if content == "" {
				content = "#!/bin/sh\ntrue\n"
			}
			art := tc.art
			if art.Kind == "" {
				art = urlArt
			}
			_, err := Build(Template{Kind: kind, Content: content}, art, params)
			if tc.isErr && err == nil {
				t.Fatalf("应报错")
			}
			if !tc.isErr && err != nil {
				t.Fatalf("不应报错: %v", err)
			}
		})
	}
}

func TestBuildUnknownPlaceholderFails(t *testing.T) {
	_, err := Build(Template{Kind: KindBinary, Content: "echo {{NOPE}}"}, urlArtifact(), baseParams())
	if err == nil || !strings.Contains(err.Error(), "未知占位符") {
		t.Fatalf("未知占位符必须让 Build 失败: %v", err)
	}
}

// TestBuildIsPureAndDeterministic 断言 Build 是纯函数：同样输入两次产出完全一致，
// 且不依赖任何 IO。
func TestBuildIsPureAndDeterministic(t *testing.T) {
	tmpl := Template{Kind: KindBinary, Content: "#!/bin/sh\necho {{VERSION}} > {{ROOT}}/v.txt\n"}
	a, err := Build(tmpl, urlArtifact(), baseParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	b, err := Build(tmpl, urlArtifact(), baseParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("重复 Build 结果不一致")
	}
	// 模板内容不会被渲染进 Step.Command 以外的地方，且渲染值出现在脚本里
	wr := stepByName(t, a.Steps, StepWriteRestartScript)
	if !strings.Contains(wr.Command, "echo 1.2.3 > /opt/leadstack/apps/my-app/v.txt") {
		t.Fatalf("模板渲染结果不符:\n%s", wr.Command)
	}
}

func TestRollbackPlanBinary(t *testing.T) {
	p, err := Build(Template{Kind: KindBinary, Content: "#!/bin/sh\ntrue\n"}, urlArtifact(), baseParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rb, err := Rollback(p)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rb.ReleaseID != "20261019-160000-d4e5f6" {
		t.Fatalf("回滚目标应为上一版本，得到 %q", rb.ReleaseID)
	}
	if rb.Root != p.Root || rb.AppSlug != p.AppSlug {
		t.Fatalf("回滚计划元数据不符: %+v", rb)
	}
	want := []string{StepSwitchCurrent, StepRestart}
	if got := stepNamesOf(rb.Steps); !reflect.DeepEqual(got, want) {
		t.Fatalf("回滚执行顺序应为「先切回再重启」，得到 %v", got)
	}
	if strings.Contains(rb.Steps[0].Command, "curl") {
		t.Fatalf("回滚不得重新下载")
	}
	if !strings.Contains(rb.Steps[0].Command, "20261019-160000-d4e5f6") {
		t.Fatalf("回滚应指向上一版本目录: %q", rb.Steps[0].Command)
	}
	for _, s := range rb.Steps {
		if s.IgnoreFailure {
			t.Fatalf("手动回滚步骤不应 IgnoreFailure（要如实报告失败）: %q", s.Name)
		}
	}
}

func TestRollbackPlanComposeAndSystemd(t *testing.T) {
	compose, err := Build(Template{Kind: KindCompose, Content: "services: {}\n"}, urlArtifact(), baseParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rb, err := Rollback(compose)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := stepNamesOf(rb.Steps); !reflect.DeepEqual(got, []string{StepSwitchCurrent, StepComposeUp}) {
		t.Fatalf("compose 回滚顺序=%v", got)
	}
	if !strings.Contains(rb.Steps[1].Command, "up -d") || !strings.Contains(rb.Steps[1].Command, "20261019-160000-d4e5f6") {
		t.Fatalf("compose 回滚应用旧 release 的 compose 文件 up -d: %q", rb.Steps[1].Command)
	}

	sysd, err := Build(Template{Kind: KindSystemd, Content: "[Unit]\n"}, urlArtifact(), baseParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rb2, err := Rollback(sysd)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := stepNamesOf(rb2.Steps); !reflect.DeepEqual(got, []string{StepSwitchCurrent, StepInstallUnit, StepDaemonReload, StepRestart}) {
		t.Fatalf("systemd 回滚顺序=%v", got)
	}
}

func TestRollbackFirstDeployUnlinksCurrent(t *testing.T) {
	params := baseParams()
	params.PrevReleaseID = ""
	p, err := Build(Template{Kind: KindBinary, Content: "#!/bin/sh\ntrue\n"}, urlArtifact(), params)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rb, err := Rollback(p)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rb.ReleaseID != "" {
		t.Fatalf("首次发布没有回滚目标，ReleaseID 应为空，得到 %q", rb.ReleaseID)
	}
	if got := stepNamesOf(rb.Steps); !reflect.DeepEqual(got, []string{StepSwitchCurrent}) {
		t.Fatalf("首次发布回滚应只撤销 current: %v", got)
	}
}

func TestRollbackWithoutRollbackStepsFails(t *testing.T) {
	_, err := Rollback(Plan{AppSlug: "a", ReleaseID: "r1", Root: "/opt/x/a"})
	if err == nil || !strings.Contains(err.Error(), "没有回滚步骤") {
		t.Fatalf("没有回滚信息时必须报错: %v", err)
	}
}

func TestRollbackIsIdempotentOnItsOwnOutput(t *testing.T) {
	p, err := Build(Template{Kind: KindBinary, Content: "#!/bin/sh\ntrue\n"}, urlArtifact(), baseParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rb, err := Rollback(p)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	// 回滚计划自身没有 RollbackSteps：再回滚必须报错而不是产出空计划
	if _, err := Rollback(rb); err == nil {
		t.Fatalf("回滚计划再次回滚应报错")
	}
}

func TestCleanupKeepsCurrentAndPrevEvenIfOld(t *testing.T) {
	// prev 比"最近 5 个"更老，也必须保留
	cmd := cleanupCommand("/opt/apps/my-app", "r7", "r1", 5)
	for _, must := range []string{
		"[ \"$d\" = 'r7' ] && continue",
		"[ \"$d\" = 'r1' ] && continue",
		"[ \"$d\" = \"$cur\" ] && continue",
		"readlink '/opt/apps/my-app/current'",
		"cd '/opt/apps/my-app/releases' || exit 1",
	} {
		if !strings.Contains(cmd, must) {
			t.Fatalf("清理命令缺少 %q:\n%s", must, cmd)
		}
	}
}

func TestArtifactFileName(t *testing.T) {
	cases := map[string]string{
		"https://e.com/a/app-1.0.tar.gz":           "app-1.0.tar.gz",
		"https://e.com/a/app-1.0.tar.gz?sig=x&y=1": "app-1.0.tar.gz",
		"https://e.com/a/app-1.0.tar.gz#frag":      "app-1.0.tar.gz",
		"https://e.com/path/":                      "artifact.bin",
		"https://e.com/":                           "artifact.bin",
		"https://e.com/a/..%2f..":                  "2f",
		"registry.example.com/app:1.0":             "app1.0",
	}
	for in, want := range cases {
		if got := artifactFileName(in); got != want {
			t.Errorf("artifactFileName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestUnpackCommandByExtension(t *testing.T) {
	cases := map[string]string{
		"https://e/a.tgz":    "tar -xzf",
		"https://e/a.tar.gz": "tar -xzf",
		"https://e/a.tar":    "tar -xf",
		"https://e/a.zip":    "unzip -oq",
		"https://e/a.bin":    "install -m 0755",
		"https://e/a":        "install -m 0755",
	}
	for in, want := range cases {
		cmd := unpackCommand("/r", "/r/.artifact", in)
		if !strings.Contains(cmd, want) {
			t.Errorf("unpackCommand(%q)=%q 应含 %q", in, cmd, want)
		}
		if !strings.Contains(cmd, "/r/.artifact") {
			t.Errorf("unpackCommand(%q) 应引用临时文件", in)
		}
	}
	// 单文件制品的落地名来自 URL basename，且不含 shell 元字符
	cmd := unpackCommand("/r", "/r/.artifact", "https://e/a/app-1.0.bin")
	if !strings.Contains(cmd, "'/r/app-1.0.bin'") {
		t.Fatalf("单文件落地路径不符: %q", cmd)
	}
}

func idxOf(steps []Step, name string) int {
	for i, s := range steps {
		if s.Name == name {
			return i
		}
	}
	return -1
}

func stepByName(t *testing.T, steps []Step, name string) Step {
	t.Helper()
	i := idxOf(steps, name)
	if i < 0 {
		t.Fatalf("找不到步骤 %q（现有 %v）", name, stepNamesOf(steps))
	}
	return steps[i]
}
