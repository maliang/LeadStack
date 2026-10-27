package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/maliang/leadstack/internal/collector"
	"github.com/maliang/leadstack/internal/store"
	"github.com/maliang/leadstack/pkg/protocol"
)

// createServerVia 通过 REST 创建服务器并返回其 ID。
func (e *testEnv) createServerVia(jwt string, body map[string]any) serverPayload {
	e.t.Helper()
	status, raw := e.request(http.MethodPost, "/api/v1/servers", jwt, body)
	if status != http.StatusCreated {
		e.t.Fatalf("创建服务器失败: status=%d body=%s", status, raw)
	}
	var out struct {
		Data serverPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("解析服务器响应失败: %v", err)
	}
	return out.Data
}

// createCredentialVia 通过 REST 创建凭证并返回其 ID。
func (e *testEnv) createCredentialVia(jwt string, body map[string]any) map[string]any {
	e.t.Helper()
	status, raw := e.request(http.MethodPost, "/api/v1/credentials", jwt, body)
	if status != http.StatusCreated {
		e.t.Fatalf("创建凭证失败: status=%d body=%s", status, raw)
	}
	var out struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("解析凭证响应失败: %v", err)
	}
	return out.Data
}

// TestCredentialsLifecycleAndNoLeak 覆盖验收 2/9：凭证明文绝不外泄，被引用删除返回 409。
func TestCredentialsLifecycleAndNoLeak(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	// 未带 token → 401
	status, _ := env.request(http.MethodGet, "/api/v1/credentials", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("未带 token 访问 /credentials 期望 401，实际 %d", status)
	}

	// 非法不匹配的组合应被拒绝（kind=key 却没有私钥）
	status, raw := env.request(http.MethodPost, "/api/v1/credentials", jwt,
		map[string]any{"name": "bad", "kind": "key", "username": "u"})
	if status != http.StatusBadRequest {
		t.Fatalf("kind=key 且无私钥期望 400，实际 %d（body=%s）", status, raw)
	}

	// 合法私钥（用 ssh-keygen 生成的真实 ed25519 私钥）
	realKey := generateTestPrivateKey(t)
	created := env.createCredentialVia(jwt, map[string]any{
		"name": "prod-key", "kind": "key", "username": "root", "private_key": realKey,
	})
	if created["id"] == nil || created["id"] == "" {
		t.Fatalf("创建凭证未返回 id: %+v", created)
	}
	credID, _ := created["id"].(string)
	if fp, _ := created["fingerprint"].(string); !strings.HasPrefix(fp, "SHA256:") {
		t.Fatalf("私钥凭证应返回 SHA256 指纹，实际 %v", created["fingerprint"])
	}

	// 密码凭证
	pwCreated := env.createCredentialVia(jwt, map[string]any{
		"name": "prod-pw", "kind": "password", "username": "root", "password": "SuperSecretPw123",
	})
	pwID, _ := pwCreated["id"].(string)

	// 列表接口：不得出现任何密钥/密码明文
	status, body := env.request(http.MethodGet, "/api/v1/credentials", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询凭证列表失败: status=%d body=%s", status, body)
	}
	bodyText := string(body)
	for _, leak := range []string{"SUPERSECRETKEYMATERIAL", "SuperSecretPw123", "BEGIN OPENSSH PRIVATE KEY", "private_key", "secret_enc"} {
		if strings.Contains(bodyText, leak) {
			t.Fatalf("凭证列表泄露了敏感内容 %q: %s", leak, bodyText)
		}
	}
	// 同时确认私钥原文（截取片段）不出现
	if strings.Contains(bodyText, strings.SplitN(realKey, "\n", 2)[0]) {
		t.Fatalf("凭证列表泄露了私钥首行: %s", bodyText)
	}

	var list struct {
		Data []credentialPayload `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("解析凭证列表失败: %v", err)
	}
	if len(list.Data) != 2 {
		t.Fatalf("期望 2 条凭证，实际 %d", len(list.Data))
	}

	// 引用保护：绑定到服务器后删除返回 409
	srv := env.createServerVia(jwt, map[string]any{
		"name": "ssh-node", "host": "127.0.0.1", "port": 22, "mode": "ssh", "credential_id": credID,
	})
	status, raw = env.request(http.MethodDelete, "/api/v1/credentials/"+credID, jwt, nil)
	if status != http.StatusConflict {
		t.Fatalf("删除被引用凭证期望 409，实际 %d（body=%s）", status, raw)
	}
	if !strings.Contains(string(raw), `"code":"conflict"`) {
		t.Fatalf("409 响应体不符合契约: %s", raw)
	}

	// 解绑后可删除
	status, raw = env.request(http.MethodPatch, "/api/v1/servers/"+srv.ID, jwt,
		map[string]any{"credential_id": ""})
	if status != http.StatusOK {
		t.Fatalf("解绑凭证失败: status=%d body=%s", status, raw)
	}
	status, raw = env.request(http.MethodDelete, "/api/v1/credentials/"+credID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("解绑后删除凭证失败: status=%d body=%s", status, raw)
	}

	// 未被引用的密码凭证可直接删除
	status, _ = env.request(http.MethodDelete, "/api/v1/credentials/"+pwID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("删除未被引用凭证失败: status=%d", status)
	}
	status, _ = env.request(http.MethodDelete, "/api/v1/credentials/"+pwID, jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("重复删除期望 404，实际 %d", status)
	}
}

// TestServerFiltersAndGroups 覆盖验收 8/10：过滤与分组聚合。
func TestServerFiltersAndGroups(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	env.createServerVia(jwt, map[string]any{"name": "a", "host": "10.0.0.1", "mode": "agent", "group": "prod", "tags": []string{"web", "bj"}})
	env.createServerVia(jwt, map[string]any{"name": "b", "host": "10.0.0.2", "mode": "ssh", "group": "prod", "tags": []string{"db"}})
	env.createServerVia(jwt, map[string]any{"name": "c", "host": "10.0.0.3", "mode": "ssh", "group": "stage"})

	cases := []struct {
		query string
		want  int
	}{
		{"", 3},
		{"?group=prod", 2},
		{"?group=stage", 1},
		{"?mode=ssh", 2},
		{"?mode=agent", 1},
		{"?tag=web", 1},
		{"?tag=db", 1},
		{"?tag=missing", 0},
		{"?status=unknown", 3},
		{"?group=prod&mode=ssh", 1},
	}
	for _, tc := range cases {
		status, body := env.request(http.MethodGet, "/api/v1/servers"+tc.query, jwt, nil)
		if status != http.StatusOK {
			t.Fatalf("查询 %s 失败: status=%d body=%s", tc.query, status, body)
		}
		var out struct {
			Data []serverPayload `json:"data"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("解析 %s 响应失败: %v", tc.query, err)
		}
		if len(out.Data) != tc.want {
			t.Fatalf("过滤 %s 期望 %d 台，实际 %d", tc.query, tc.want, len(out.Data))
		}
	}

	// 非法过滤值 → 400
	for _, q := range []string{"?status=bogus", "?mode=bogus"} {
		status, _ := env.request(http.MethodGet, "/api/v1/servers"+q, jwt, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("非法过滤 %s 期望 400，实际 %d", q, status)
		}
	}

	// 分组聚合
	status, body := env.request(http.MethodGet, "/api/v1/groups", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询分组失败: status=%d body=%s", status, body)
	}
	var groups struct {
		Data []store.GroupCount `json:"data"`
	}
	if err := json.Unmarshal(body, &groups); err != nil {
		t.Fatalf("解析分组响应失败: %v", err)
	}
	if len(groups.Data) != 2 {
		t.Fatalf("期望 2 个分组，实际 %+v", groups.Data)
	}
	if groups.Data[0].Name != "prod" || groups.Data[0].Count != 2 {
		t.Fatalf("第一个分组期望 prod/2，实际 %+v", groups.Data[0])
	}
}

