package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// securityAuditScript 是只读的 Linux 安全基线采集脚本。
//
// 不使用 set -e：缺某个命令/文件时继续采集其余项，输出固定 key=value 行。
// 所有值截断为单行，避免日志/口令文件内容进入响应。
const securityAuditScript = `echo "ssh_root=$(grep -Eis '^[[:space:]]*PermitRootLogin[[:space:]]+' /etc/ssh/sshd_config 2>/dev/null | tail -1 | awk '{print $2}')"
echo "ssh_password=$(grep -Eis '^[[:space:]]*PasswordAuthentication[[:space:]]+' /etc/ssh/sshd_config 2>/dev/null | tail -1 | awk '{print $2}')"
echo "firewall=$(if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qi active; then echo active; elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state 2>/dev/null | grep -qi running; then echo active; elif command -v nft >/dev/null 2>&1 && nft list ruleset 2>/dev/null | grep -q 'hook input'; then echo active; elif command -v iptables >/dev/null 2>&1 && iptables -S INPUT 2>/dev/null | grep -qv '^-P INPUT ACCEPT'; then echo active; else echo inactive; fi)"
echo "fail2ban=$(if command -v fail2ban-client >/dev/null 2>&1; then if systemctl is-active --quiet fail2ban 2>/dev/null || service fail2ban status >/dev/null 2>&1; then echo active; else echo inactive; fi; else echo missing; fi)"
echo "shadow_mode=$(stat -c '%a' /etc/shadow 2>/dev/null || stat -f '%Lp' /etc/shadow 2>/dev/null)"
echo "empty_passwords=$(awk -F: '($2 == \"\") { n++ } END { print n+0 }' /etc/shadow 2>/dev/null)"
echo "authorized_keys=$(find /root /home -maxdepth 3 -name authorized_keys -type f 2>/dev/null | wc -l | tr -d ' ')"
echo "selinux=$(if command -v getenforce >/dev/null 2>&1; then getenforce 2>/dev/null; else echo unknown; fi)"
echo "fail2ban_ssh=$(if command -v fail2ban-client >/dev/null 2>&1; then fail2ban-client status sshd 2>/dev/null | grep -c 'Currently banned'; else echo 0; fi)"`

type securityFinding struct {
	ID             string `json:"id"`
	Level          string `json:"level"` // critical | high | medium | low | ok
	Title          string `json:"title"`
	Detail         string `json:"detail"`
	Recommendation string `json:"recommendation,omitempty"`
}

type securityAuditResponse struct {
	ServerID  string            `json:"server_id"`
	Score     int               `json:"score"`
	CheckedAt int64             `json:"checked_at"`
	Findings  []securityFinding `json:"findings"`
}

// handleSecurityAudit 处理 POST /api/v1/servers/{id}/security/audit。
//
// 只做只读采集，不修改目标机；由 agent/SSH 通道执行，因此 agent 接入的机器
// 无需开放入向端口。结果不落库，前端只在当前页面展示。
func (a *API) handleSecurityAudit(w http.ResponseWriter, r *http.Request) {
	serverID := strings.TrimSpace(chi.URLParam(r, "serverID"))
	if serverID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}
	if a.remoteExec == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "远程执行通道未启用，无法执行安全体检")
		return
	}
	stdout, stderr, exitCode, err := a.remoteExec.Exec(r.Context(), serverID, securityAuditScript, 20)
	if err != nil {
		writeError(w, http.StatusBadGateway, codeInternal, "安全体检执行失败: "+err.Error())
		return
	}
	if exitCode != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = fmt.Sprintf("脚本退出码 %d", exitCode)
		}
		writeError(w, http.StatusBadGateway, codeInternal, "安全体检执行失败: "+msg)
		return
	}
	findings, score := parseSecurityAudit(stdout)
	writeData(w, http.StatusOK, securityAuditResponse{
		ServerID:  serverID,
		Score:     score,
		CheckedAt: a.nowMS(),
		Findings:  findings,
	})
}

