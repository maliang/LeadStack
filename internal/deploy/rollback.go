package deploy

import (
	"fmt"
	"regexp"
	"strings"
)

// releaseIDInCommandRe 从命令文本里提取 `<root>/releases/<releaseID>` 中的 releaseID。
var releaseIDInCommandRe = regexp.MustCompile(`releases/([A-Za-z0-9][A-Za-z0-9._-]{0,63})`)

// Rollback 生成回滚计划（**纯函数**，不重新下载、不查库）。
//
// 用法与语义：p 传**正在生效的这次发布**的计划 —— Build 在构建时就按当时的 PrevReleaseID
// 把回滚步骤写进了 p.RollbackSteps（旧目录、旧 unit/compose、启动脚本都还在原处），
// 因此 Rollback 只需把它翻成「顺序执行」的新计划：
//
//	回滚 = 切回上一版本目录 →（systemd 重新安装旧 unit / compose 用旧文件 up -d）→ 重启
//
// 这是秒级操作，不依赖网络。返回计划的 ReleaseID 是**回滚目标**（上一个 release）；
// 首次发布没有上一版本时 ReleaseID 为空，Steps 只会撤掉本次创建的 current 链接。
//
// 与自动回滚的区别：这里返回的 Steps 清掉了 IgnoreFailure，
// 因为用户手动回滚必须如实报告失败（自动回滚才是 best-effort）。
func Rollback(p Plan) (Plan, error) {
	if len(p.RollbackSteps) == 0 {
		return Plan{}, fmt.Errorf("deploy: 计划没有回滚步骤，无法回滚（app=%q release=%q）：首次发布或该计划是回滚计划", p.AppSlug, p.ReleaseID)
	}
	target := rollbackTargetReleaseID(p)
	steps := make([]Step, 0, len(p.RollbackSteps))
	for i := len(p.RollbackSteps) - 1; i >= 0; i-- {
		step := p.RollbackSteps[i]
		step.IgnoreFailure = false
		steps = append(steps, step)
	}
	return Plan{
		AppSlug:   p.AppSlug,
		ReleaseID: target,
		Version:   "",
		Root:      p.Root,
		Steps:     steps,
	}, nil
}

// rollbackTargetReleaseID 从回滚步骤的命令里解析出「要回滚到哪个 release」。
// 排除了本次 release 自己的目录名（首次发布的撤销步骤里会出现自己）。
func rollbackTargetReleaseID(p Plan) string {
	for _, step := range p.RollbackSteps {
		for _, m := range releaseIDInCommandRe.FindAllStringSubmatch(step.Command, -1) {
			if len(m) > 1 && m[1] != p.ReleaseID {
				return m[1]
			}
		}
	}
	return ""
}

// stepNamesOf 返回步骤名的有序列表，便于日志与测试断言。
func stepNamesOf(steps []Step) []string {
	names := make([]string, 0, len(steps))
	for _, s := range steps {
		names = append(names, s.Name)
	}
	return names
}

// commandsContain 判断步骤序列里是否有命令包含给定子串（测试/排障用）。
func commandsContain(steps []Step, sub string) bool {
	for _, s := range steps {
		if strings.Contains(s.Command, sub) {
			return true
		}
	}
	return false
}
