package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/maliang/leadstack/internal/alerting"
	"github.com/maliang/leadstack/internal/store"
)

// createChannelVia 通过 REST 创建渠道。
func (e *testEnv) createChannelVia(jwt string, body map[string]any) alertChannelPayload {
	e.t.Helper()
	status, raw := e.request(http.MethodPost, "/api/v1/alert-channels", jwt, body)
	if status != http.StatusCreated {
		e.t.Fatalf("创建渠道失败: status=%d body=%s", status, raw)
	}
	var out struct {
		Data alertChannelPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("解析渠道响应失败: %v", err)
	}
	return out.Data
}

// createRuleVia 通过 REST 创建规则。
func (e *testEnv) createRuleVia(jwt string, body map[string]any) store.AlertRule {
	e.t.Helper()
	status, raw := e.request(http.MethodPost, "/api/v1/alert-rules", jwt, body)
	if status != http.StatusCreated {
		e.t.Fatalf("创建规则失败: status=%d body=%s", status, raw)
	}
	var out struct {
		Data store.AlertRule `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("解析规则响应失败: %v", err)
	}
	return out.Data
}

// TestAlertChannelEncryptionAndMasking 覆盖验收 3：落库加密 + 接口脱敏 + 部分更新合并。
func TestAlertChannelEncryptionAndMasking(t *testing.T) {
	env := newTestEnv(t)

	status, _ := env.request(http.MethodGet, "/api/v1/alert-channels", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("未带 token 访问 /alert-channels 期望 401，实际 %d", status)
	}

	jwt := env.login()
	const (
		rawSecret = "SECRET-abcdefghijklmnop-9876543210"
		rawToken  = "1234567:BOT-TOKEN-abcdefghijklmnop"
	)
	channel := env.createChannelVia(jwt, map[string]any{
		"name": "运维群", "kind": "dingtalk", "enabled": true,
		"config": map[string]any{"webhook": "http://127.0.0.1:9/hook", "secret": rawSecret},
	})
	if channel.ID == "" || channel.Kind != "dingtalk" {
		t.Fatalf("渠道创建结果不正确: %+v", channel)
	}

	// 落库必须是密文：直接读库断言不含明文
	stored, err := env.st.GetAlertChannel(context.Background(), channel.ID)
	if err != nil {
		t.Fatalf("读取渠道失败: %v", err)
	}
	if strings.Contains(string(stored.ConfigEnc), rawSecret) {
		t.Fatal("config_enc 里出现了明文 secret")
	}
	if len(stored.ConfigEnc) == 0 {
		t.Fatal("config_enc 为空")
	}

	// 接口返回必须脱敏
	status, raw := env.request(http.MethodGet, "/api/v1/alert-channels", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询渠道列表失败: %d", status)
	}
	body := string(raw)
	if strings.Contains(body, rawSecret) {
		t.Fatalf("渠道列表泄露了 secret 明文: %s", body)
	}
	if strings.Contains(body, "abcdefghijklmnop") {
		t.Fatalf("渠道列表泄露了 secret 片段: %s", body)
	}
	if !strings.Contains(body, `"secret":"SECR***"`) {
		t.Fatalf("secret 应按「前 4 位 + ***」脱敏返回: %s", body)
	}
	// 非密钥字段应原样返回，便于编辑
	if !strings.Contains(body, "http://127.0.0.1:9/hook") {
		t.Fatalf("url/webhook 等非密钥字段应原样返回: %s", body)
	}

	// telegram bot_token 同样脱敏
	telegram := env.createChannelVia(jwt, map[string]any{
		"name": "TG", "kind": "telegram",
		"config": map[string]any{"bot_token": rawToken, "chat_id": "-100200"},
	})
	_, raw = env.request(http.MethodGet, "/api/v1/alert-channels", jwt, nil)
	if strings.Contains(string(raw), rawToken) || strings.Contains(string(raw), "BOT-TOKEN") {
		t.Fatalf("telegram bot_token 未脱敏: %s", raw)
	}
	if telegram.ID == "" {
		t.Fatal("telegram 渠道未创建")
	}

	// PATCH 部分配置：只改 secret，webhook 必须保留（前端「留空则不发」语义）
	status, raw = env.request(http.MethodPatch, "/api/v1/alert-channels/"+channel.ID, jwt, map[string]any{
		"config": map[string]any{"secret": "NEWSECRET-zyxwvutsrq"},
	})
	if status != http.StatusOK {
		t.Fatalf("更新渠道失败: %d（body=%s）", status, raw)
	}
	updated, err := env.st.GetAlertChannel(context.Background(), channel.ID)
	if err != nil {
		t.Fatalf("读取渠道失败: %v", err)
	}
	plain := decryptChannelConfigForTest(t, env, *updated)
	if plain["webhook"] != "http://127.0.0.1:9/hook" {
		t.Fatalf("部分更新不应丢失未提供的字段: %+v", plain)
	}
	if plain["secret"] != "NEWSECRET-zyxwvutsrq" {
		t.Fatalf("secret 未被更新: %v", plain["secret"])
	}

	// 把脱敏串回传 → 视为不修改
	status, raw = env.request(http.MethodPatch, "/api/v1/alert-channels/"+channel.ID, jwt, map[string]any{
		"config": map[string]any{"secret": "NEWS***"},
	})
	if status != http.StatusOK {
		t.Fatalf("更新渠道失败: %d（body=%s）", status, raw)
	}
	updated, _ = env.st.GetAlertChannel(context.Background(), channel.ID)
	plain = decryptChannelConfigForTest(t, env, *updated)
	if plain["secret"] != "NEWSECRET-zyxwvutsrq" {
		t.Fatalf("脱敏串回传不应覆盖真实值: %v", plain["secret"])
	}

	// 非法配置 → 400
	status, _ = env.request(http.MethodPost, "/api/v1/alert-channels", jwt, map[string]any{
		"name": "bad", "kind": "bogus-kind", "config": map[string]any{},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("非法 kind 期望 400，实际 %d", status)
	}
	status, _ = env.request(http.MethodPost, "/api/v1/alert-channels", jwt, map[string]any{
		"name": "bad", "kind": "webhook", "config": map[string]any{},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("webhook 缺 url 期望 400，实际 %d", status)
	}

	// 删除
	status, _ = env.request(http.MethodDelete, "/api/v1/alert-channels/"+channel.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("删除渠道失败: %d", status)
	}
	status, _ = env.request(http.MethodDelete, "/api/v1/alert-channels/"+channel.ID, jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("重复删除期望 404，实际 %d", status)
	}
}

// decryptChannelConfigForTest 用测试主密钥解出渠道配置。
func decryptChannelConfigForTest(t *testing.T, env *testEnv, channel store.AlertChannel) map[string]any {
	t.Helper()
	plain, err := env.cipher.Decrypt(channel.ConfigEnc)
	if err != nil {
		t.Fatalf("解密渠道配置失败: %v", err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(plain, &out); err != nil {
		t.Fatalf("解析渠道配置失败: %v", err)
	}
	return out
}

// TestAlertChannelTestEndpoint 覆盖验收 2 的接口语义：恒 200 + {ok,error}。
func TestAlertChannelTestEndpoint(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	channel := env.createChannelVia(jwt, map[string]any{
		"name": "hook", "kind": "webhook", "config": map[string]any{"url": "http://127.0.0.1:9/hook"},
	})

	status, raw := env.request(http.MethodPost, "/api/v1/alert-channels/"+channel.ID+"/test", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("渠道测试期望 200，实际 %d", status)
	}
	var okResult struct {
		Data channelTestResult `json:"data"`
	}
	if err := json.Unmarshal(raw, &okResult); err != nil {
		t.Fatalf("解析测试响应失败: %v", err)
	}
	if !okResult.Data.OK {
		t.Fatalf("测试发送应成功: %+v", okResult.Data)
	}
	if env.notif.sentCount() != 1 {
		t.Fatalf("替身应收到 1 次发送，实际 %d", env.notif.sentCount())
	}
	// 成功应回写 last_ok_at
	stored, _ := env.st.GetAlertChannel(context.Background(), channel.ID)
	if stored.LastOKAt == nil {
		t.Fatal("成功后应回写 last_ok_at")
	}

	// 失败：HTTP 仍 200，ok=false + error，并回写 last_error
	env.notif.mu.Lock()
	env.notif.failOn = map[string]string{channel.ID: "HTTP 非 2xx: 500 body=boom"}
	env.notif.mu.Unlock()
	status, raw = env.request(http.MethodPost, "/api/v1/alert-channels/"+channel.ID+"/test", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("失败时也应返回 200，实际 %d", status)
	}
	var failResult struct {
		Data channelTestResult `json:"data"`
	}
	if err := json.Unmarshal(raw, &failResult); err != nil {
		t.Fatalf("解析测试响应失败: %v", err)
	}
	if failResult.Data.OK || failResult.Data.Error == "" {
		t.Fatalf("失败结果应带 error: %+v", failResult.Data)
	}
	stored, _ = env.st.GetAlertChannel(context.Background(), channel.ID)
	if stored.LastError == nil || !strings.Contains(*stored.LastError, "500") {
		t.Fatalf("失败原因应回写到 last_error: %v", stored.LastError)
	}

	status, _ = env.request(http.MethodPost, "/api/v1/alert-channels/not-exist/test", jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("不存在的渠道测试期望 404，实际 %d", status)
	}
}

// TestAlertRuleEndpoints 覆盖规则 CRUD、静默与 Reload。
func TestAlertRuleEndpoints(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	status, _ := env.request(http.MethodGet, "/api/v1/alert-rules", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("未带 token 访问 /alert-rules 期望 401，实际 %d", status)
	}

	channel := env.createChannelVia(jwt, map[string]any{
		"name": "hook", "kind": "webhook", "config": map[string]any{"url": "http://127.0.0.1:9/hook"},
	})
	srv := env.createServerVia(jwt, map[string]any{"name": "s1", "host": "10.20.0.1"})
	check := env.createCheckVia(jwt, map[string]any{
		"name": "tcp", "kind": "tcp", "target": "127.0.0.1:22", "interval_s": 10, "server_ids": []string{srv.ID},
	})

	rule := env.createRuleVia(jwt, map[string]any{
		"name": "检测异常", "source": "check", "severity": "critical",
		"check_ids": []string{check.ID}, "channel_ids": []string{channel.ID},
		"for_duration_s": 0, "dedup_window_s": 60,
	})
	if rule.ID == "" || !rule.Enabled || len(rule.ChannelIDs) != 1 || len(rule.CheckIDs) != 1 {
		t.Fatalf("规则创建结果不正确: %+v", rule)
	}
	if env.alerts.reloads == 0 {
		t.Fatal("创建规则后应调用 Reload")
	}

	// 列表
	status, raw := env.request(http.MethodGet, "/api/v1/alert-rules", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询规则列表失败: %d", status)
	}
	if !strings.Contains(string(raw), `"source":"check"`) {
		t.Fatalf("规则列表内容不正确: %s", raw)
	}

	// 校验：非法 source / severity / 指标规则缺 comparator
	for _, body := range []map[string]any{
		{"name": "x", "source": "bogus", "severity": "critical"},
		{"name": "x", "source": "check", "severity": "bogus"},
		{"name": "x", "source": "metric", "severity": "info", "metric": "cpu.pct"},
	} {
		status, _ := env.request(http.MethodPost, "/api/v1/alert-rules", jwt, body)
		if status != http.StatusBadRequest {
			t.Fatalf("非法规则 %v 期望 400，实际 %d", body, status)
		}
	}
	// 引用不存在的检测项 / 渠道 → 404
	status, _ = env.request(http.MethodPost, "/api/v1/alert-rules", jwt, map[string]any{
		"name": "x", "source": "check", "severity": "info", "check_ids": []string{"ghost"},
	})
	if status != http.StatusNotFound {
		t.Fatalf("引用不存在的检测项期望 404，实际 %d", status)
	}
	status, _ = env.request(http.MethodPost, "/api/v1/alert-rules", jwt, map[string]any{
		"name": "x", "source": "check", "severity": "info", "channel_ids": []string{"ghost"},
	})
	if status != http.StatusNotFound {
		t.Fatalf("引用不存在的渠道期望 404，实际 %d", status)
	}

	// 详情 / 404
	status, _ = env.request(http.MethodGet, "/api/v1/alert-rules/"+rule.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询规则详情失败: %d", status)
	}
	status, _ = env.request(http.MethodGet, "/api/v1/alert-rules/ghost", jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("不存在的规则期望 404，实际 %d", status)
	}

	// 静默
	status, raw = env.request(http.MethodPost, "/api/v1/alert-rules/"+rule.ID+"/silence", jwt,
		map[string]any{"duration_s": 300})
	if status != http.StatusOK {
		t.Fatalf("静默失败: %d（body=%s）", status, raw)
	}
	var silenced struct {
		Data store.AlertRule `json:"data"`
	}
	if err := json.Unmarshal(raw, &silenced); err != nil {
		t.Fatalf("解析静默响应失败: %v", err)
	}
	if silenced.Data.SilenceUntil == nil || *silenced.Data.SilenceUntil <= store.NowMS() {
		t.Fatalf("silence_until 未生效: %+v", silenced.Data.SilenceUntil)
	}
	// duration_s=0 → 取消静默（清空 silence_until）
	status, raw = env.request(http.MethodPost, "/api/v1/alert-rules/"+rule.ID+"/silence", jwt,
		map[string]any{"duration_s": 0})
	if status != http.StatusOK {
		t.Fatalf("取消静默失败: %d（body=%s）", status, raw)
	}
	var cleared struct {
		Data store.AlertRule `json:"data"`
	}
	if err := json.Unmarshal(raw, &cleared); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if cleared.Data.SilenceUntil != nil {
		t.Fatalf("duration_s=0 应清空 silence_until，实际 %v", *cleared.Data.SilenceUntil)
	}
	if after := env.getRule(jwt, rule.ID); after.SilenceUntil != nil {
		t.Fatalf("取消静默未持久化: %v", *after.SilenceUntil)
	}
	// 负数 → 400
	status, _ = env.request(http.MethodPost, "/api/v1/alert-rules/"+rule.ID+"/silence", jwt,
		map[string]any{"duration_s": -1})
	if status != http.StatusBadRequest {
		t.Fatalf("负 duration_s 期望 400，实际 %d", status)
	}

	// PATCH
	status, raw = env.request(http.MethodPatch, "/api/v1/alert-rules/"+rule.ID, jwt,
		map[string]any{"enabled": false, "severity": "warning"})
	if status != http.StatusOK {
		t.Fatalf("更新规则失败: %d（body=%s）", status, raw)
	}
	var patched struct {
		Data store.AlertRule `json:"data"`
	}
	if err := json.Unmarshal(raw, &patched); err != nil {
		t.Fatalf("解析更新响应失败: %v", err)
	}
	if patched.Data.Enabled || patched.Data.Severity != "warning" {
		t.Fatalf("更新结果不正确: %+v", patched.Data)
	}
	if len(patched.Data.CheckIDs) != 1 {
		t.Fatalf("未提供的字段不应被清空: %+v", patched.Data.CheckIDs)
	}

	// 删除
	status, _ = env.request(http.MethodDelete, "/api/v1/alert-rules/"+rule.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("删除规则失败: %d", status)
	}
	status, _ = env.request(http.MethodDelete, "/api/v1/alert-rules/"+rule.ID, jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("重复删除期望 404，实际 %d", status)
	}
}

// TestAlertsEndpoints 覆盖告警列表/详情/确认/概览。
func TestAlertsEndpoints(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	ctx := context.Background()

	srv := env.createServerVia(jwt, map[string]any{"name": "srv-a", "host": "10.21.0.1"})
	check := env.createCheckVia(jwt, map[string]any{
		"name": "tcp-a", "kind": "tcp", "target": "127.0.0.1:22", "interval_s": 10, "server_ids": []string{srv.ID},
	})
	rule := env.createRuleVia(jwt, map[string]any{
		"name": "规则A", "source": "check", "severity": "critical", "check_ids": []string{check.ID},
	})

	now := store.NowMS()
	alert := store.Alert{
		ID: "alert-1", RuleID: rule.ID, DedupKey: "check:" + check.ID + ":" + srv.ID,
		ServerID: &srv.ID, CheckID: &check.ID, Status: store.AlertStatusFiring,
		Severity: "critical", Title: "检测项「tcp-a」异常", StartedAt: now, UpdatedAt: now,
		NotifyCount: 1,
	}
	if err := env.st.CreateAlert(ctx, alert); err != nil {
		t.Fatalf("写入告警失败: %v", err)
	}
	if err := env.st.AppendAlertEvent(ctx, alert.ID, store.AlertEventFiring, now, "规则命中"); err != nil {
		t.Fatalf("写入事件失败: %v", err)
	}

	status, raw := env.request(http.MethodGet, "/api/v1/alerts?status=firing", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询告警列表失败: %d", status)
	}
	var list []alertPayload
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), `{"data":[`) {
		t.Fatalf("告警列表应在 data 下返回数组: %s", raw)
	}
	var wrapped struct {
		Data []alertPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		t.Fatalf("解析告警列表失败: %v", err)
	}
	list = wrapped.Data
	if len(list) != 1 {
		t.Fatalf("期望 1 条告警，实际 %d", len(list))
	}
	if list[0].RuleName != "规则A" || list[0].ServerName != "srv-a" || list[0].CheckName != "tcp-a" {
		t.Fatalf("列表应带可读名称: %+v", list[0])
	}
	if !strings.Contains(string(raw), `"notify_count":1`) || !strings.Contains(string(raw), `"dedup_key"`) {
		t.Fatalf("列表字段缺失: %s", raw)
	}

	// 过滤：不匹配的条件应返回空
	status, raw = env.request(http.MethodGet, "/api/v1/alerts?status=resolved", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询失败: %d", status)
	}
	wrapped.Data = nil
	_ = json.Unmarshal(raw, &wrapped)
	if len(wrapped.Data) != 0 {
		t.Fatalf("status=resolved 应为空，实际 %d", len(wrapped.Data))
	}
	status, _ = env.request(http.MethodGet, "/api/v1/alerts?status=bogus", jwt, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("非法 status 期望 400，实际 %d", status)
	}

	// 详情含 events
	status, raw = env.request(http.MethodGet, "/api/v1/alerts/"+alert.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询告警详情失败: %d", status)
	}
	var detail struct {
		Data alertPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("解析告警详情失败: %v", err)
	}
	if len(detail.Data.Events) != 1 || detail.Data.Events[0].Kind != store.AlertEventFiring {
		t.Fatalf("详情应带 events: %+v", detail.Data.Events)
	}

	// 确认
	status, raw = env.request(http.MethodPost, "/api/v1/alerts/"+alert.ID+"/ack", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("确认告警失败: %d（body=%s）", status, raw)
	}
	var acked struct {
		Data alertPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &acked); err != nil {
		t.Fatalf("解析确认响应失败: %v", err)
	}
	if acked.Data.Status != store.AlertStatusAcked || acked.Data.AckedAt == nil || acked.Data.AckedBy == nil {
		t.Fatalf("确认结果不正确: %+v", acked.Data)
	}
	// 确认事件落库
	events, err := env.st.ListAlertEvents(ctx, alert.ID, 10)
	if err != nil {
		t.Fatalf("查询事件失败: %v", err)
	}
	foundAck := false
	for _, ev := range events {
		if ev.Kind == store.AlertEventAcked {
			foundAck = true
		}
	}
	if !foundAck {
		t.Fatalf("确认后应写入 acked 事件: %+v", events)
	}

	// summary
	status, raw = env.request(http.MethodGet, "/api/v1/alerts/summary", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询概览失败: %d", status)
	}
	var summary struct {
		Data store.AlertSummary `json:"data"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("解析概览失败: %v", err)
	}
	if summary.Data.Acked != 1 || summary.Data.BySeverity["critical"] != 1 {
		t.Fatalf("概览不正确: %+v", summary.Data)
	}

	// 不存在的告警 → 404
	status, _ = env.request(http.MethodGet, "/api/v1/alerts/ghost", jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("不存在的告警期望 404，实际 %d", status)
	}
	status, _ = env.request(http.MethodPost, "/api/v1/alerts/ghost/ack", jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("确认不存在的告警期望 404，实际 %d", status)
	}
}

// TestAlertNotifierChannelIsolation 覆盖重点 3：多渠道互不影响，失败原因写回渠道与告警事件。
func TestAlertNotifierChannelIsolation(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	ctx := context.Background()

	good := env.createChannelVia(jwt, map[string]any{
		"name": "可用渠道", "kind": "webhook", "config": map[string]any{"url": "http://127.0.0.1:9/ok"},
	})
	bad := env.createChannelVia(jwt, map[string]any{
		"name": "故障渠道", "kind": "webhook", "config": map[string]any{"url": "http://127.0.0.1:9/bad"},
	})
	disabled := env.createChannelVia(jwt, map[string]any{
		"name": "禁用渠道", "kind": "webhook", "enabled": false,
		"config": map[string]any{"url": "http://127.0.0.1:9/disabled"},
	})

	env.notif.mu.Lock()
	env.notif.failOn = map[string]string{bad.ID: "HTTP 非 2xx: 500 body=boom"}
	env.notif.mu.Unlock()

	notifier := NewAlertNotifier(env.st, env.cipher, env.notif, env.api.logger)
	if notifier == nil {
		t.Fatal("构造通知器失败")
	}

	alert := alerting.Alert{
		ID: "alert-iso", RuleID: "rule-1", DedupKey: "check:c:s",
		Status: alerting.StatusFiring, Severity: "critical",
		Title: "检测项异常", Message: "端口不可达", StartedAt: store.NowMS(),
	}
	if err := env.st.CreateAlert(ctx, store.Alert{
		ID: alert.ID, RuleID: alert.RuleID, DedupKey: alert.DedupKey,
		Status: alert.Status, Severity: string(alert.Severity), Title: alert.Title,
		StartedAt: alert.StartedAt, UpdatedAt: alert.StartedAt,
	}); err != nil {
		t.Fatalf("写入告警失败: %v", err)
	}

	// 故意把「故障渠道」放在最前面：它失败后不能阻断后面的渠道。
	notifier.Notify(ctx, alert, []string{bad.ID, good.ID, disabled.ID}, false)

	sent := env.notif.sentTitles()
	if len(sent) != 2 {
		t.Fatalf("应只向 2 个启用渠道发送（禁用渠道跳过），实际 %d: %v", len(sent), sent)
	}
	// 失败渠道的记录
	badStored, _ := env.st.GetAlertChannel(ctx, bad.ID)
	if badStored.LastError == nil || !strings.Contains(*badStored.LastError, "500") {
		t.Fatalf("失败渠道应写入 last_error，实际 %v", badStored.LastError)
	}
	if badStored.LastOKAt != nil {
		t.Fatalf("失败渠道不应写 last_ok_at: %v", badStored.LastOKAt)
	}
	// 成功渠道的记录（在失败渠道之后仍然被调用）
	goodStored, _ := env.st.GetAlertChannel(ctx, good.ID)
	if goodStored.LastOKAt == nil {
		t.Fatal("成功渠道应写入 last_ok_at（说明失败渠道没有阻断后续）")
	}
	if goodStored.LastError != nil {
		t.Fatalf("成功渠道不应有 last_error: %v", goodStored.LastError)
	}
	// 禁用渠道：既不发送，也不写结果
	disabledStored, _ := env.st.GetAlertChannel(ctx, disabled.ID)
	if disabledStored.LastOKAt != nil || disabledStored.LastError != nil {
		t.Fatalf("禁用渠道不应写发送结果: %+v", disabledStored)
	}

	// 告警事件：每个渠道一条 notify 记录，含成功/失败与原因
	events, err := env.st.ListAlertEvents(ctx, alert.ID, 20)
	if err != nil {
		t.Fatalf("查询事件失败: %v", err)
	}
	var success, failure, skipped int
	for _, ev := range events {
		if ev.Kind != store.AlertEventNotify || ev.Detail == nil {
			continue
		}
		switch {
		case strings.Contains(*ev.Detail, "发送成功"):
			success++
		case strings.Contains(*ev.Detail, "发送失败"):
			failure++
			if !strings.Contains(*ev.Detail, "500") {
				t.Fatalf("失败事件应含原因: %q", *ev.Detail)
			}
		case strings.Contains(*ev.Detail, "已禁用"):
			skipped++
		}
	}
	if success != 1 || failure != 1 || skipped != 1 {
		t.Fatalf("通知事件不正确（成功 %d 失败 %d 跳过 %d）: %+v", success, failure, skipped, events)
	}

	// 恢复通知：标题带 [已恢复] 标记
	notifier.Notify(ctx, alert, []string{good.ID}, true)
	titles := env.notif.sentTitles()
	if len(titles) != 3 || !strings.Contains(titles[len(titles)-1], "[已恢复]") {
		t.Fatalf("恢复通知标题应带标记: %v", titles)
	}
}

// TestAlertEventBridge 覆盖三条接线：检测翻转、服务器上下线、指标评估。
func TestAlertEventBridge(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	srv := env.createServerVia(jwt, map[string]any{
		"name": "bridge-srv", "host": "10.22.0.1", "group": "prod", "tags": []string{"web"},
	})
	check := env.createCheckVia(jwt, map[string]any{
		"name": "bridge-check", "kind": "tcp", "target": "127.0.0.1:22",
		"interval_s": 10, "server_ids": []string{srv.ID},
	})

	bridge := NewAlertEventBridge(env.st, env.alerts, env.api.logger)

	// 1) 检测翻转 → 事件（异常）
	bridge.OnCheckStateChange(check.ID, srv.ID, false, store.NowMS())
	// 2) 检测恢复 → 事件
	bridge.OnCheckStateChange(check.ID, srv.ID, true, store.NowMS())

	events, _, _ := env.alerts.snapshot()
	if len(events) != 2 {
		t.Fatalf("检测翻转应产生 2 个事件，实际 %d", len(events))
	}
	if events[0].Source != alerting.SourceCheck || !events[0].Active {
		t.Fatalf("异常事件不正确: %+v", events[0])
	}
	if !events[1].Active && events[1].Source == alerting.SourceCheck {
		// 恢复事件 Active=false
	} else {
		t.Fatalf("恢复事件不正确: %+v", events[1])
	}
	if events[0].ServerName != "bridge-srv" || events[0].CheckName != "bridge-check" {
		t.Fatalf("事件应带可读名称: %+v", events[0])
	}
	if events[0].Group != "prod" || len(events[0].Tags) != 1 || events[0].Tags[0] != "web" {
		t.Fatalf("事件应带分组与标签: %+v", events[0])
	}

	// 3) 服务器上下线：先置为在线建立基线（首轮不产生事件），再失联 → 离线事件
	if _, err := env.st.DB().ExecContext(context.Background(),
		`UPDATE servers SET status = 'online', last_seen_at = ? WHERE id = ?`, store.NowMS(), srv.ID); err != nil {
		t.Fatalf("构造在线状态失败: %v", err)
	}
	before := len(events)
	bridge.PollServerStatuses(context.Background())
	_, _, _ = env.alerts.snapshot()
	if _, err := env.st.DB().ExecContext(context.Background(),
		`UPDATE servers SET status = 'online', last_seen_at = ? WHERE id = ?`, store.NowMS()-120000, srv.ID); err != nil {
		t.Fatalf("构造离线状态失败: %v", err)
	}
	bridge.PollServerStatuses(context.Background())
	events, _, _ = env.alerts.snapshot()
	serverEvents := 0
	for _, ev := range events {
		if ev.Source == alerting.SourceServer {
			serverEvents++
			if !ev.Active {
				t.Fatalf("离线事件应为 Active=true: %+v", ev)
			}
			if ev.ServerName != "bridge-srv" {
				t.Fatalf("服务器事件应带名称: %+v", ev)
			}
		}
	}
	if serverEvents != 1 {
		t.Fatalf("离线应产生 1 个服务器事件，实际 %d（基线前事件 %d，总事件 %d）", serverEvents, before, len(events))
	}

	// 3b) 恢复在线 → Active=false 事件
	if _, err := env.st.DB().ExecContext(context.Background(),
		`UPDATE servers SET status = 'online', last_seen_at = ? WHERE id = ?`, store.NowMS(), srv.ID); err != nil {
		t.Fatalf("构造恢复状态失败: %v", err)
	}
	bridge.PollServerStatuses(context.Background())
	events, _, _ = env.alerts.snapshot()
	recoveredEvents := 0
	for _, ev := range events {
		if ev.Source == alerting.SourceServer && !ev.Active {
			recoveredEvents++
		}
	}
	if recoveredEvents != 1 {
		t.Fatalf("恢复在线应产生 1 个恢复事件，实际 %d", recoveredEvents)
	}

	// 4) 指标评估：把最新指标快照喂给引擎
	now := store.NowMS()
	if _, err := env.st.InsertSamples(context.Background(), srv.ID, []store.MetricSample{
		{Metric: "cpu.pct", TS: now, Value: 95.5},
		{Metric: "mem.used_pct", TS: now, Value: 42},
	}); err != nil {
		t.Fatalf("写入指标失败: %v", err)
	}
	bridge.EvaluateMetrics(context.Background())
	_, samples, _ := env.alerts.snapshot()
	foundCPU := false
	for _, smp := range samples {
		if smp.ServerID == srv.ID && smp.Metric == "cpu.pct" && smp.Value == 95.5 {
			foundCPU = true
			if smp.ServerName != "bridge-srv" || smp.Group != "prod" {
				t.Fatalf("指标样本应带服务器元信息: %+v", smp)
			}
		}
	}
	if !foundCPU {
		t.Fatalf("指标评估应包含 cpu.pct=95.5，实际 %+v", samples)
	}
}

// TestAlertRuleSilenceStopsNotifyPath 校验静默字段可被读回（引擎据此不发通知）。
func TestAlertRuleSilenceStopsNotifyPath(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	rule := env.createRuleVia(jwt, map[string]any{
		"name": "静默规则", "source": "server", "severity": "warning",
	})
	if rule.SilenceUntil != nil {
		t.Fatalf("新建规则不应带静默: %v", rule.SilenceUntil)
	}
	status, raw := env.request(http.MethodPost, "/api/v1/alert-rules/"+rule.ID+"/silence", jwt,
		map[string]any{"duration_s": 600})
	if status != http.StatusOK {
		t.Fatalf("静默失败: %d（body=%s）", status, raw)
	}
	// 引擎 Reload 后应能读到静默（此处直接读接口）
	after := env.getRule(jwt, rule.ID)
	if after.SilenceUntil == nil || *after.SilenceUntil <= time.Now().UnixMilli() {
		t.Fatalf("静默未持久化: %+v", after.SilenceUntil)
	}
}

// getRule 读取规则详情。
func (e *testEnv) getRule(jwt, id string) store.AlertRule {
	e.t.Helper()
	status, raw := e.request(http.MethodGet, "/api/v1/alert-rules/"+id, jwt, nil)
	if status != http.StatusOK {
		e.t.Fatalf("查询规则失败: status=%d body=%s", status, raw)
	}
	var out struct {
		Data store.AlertRule `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("解析规则失败: %v", err)
	}
	return out.Data
}

// TestEvaluateMetricsRunsWithoutServers 覆盖 lead 指出的关键点：
// 即使没有任何服务器/样本，周期评估也必须调用 EvaluateMetrics ——
// 引擎在其中推进 for_duration 到期的 pending 规则，提前返回会让这类告警永不触发。
func TestEvaluateMetricsRunsWithoutServers(t *testing.T) {
	env := newTestEnv(t)
	bridge := NewAlertEventBridge(env.st, env.alerts, env.api.logger)

	bridge.EvaluateMetrics(context.Background())
	if _, samples, _ := env.alerts.snapshot(); len(samples) != 0 {
		t.Fatalf("无服务器时不应产生样本，实际 %d", len(samples))
	}
	// 关键断言：引擎仍被调用了一次（count 通过 samples 切片无法体现，用专门计数）
	if env.alerts.evaluateCalls() == 0 {
		t.Fatal("无服务器时也必须调用 EvaluateMetrics（否则 for_duration 无法推进）")
	}

	// 有服务器但没有指标时同样要调用
	env.createServerVia(env.login(), map[string]any{"name": "empty", "host": "10.30.0.1"})
	before := env.alerts.evaluateCalls()
	bridge.EvaluateMetrics(context.Background())
	if env.alerts.evaluateCalls() != before+1 {
		t.Fatal("有服务器但无指标时也必须调用 EvaluateMetrics")
	}
}

// TestChannelURLMasking 校验凭证型 URL 也会被脱敏（钉钉/企微/飞书的整条 webhook 就是密钥）。
func TestChannelURLMasking(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	const rawToken = "SECRETTOKEN1234567890abcdef"
	channel := env.createChannelVia(jwt, map[string]any{
		"name": "钉钉群", "kind": "dingtalk",
		"config": map[string]any{
			"webhook": "https://oapi.dingtalk.com/robot/send?access_token=" + rawToken,
			"secret":  "SECabcdefghijklmnop",
		},
	})
	if channel.ID == "" {
		t.Fatal("渠道未创建")
	}

	status, raw := env.request(http.MethodGet, "/api/v1/alert-channels", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询渠道失败: %d", status)
	}
	body := string(raw)
	if strings.Contains(body, rawToken) {
		t.Fatalf("响应泄露了 URL 中的 access_token: %s", body)
	}
	if strings.Contains(body, "SECabcdefghijklmnop") {
		t.Fatalf("响应泄露了 secret 明文: %s", body)
	}
	if !strings.Contains(body, "***") {
		t.Fatalf("响应应包含脱敏标记: %s", body)
	}
	// 脱敏后仍能看出「已配置」（URL 主体保留，尾部凭证被抹掉）
	if !strings.Contains(body, "oapi.dingtalk.com") {
		t.Fatalf("脱敏不应抹掉主机名，否则前端无法展示: %s", body)
	}
}

// TestAlertValueZeroRoundTrip 校验 value=0 不会被当成「未提供」（NULL）。
func TestAlertValueZeroRoundTrip(t *testing.T) {
	// 适配器层：引擎的 0 值必须原样落库
	converted := fromAlertingAlert(alerting.Alert{
		ID: "a1", RuleID: "r1", DedupKey: "metric:r1:s1",
		Status: alerting.StatusFiring, Severity: "warning", Title: "t",
		Value: 0,
	})
	if converted.Value == nil {
		t.Fatal("value=0 必须落库为 0，而不是 NULL")
	}
	if *converted.Value != 0 {
		t.Fatalf("value 应为 0，实际 %v", *converted.Value)
	}
	// 反向：读回时也保持 0
	back := toAlertingAlert(converted)
	if back.Value != 0 {
		t.Fatalf("往返后 value 应为 0，实际 %v", back.Value)
	}

	// 接口层：value=0 必须序列化出来
	env := newTestEnv(t)
	jwt := env.login()
	val := 0.0
	alert := store.Alert{
		ID: "alert-zero", RuleID: "rule-z", DedupKey: "metric:rule-z:srv-z",
		Status: store.AlertStatusFiring, Severity: "warning", Title: "阈值为 0",
		Value: &val, StartedAt: store.NowMS(), UpdatedAt: store.NowMS(),
	}
	if err := env.st.CreateAlert(context.Background(), alert); err != nil {
		t.Fatalf("写入告警失败: %v", err)
	}
	status, raw := env.request(http.MethodGet, "/api/v1/alerts/"+alert.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询告警失败: %d", status)
	}
	if !strings.Contains(string(raw), `"value":0`) {
		t.Fatalf("value=0 应出现在响应中: %s", raw)
	}
}

// TestAlertEventKindsMatchStore 校验事件 kind 与 store 常量一致（前端按 kind 显示中文）。
func TestAlertEventKindsMatchStore(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	ctx := context.Background()

	alert := store.Alert{
		ID: "alert-kind", RuleID: "rule-k", DedupKey: "server:srv-k",
		Status: store.AlertStatusFiring, Severity: "critical", Title: "t",
		StartedAt: store.NowMS(), UpdatedAt: store.NowMS(),
	}
	if err := env.st.CreateAlert(ctx, alert); err != nil {
		t.Fatalf("写入告警失败: %v", err)
	}
	for _, kind := range []string{
		store.AlertEventFiring, store.AlertEventResolved, store.AlertEventNotify,
	} {
		if err := env.st.AppendAlertEvent(ctx, alert.ID, kind, store.NowMS(), "d"); err != nil {
			t.Fatalf("写入 %s 事件失败: %v", kind, err)
		}
	}
	if store.AlertEventFiring != "firing" || store.AlertEventResolved != "resolved" || store.AlertEventNotify != "notify" {
		t.Fatalf("事件 kind 常量与引擎不一致: %q %q %q",
			store.AlertEventFiring, store.AlertEventResolved, store.AlertEventNotify)
	}

	status, raw := env.request(http.MethodGet, "/api/v1/alerts/"+alert.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询告警失败: %d", status)
	}
	for _, kind := range []string{`"kind":"firing"`, `"kind":"resolved"`, `"kind":"notify"`} {
		if !strings.Contains(string(raw), kind) {
			t.Fatalf("事件轨迹缺少 %s: %s", kind, raw)
		}
	}
}

// alertSummaryPayload 是 GET /alerts/summary 的响应体。
type alertSummaryPayload struct {
	Firing      int            `json:"firing"`
	Acked       int            `json:"acked"`
	Resolved24h int            `json:"resolved_24h"`
	BySeverity  map[string]int `json:"by_severity"`
}

// getAlertSummary 查询告警概览。
func (e *testEnv) getAlertSummary(jwt string) alertSummaryPayload {
	e.t.Helper()
	status, raw := e.request(http.MethodGet, "/api/v1/alerts/summary", jwt, nil)
	if status != http.StatusOK {
		e.t.Fatalf("查询 alerts summary 失败: %d（body=%s）", status, raw)
	}
	var out struct {
		Data alertSummaryPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("解析 alerts summary 失败: %v（body=%s）", err, raw)
	}
	return out.Data
}

// countAlertsByStatus 返回 GET /alerts?status=X 的条数（与 summary 对账用）。
func (e *testEnv) countAlertsByStatus(jwt, status string) int {
	e.t.Helper()
	code, raw := e.request(http.MethodGet, "/api/v1/alerts?status="+status, jwt, nil)
	if code != http.StatusOK {
		e.t.Fatalf("查询告警列表(status=%s)失败: %d（body=%s）", status, code, raw)
	}
	var out struct {
		Data []alertPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("解析告警列表失败: %v（body=%s）", err, raw)
	}
	return len(out.Data)
}

// TestAlertSummaryScopedToVisibleAlerts 覆盖 A13/A14：受限角色的告警概览只统计范围内服务器，
// 规则级告警（server_id IS NULL）不计入；全局角色返回与改动前一致。
func TestAlertSummaryScopedToVisibleAlerts(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	admin := env.login()
	now := store.NowMS()

	srvIn := env.createServerVia(admin, map[string]any{"name": "al-sum-in", "host": "10.21.0.1"})
	srvOut := env.createServerVia(admin, map[string]any{"name": "al-sum-out", "host": "10.21.0.2"})
	groupID := env.createGroupVia(admin, "告警汇总组")
	env.setGroupMembers(admin, groupID, []string{srvIn.ID})

	roleKey := env.createRoleVia(admin, "告警只读", []string{"server.read", "alert.read"},
		map[string]any{"mode": "groups", "group_ids": []string{groupID}, "server_ids": []string{}})
	env.createUserVia(admin, "alviewer", "AlPass-123", roleKey)
	scoped := env.loginAs(t, "alviewer", "AlPass-123")

	seed := func(id, status, severity string, serverID *string, resolvedAt *int64) {
		t.Helper()
		if err := env.st.CreateAlert(ctx, store.Alert{
			ID: id, RuleID: "rule-summary", DedupKey: "k-" + id, ServerID: serverID,
			Status: status, Severity: severity, Title: "告警 " + id,
			StartedAt: now, UpdatedAt: now, ResolvedAt: resolvedAt,
		}); err != nil {
			t.Fatalf("写入告警 %s 失败: %v", id, err)
		}
	}
	recent := now - 3600*1000
	old := now - 48*3600*1000
	seed("al-in-firing", store.AlertStatusFiring, "critical", &srvIn.ID, nil)
	seed("al-in-acked", store.AlertStatusAcked, "warning", &srvIn.ID, nil)
	seed("al-in-resolved", store.AlertStatusResolved, "info", &srvIn.ID, &recent)
	seed("al-out-firing", store.AlertStatusFiring, "critical", &srvOut.ID, nil)
	seed("al-out-resolved-old", store.AlertStatusResolved, "warning", &srvOut.ID, &old)
	seed("al-rule-firing", store.AlertStatusFiring, "info", nil, nil)

	// 全局角色：与改动前完全一致（A14）。
	adminSummary := env.getAlertSummary(admin)
	if adminSummary.Firing != 3 || adminSummary.Acked != 1 || adminSummary.Resolved24h != 1 {
		t.Fatalf("全局角色概览不正确: %+v", adminSummary)
	}
	if adminSummary.BySeverity["critical"] != 2 || adminSummary.BySeverity["warning"] != 1 ||
		adminSummary.BySeverity["info"] != 1 {
		t.Fatalf("全局角色严重级别分布不正确: %+v", adminSummary.BySeverity)
	}
	if adminSummary.Firing != env.countAlertsByStatus(admin, store.AlertStatusFiring) {
		t.Fatalf("全局角色 firing 与列表条数不一致: %+v", adminSummary)
	}
	if adminSummary.Acked != env.countAlertsByStatus(admin, store.AlertStatusAcked) {
		t.Fatalf("全局角色 acked 与列表条数不一致: %+v", adminSummary)
	}

	// 受限角色：范围外 firing 与规则级告警都不计入（A13）。
	scopedSummary := env.getAlertSummary(scoped)
	if scopedSummary.Firing != 1 || scopedSummary.Acked != 1 || scopedSummary.Resolved24h != 1 {
		t.Fatalf("受限角色概览应只覆盖范围内告警: %+v", scopedSummary)
	}
	if scopedSummary.BySeverity["critical"] != 1 || scopedSummary.BySeverity["warning"] != 1 ||
		scopedSummary.BySeverity["info"] != 0 {
		t.Fatalf("受限角色严重级别分布不正确: %+v", scopedSummary.BySeverity)
	}
	if scopedSummary.Firing != env.countAlertsByStatus(scoped, store.AlertStatusFiring) {
		t.Fatalf("受限角色 firing 与列表条数不一致: %+v", scopedSummary)
	}
	if scopedSummary.Acked != env.countAlertsByStatus(scoped, store.AlertStatusAcked) {
		t.Fatalf("受限角色 acked 与列表条数不一致: %+v", scopedSummary)
	}

	// 范围内一台服务器都没有 → 200 且全 0（by_severity 三个键都在）。
	emptyGroup := env.createGroupVia(admin, "告警空范围组")
	emptyRole := env.createRoleVia(admin, "告警空范围只读", []string{"server.read", "alert.read"},
		map[string]any{"mode": "groups", "group_ids": []string{emptyGroup}, "server_ids": []string{}})
	env.createUserVia(admin, "alempty", "AlEmpty-123", emptyRole)
	emptyJWT := env.loginAs(t, "alempty", "AlEmpty-123")

	emptySummary := env.getAlertSummary(emptyJWT)
	if emptySummary.Firing != 0 || emptySummary.Acked != 0 || emptySummary.Resolved24h != 0 {
		t.Fatalf("空范围概览应全 0: %+v", emptySummary)
	}
	for _, sev := range []string{"critical", "warning", "info"} {
		if v, ok := emptySummary.BySeverity[sev]; !ok || v != 0 {
			t.Fatalf("空范围 by_severity[%s] 期望 0 且存在，实际 %+v", sev, emptySummary.BySeverity)
		}
	}
	if n := env.countAlertsByStatus(emptyJWT, store.AlertStatusFiring); n != 0 {
		t.Fatalf("空范围 firing 列表应为空，实际 %d", n)
	}
}
