package deploy

import (
	"strings"
	"testing"
)

func validVars() map[string]string {
	return map[string]string{
		VarAppSlug:        "my-app",
		VarVersion:        "1.2.3-rc.1",
		VarReleaseID:      "20261019-171500-a1b2c3",
		VarRoot:           "/opt/leadstack/apps/my-app",
		VarReleaseDir:     "/opt/leadstack/apps/my-app/releases/20261019-171500-a1b2c3",
		VarArtifactURL:    "https://example.com/dist/app-1.2.3.tar.gz?token=a&x=1",
		VarArtifactSHA256: strings.Repeat("a", 64),
		VarPrevReleaseID:  "20261019-160000-d4e5f6",
	}
}

func TestRenderAllPlaceholders(t *testing.T) {
	content := strings.Join([]string{
		"slug={{APP_SLUG}}",
		"version={{VERSION}}",
		"release={{RELEASE_ID}}",
		"root={{ROOT}}",
		"dir={{RELEASE_DIR}}",
		"url={{ARTIFACT_URL}}",
		"sha={{ARTIFACT_SHA256}}",
		"prev={{PREV_RELEASE_ID}}",
	}, "\n")
	got, err := Render(content, validVars())
	if err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	want := strings.Join([]string{
		"slug=my-app",
		"version=1.2.3-rc.1",
		"release=20261019-171500-a1b2c3",
		"root=/opt/leadstack/apps/my-app",
		"dir=/opt/leadstack/apps/my-app/releases/20261019-171500-a1b2c3",
		"url=https://example.com/dist/app-1.2.3.tar.gz?token=a&x=1",
		"sha=" + strings.Repeat("a", 64),
		"prev=20261019-160000-d4e5f6",
	}, "\n")
	if got != want {
		t.Fatalf("渲染结果不符:\n got=%q\nwant=%q", got, want)
	}
}

func TestRenderDotPrefixAndSpacesAccepted(t *testing.T) {
	got, err := Render("a={{.APP_SLUG}} b={{ VERSION }} c={{. RELEASE_ID }}", validVars())
	if err != nil {
		t.Fatalf("兼容写法应被接受: %v", err)
	}
	if got != "a=my-app b=1.2.3-rc.1 c=20261019-171500-a1b2c3" {
		t.Fatalf("渲染结果=%q", got)
	}
}

func TestRenderUnknownPlaceholder(t *testing.T) {
	for _, content := range []string{
		"{{UNKNOWN_VAR}}",
		"{{.NOPE}}",
		"{{}}",
		"x={{FOO_BAR}}",
		"{{APP_SLUGX}}",
	} {
		_, err := Render(content, validVars())
		if err == nil {
			t.Fatalf("未知占位符必须报错: %q", content)
		}
		if !strings.Contains(err.Error(), "未知占位符") {
			t.Fatalf("错误信息应指明未知占位符: %v", err)
		}
	}
}

func TestRenderUnclosedPlaceholder(t *testing.T) {
	_, err := Render("echo {{APP_SLUG", validVars())
	if err == nil || !strings.Contains(err.Error(), "未闭合") {
		t.Fatalf("未闭合必须报错，得到 %v", err)
	}
}

func TestRenderMissingValue(t *testing.T) {
	vars := validVars()
	delete(vars, VarVersion)
	_, err := Render("{{VERSION}}", vars)
	if err == nil || !strings.Contains(err.Error(), "VERSION") {
		t.Fatalf("缺少值必须报错，得到 %v", err)
	}
	// 允许为空的变量缺失时按空串处理
	delete(vars, VarPrevReleaseID)
	got, err := Render("[{{PREV_RELEASE_ID}}]", vars)
	if err != nil || got != "[]" {
		t.Fatalf("PREV_RELEASE_ID 缺失应按空串渲染，得到 %q err=%v", got, err)
	}
}

// TestRenderRejectsCommandInjection 是安全红线用例：
// 任何变量值里带 shell 元字符都必须被拒绝，且错误信息不回显完整恶意载荷。
func TestRenderRejectsCommandInjection(t *testing.T) {
	payloads := []string{
		"1.0; rm -rf /",
		"1.0 && rm -rf /",
		"1.0 | rm -rf /",
		"1.0$(id)",
		"1.0`id`",
		"1.0\nrm -rf /",
		"1.0 rm -rf /",
		"1.0'",
		`1.0"`,
		"1.0>out",
		"1.0${IFS}rm",
		"1.0\\n",
	}
	for _, payload := range payloads {
		t.Run(payload, func(t *testing.T) {
			vars := validVars()
			vars[VarVersion] = payload
			_, err := Render("echo {{VERSION}}", vars)
			if err == nil {
				t.Fatalf("注入载荷必须被拒绝: %q", payload)
			}
			if !strings.Contains(err.Error(), VarVersion) {
				t.Fatalf("错误信息应指明变量名: %v", err)
			}
			if strings.Contains(err.Error(), "\n") {
				t.Fatalf("错误信息不应含换行: %v", err)
			}
		})
	}
}

