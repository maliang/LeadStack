package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maliang/leadstack/internal/config"
)

// networkPayload 是 GET/PUT /system/network 的响应结构。
type networkPayload struct {
	Data networkSettings `json:"data"`
}

// getNetwork 读取当前网络设置。
func (e *testEnv) getNetwork(t *testing.T, jwt string) networkSettings {
	t.Helper()
	status, raw := e.request(http.MethodGet, "/api/v1/system/network", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询网络设置失败: %d（body=%s）", status, raw)
	}
	var out networkPayload
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析网络设置失败: %v", err)
	}
	return out.Data
}

// TestNetworkSettingsReadAndSource 覆盖 GET 的字段与来源解析。
func TestNetworkSettingsReadAndSource(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	settings := env.getNetwork(t, jwt)
	if settings.ListenSource != sourceDefault {
		t.Fatalf("默认来源应为 default，实际 %q", settings.ListenSource)
	}
	if settings.ActiveListen == "" {
		t.Fatal("active_listen 不应为空")
	}
	if settings.DefaultSSHPort != 22 {
		t.Fatalf("默认 SSH 端口应为 22，实际 %d", settings.DefaultSSHPort)
	}
	// 测试环境配置里 listen 是 127.0.0.1:0（真实端口由内核分配），与 active_listen 不同，
	// 因此这里只要求字段可读；「值相同则不要求重启」由 TestPutNetworkSameAsActiveIsIdempotent 覆盖。
	if settings.ActiveListen == "" {
		t.Fatal("active_listen 不应为空")
	}

	// settings 优先于 env
	t.Setenv(config.EnvPublicURL, "http://env.example:8080")
	settings = env.getNetwork(t, jwt)
	if settings.PublicURLSource != sourceEnv || settings.PublicURL != "http://env.example:8080" {
		t.Fatalf("环境变量来源解析不正确: %+v", settings)
	}

	// 写设置后来源变为 settings
	status, raw := env.request(http.MethodPut, "/api/v1/system/network", jwt,
		map[string]any{"public_url": "http://settings.example:9090"})
	if status != http.StatusOK {
		t.Fatalf("更新网络设置失败: %d（body=%s）", status, raw)
	}
	settings = env.getNetwork(t, jwt)
	if settings.PublicURL != "http://settings.example:9090" || settings.PublicURLSource != sourceSettings {
		t.Fatalf("settings 应优先于 env: %+v", settings)
	}
}

// TestNetworkSettingsValidation 覆盖三类校验失败与端口占用。
func TestNetworkSettingsValidation(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	put := func(body map[string]any) (int, []byte) {
		return env.request(http.MethodPut, "/api/v1/system/network", jwt, body)
	}

	// 非法 URL
	for _, bad := range []string{"ftp://x", "0.0.0.0:8080", "::", "not-a-url"} {
		status, raw := put(map[string]any{"public_url": bad})
		if status != http.StatusBadRequest {
			t.Fatalf("非法 public_url %q 期望 400，实际 %d（body=%s）", bad, status, raw)
		}
	}
	// 非法端口
	for _, bad := range []int{0, -1, 65536} {
		status, raw := put(map[string]any{"default_ssh_port": bad})
		if status != http.StatusBadRequest {
			t.Fatalf("非法 default_ssh_port %d 期望 400，实际 %d（body=%s）", bad, status, raw)
		}
	}
	// 非法 listen 格式
	for _, bad := range []string{"", "abc", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:70000"} {
		status, raw := put(map[string]any{"listen": bad})
		if status != http.StatusBadRequest {
			t.Fatalf("非法 listen %q 期望 400，实际 %d（body=%s）", bad, status, raw)
		}
	}

	// 端口被占用 → 400（先自己占住一个端口）
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占位监听失败: %v", err)
	}
	defer func() { _ = ln.Close() }()
	occupied := ln.Addr().String()
	status, raw := put(map[string]any{"listen": occupied})
	if status != http.StatusBadRequest {
		t.Fatalf("被占用端口期望 400，实际 %d（body=%s）", status, raw)
	}
	if !strings.Contains(string(raw), "绑定") && !strings.Contains(string(raw), "占用") {
		t.Fatalf("占用提示应可指导用户: %s", raw)
	}
}