// TestBatchOperations 覆盖验收 7：批量操作逐台返回结果与汇总。
func TestBatchOperations(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	first := env.createServerVia(jwt, map[string]any{"name": "b1", "host": "10.0.0.11", "mode": "ssh"})
	second := env.createServerVia(jwt, map[string]any{"name": "b2", "host": "10.0.0.12", "mode": "ssh"})

	// add-tags
	status, body := env.request(http.MethodPost, "/api/v1/servers/batch", jwt, map[string]any{
		"ids": []string{first.ID, second.ID}, "action": "add-tags",
		"params": map[string]any{"tags": []string{"batch", "e2e"}},
	})
	if status != http.StatusOK {
		t.Fatalf("批量 add-tags 失败: status=%d body=%s", status, body)
	}
	tagged := decodeBatch(t, body)
	if len(tagged.Results) != 2 || tagged.Summary.OK != 2 || tagged.Summary.Failed != 0 {
		t.Fatalf("批量 add-tags 结果异常: %+v", tagged)
	}
	if !tagged.Results[0].OK || !tagged.Results[1].OK {
		t.Fatalf("逐台结果应全部成功: %+v", tagged.Results)
	}

	// 确认标签真的写进去了
	status, body = env.request(http.MethodGet, "/api/v1/servers?tag=batch", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("按标签查询失败: %d", status)
	}
	var taggedList struct {
		Data []serverPayload `json:"data"`
	}
	if err := json.Unmarshal(body, &taggedList); err != nil {
		t.Fatalf("解析列表失败: %v", err)
	}
	if len(taggedList.Data) != 2 {
		t.Fatalf("tag=batch 期望 2 台，实际 %d", len(taggedList.Data))
	}

	// set-group
	status, body = env.request(http.MethodPost, "/api/v1/servers/batch", jwt, map[string]any{
		"ids": []string{first.ID, second.ID}, "action": "set-group",
		"params": map[string]any{"group": "batch-group"},
	})
	if status != http.StatusOK {
		t.Fatalf("批量 set-group 失败: status=%d body=%s", status, body)
	}
	grouped := decodeBatch(t, body)
	if grouped.Summary.OK != 2 {
		t.Fatalf("批量 set-group 期望成功 2 台，实际 %+v", grouped.Summary)
	}

	// test-connection（走 SSH 替身）
	status, body = env.request(http.MethodPost, "/api/v1/servers/batch", jwt, map[string]any{
		"ids": []string{first.ID, second.ID}, "action": "test-connection",
	})
	if status != http.StatusOK {
		t.Fatalf("批量 test-connection 失败: status=%d body=%s", status, body)
	}
	tested := decodeBatch(t, body)
	if tested.Summary.OK != 2 || len(tested.Results) != 2 {
		t.Fatalf("批量 test-connection 结果异常: %+v", tested)
	}

	// 部分失败：混入一个不存在的 ID
	status, body = env.request(http.MethodPost, "/api/v1/servers/batch", jwt, map[string]any{
		"ids": []string{first.ID, "not-exist"}, "action": "test-connection",
	})
	if status != http.StatusOK {
		t.Fatalf("批量操作整体应返回 200: %d", status)
	}
	partial := decodeBatch(t, body)
	if partial.Summary.OK != 1 || partial.Summary.Failed != 1 {
		t.Fatalf("部分失败期望 1 成功 1 失败，实际 %+v", partial.Summary)
	}
	if partial.Results[1].Error == "" {
		t.Fatalf("失败项应带 error: %+v", partial.Results[1])
	}

	// 非法 action / 空 ids
	status, _ = env.request(http.MethodPost, "/api/v1/servers/batch", jwt,
		map[string]any{"ids": []string{first.ID}, "action": "explode"})
	if status != http.StatusBadRequest {
		t.Fatalf("非法 action 期望 400，实际 %d", status)
	}
	status, _ = env.request(http.MethodPost, "/api/v1/servers/batch", jwt,
		map[string]any{"ids": []string{}, "action": "add-tags", "params": map[string]any{"tags": []string{"x"}}})
	if status != http.StatusBadRequest {
		t.Fatalf("空 ids 期望 400，实际 %d", status)
	}

	// 批量删除
	status, body = env.request(http.MethodPost, "/api/v1/servers/batch", jwt,
		map[string]any{"ids": []string{first.ID, second.ID}, "action": "delete"})
	if status != http.StatusOK {
		t.Fatalf("批量删除失败: status=%d body=%s", status, body)
	}
	deleted := decodeBatch(t, body)
	if deleted.Summary.OK != 2 {
		t.Fatalf("批量删除期望成功 2 台，实际 %+v", deleted.Summary)
	}
	status, _ = env.request(http.MethodGet, "/api/v1/servers", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("删除后查询列表失败: %d", status)
	}
}

