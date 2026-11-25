package agent

import "testing"

func TestParseOSRelease(t *testing.T) {
	values := parseOSRelease(`# comment
ID=ubuntu
NAME="Ubuntu"
PRETTY_NAME="Ubuntu 24.04.1 LTS"
VERSION_ID="24.04"
`)
	if values["ID"] != "ubuntu" || values["NAME"] != "Ubuntu" || values["PRETTY_NAME"] != "Ubuntu 24.04.1 LTS" {
		t.Fatalf("os-release 解析错误: %#v", values)
	}
}

func TestFallbackOSID(t *testing.T) {
	for platform, want := range map[string]string{
		"ubuntu": "ubuntu",
		"debian": "debian",
		"centos": "centos",
		"rocky":  "rocky",
		"alpine": "alpine",
		"arch":   "arch",
	} {
		if got := fallbackOSID(platform); got != want {
			t.Fatalf("fallbackOSID(%q)=%q，期望 %q", platform, got, want)
		}
	}
}
