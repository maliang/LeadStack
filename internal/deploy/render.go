package deploy

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// 模板占位符名（§26，已冻结）。Render 同时接受 {{X}} 与 {{.X}} 两种写法。
const (
	VarAppSlug        = "APP_SLUG"
	VarVersion        = "VERSION"
	VarReleaseID      = "RELEASE_ID"
	VarRoot           = "ROOT"
	VarReleaseDir     = "RELEASE_DIR"
	VarArtifactURL    = "ARTIFACT_URL"
	VarArtifactSHA256 = "ARTIFACT_SHA256"
	VarPrevReleaseID  = "PREV_RELEASE_ID"
)

// 变量值白名单（安全红线：所有值代入命令前必须过校验）。
//
// 说明：slug/version/releaseID 额外要求**以字母或数字开头**，
// 避免 `-rf`、`.`、`..` 这类值在命令里被当成选项或路径穿越。
var (
	slugRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	versionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	releaseRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	sha256Re  = regexp.MustCompile(`^[A-Fa-f0-9]{64}$`)
	// 绝对路径：禁止空格、引号、`;|&$`、反引号、换行、通配符等所有 shell 元字符。
	absPathRe = regexp.MustCompile(`^/[A-Za-z0-9._+~/-]*$`)
	// http(s) URL：允许查询串必需的 & = ? % 等，禁止引号、反引号、$、空格、换行、分号与管道。
	urlRe = regexp.MustCompile(`^https?://[A-Za-z0-9._~:/?#\[\]@!&()*+,=%-]+$`)
	// 容器镜像引用：registry[:port]/path[:tag][@sha256:...]
	imageRefRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,254}$`)
)

// placeholderRule 描述一个占位符的取值约束。
type placeholderRule struct {
	allowEmpty bool
	validate   func(string) error
}

var placeholderRules = map[string]placeholderRule{
	VarAppSlug:        {validate: validateSlug},
	VarVersion:        {validate: validateVersion},
	VarReleaseID:      {validate: validateReleaseID},
	VarRoot:           {validate: validateAbsPath},
	VarReleaseDir:     {validate: validateAbsPath},
	VarArtifactURL:    {validate: validateArtifactLocation},
	VarArtifactSHA256: {allowEmpty: true, validate: validateSHA256},
	VarPrevReleaseID:  {allowEmpty: true, validate: validateReleaseID},
}

// Render 渲染模板占位符（见 §26），未知占位符必须报错而不是留空。
//
// 语义：
//   - 占位符写作 {{APP_SLUG}}，也接受 {{.APP_SLUG}}（兼容 Go template 习惯），允许内部空白；
//   - 只做**单遍**替换，变量值里即使出现 {{...}} 也不会被二次展开；
//   - {{ 未能闭合、占位符名未知、值未提供、值非法 → 都返回 error；
//   - 变量值先按白名单校验再代入（防命令注入）。
func Render(content string, vars map[string]string) (string, error) {
	var b strings.Builder
	b.Grow(len(content))
	rest := content
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:start])
		tail := rest[start+2:]
		end := strings.Index(tail, "}}")
		if end < 0 {
			return "", fmt.Errorf("模板占位符未闭合（缺少 }}）: %q", snippetText(rest[start:]))
		}
		raw := tail[:end]
		name := normalizeVarName(raw)
		rule, ok := placeholderRules[name]
		if !ok {
			return "", fmt.Errorf("未知占位符 {{%s}}", strings.TrimSpace(raw))
		}
		val, provided := vars[name]
		if !provided {
			if !rule.allowEmpty {
				return "", fmt.Errorf("变量 %s 未提供值", name)
			}
			val = ""
		}
		if !(rule.allowEmpty && val == "") {
			if err := rule.validate(val); err != nil {
				return "", fmt.Errorf("变量 %s 非法: %s", name, err.Error())
			}
		}
		b.WriteString(val)
		rest = tail[end+2:]
	}
	return b.String(), nil
}

// normalizeVarName 归一化占位符名：去空白、去掉可选的 `.` 前缀。
func normalizeVarName(raw string) string {
	name := strings.TrimSpace(raw)
	name = strings.TrimPrefix(name, ".")
	return strings.TrimSpace(name)
}

func validateSlug(v string) error {
	if !slugRe.MatchString(v) {
		return fmt.Errorf("必须以字母或数字开头，且匹配 [a-z0-9-]{1,40}: %q", snippetText(v))
	}
	return nil
}

func validateVersion(v string) error {
	if !versionRe.MatchString(v) {
		return fmt.Errorf("必须以字母或数字开头，且匹配 [A-Za-z0-9._-]{1,64}: %q", snippetText(v))
	}
	return nil
}

func validateReleaseID(v string) error {
	if !releaseRe.MatchString(v) {
		return fmt.Errorf("必须以字母或数字开头，且匹配 [A-Za-z0-9._-]{1,64}: %q", snippetText(v))
	}
	return nil
}

func validateSHA256(v string) error {
	if v == "" {
		return nil
	}
	if !sha256Re.MatchString(v) {
		return fmt.Errorf("必须是 64 位十六进制 sha256: %q", snippetText(v))
	}
	return nil
}

// validateAbsPath 校验绝对路径（部署根 / release 目录）。
func validateAbsPath(v string) error {
	if v == "" {
		return errors.New("不能为空")
	}
	if !strings.HasPrefix(v, "/") {
		return fmt.Errorf("必须是绝对路径: %q", snippetText(v))
	}
	if !absPathRe.MatchString(v) {
		return fmt.Errorf("含非法字符（只允许字母/数字/._+~/-，禁止空格、引号、;|&$、反引号与换行）: %q", snippetText(v))
	}
	if strings.HasSuffix(v, "/") {
		return fmt.Errorf("不能以 / 结尾: %q", snippetText(v))
	}
	for _, seg := range strings.Split(v, "/") {
		if seg == ".." {
			return fmt.Errorf("不能包含 .. 路径段: %q", snippetText(v))
		}
	}
	return nil
}

// validateArtifactLocation 校验制品地址：http(s) URL 或容器镜像引用。
//
// 说明：url/upload 制品在 Build 里会再按类型严格要求为 http(s) URL；
// 这里是 Render 视角的通用防线（模板里 {{ARTIFACT_URL}} 对 image 制品承载镜像引用）。
func validateArtifactLocation(v string) error {
	if v == "" {
		return errors.New("不能为空")
	}
	if strings.Contains(v, "://") {
		return validateURL(v)
	}
	return validateImageRef(v)
}

func validateURL(v string) error {
	if !urlRe.MatchString(v) {
		return fmt.Errorf("必须是 http(s) URL，且不含引号/反引号/$/空格/换行/;|& 之外的字符: %q", snippetText(v))
	}
	return nil
}

func validateImageRef(v string) error {
	if !imageRefRe.MatchString(v) {
		return fmt.Errorf("镜像引用非法: %q", snippetText(v))
	}
	return nil
}

// snippetText 把值截断后放进错误信息，避免把超长/含控制字符的输入灌进日志。
func snippetText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 {
			return -1
		}
		return r
	}, s)
	const max = 60
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