// TestSSHEndpoints 覆盖验收 3/4：test-connection 与 refresh-host-info。
func TestSSHEndpoints(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	srv := env.createServerVia(jwt, map[string]any{"name": "ssh", "host": "127.0.0.1", "port": 2222, "mode": "ssh"})

	status, body := env.request(http.MethodPost, "/api/v1/servers/"+srv.ID+"/test-connection", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("test-connection 失败: status=%d body=%s", status, body)
	}
	var tc struct {
		Data collector.TestResult `json:"data"`
	}
	if err := json.Unmarshal(body, &tc); err != nil {
		t.Fatalf("解析 test-connection 响应失败: %v", err)
	}
	if !tc.Data.OK || tc.Data.SSHBanner == "" || tc.Data.HostKeyFingerprint == "" {
		t.Fatalf("test-connection 结果不完整: %+v", tc.Data)
	}

	// 失败时应返回 200 + ok=false + error
	env.ssh.testResult = collector.TestResult{OK: false, Error: "连接被拒绝"}
	status, body = env.request(http.MethodPost, "/api/v1/servers/"+srv.ID+"/test-connection", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("SSH 失败仍应返回 200，实际 %d（body=%s）", status, body)
	}
	var failed struct {
		Data collector.TestResult `json:"data"`
	}
	if err := json.Unmarshal(body, &failed); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if failed.Data.OK || failed.Data.Error == "" {
		t.Fatalf("失败结果应带 error: %+v", failed.Data)
	}

	// refresh-host-info：{data:{host_info:{...}}}
	status, body = env.request(http.MethodPost, "/api/v1/servers/"+srv.ID+"/refresh-host-info", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("refresh-host-info 失败: status=%d body=%s", status, body)
	}
	var hi struct {
		Data struct {
			HostInfo protocol.HostInfo `json:"host_info"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &hi); err != nil {
		t.Fatalf("解析 host_info 响应失败: %v", err)
	}
	if hi.Data.HostInfo.Hostname == "" || hi.Data.HostInfo.OS == "" {
		t.Fatalf("host_info 缺少关键字段: %+v", hi.Data.HostInfo)
	}

	// 服务器不存在 → 404
	status, _ = env.request(http.MethodPost, "/api/v1/servers/not-exist/test-connection", jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("不存在的服务器期望 404，实际 %d", status)
	}

	// SSH 采集失败 → 502
	env.ssh.hostErr = context.DeadlineExceeded
	status, _ = env.request(http.MethodPost, "/api/v1/servers/"+srv.ID+"/refresh-host-info", jwt, nil)
	if status != http.StatusBadGateway {
		t.Fatalf("SSH 采集失败期望 502，实际 %d", status)
	}
}

// TestMetricsHistoryAPI 覆盖验收 6：raw 与 1m 指标历史接口。
func TestMetricsHistoryAPI(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	srv := env.createServerVia(jwt, map[string]any{"name": "m1", "host": "10.0.0.21", "mode": "agent"})

	// 造 2 分钟前的原始采样
	base := (store.NowMS()/60000)*60000 - 120000
	samples := []store.MetricSample{
		{Metric: "cpu.pct", TS: base, Value: 10},
		{Metric: "cpu.pct", TS: base + 15000, Value: 20},
		{Metric: "mem.used_pct", TS: base, Value: 30},
	}
	if _, err := env.st.InsertSamples(context.Background(), srv.ID, samples); err != nil {
		t.Fatalf("写入采样失败: %v", err)
	}

	now := store.NowMS()
	from := base - 60000
	status, body := env.request(http.MethodGet,
		"/api/v1/servers/"+srv.ID+"/metrics?from="+itoa(from)+"&to="+itoa(now)+"&step=raw", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询 raw 指标失败: status=%d body=%s", status, body)
	}
	var raw struct {
		Data metricsResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("解析指标响应失败: %v", err)
	}
	if raw.Data.Step != store.RollupBucketRaw {
		t.Fatalf("step 期望 raw，实际 %q", raw.Data.Step)
	}
	if len(raw.Data.Series) != 2 {
		t.Fatalf("期望 2 条序列，实际 %d（%+v）", len(raw.Data.Series), raw.Data.Series)
	}
	if len(raw.Data.Series[0].Points) != 2 {
		t.Fatalf("cpu.pct 期望 2 个点，实际 %d", len(raw.Data.Series[0].Points))
	}

	// 降采样后 step=1m 能拿到汇总
	if _, err := env.st.RollupOnce(context.Background(), store.DefaultRollupLimit); err != nil {
		t.Fatalf("降采样失败: %v", err)
	}
	status, body = env.request(http.MethodGet,
		"/api/v1/servers/"+srv.ID+"/metrics?from="+itoa(from)+"&to="+itoa(now)+"&step=1m", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询 1m 指标失败: status=%d body=%s", status, body)
	}
	var minute struct {
		Data metricsResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &minute); err != nil {
		t.Fatalf("解析指标响应失败: %v", err)
	}
	if minute.Data.Step != store.RollupBucketMinute {
		t.Fatalf("step 期望 1m，实际 %q", minute.Data.Step)
	}
	if len(minute.Data.Series) == 0 {
		t.Fatalf("1m 汇总应有数据: %+v", minute.Data)
	}
	if got := minute.Data.Series[0].Points[0].Value; got != 15 {
		t.Fatalf("cpu.pct 1m 平均期望 15，实际 %v", got)
	}

	// 非法 step / 服务器不存在
	status, _ = env.request(http.MethodGet, "/api/v1/servers/"+srv.ID+"/metrics?step=5s", jwt, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("非法 step 期望 400，实际 %d", status)
	}
	status, _ = env.request(http.MethodGet, "/api/v1/servers/not-exist/metrics", jwt, nil)
	if status != http.StatusNotFound {
		t.Fatalf("不存在的服务器期望 404，实际 %d", status)
	}
}

// TestAgentHostInfoVisibleInList 覆盖验收 8：agent 上报 host_info 后列表可见。
func TestAgentHostInfoVisibleInList(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	token := env.createEnrollToken(jwt, "")
	conn, _, err := env.dialAgent(token)
	if err != nil {
		t.Fatalf("连接 agent WebSocket 失败: %v", err)
	}
	defer func() { _ = conn.Close() }()

	sendFrame(t, conn, protocol.TypeHello, protocol.Hello{Hostname: "hi-agent", OS: "linux", Arch: "amd64"})
	ack := readFrame(t, conn, 5*time.Second)
	if ack.Type != protocol.TypeHelloAck {
		t.Fatalf("期望 hello_ack，实际 %q", ack.Type)
	}

	sendFrame(t, conn, protocol.TypeHostInfo, protocol.HostInfo{
		Hostname: "hi-agent", OS: "linux", OSVersion: "22.04", Kernel: "5.15.0", Arch: "amd64",
		CPUModel: "Xeon", CPUCores: 8, MemTotalBytes: 16 << 30, DiskTotalBytes: 200 << 30,
		DiskUsedBytes: 80 << 30, UptimeS: 12345, Timezone: "Asia/Shanghai",
		IPAddrs: []string{"10.0.0.9"}, CollectedAt: store.NowMS(),
	})

	// 轮询条件必须看内容而不只是 HostInfo != nil：零值快照（Hostname 为空）说明这一帧没有真正落进去，
	// 要继续等到正确的那一帧。否则「空载荷覆盖了快照」这类缺陷会表现为随机失败（改了 poll 条件才稳定暴露）。
	var srv serverPayload
	var lastSeen []serverPayload
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		servers := env.listServers(jwt)
		lastSeen = servers
		if len(servers) == 1 && servers[0].HostInfo != nil {
			var probe protocol.HostInfo
			if err := json.Unmarshal(servers[0].HostInfo, &probe); err == nil && probe.Hostname != "" {
				srv = servers[0]
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if srv.HostInfo == nil {
		t.Fatalf("agent 上报后列表应带 host_info: %+v", lastSeen)
	}

	var info protocol.HostInfo
	if err := json.Unmarshal(srv.HostInfo, &info); err != nil {
		t.Fatalf("解析 host_info 失败: %v", err)
	}
	if info.Hostname != "hi-agent" || info.CPUCores != 8 || info.MemTotalBytes != 16<<30 {
		t.Fatalf("host_info 内容不正确: %+v", info)
	}

	// 详情接口同样可见
	status, body := env.request(http.MethodGet, "/api/v1/servers/"+srv.ID, jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("查询详情失败: %d（body=%s）", status, body)
	}
	var detail struct {
		Data serverPayload `json:"data"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("解析详情失败: %v", err)
	}
	if detail.Data.HostInfo == nil {
		t.Fatal("详情接口应带 host_info")
	}

	// 空载荷的 host_info 帧（agent 版本回退/序列化失败时的真实形态）不得把上面的快照清空。
	// 这条断言让「缺防空守卫」在走真实 WebSocket 链路时也必然稳定变红，而不是靠负载运气。
	sendFrame(t, conn, protocol.TypeHostInfo, protocol.HostInfo{})
	time.Sleep(200 * time.Millisecond)
	afterEmpty := env.listServers(jwt)
	if len(afterEmpty) != 1 || afterEmpty[0].HostInfo == nil {
		t.Fatalf("空帧后列表仍应带 host_info: %+v", afterEmpty)
	}
	var afterInfo protocol.HostInfo
	if err := json.Unmarshal(afterEmpty[0].HostInfo, &afterInfo); err != nil {
		t.Fatalf("空帧后解析 host_info 失败: %v", err)
	}
	if afterInfo.Hostname != "hi-agent" || afterInfo.CPUCores != 8 {
		t.Fatalf("空载荷的 host_info 覆盖了已有快照: %+v", afterInfo)
	}
}

// TestAgentHostInfoEmptyPayloadKeepsSnapshot 锁定「空 host_info 帧不得覆盖已有快照」。
//
// 现实形态：agent 因版本回退/序列化失败发来一帧没有载荷的 host_info，服务端把零值 struct
// 序列化后写库，页面上服务器的主机信息就变成一片空。这里直接调 onAgentHostInfo 走出真实落库路径。
func TestAgentHostInfoEmptyPayloadKeepsSnapshot(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	srv := env.createServerVia(jwt, map[string]any{"name": "hi-guard", "host": "10.10.8.1", "mode": "agent"})

	ctx := context.Background()
	ac := &agentConn{}
	ac.setServerID(srv.ID)
	tok := &store.EnrollToken{ID: "tok-host-info-guard"}

	// 1) 先落一份正常快照，后续空帧都不许改动它的字节与 collected_at。
	good := protocol.HostInfo{
		Hostname: "hi-guard", OS: "linux", Arch: "amd64", Kernel: "6.1.0",
		CPUCores: 8, MemTotalBytes: 16 << 30, CollectedAt: 1_700_000_000_000,
	}
	raw, err := json.Marshal(good)
	if err != nil {
		t.Fatalf("序列化正常快照失败: %v", err)
	}
	if err := env.api.onAgentHostInfo(ctx, ac, tok, "10.10.8.1", protocol.Envelope{Type: protocol.TypeHostInfo, Payload: raw}); err != nil {
		t.Fatalf("落正常快照失败: %v", err)
	}
	before, err := env.st.GetServerHostInfo(ctx, srv.ID)
	if err != nil {
		t.Fatalf("读取初次快照失败: %v", err)
	}
	if before.CollectedAt != good.CollectedAt {
		t.Fatalf("初次快照 collected_at 应为上报值 %d，实际 %d", good.CollectedAt, before.CollectedAt)
	}

	// 2) 空载荷、空对象、字段全空三种形态：一律静默忽略（返回 nil），且快照一字不变。
	cases := []struct {
		name    string
		payload []byte
	}{
		{"无载荷", nil},
		{"空对象", []byte("{}")},
		{"字段全空", []byte(`{"hostname":"","os":""}`)},
	}
	for _, c := range cases {
		if err := env.api.onAgentHostInfo(ctx, ac, tok, "10.10.8.1", protocol.Envelope{Type: protocol.TypeHostInfo, Payload: c.payload}); err != nil {
			t.Fatalf("%s：空帧应被静默忽略，实际返回错误 %v", c.name, err)
		}
		after, err := env.st.GetServerHostInfo(ctx, srv.ID)
		if err != nil {
			t.Fatalf("%s：读取快照失败: %v", c.name, err)
		}
		if after.InfoJSON != before.InfoJSON || after.CollectedAt != before.CollectedAt {
			t.Fatalf("%s：空帧覆盖了已有快照（before=%+v after=%+v）", c.name, before, after)
		}
	}

	// 3) 反向确认守卫没有把正常帧一起挡掉：换一份新快照必须能写进去。
	newer := good
	newer.Hostname = "hi-guard-2"
	newer.CollectedAt = 1_700_000_600_000
	raw2, err := json.Marshal(newer)
	if err != nil {
		t.Fatalf("序列化第二份快照失败: %v", err)
	}
	if err := env.api.onAgentHostInfo(ctx, ac, tok, "10.10.8.1", protocol.Envelope{Type: protocol.TypeHostInfo, Payload: raw2}); err != nil {
		t.Fatalf("第二次正常上报失败: %v", err)
	}
	final, err := env.st.GetServerHostInfo(ctx, srv.ID)
	if err != nil {
		t.Fatalf("读取最终快照失败: %v", err)
	}
	if final.CollectedAt != newer.CollectedAt || final.InfoJSON == before.InfoJSON {
		t.Fatalf("正常帧应覆盖快照（期望 collected_at=%d，实际 %+v）", newer.CollectedAt, final)
	}
}

// decodeBatch 解包 {"data":{results,summary}}。
func decodeBatch(t *testing.T, body []byte) batchResponse {
	t.Helper()
	var out struct {
		Data batchResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("解析批量响应失败: %v（body=%s）", err, body)
	}
	return out.Data
}

// itoa 把 int64 转成十进制字符串。
func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

// generateTestPrivateKey 生成一把真实可用的 ed25519 私钥（PEM 文本）。
func generateTestPrivateKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成测试私钥失败: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("编码测试私钥失败: %v", err)
	}
	return string(pem.EncodeToMemory(block))
}
