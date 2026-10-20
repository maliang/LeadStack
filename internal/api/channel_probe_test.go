package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/maliang/leadstack/internal/notify"
)

// probeFakeNotifier 在既有 Notifier 替身之上补一个 Probe 能力，
// 用于验证「测试发送」响应里新增的 probe 字段。
type probeFakeNotifier struct {
	*fakeNotifier
	probeRes notify.ProbeResult
	probeErr error
	probes   int
}

func (f *probeFakeNotifier) Probe(_ context.Context, _ notify.Channel) (notify.ProbeResult, error) {
	f.probes++
	return f.probeRes, f.probeErr
}

// TestChannelTestEndpointProbeField 覆盖测试发送接口的 probe 字段：
//   - 既有 {ok,error} 语义与字段保持不变（只新增 probe）；
//   - 发送成功时不额外探测（避免向用户渠道二次投递探针消息）；
//   - 发送失败时返回探测结果，区分「服务商已应答」与「网络不可达」。
func TestChannelTestEndpointProbeField(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	prober := &probeFakeNotifier{fakeNotifier: env.notif}
	env.api.notifier = prober

	channel := env.createChannelVia(jwt, map[string]any{
		"name": "probe-hook", "kind": "webhook",
		"config": map[string]any{"url": "http://127.0.0.1:9/hook"},
	})
	path := "/api/v1/alert-channels/" + channel.ID + "/test"

	parse := func(t *testing.T, raw []byte) channelTestResult {
		t.Helper()
		var out struct {
			Data channelTestResult `json:"data"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("解析测试响应失败: %v", err)
		}
		return out.Data
	}

	// 1) 发送成功：{ok:true} 语义不变，且不触发探测。
	status, raw := env.request(http.MethodPost, path, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", status)
	}
	res := parse(t, raw)
	if !res.OK {
		t.Fatalf("应发送成功: %+v", res)
	}
	if res.Probe != nil {
		t.Errorf("发送成功时不应做额外探测（probe=%+v）", res.Probe)
	}
	if prober.probes != 0 {
		t.Errorf("发送成功时不应调用 Probe，实际调用 %d 次", prober.probes)
	}

	// 2) 发送失败 + 服务商已应答：probe 带状态码/业务码/片段。
	prober.mu.Lock()
	prober.failOn = map[string]string{channel.ID: "HTTP 非 2xx: 500 body=boom"}
	prober.mu.Unlock()
	prober.probeRes = notify.ProbeResult{
		Kind: notify.KindWebhook, Reachable: true, StatusCode: http.StatusUnauthorized,
		ProviderCode: "errcode=300005", ResponseSnippet: "token is not exist",
	}
	status, raw = env.request(http.MethodPost, path, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("失败时也应返回 200，实际 %d", status)
	}
	res = parse(t, raw)
	if res.OK || res.Error == "" {
		t.Fatalf("失败结果应带 error: %+v", res)
	}
	if res.Probe == nil {
		t.Fatal("发送失败时应附带 probe")
	}
	if !res.Probe.Reachable || res.Probe.StatusCode != http.StatusUnauthorized {
		t.Errorf("probe 状态不符: %+v", res.Probe)
	}
	if res.Probe.ProviderCode != "errcode=300005" || res.Probe.Snippet != "token is not exist" {
		t.Errorf("probe 业务码/片段不符: %+v", res.Probe)
	}
	if prober.probes != 1 {
		t.Errorf("失败时 Probe 调用次数 = %d, want 1", prober.probes)
	}

	// 3) 发送失败 + 网络不可达：probe 带分类错误与中文标签。
	prober.probeRes = notify.ProbeResult{Kind: notify.KindWebhook, ErrorKind: notify.ErrorKindDNS}
	prober.probeErr = &notify.SendError{
		Kind: notify.ErrorKindDNS, Channel: notify.KindWebhook,
		Message: "请求失败: lookup hooks.example.invalid: no such host",
	}
	status, raw = env.request(http.MethodPost, path, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", status)
	}
	res = parse(t, raw)
	if res.Probe == nil {
		t.Fatal("发送失败时应附带 probe")
	}
	if res.Probe.Reachable {
		t.Errorf("网络不可达时 reachable 应为 false: %+v", res.Probe)
	}
	if !strings.Contains(res.Probe.Error, "DNS 解析失败") {
		t.Errorf("probe.error 应包含中文分类: %q", res.Probe.Error)
	}
	if res.Probe.ErrorKind != string(notify.ErrorKindDNS) {
		t.Errorf("probe.error_kind = %q, want dns", res.Probe.ErrorKind)
	}
	if res.Probe.ErrorLabel != "DNS 解析失败" {
		t.Errorf("probe.error_label = %q", res.Probe.ErrorLabel)
	}
	if prober.probes != 2 {
		t.Errorf("Probe 调用次数 = %d, want 2", prober.probes)
	}
}

// TestChannelTestEndpointProbeFieldOmittedForPlainNotifier 确认只实现 Notifier 的替身
// 不会因为缺少 Probe 而失败（可选能力的向下兼容）。
func TestChannelTestEndpointProbeFieldOmittedForPlainNotifier(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	channel := env.createChannelVia(jwt, map[string]any{
		"name": "plain-hook", "kind": "webhook",
		"config": map[string]any{"url": "http://127.0.0.1:9/hook"},
	})
	env.notif.mu.Lock()
	env.notif.failOn = map[string]string{channel.ID: "boom"}
	env.notif.mu.Unlock()

	status, raw := env.request(http.MethodPost, "/api/v1/alert-channels/"+channel.ID+"/test", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", status)
	}
	var out struct {
		Data channelTestResult `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if out.Data.OK || out.Data.Error == "" {
		t.Fatalf("失败语义应保持不变: %+v", out.Data)
	}
	if out.Data.Probe != nil {
		t.Errorf("没有 Probe 能力时不应出现 probe 字段: %+v", out.Data.Probe)
	}
}
