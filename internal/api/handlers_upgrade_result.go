package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/maliang/leadstack/internal/store"
	"github.com/maliang/leadstack/pkg/protocol"
)

// upgradeRunPrefix 是 agent 升级回报使用的保留 run_id 前缀（契约第 44 节）。
const upgradeRunPrefix = "agent.upgrade:"

// handleUpgradeResultFrame 处理 agent 升级结果帧（复用 job.log / job.done）。
//
// 返回 true 表示该帧属于升级回报，已处理完毕（不再交给作业分发）。
func (a *API) handleUpgradeResultFrame(serverID string, env protocol.Envelope) bool {
	var head struct {
		RunID string `json:"run_id"`
	}
	if len(env.Payload) == 0 {
		return false
	}
	if err := json.Unmarshal(env.Payload, &head); err != nil {
		return false
	}
	if !strings.HasPrefix(head.RunID, upgradeRunPrefix) {
		return false
	}
	version := strings.TrimPrefix(head.RunID, upgradeRunPrefix)

	switch env.Type {
	case protocol.TypeJobLog:
		var frame protocol.JobLog
		if err := json.Unmarshal(env.Payload, &frame); err == nil && strings.TrimSpace(frame.Chunk) != "" {
			a.logger.Info("Agent 升级输出", "server_id", serverID, "version", version,
				"stream", frame.Stream, "chunk", truncateForLog(frame.Chunk, 500))
		}
		return true

	case protocol.TypeJobDone:
		var frame struct {
			ExitCode *int   `json:"exit_code"`
			Error    string `json:"error"`
		}
		if err := json.Unmarshal(env.Payload, &frame); err != nil {
			a.rejectUpgradeReport(serverID, version, fmt.Sprintf("解析升级回报载荷失败: %v", err))
			return true
		}
		if frame.ExitCode == nil {
			// 缺 exit_code 的回报无法判定成败，按坏包拒绝并留下可定位原因，绝不能当成功处理。
			a.rejectUpgradeReport(serverID, version, `升级回报缺少 exit_code 字段`)
			return true
		}
		ok := *frame.ExitCode == 0 && strings.TrimSpace(frame.Error) == ""
		if !ok {
			a.logger.Warn("Agent 升级失败", "server_id", serverID, "version", version,
				"exit_code", *frame.ExitCode, "error", frame.Error)
			a.writeUpgradeAudit(serverID, fmt.Sprintf(
				`{"version":%q,"exit_code":%d,"success":false,"error":%q}`,
				version, *frame.ExitCode, frame.Error))
			return true
		}
		a.logger.Info("Agent 升级成功", "server_id", serverID, "version", version)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		updateErr := a.st.UpdateServerAgentVersion(ctx, serverID, version)
		cancel()
		detail := fmt.Sprintf(`{"version":%q,"exit_code":0,"success":true}`, version)
		if updateErr != nil {
			// 升级本身成功，但控制面版本号没落库：审计里必须留下痕迹，且日志不能只到 Debug。
			a.logger.Warn("更新 agent 版本号失败", "server_id", serverID, "version", version, "err", updateErr)
			detail = fmt.Sprintf(`{"version":%q,"exit_code":0,"success":true,"version_update_error":%q}`,
				version, updateErr.Error())
		}
		a.writeUpgradeAudit(serverID, detail)
		return true
	}
	return false
}

// writeUpgradeAudit 写入一条 agent 升级审计。
//
// WS 场景没有 *http.Request：直接构造审计记录（actor=system，来源=agent 回报）。
func (a *API) writeUpgradeAudit(serverID, detail string) {
	resourceType, resourceID := "server", serverID
	a.writeAudit(&store.AuditLog{
		TS: a.nowMS(), Actor: "system", Action: "agent.upgrade",
		ResourceType: &resourceType, ResourceID: &resourceID, Detail: &detail,
	})
}

// rejectUpgradeReport 记录一次无法采信的升级回报（坏包或缺字段），
// 审计里必须带可定位的拒绝原因，否则这次升级失败在系统里没有任何痕迹。
func (a *API) rejectUpgradeReport(serverID, version, reason string) {
	a.logger.Warn("Agent 升级回报被拒绝", "server_id", serverID, "version", version, "reason", reason)
	a.writeUpgradeAudit(serverID, fmt.Sprintf(
		`{"version":%q,"success":false,"rejected":true,"error":%q}`, version, reason))
}

// truncateForLog 截断日志文本。
func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
