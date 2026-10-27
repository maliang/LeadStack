package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/maliang/leadstack/internal/deploy"
	"github.com/maliang/leadstack/internal/store"
)

// DiscoveryCandidate 是「接管向导」发现的一个候选应用。
type DiscoveryCandidate struct {
	// Kind：systemd | docker | site（宝塔/nginx 站点目录）
	Kind string `json:"kind"`
	// Name 是展示名（服务名/容器名/目录名）。
	Name string `json:"name"`
	// Detail 是补充信息（ExecStart/镜像/端口等），只用于展示。
	Detail string `json:"detail"`
	// Suggested 是本项目建议的接管参数（前端直接回填）。
	Suggested DiscoverySuggestion `json:"suggested"`
}

// DiscoverySuggestion 是接管参数的预填值。
type DiscoverySuggestion struct {
	DeployMode      string `json:"deploy_mode"`
	TargetDir       string `json:"target_dir,omitempty"`
	RestartCmd      string `json:"restart_cmd,omitempty"`
	HealthcheckPort int    `json:"healthcheck_port,omitempty"`
	Root            string `json:"root,omitempty"`
}

// DiscoveryReport 是一次发现的结果。
type DiscoveryReport struct {
	ServerID   string               `json:"server_id"`
	Candidates []DiscoveryCandidate `json:"candidates"`
	// Raw 是采集到的原始分段（排障用，前端默认折叠）。
	Raw map[string]string `json:"raw"`
}

// discoverScript 是只在目标机上读信息的脚本（不修改任何东西）。
//
// 用固定分隔符输出分段，控制端按段解析；命令全部带 2>/dev/null，
// 缺少 systemctl/docker 的机器不会因此失败。
const discoverScript = `set -u
echo "###SYSTEMD"
systemctl list-units --type=service --state=running --no-pager --no-legend 2>/dev/null | head -200
echo "###EXECSTART"
for u in $(systemctl list-units --type=service --state=running --no-pager --no-legend 2>/dev/null | awk '{print $1}' | head -80); do
  cmd=$(systemctl show -p ExecStart --value "$u" 2>/dev/null | head -1)
  echo "$u|$cmd"
done
echo "###PORTS"
(ss -lntp 2>/dev/null || netstat -lntp 2>/dev/null) | head -200
echo "###DOCKER"
docker ps --format '{{.Names}}|{{.Image}}|{{.Ports}}|{{.Status}}' 2>/dev/null | head -100
echo "###WWWROOT"
ls -1d /www/wwwroot/* 2>/dev/null | head -100
echo "###GITDIRS"
for d in /www/wwwroot/*; do
  if [ -d "$d/.git" ]; then
    echo "$d|$(git -C "$d" rev-parse --abbrev-ref HEAD 2>/dev/null)|$(git -C "$d" remote get-url origin 2>/dev/null)"
  fi
done
echo "###END"
`

// errOrchestratorDisabled 表示控制端本地没装配发布执行器（配置/装配问题）。
//
// 调用方必须翻成 503 unavailable，绝不能当远端失败（502）——「发布执行器未启用」
// 是控制端自己的问题，套上「探测失败: 」会让用户去目标机查日志，一条都查不到。
var errOrchestratorDisabled = errors.New("发布执行器未启用")

// Discover 在目标机上做一次只读探测，返回可接管的候选列表。
func (o *deployOrchestrator) Discover(ctx context.Context, serverID string) (*DiscoveryReport, error) {
	if o == nil || o.exec == nil {
		return nil, errOrchestratorDisabled
	}
	plan := deploy.Plan{
		AppSlug:   "discover",
		ReleaseID: "discover",
		Version:   "0",
		Root:      "/tmp",
		Steps:     []deploy.Step{{Name: "discover", Command: discoverScript, TimeoutS: 60}},
	}
	var stdout strings.Builder
	hooks := deploy.Hooks{
		OnLog: func(stream, chunk string) {
			if stream == "stdout" {
				stdout.WriteString(chunk)
			}
		},
	}
	outcome, err := o.exec.RunPlan(ctx, serverID, plan, hooks)
	if err != nil && outcome.Status == "" {
		return nil, fmt.Errorf("探测失败: %w", err)
	}
	if outcome.Status != "" && outcome.Status != deploy.StatusSuccess {
		return nil, fmt.Errorf("探测失败: %s", strings.TrimSpace(outcome.Message))
	}
	return parseDiscoveryOutput(serverID, stdout.String()), nil
}

