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
			RunID    string `json:"run_id"`
			ExitCode int    `json:"exit_code"`
			Error    string `json:"error"`
		}
		if err := json.Unmarshal(env.Payload, &frame); err != nil {
			return true
		}
		ok := frame.ExitCode == 0 && strings.TrimSpace(frame.Error) == ""
		detail := fmt.Sprintf(`{"version":%q,"exit_code":%d,"success":%t}`, version, frame.ExitCode, ok)
		if !ok {
			detail = fmt.Sprintf(`{"version":%q,"exit_code":%d,"success":false,"error":%q}`,
				version, frame.ExitCode, frame.Error)
			a.logger.Warn("Agent 升级失败", "server_id", serverID, "version", version,
				"exit_code", frame.ExitCode, "error", frame.Error)
		} else {
			a.logger.Info("Agent 升级成功", "server_id", serverID, "version", version)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := a.st.UpdateServerAgentVersion(ctx, serverID, version); err != nil {
				a.logger.Debug("更新 agent 版本号失败", "server_id", serverID, "err", err)
			}
			cancel()
		}
		// WS 场景没有 *http.Request：直接构造审计记录（actor=system，来源=agent 回报）。
		resourceType, resourceID := "server", serverID
		a.writeAudit(&store.AuditLog{
			TS: a.nowMS(), Actor: "system", Action: "agent.upgrade",
			ResourceType: &resourceType, ResourceID: &resourceID, Detail: &detail,
		})
		return true
	}
	return false
}

// truncateForLog 截断日志文本。
func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