func TestRenderVariableWhitelists(t *testing.T) {
	cases := []struct {
		name  string
		varN  string
		value string
		bad   bool
	}{
		{name: "slug 合法", varN: VarAppSlug, value: "my-app-2"},
		{name: "slug 大写非法", varN: VarAppSlug, value: "MyApp", bad: true},
		{name: "slug 以短横开头非法", varN: VarAppSlug, value: "-app", bad: true},
		{name: "slug 路径穿越非法", varN: VarAppSlug, value: "../etc", bad: true},
		{name: "slug 超长非法", varN: VarAppSlug, value: strings.Repeat("a", 41), bad: true},
		{name: "slug 恰好40合法", varN: VarAppSlug, value: strings.Repeat("a", 40)},
		{name: "version 合法", varN: VarVersion, value: "v1.2.3_beta-4"},
		{name: "version 以点开头非法", varN: VarVersion, value: ".1", bad: true},
		{name: "version 超长非法", varN: VarVersion, value: "1" + strings.Repeat("a", 64), bad: true},
		{name: "releaseID 合法", varN: VarReleaseID, value: "20261019-171500-a1b2c3"},
		{name: "releaseID 以短横开头非法", varN: VarReleaseID, value: "-rf", bad: true},
		{name: "root 合法", varN: VarRoot, value: "/opt/leadstack/apps/x"},
		{name: "root 相对路径非法", varN: VarRoot, value: "opt/x", bad: true},
		{name: "root 尾斜杠非法", varN: VarRoot, value: "/opt/x/", bad: true},
		{name: "root 含..非法", varN: VarRoot, value: "/opt/../etc", bad: true},
		{name: "root 含空格非法", varN: VarRoot, value: "/opt/my apps", bad: true},
		{name: "root 含分号非法", varN: VarRoot, value: "/opt/x;rm", bad: true},
		{name: "release_dir 合法", varN: VarReleaseDir, value: "/opt/leadstack/apps/x/releases/r1"},
		{name: "url 合法查询串", varN: VarArtifactURL, value: "https://h/p/a.tgz?x=1&y=2#frag"},
		{name: "url 单引号非法", varN: VarArtifactURL, value: "https://h/a';rm -rf /", bad: true},
		{name: "url $() 非法", varN: VarArtifactURL, value: "https://h/$(id)", bad: true},
		{name: "url 反引号非法", varN: VarArtifactURL, value: "https://h/`id`", bad: true},
		{name: "url 空格非法", varN: VarArtifactURL, value: "https://h/a b", bad: true},
		{name: "url 换行非法", varN: VarArtifactURL, value: "https://h/a\nrm", bad: true},
		{name: "url 管道非法", varN: VarArtifactURL, value: "https://h/a|id", bad: true},
		{name: "url 分号非法", varN: VarArtifactURL, value: "https://h/a;id", bad: true},
		{name: "image 引用合法", varN: VarArtifactURL, value: "registry.example.com:5000/app:1.0@sha256:" + strings.Repeat("b", 64)},
		{name: "sha 合法", varN: VarArtifactSHA256, value: strings.Repeat("F", 64)},
		{name: "sha 长度不对非法", varN: VarArtifactSHA256, value: "abc", bad: true},
		{name: "sha 非十六进制非法", varN: VarArtifactSHA256, value: strings.Repeat("z", 64), bad: true},
		{name: "prev 空合法", varN: VarPrevReleaseID, value: ""},
		{name: "prev 非法", varN: VarPrevReleaseID, value: "bad id", bad: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vars := validVars()
			vars[tc.varN] = tc.value
			_, err := Render("{{"+tc.varN+"}}", vars)
			if tc.bad && err == nil {
				t.Fatalf("值 %q 应被拒绝", tc.value)
			}
			if !tc.bad && err != nil {
				t.Fatalf("值 %q 应被接受，得到 %v", tc.value, err)
			}
		})
	}
}

func TestRenderSinglePassNoReexpansion(t *testing.T) {
	// 变量值不会被二次展开：这里用合法的 root 值构造，确保只替换一遍。
	vars := validVars()
	got, err := Render("{{ROOT}}-{{ROOT}}", vars)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != vars[VarRoot]+"-"+vars[VarRoot] {
		t.Fatalf("单遍替换结果=%q", got)
	}
}

func TestRenderNoPlaceholdersAndMultiline(t *testing.T) {
	content := "#!/bin/sh\nset -e\necho ok\n"
	got, err := Render(content, validVars())
	if err != nil {
		t.Fatalf("无占位符内容应原样返回: %v", err)
	}
	if got != content {
		t.Fatalf("内容被改动: %q", got)
	}
}

func TestSnippetTextSanitizesAndTruncates(t *testing.T) {
	got := snippetText("a\nb\tc\r" + strings.Repeat("x", 100))
	if strings.ContainsAny(got, "\n\r\t") {
		t.Fatalf("应压掉控制字符: %q", got)
	}
	if len(got) > 70 {
		t.Fatalf("应截断: len=%d", len(got))
	}
}
