package api

import "testing"

func TestParseSecurityAuditFindsRisks(t *testing.T) {
	out := `ssh_root=yes
ssh_password=yes
firewall=inactive
fail2ban=missing
shadow_mode=644
empty_passwords=1
authorized_keys=2
selinux=Disabled
`
	findings, score := parseSecurityAudit(out)
	if score >= 60 {
		t.Fatalf("高风险配置分值应明显下降，实际 %d", score)
	}
	want := map[string]string{
		"ssh-root-login":    "high",
		"ssh-password-auth": "high",
		"firewall":          "high",
		"fail2ban":          "medium",
		"shadow-mode":       "high",
		"empty-passwords":   "critical",
	}
	for _, finding := range findings {
		if level, ok := want[finding.ID]; ok && finding.Level != level {
			t.Errorf("%s level = %s，期望 %s", finding.ID, finding.Level, level)
		}
	}
}

func TestParseSecurityAuditHealthyHost(t *testing.T) {
	out := `ssh_root=prohibit-password
ssh_password=no
firewall=active
fail2ban=active
shadow_mode=640
empty_passwords=0
authorized_keys=1
selinux=Enforcing
`
	findings, score := parseSecurityAudit(out)
	if score != 100 {
		t.Fatalf("健康主机应为 100 分，实际 %d（%+v）", score, findings)
	}
	for _, finding := range findings {
		if finding.Level != "ok" {
			t.Fatalf("健康主机不应有风险项: %+v", finding)
		}
	}
}