// TestNetworkSettingsRestartRequiredAndReset 覆盖 restart_required 与 reset。
func TestNetworkSettingsRestartRequiredAndReset(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	// 找一个当前空闲的端口（保存时会被试 bind，随后释放）
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("探测空闲端口失败: %v", err)
	}
	freeAddr := probe.Addr().String()
	_ = probe.Close()

	status, raw := env.request(http.MethodPut, "/api/v1/system/network", jwt,
		map[string]any{"listen": freeAddr, "default_ssh_port": 2222})
	if status != http.StatusOK {
		t.Fatalf("保存监听地址失败: %d（body=%s）", status, raw)
	}
	var updated networkPayload
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	// 配置值 ≠ 当前生效值（测试环境 active 是随机端口）
	if updated.Data.Listen != freeAddr {
		t.Fatalf("配置值未保存: %+v", updated.Data)
	}
	if !updated.Data.RestartRequired {
		t.Fatalf("改监听地址后应要求重启: %+v", updated.Data)
	}
	if updated.Data.DefaultSSHPort != 2222 || updated.Data.DefaultSSHPortSrc != sourceSettings {
		t.Fatalf("默认端口未生效: %+v", updated.Data)
	}

	// reset 后回落 default
	status, raw = env.request(http.MethodPost, "/api/v1/system/network/reset", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("重置网络设置失败: %d（body=%s）", status, raw)
	}
	var reset networkPayload
	if err := json.Unmarshal(raw, &reset); err != nil {
		t.Fatalf("解析重置响应失败: %v", err)
	}
	if reset.Data.ListenSource != sourceDefault || reset.Data.DefaultSSHPort != 22 {
		t.Fatalf("重置后应回落默认值: %+v", reset.Data)
	}
}

// TestDefaultSSHPortAppliesToCreateAndImport 覆盖默认端口在新建与导入时生效。
func TestDefaultSSHPortAppliesToCreateAndImport(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	// 先设默认端口
	status, raw := env.request(http.MethodPut, "/api/v1/system/network", jwt,
		map[string]any{"default_ssh_port": 2222})
	if status != http.StatusOK {
		t.Fatalf("设置默认端口失败: %d（body=%s）", status, raw)
	}

	// 新建服务器不传 port → 2222
	status, raw = env.request(http.MethodPost, "/api/v1/servers", jwt,
		map[string]any{"name": "default-port", "host": "10.60.0.1"})
	if status != http.StatusCreated {
		t.Fatalf("建服务器失败: %d（body=%s）", status, raw)
	}
	var created struct {
		Data serverPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("解析服务器失败: %v", err)
	}
	if created.Data.Port != 2222 {
		t.Fatalf("未传 port 时应使用默认端口 2222，实际 %d", created.Data.Port)
	}

	// 导入不传 default_port → 2222
	status, raw = env.request(http.MethodPost, "/api/v1/servers/import", jwt,
		map[string]any{"text": "10.60.0.2"})
	if status != http.StatusOK {
		t.Fatalf("导入失败: %d（body=%s）", status, raw)
	}
	var imported struct {
		Data importResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &imported); err != nil {
		t.Fatalf("解析导入结果失败: %v", err)
	}
	if len(imported.Data.Created) != 1 || imported.Data.Created[0].Port != 2222 {
		t.Fatalf("导入应使用默认端口 2222: %+v", imported.Data.Created)
	}

	// 显式给端口仍然优先
	status, raw = env.request(http.MethodPost, "/api/v1/servers", jwt,
		map[string]any{"name": "explicit-port", "host": "10.60.0.3", "port": 2200})
	if status != http.StatusCreated {
		t.Fatalf("建服务器失败: %d（body=%s）", status, raw)
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("解析服务器失败: %v", err)
	}
	if created.Data.Port != 2200 {
		t.Fatalf("显式端口应优先，实际 %d", created.Data.Port)
	}

	// 清空设置后回到 22（兼容性红线）
	status, _ = env.request(http.MethodPost, "/api/v1/system/network/reset", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("重置失败: %d", status)
	}
	status, raw = env.request(http.MethodPost, "/api/v1/servers", jwt,
		map[string]any{"name": "back-to-22", "host": "10.60.0.4"})
	if status != http.StatusCreated {
		t.Fatalf("建服务器失败: %d（body=%s）", status, raw)
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("解析服务器失败: %v", err)
	}
	if created.Data.Port != 22 {
		t.Fatalf("未改设置时应默认 22，实际 %d", created.Data.Port)
	}
}

