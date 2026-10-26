package config

import "testing"

// TestParseTrustedProxies 覆盖 CIDR / 单 IP / 空值与非法值。
func TestParseTrustedProxies(t *testing.T) {
	nets, err := ParseTrustedProxies("")
	if err != nil || nets != nil {
		t.Fatalf("空配置应返回 nil, nil；实际 %v, %v", nets, err)
	}

	nets, err = ParseTrustedProxies("10.0.0.0/8, 192.168.1.5 ,::1/128")
	if err != nil {
		t.Fatalf("合法配置解析失败: %v", err)
	}
	if len(nets) != 3 {
		t.Fatalf("解析出 %d 个网段，期望 3", len(nets))
	}
	// 单 IP 必须被当成 /32 网段：CIDR 里最容易踩的坑就是把 IP 当网段用。
	nets, err = ParseTrustedProxies("192.168.1.5")
	if err != nil {
		t.Fatalf("单 IP 解析失败: %v", err)
	}
	if len(nets) != 1 || nets[0].String() != "192.168.1.5/32" {
		t.Fatalf("单 IP 未被归一成 /32: %v", nets)
	}
	nets, err = ParseTrustedProxies("::1")
	if err != nil {
		t.Fatalf("IPv6 单地址解析失败: %v", err)
	}
	if len(nets) != 1 || nets[0].String() != "::1/128" {
		t.Fatalf("IPv6 单地址未被归一成 /128: %v", nets)
	}

	for _, bad := range []string{"not-an-ip", "10.0.0.0/33", "10.0.0.0/8, oops"} {
		if _, err := ParseTrustedProxies(bad); err == nil {
			t.Errorf("%q 应解析失败", bad)
		}
	}
}

// TestLoadRejectsBadTrustedProxies 验证非法配置在启动时就失败。
func TestLoadRejectsBadTrustedProxies(t *testing.T) {
	t.Setenv(EnvTrustedProxies, "10.0.0.0/8,oops")
	if _, err := Load("test", nil); err == nil {
		t.Fatal("非法 trusted-proxies 应导致启动失败")
	}
	t.Setenv(EnvTrustedProxies, "10.0.0.0/8")
	cfg, err := Load("test", nil)
	if err != nil {
		t.Fatalf("合法配置不应失败: %v", err)
	}
	if cfg.TrustedProxies != "10.0.0.0/8" {
		t.Errorf("TrustedProxies = %q", cfg.TrustedProxies)
	}
}