// parseDiscoveryOutput 解析探测脚本输出，生成候选列表。
func parseDiscoveryOutput(serverID, raw string) *DiscoveryReport {
	sections := map[string]string{}
	current := ""
	var buf strings.Builder
	flush := func() {
		if current != "" {
			sections[current] = buf.String()
			buf.Reset()
		}
	}
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "###") {
			flush()
			current = strings.TrimPrefix(line, "###")
			continue
		}
		buf.WriteString(line)
		buf.WriteString("\n")
	}
	flush()

	report := &DiscoveryReport{ServerID: serverID, Candidates: []DiscoveryCandidate{}, Raw: sections}

	// systemd：服务名 + ExecStart（用来判断是不是我们要接管的业务进程）
	execMap := map[string]string{}
	for _, line := range strings.Split(sections["EXECSTART"], "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) == 2 {
			execMap[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	for _, line := range strings.Split(sections["SYSTEMD"], "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasSuffix(fields[0], ".service") {
			continue
		}
		name := fields[0]
		if strings.HasPrefix(name, "systemd-") || strings.HasPrefix(name, "dbus") {
			continue
		}
		execStart := execMap[name]
		report.Candidates = append(report.Candidates, DiscoveryCandidate{
			Kind:   "systemd",
			Name:   name,
			Detail: execStart,
			Suggested: DiscoverySuggestion{
				DeployMode: store.DeployModeInplace,
				RestartCmd: "systemctl restart " + name,
			},
		})
	}

	// docker：容器名 + 镜像 + 端口
	for _, line := range strings.Split(sections["DOCKER"], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		report.Candidates = append(report.Candidates, DiscoveryCandidate{
			Kind:   "docker",
			Name:   parts[0],
			Detail: strings.Join(parts[1:], " · "),
			Suggested: DiscoverySuggestion{
				DeployMode: store.DeployModeInplace,
				RestartCmd: "docker restart " + parts[0],
			},
		})
	}

	// git 仓库目录 → path|branch|remote
	gitDirs := map[string]string{}
	for _, line := range strings.Split(sections["GITDIRS"], "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) >= 2 && parts[0] != "" {
			gitDirs[parts[0]] = parts[1] + " " + strings.Join(parts[2:], " ")
		}
	}

	// 站点目录：/www/wwwroot/<site>
	for _, line := range strings.Split(sections["WWWROOT"], "\n") {
		dir := strings.TrimSpace(line)
		if dir == "" {
			continue
		}
		name := dir[strings.LastIndex(dir, "/")+1:]
		suggest := DiscoverySuggestion{
			DeployMode: store.DeployModeInplace,
			TargetDir:  dir,
			Root:       dir,
		}
		detail := dir
		// 是 git 仓库 → 推荐「脚本模式 + git pull」，因为这类项目通常靠 pull 更新。
		if info, ok := gitDirs[dir]; ok {
			suggest.DeployMode = store.DeployModeScript
			detail = dir + " · git(" + strings.TrimSpace(info) + ")"
		}
		report.Candidates = append(report.Candidates, DiscoveryCandidate{
			Kind:      "site",
			Name:      name,
			Detail:    detail,
			Suggested: suggest,
		})
	}

	// 端口：给 systemd 候选补一个「可能的健康检查端口」（按 ExecStart 里出现的第一处 :端口）。
	ports := parseListeningPorts(sections["PORTS"])
	for i := range report.Candidates {
		if report.Candidates[i].Kind != "systemd" {
			continue
		}
		for _, p := range ports {
			if strings.Contains(report.Candidates[i].Detail, fmt.Sprintf(":%d", p)) {
				report.Candidates[i].Suggested.HealthcheckPort = p
				break
			}
		}
	}
	sort.SliceStable(report.Candidates, func(i, j int) bool {
		if report.Candidates[i].Kind != report.Candidates[j].Kind {
			return report.Candidates[i].Kind < report.Candidates[j].Kind
		}
		return report.Candidates[i].Name < report.Candidates[j].Name
	})
	return report
}

// parseListeningPorts 从 ss/netstat 输出里提取监听端口（去重、升序）。
func parseListeningPorts(raw string) []int {
	seen := map[int]bool{}
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		for _, f := range fields {
			idx := strings.LastIndex(f, ":")
			if idx < 0 || idx == len(f)-1 {
				continue
			}
			portStr := strings.TrimSuffix(f[idx+1:], "-")
			if strings.HasPrefix(portStr, "") {
				portStr = strings.TrimLeft(portStr, "0123456789") // 理论上不会发生，防御
			}
			var port int
			valid := portStr != ""
			for _, r := range portStr {
				if r < '0' || r > '9' {
					valid = false
					break
				}
				port = port*10 + int(r-'0')
			}
			if valid && port > 0 && port < 65536 {
				seen[port] = true
			}
		}
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// Discover 是 DeployOrchestratorHandle 的实现（未装配编排器时返回明确错误）。
func (h *DeployOrchestratorHandle) Discover(ctx context.Context, serverID string) (*DiscoveryReport, error) {
	if h == nil || h.inner == nil {
		return nil, errors.New("发布编排器未启用，无法探测目标机")
	}
	return h.inner.Discover(ctx, serverID)
}
