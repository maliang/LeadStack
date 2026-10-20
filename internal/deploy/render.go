package deploy

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
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

// 路径类变量的约束（ROOT / RELEASE_DIR）。
//
// 允许 **UTF-8 可打印字符**（中文目录名 `/srv/应用`、`/Users/张三/项目` 必须可用），
// 但仍然拒绝一切会破坏 shell 命令的字符：
//
//   - 空白（空格/Tab/换行/回车）：路径里有空格就得靠引号，容易在模板里漏；
//   - 控制字符与不可打印字符（含 NUL、RTL 覆写 U+202E 之类的格式字符）；
//   - shell 元字符 ; | & $ ` " ' ( ) { } < > * ? ! ~ #。
//
// 说明：本包生成的所有命令里路径**一律经 shQuote 单引号包裹**（已用真实 shell 验证：
// 单引号内的 `~`、`*`、`$` 都不会被展开），所以单引号本身足够安全；
// 这里仍然把这些字符禁掉，是为了不依赖「模板作者/未来改动记得加引号」，属于纵深防御。
const (
	maxPathBytes = 1024
	// pathForbiddenRunes 是路径里一律拒绝的 shell 元字符（含会被展开的 ~ 与通配符 *）。
	pathForbiddenRunes = ";|&$`\"'(){}<>*?!~#"
)

// validateAbsPath 校验绝对路径（部署根 / release 目录），允许 UTF-8。
func validateAbsPath(v string) error {
	if v == "" {
		return errors.New("不能为空")
	}
	if len(v) > maxPathBytes {
		return fmt.Errorf("过长（%d 字节，上限 %d）", len(v), maxPathBytes)
	}
	if !strings.HasPrefix(v, "/") {
		return fmt.Errorf("必须是绝对路径: %q", snippetText(v))
	}
	if strings.HasSuffix(v, "/") {
		return fmt.Errorf("不能以 / 结尾: %q", snippetText(v))
	}
	for _, r := range v {
		if err := validatePathRune(r); err != nil {
			return fmt.Errorf("%s（问题字符 %q）: %q", err.Error(), r, snippetText(v))
		}
	}
	for _, seg := range strings.Split(v, "/") {
		if seg == ".." {
			return fmt.Errorf("不能包含 .. 路径段: %q", snippetText(v))
		}
	}
	return nil
}

// validatePathRune 逐个字符校验路径（UTF-8 友好：只按「可打印/危险字符」判断，不看码点范围）。
func validatePathRune(r rune) error {
	switch {
	case r == ' ', r == '\t', r == '\n', r == '\r', r == '\v', r == '\f':
		return errors.New("不能包含空白字符（空格/制表符/换行）")
	case !unicode.IsPrint(r):
		return errors.New("只能包含可打印字符（禁止控制字符/不可打印字符）")
	case strings.ContainsRune(pathForbiddenRunes, r):
		return errors.New("不能包含 shell 元字符（; | & $ ` \" ' ( ) { } < > * ? ! ~ #）")
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