// TestPatchServerHostAndPort 覆盖事后修改目标机端口/地址与主机密钥清理。
func TestPatchServerHostAndPort(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	srv := env.createServerVia(jwt, map[string]any{
		"name": "move-me", "host": "10.61.0.1", "port": 22,
	})

	// 预置一条该机的主机密钥记录
	knownHosts := filepath.Join(env.cfg.DataDir, "known_hosts")
	content := "10.61.0.1:22 ssh-ed25519 AAAAB3NZA-old\n10.99.0.9:22 ssh-ed25519 AAAAB3NZA-other\n"
	if err := os.WriteFile(knownHosts, []byte(content), 0o600); err != nil {
		t.Fatalf("写入 known_hosts 失败: %v", err)
	}

	// 改端口
	status, raw := env.request(http.MethodPatch, "/api/v1/servers/"+srv.ID, jwt,
		map[string]any{"port": 2222})
	if status != http.StatusOK {
		t.Fatalf("修改端口失败: %d（body=%s）", status, raw)
	}
	var updated struct {
		Data serverPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatalf("解析服务器失败: %v", err)
	}
	if updated.Data.Port != 2222 {
		t.Fatalf("端口未更新，实际 %d", updated.Data.Port)
	}

	// 改 host
	status, raw = env.request(http.MethodPatch, "/api/v1/servers/"+srv.ID, jwt,
		map[string]any{"host": "10.61.0.2"})
	if status != http.StatusOK {
		t.Fatalf("修改主机失败: %d（body=%s）", status, raw)
	}
	_ = json.Unmarshal(raw, &updated)
	if updated.Data.Host != "10.61.0.2" || updated.Data.Port != 2222 {
		t.Fatalf("主机/端口更新不正确: %+v", updated.Data)
	}

	// 旧主机密钥应被清除，其它机器的不受影响
	raw2, err := os.ReadFile(knownHosts)
	if err != nil {
		t.Fatalf("读取 known_hosts 失败: %v", err)
	}
	if strings.Contains(string(raw2), "10.61.0.1:22") {
		t.Fatalf("旧主机密钥未清除: %s", raw2)
	}
	if !strings.Contains(string(raw2), "10.99.0.9:22") {
		t.Fatalf("不应误删其它机器的主机密钥: %s", raw2)
	}

	// 审计里应记录旧值→新值
	status, raw = env.request(http.MethodGet, "/api/v1/audit-logs?action=server.update&limit=20", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询审计失败: %d", status)
	}
	if !strings.Contains(string(raw), "old") || !strings.Contains(string(raw), "10.61.0.1") {
		t.Fatalf("审计应记录旧值→新值: %s", raw)
	}

	// 非法端口
	status, _ = env.request(http.MethodPatch, "/api/v1/servers/"+srv.ID, jwt, map[string]any{"port": 70000})
	if status != http.StatusBadRequest {
		t.Fatalf("非法端口期望 400，实际 %d", status)
	}
}

// TestInstallScriptUsesPublicURL 覆盖 install.sh 命令使用配置的对外地址。
func TestInstallScriptUsesPublicURL(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	// 未配置：回落到请求 Host
	status, raw := env.request(http.MethodPost, "/api/v1/agent/install-script", jwt,
		map[string]any{"token": "abc123"})
	if status != http.StatusOK {
		t.Fatalf("生成安装命令失败: %d（body=%s）", status, raw)
	}
	if !strings.Contains(string(raw), env.srv.Listener.Addr().String()) {
		t.Fatalf("未配置 public_url 时应回落到请求 Host: %s", raw)
	}

	// 配置 public_url 后立即生效
	status, _ = env.request(http.MethodPut, "/api/v1/system/network", jwt,
		map[string]any{"public_url": "https://ops.example.com"})
	if status != http.StatusOK {
		t.Fatalf("设置 public_url 失败: %d", status)
	}
	status, raw = env.request(http.MethodPost, "/api/v1/agent/install-script", jwt,
		map[string]any{"token": "abc123"})
	if status != http.StatusOK {
		t.Fatalf("生成安装命令失败: %d", status)
	}
	if !strings.Contains(string(raw), "https://ops.example.com/install.sh") {
		t.Fatalf("安装命令应使用 public_url: %s", raw)
	}
}

