package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/maliang/leadstack/pkg/protocol"
)

// TestUpgradeResultFrameRouting 覆盖「agent 升级结果回报帧接入 hub_sender」的关键链路：
//   - run_id=agent.upgrade:<ver> 的 job.log/job.done 不得交给作业分发器（否则被当未知作业丢弃）；
//   - 成功回报要写 action=agent.upgrade 且 success=true 的审计，并更新 servers.agent_version；
//   - 失败回报与坏包要被拒绝，审计里带可定位的原因，且不得把版本号改成坏包的版本。
func TestUpgradeResultFrameRouting(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	token := env.createEnrollToken(jwt, "")
	conn, _, err := env.dialAgent(token)
	if err != nil {
		t.Fatalf("连接 agent WebSocket 失败: %v", err)
	}
	defer func() { _ = conn.Close() }()

	sendFrame(t, conn, protocol.TypeHello, protocol.Hello{
		Hostname: "upgrade-agent", OS: "linux", Arch: "amd64", AgentVersion: "0.1.0",
	})
	ack := readFrame(t, conn, 5*time.Second)
	if ack.Type != protocol.TypeHelloAck {
		t.Fatalf("期望 hello_ack，实际 %q", ack.Type)
	}
	srv := waitForServer(t, env, jwt, func(s serverPayload) bool {
		return s.Status == protocol.StatusOnline
	})

	const version = "9.9.9"
	upgradeRunID := upgradeRunPrefix + version
	sendFrame(t, conn, protocol.TypeJobLog, protocol.JobLog{
		RunID: upgradeRunID, Seq: 0, Stream: "stdout", Chunk: "upgrading\n",
	})
	sendFrame(t, conn, protocol.TypeJobDone, protocol.JobDone{RunID: upgradeRunID, ExitCode: 0})

	// 1) 成功回报：servers.agent_version 更新到回报版本。
	waitForServer(t, env, jwt, func(s serverPayload) bool {
		return s.AgentVersion != nil && *s.AgentVersion == version
	})

	// 2) 成功回报：审计 action=agent.upgrade、success=true。
	successDetail := waitUpgradeAudit(t, env, jwt, func(detail string) bool {
		return strings.Contains(detail, `"version":"`+version+`"`)
	})
	var successAudit struct {
		Version  string `json:"version"`
		Success  bool   `json:"success"`
		ExitCode int    `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(successDetail), &successAudit); err != nil {
		t.Fatalf("解析成功审计 detail 失败: %v（%s）", err, successDetail)
	}
	if !successAudit.Success || successAudit.Version != version || successAudit.ExitCode != 0 {
		t.Fatalf("成功升级审计内容不正确: %s", successDetail)
	}

	// 3) 升级回报帧不进入作业分发器（被静默丢弃的旧行为必须消失）。
	acks, logs, dones, _ := env.jobs.counts()
	if acks != 0 || logs != 0 || dones != 0 {
		t.Fatalf("升级回报帧不应进入作业分发器，实际 ack=%d log=%d done=%d", acks, logs, dones)
	}

	// 4) 失败回报：审计 success=false 且带拒绝/失败原因，版本号保持成功版本不变。
	const failedVersion = "9.9.10"
	failedRunID := upgradeRunPrefix + failedVersion
	sendFrame(t, conn, protocol.TypeJobDone, protocol.JobDone{
		RunID: failedRunID, ExitCode: -1, Error: "sha256 校验失败",
	})
	failedDetail := waitUpgradeAudit(t, env, jwt, func(detail string) bool {
		return strings.Contains(detail, `"version":"`+failedVersion+`"`)
	})
	var failedAudit struct {
		Version string `json:"version"`
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal([]byte(failedDetail), &failedAudit); err != nil {
		t.Fatalf("解析失败审计 detail 失败: %v（%s）", err, failedDetail)
	}
	if failedAudit.Success {
		t.Fatalf("失败升级不得记为 success: %s", failedDetail)
	}
	if !strings.Contains(failedAudit.Error, "sha256 校验失败") {
		t.Fatalf("失败审计应带拒绝原因，实际: %s", failedDetail)
	}
	if got := serverAgentVersion(t, env, jwt, srv.ID); got != version {
		t.Fatalf("失败回报不得改写 agent_version，期望 %q 实际 %q", version, got)
	}

	// 5) 坏包回报（exit_code 类型非法）：审计 success=false 且原因可定位到字段，不改版本号。
	const badPacketVersion = "9.9.11"
	sendFrame(t, conn, protocol.TypeJobDone, map[string]any{
		"run_id":    upgradeRunPrefix + badPacketVersion,
		"exit_code": "oops",
	})
	badDetail := waitUpgradeAudit(t, env, jwt, func(detail string) bool {
		return strings.Contains(detail, `"version":"`+badPacketVersion+`"`)
	})
	var badAudit struct {
		Success  bool   `json:"success"`
		Rejected bool   `json:"rejected"`
		Error    string `json:"error"`
		ExitCode *int   `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(badDetail), &badAudit); err != nil {
		t.Fatalf("解析坏包审计 detail 失败: %v（%s）", err, badDetail)
	}
	if badAudit.Success || !badAudit.Rejected {
		t.Fatalf("坏包回报应被拒绝并记为 success=false，实际: %s", badDetail)
	}
	if !strings.Contains(badAudit.Error, "exit_code") {
		t.Fatalf("坏包审计原因应定位到字段 exit_code，实际: %s", badDetail)
	}
	if got := serverAgentVersion(t, env, jwt, srv.ID); got != version {
		t.Fatalf("坏包回报不得改写 agent_version，期望 %q 实际 %q", version, got)
	}
}

// waitUpgradeAudit 轮询 action=agent.upgrade 的审计，返回第一条满足 match 的 detail。
func waitUpgradeAudit(t *testing.T, env *testEnv, jwt string, match func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		status, raw := env.request(http.MethodGet, "/api/v1/audit-logs?action=agent.upgrade&limit=20", jwt, nil)
		if status == http.StatusOK {
			var out struct {
				Data struct {
					Logs []struct {
						Detail *string `json:"detail"`
					} `json:"logs"`
				} `json:"data"`
			}
			if err := json.Unmarshal(raw, &out); err == nil {
				for _, item := range out.Data.Logs {
					if item.Detail == nil {
						continue
					}
					last = *item.Detail
					if match(last) {
						return last
					}
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待 agent.upgrade 审计超时，最后一次 detail=%q", last)
	return ""
}

// serverAgentVersion 读回指定服务器当前记录的 agent 版本。
func serverAgentVersion(t *testing.T, env *testEnv, jwt, serverID string) string {
	t.Helper()
	for _, s := range env.listServers(jwt) {
		if s.ID == serverID {
			if s.AgentVersion == nil {
				return ""
			}
			return *s.AgentVersion
		}
	}
	t.Fatalf("服务器列表里找不到 %s", serverID)
	return ""
}
