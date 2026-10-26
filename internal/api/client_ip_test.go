package api

import (
	"net"
	"net/http/httptest"
	"testing"
)

// mustNets 把 CIDR 文本列表解析成网段（测试用，失败即 Fatal）。
func mustNets(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatalf("非法 CIDR %q: %v", c, err)
		}
		out = append(out, n)
	}
	return out
}

// TestClientIPIgnoresHeadersByDefault 验证未配置可信代理时，代理头一律不被采信。
func TestClientIPIgnoresHeadersByDefault(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/servers", nil)
	r.RemoteAddr = "203.0.113.7:51234"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	r.Header.Set("X-Real-IP", "5.6.7.8")

	if got := clientIPWith(r, nil); got != "203.0.113.7" {
		t.Errorf("clientIP = %q，期望 TCP 对端 203.0.113.7", got)
	}
}

// TestClientIPTrustedProxyChain 验证可信代理场景下穿透 XFF，且只信配置过的网段。
func TestClientIPTrustedProxyChain(t *testing.T) {
	trusted := mustNets(t, "10.0.0.0/8", "127.0.0.1/32", "::1/128")

	cases := []struct {
		name   string
		remote string
		xff    []string
		realIP string
		want   string
	}{
		{name: "单级代理", remote: "10.0.0.5:1000", xff: []string{"203.0.113.9"}, want: "203.0.113.9"},
		{name: "多级代理取最左不可信", remote: "10.0.0.5:1000", xff: []string{"198.51.100.7, 10.0.0.6"}, want: "198.51.100.7"},
		{name: "客户端伪造左侧条目时仍取右起第一个不可信", remote: "10.0.0.5:1000", xff: []string{"1.1.1.1, 198.51.100.7"}, want: "198.51.100.7"},
		{name: "重复头按顺序拼接", remote: "10.0.0.5:1000", xff: []string{"198.51.100.7", "10.0.0.6"}, want: "198.51.100.7"},
		{name: "对端不可信则不采信XFF", remote: "203.0.113.7:1000", xff: []string{"1.2.3.4"}, want: "203.0.113.7"},
		{name: "无XFF时用X-Real-IP", remote: "10.0.0.5:1000", realIP: "198.51.100.8", want: "198.51.100.8"},
		{name: "XFF全长可信则取最左", remote: "10.0.0.5:1000", xff: []string{"10.0.0.1, 10.0.0.2"}, want: "10.0.0.1"},
		{name: "非法XFF条目被忽略", remote: "10.0.0.5:1000", xff: []string{"not-an-ip, 198.51.100.9"}, want: "198.51.100.9"},
		{name: "带端口的XFF被归一", remote: "10.0.0.5:1000", xff: []string{"198.51.100.10:443"}, want: "198.51.100.10"},
		{name: "IPv6回环代理", remote: "[::1]:1000", xff: []string{"2001:db8::1"}, want: "2001:db8::1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/v1/servers", nil)
			r.RemoteAddr = c.remote
			for _, v := range c.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if c.realIP != "" {
				r.Header.Set("X-Real-IP", c.realIP)
			}
			got := clientIPWith(r, trusted)
			if got != c.want {
				t.Errorf("clientIP = %q，期望 %q", got, c.want)
			}
		})
	}
}

// TestClientIPUnknownPlaceholder 验证 "unknown" 这类占位值不会污染审计 IP。
func TestClientIPUnknownPlaceholder(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.5:1000"
	r.Header.Set("X-Forwarded-For", "unknown")
	r.Header.Set("X-Real-IP", "unknown")

	if got := clientIPWith(r, mustNets(t, "10.0.0.0/8")); got != "10.0.0.5" {
		t.Errorf("clientIP = %q，期望回落到对端地址 10.0.0.5", got)
	}
}

// TestSetTrustedProxiesGlobal 验证启动注入后全局 clientIP 生效。
func TestSetTrustedProxiesGlobal(t *testing.T) {
	t.Cleanup(func() { SetTrustedProxies(nil) })
	SetTrustedProxies(mustNets(t, "10.0.0.0/8"))

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.5:1000"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")

	if got := clientIP(r); got != "203.0.113.9" {
		t.Errorf("clientIP = %q，期望 203.0.113.9", got)
	}
}