// TestNetworkSettingsAudited 覆盖审计 action。
func TestNetworkSettingsAudited(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	if status, _ := env.request(http.MethodPut, "/api/v1/system/network", jwt,
		map[string]any{"default_ssh_port": 2020}); status != http.StatusOK {
		t.Fatalf("更新失败: %d", status)
	}
	if status, _ := env.request(http.MethodPost, "/api/v1/system/network/reset", jwt, nil); status != http.StatusOK {
		t.Fatalf("重置失败: %d", status)
	}

	for _, action := range []string{"system.network_update", "system.network_reset"} {
		status, raw := env.request(http.MethodGet, "/api/v1/audit-logs?action="+action, jwt, nil)
		if status != http.StatusOK {
			t.Fatalf("查询审计失败: %d", status)
		}
		if !strings.Contains(string(raw), action) {
			t.Fatalf("审计缺少 %s: %s", action, raw)
		}
	}
}

// TestResolveHelpers 覆盖解析辅助函数的优先级（纯逻辑）。
func TestResolveHelpers(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	// 无设置 + 无 env → default
	t.Setenv(config.EnvListen, "")
	listen, source, err := ResolveNetworkListen(ctx, env.st, "", "127.0.0.1:1234")
	if err != nil || listen != "127.0.0.1:1234" || source != sourceDefault {
		t.Fatalf("默认解析不正确: %q %q %v", listen, source, err)
	}
	// env 优先于 default
	t.Setenv(config.EnvListen, "127.0.0.1:4321")
	listen, source, _ = ResolveNetworkListen(ctx, env.st, "", "127.0.0.1:1234")
	if listen != "127.0.0.1:4321" || source != sourceEnv {
		t.Fatalf("env 解析不正确: %q %q", listen, source)
	}
	// settings 优先于 env
	if err := env.st.SetSetting(ctx, settingNetworkListen, "127.0.0.1:5555"); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}
	listen, source, _ = ResolveNetworkListen(ctx, env.st, "", "127.0.0.1:1234")
	if listen != "127.0.0.1:5555" || source != sourceSettings {
		t.Fatalf("settings 解析不正确: %q %q", listen, source)
	}
	// 缺少 public_url 时返回空（调用方回落到 Host）
	t.Setenv(config.EnvPublicURL, "")
	publicURL, pSource, _ := ResolvePublicURL(ctx, env.st)
	if publicURL != "" || pSource != sourceDefault {
		t.Fatalf("public_url 默认应为空: %q %q", publicURL, pSource)
	}
	_ = fmt.Sprint(publicURL)
}

// TestPutNetworkSameAsActiveIsIdempotent 覆盖「把当前生效地址保存进设置」不应被误判为端口占用。
func TestPutNetworkSameAsActiveIsIdempotent(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	active := env.getNetwork(t, jwt).ActiveListen
	if active == "" {
		t.Skip("测试环境未暴露 active_listen")
	}

	// 保存与当前生效地址相同的值：必须成功（而不是 400「已被占用」）
	status, raw := env.request(http.MethodPut, "/api/v1/system/network", jwt,
		map[string]any{"listen": active})
	if status != http.StatusOK {
		t.Fatalf("保存当前生效地址期望 200，实际 %d（body=%s）", status, raw)
	}
	settings := env.getNetwork(t, jwt)
	if settings.Listen != active || settings.ListenSource != sourceSettings {
		t.Fatalf("保存后应记录为 settings 来源: %+v", settings)
	}
	if settings.RestartRequired {
		t.Fatalf("值未变化时不应要求重启: %+v", settings)
	}

	// 再保存一次（幂等）
	status, raw = env.request(http.MethodPut, "/api/v1/system/network", jwt,
		map[string]any{"listen": active})
	if status != http.StatusOK {
		t.Fatalf("重复保存期望 200，实际 %d（body=%s）", status, raw)
	}

	// 换成别的地址仍然要做占用探测（这里用被占端口验证行为未退化）
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占位监听失败: %v", err)
	}
	defer func() { _ = ln.Close() }()
	status, raw = env.request(http.MethodPut, "/api/v1/system/network", jwt,
		map[string]any{"listen": ln.Addr().String()})
	if status != http.StatusBadRequest {
		t.Fatalf("保存其它被占端口期望 400，实际 %d（body=%s）", status, raw)
	}
}