// parseSecurityAudit 把脚本的 key=value 输出转换为安全建议。
// 单独抽成纯函数，便于不依赖目标机单测。
func parseSecurityAudit(output string) ([]securityFinding, int) {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	findings := make([]securityFinding, 0, 8)

	root := strings.ToLower(values["ssh_root"])
	switch root {
	case "yes":
		findings = append(findings, securityFinding{
			ID: "ssh-root-login", Level: "high", Title: "SSH 允许 root 直接登录",
			Detail: "PermitRootLogin=yes", Recommendation: "改为 prohibit-password，仅允许密钥登录 root",
		})
	case "prohibit-password", "without-password", "no":
		findings = append(findings, securityFinding{
			ID: "ssh-root-login", Level: "ok", Title: "SSH root 登录已收紧", Detail: "PermitRootLogin=" + root,
		})
	default:
		findings = append(findings, securityFinding{
			ID: "ssh-root-login", Level: "medium", Title: "未显式配置 SSH root 登录策略",
			Detail: "sshd_config 未找到 PermitRootLogin", Recommendation: "显式设置 PermitRootLogin prohibit-password",
		})
	}

	password := strings.ToLower(values["ssh_password"])
	if password == "yes" {
		findings = append(findings, securityFinding{
			ID: "ssh-password-auth", Level: "high", Title: "SSH 允许密码登录",
			Detail: "PasswordAuthentication=yes", Recommendation: "优先使用密钥登录；如需保留密码登录，请配合 fail2ban",
		})
	} else {
		findings = append(findings, securityFinding{
			ID: "ssh-password-auth", Level: "ok", Title: "SSH 密码登录已关闭", Detail: "PasswordAuthentication=" + password,
		})
	}

	if strings.EqualFold(values["firewall"], "active") {
		findings = append(findings, securityFinding{ID: "firewall", Level: "ok", Title: "防火墙已启用", Detail: "检测到 ufw/firewalld/nftables/iptables 规则"})
	} else {
		findings = append(findings, securityFinding{
			ID: "firewall", Level: "high", Title: "未检测到启用的主机防火墙",
			Detail: "ufw/firewalld/nftables/iptables 均未发现有效 INPUT 规则", Recommendation: "启用 ufw 或 firewalld，仅放行 SSH/业务端口",
		})
	}

	switch strings.ToLower(values["fail2ban"]) {
	case "active":
		findings = append(findings, securityFinding{ID: "fail2ban", Level: "ok", Title: "fail2ban 正在运行", Detail: "已启用 SSH 暴力破解防护"})
	case "missing":
		findings = append(findings, securityFinding{
			ID: "fail2ban", Level: "medium", Title: "未安装 fail2ban",
			Detail: "无法自动封禁暴力破解来源 IP", Recommendation: "安装 fail2ban 并启用 sshd jail",
		})
	default:
		findings = append(findings, securityFinding{
			ID: "fail2ban", Level: "medium", Title: "fail2ban 未运行",
			Detail: "已安装但服务未启动", Recommendation: "systemctl enable --now fail2ban",
		})
	}

	shadow := values["shadow_mode"]
	if shadow != "" && shadow != "600" && shadow != "640" && shadow != "000" {
		findings = append(findings, securityFinding{
			ID: "shadow-mode", Level: "high", Title: "/etc/shadow 权限过宽",
			Detail: "当前权限 " + shadow, Recommendation: "chmod 640 /etc/shadow 并确认属主 root:shadow（或 root:root）",
		})
	} else {
		findings = append(findings, securityFinding{ID: "shadow-mode", Level: "ok", Title: "/etc/shadow 权限正常", Detail: "mode=" + shadow})
	}

	empty, _ := strconv.Atoi(values["empty_passwords"])
	if empty > 0 {
		findings = append(findings, securityFinding{
			ID: "empty-passwords", Level: "critical", Title: "存在无密码账号",
			Detail: fmt.Sprintf("%d 个账号密码字段为空", empty), Recommendation: "立即锁定无密码账号或设置强密码",
		})
	} else {
		findings = append(findings, securityFinding{ID: "empty-passwords", Level: "ok", Title: "未发现无密码账号", Detail: "0 个"})
	}

	if strings.Contains(strings.ToLower(values["selinux"]), "enforcing") {
		findings = append(findings, securityFinding{ID: "selinux", Level: "ok", Title: "SELinux 已启用", Detail: values["selinux"]})
	}

	score := 100
	for _, finding := range findings {
		switch finding.Level {
		case "critical":
			score -= 30
		case "high":
			score -= 20
		case "medium":
			score -= 10
		case "low":
			score -= 3
		}
	}
	if score < 0 {
		score = 0
	}
	return findings, score
}

// securityHardenScript 只做低风险、幂等的基础加固：
//   - 收紧 /etc/shadow 与 sshd_config 权限；
//   - 写入 SSH 尝试次数/超时策略（不改 root 登录与密码登录，避免锁死）；
//   - fail2ban 已安装但未运行时启动它（不联网安装软件）。
//
// 不启用防火墙、不改 SSH 端口、不重启 sshd，避免把用户挡在门外。
const securityHardenScript = `changed=""
if [ -f /etc/shadow ]; then chmod 640 /etc/shadow 2>/dev/null && changed="$changed shadow-mode"; fi
if [ -f /etc/ssh/sshd_config ]; then chmod 600 /etc/ssh/sshd_config 2>/dev/null && changed="$changed sshd-config-mode"; fi
if [ -d /etc/ssh/sshd_config.d ]; then
  printf '%s\n' 'MaxAuthTries 5' 'LoginGraceTime 30' 'ClientAliveInterval 300' 'ClientAliveCountMax 2' > /etc/ssh/sshd_config.d/99-leadstack-hardening.conf 2>/dev/null && changed="$changed ssh-policy"
fi
if command -v fail2ban-client >/dev/null 2>&1; then
  if ! systemctl is-active --quiet fail2ban 2>/dev/null; then
    systemctl enable --now fail2ban >/dev/null 2>&1 && changed="$changed fail2ban"
  fi
fi
echo "changed=$changed"`

type securityHardenResponse struct {
	ServerID string   `json:"server_id"`
	Changed  []string `json:"changed"`
}

// handleSecurityHarden 处理 POST /api/v1/servers/{id}/security/harden。
func (a *API) handleSecurityHarden(w http.ResponseWriter, r *http.Request) {
	serverID := strings.TrimSpace(chi.URLParam(r, "serverID"))
	if serverID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}
	if a.remoteExec == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "远程执行通道未启用，无法执行安全加固")
		return
	}
	stdout, stderr, exitCode, err := a.remoteExec.Exec(r.Context(), serverID, securityHardenScript, 30)
	if err != nil {
		writeError(w, http.StatusBadGateway, codeInternal, "安全加固执行失败: "+err.Error())
		return
	}
	if exitCode != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = fmt.Sprintf("脚本退出码 %d", exitCode)
		}
		writeError(w, http.StatusBadGateway, codeInternal, "安全加固执行失败: "+msg)
		return
	}
	changed := []string{}
	for _, line := range strings.Split(stdout, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && key == "changed" {
			changed = append(changed, strings.Fields(value)...)
		}
	}
	a.auditManual(r, "server.security_harden", "server", serverID, fmt.Sprintf(`{"changed":%d}`, len(changed)))
	writeData(w, http.StatusOK, securityHardenResponse{ServerID: serverID, Changed: changed})
}