// TestPublicURLEmptyHintMentionsReset 覆盖 public_url 空值的可执行提示。
func TestPublicURLEmptyHintMentionsReset(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	status, raw := env.request(http.MethodPut, "/api/v1/system/network", jwt,
		map[string]any{"public_url": "  "})
	if status != http.StatusBadRequest {
		t.Fatalf("空 public_url 期望 400，实际 %d", status)
	}
	if !strings.Contains(string(raw), "恢复默认") || !strings.Contains(string(raw), "reset") {
		t.Fatalf("空值提示应引导使用恢复默认: %s", raw)
	}
}

// TestListenCandidatesPriority 覆盖启动回退链的顺序与去重（纯函数）。
func TestListenCandidatesPriority(t *testing.T) {
	// 全部来源都有：flag > settings > env > default > temp
	got := ListenCandidates("10.0.0.2:9000", "10.0.0.1:8080", "10.0.0.3:7000")
	want := []struct{ value, source string }{
		{"10.0.0.1:8080", sourceFlag},
		{"10.0.0.2:9000", sourceSettings},
		{"10.0.0.3:7000", sourceEnv},
		{"127.0.0.1:8080", sourceDefault},
		{"127.0.0.1:0", sourceTemp},
	}
	if len(got) != len(want) {
		t.Fatalf("候选数量期望 %d，实际 %d: %+v", len(want), len(got), got)
	}
	for i := range want {
		if got[i].Value != want[i].value || got[i].Source != want[i].source {
			t.Fatalf("第 %d 个候选期望 %+v，实际 %+v", i+1, want[i], got[i])
		}
	}

	// 只有 settings：settings 排第一，其后是 default/temp
	got = ListenCandidates("10.0.0.9:9001", "", "")
	if len(got) != 3 || got[0].Source != sourceSettings || got[1].Source != sourceDefault {
		t.Fatalf("仅 settings 时候选不正确: %+v", got)
	}

	// 相邻重复值去重（settings 与 env 相同）
	got = ListenCandidates("10.0.0.9:9002", "", "10.0.0.9:9002")
	for _, c := range got[1:] {
		if c.Value == "10.0.0.9:9002" && c.Source == sourceEnv {
			t.Fatalf("重复值应被去重: %+v", got)
		}
	}

	// 空显式值不产生候选
	got = ListenCandidates("", "  ", "")
	if len(got) != 2 || got[0].Source != sourceDefault || got[1].Source != sourceTemp {
		t.Fatalf("空值不应产生候选: %+v", got)
	}
}

// TestResolveNetworkListenPrefersExplicitFlag 覆盖 flag 压过 settings 的优先级。
func TestResolveNetworkListenPrefersExplicitFlag(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	// settings 存一个"坏值"
	if err := env.st.SetSetting(ctx, settingNetworkListen, "10.9.9.9:1"); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}
	t.Setenv(config.EnvListen, "10.0.0.3:7000")

	// 显式 flag 最高
	value, source, err := ResolveNetworkListen(ctx, env.st, "127.0.0.1:7777", "127.0.0.1:8080")
	if err != nil || value != "127.0.0.1:7777" || source != sourceFlag {
		t.Fatalf("flag 应压过 settings: %q %q %v", value, source, err)
	}
	// 无 flag 时 settings 优先于 env
	value, source, _ = ResolveNetworkListen(ctx, env.st, "", "127.0.0.1:8080")
	if value != "10.9.9.9:1" || source != sourceSettings {
		t.Fatalf("无 flag 时应使用 settings: %q %q", value, source)
	}
}

// TestNetworkSnapshotShowsThreeSources 覆盖离线快照的三者取值。
func TestNetworkSnapshotShowsThreeSources(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	t.Setenv(config.EnvListen, "10.0.0.3:7000")
	if err := env.st.SetSetting(ctx, settingNetworkListen, "10.0.0.2:9000"); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}
	snap, err := ReadNetworkSnapshot(ctx, env.st)
	if err != nil {
		t.Fatalf("读取快照失败: %v", err)
	}
	if snap.SettingsListen != "10.0.0.2:9000" || snap.EnvListen != "10.0.0.3:7000" ||
		snap.DefaultListen == "" || snap.EffectiveListenFrom != sourceSettings {
		t.Fatalf("快照不正确: %+v", snap)
	}
}
