package api

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/maliang/leadstack/internal/store"
)

// AgentAutoUpgradeInterval 是控制端巡检 agent 版本的间隔。
// 可在环境变量 LEADSTACK_AGENT_AUTO_UPGRADE=false 时关闭。
const AgentAutoUpgradeInterval = 6 * time.Hour

// RunAgentAutoUpgrade 周期性检查在线 agent 版本，并自动下发升级。
//
// 只处理：
//   - mode=agent 且当前有 WebSocket 连接的服务器；
//   - 上报过 agent_version 且低于最新 release 的；
//   - 最新 release 中存在匹配目标 OS/arch 且带 sha256 的 agent 资产。
//
// 失败只记录并等待下一轮，不阻塞控制端；升级结果仍由既有 agent.upgrade 回报处理。
func (a *API) RunAgentAutoUpgrade(ctx context.Context) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("LEADSTACK_AGENT_AUTO_UPGRADE")), "false") {
		a.logger.Info("agent 自动升级已关闭（LEADSTACK_AGENT_AUTO_UPGRADE=false）")
		return
	}
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			a.autoUpgradeAgents(ctx)
			timer.Reset(AgentAutoUpgradeInterval)
		}
	}
}

func (a *API) autoUpgradeAgents(ctx context.Context) {
	info := a.updates.Get(ctx, true)
	if strings.TrimSpace(info.Latest) == "" || len(info.AgentAssets) == 0 {
		return
	}
	servers, err := a.st.ListServersFiltered(ctx, store.ServerFilter{Mode: store.ModeAgent}, a.nowMS())
	if err != nil {
		a.logger.Warn("agent 自动升级：读取服务器失败", "err", err)
		return
	}
	for _, srv := range servers {
		if srv.AgentVersion == nil || strings.TrimSpace(*srv.AgentVersion) == "" {
			continue
		}
		if !versionIsNewer(info.Latest, *srv.AgentVersion) {
			continue
		}
		if _, online := a.hub.Get(srv.ID); !online {
			continue
		}
		asset, ok := pickAgentAsset(info.AgentAssets, deref(srv.OS), deref(srv.Arch))
		if !ok {
			continue
		}
		if err := a.sendAgentUpgrade(ctx, srv.ID, info.Latest, asset.URL, asset.SHA256); err != nil {
			a.logger.Warn("agent 自动升级下发失败", "server_id", srv.ID, "from", *srv.AgentVersion, "to", info.Latest, "err", err)
			continue
		}
		a.logger.Info("已自动下发 agent 升级", "server_id", srv.ID, "from", *srv.AgentVersion, "to", info.Latest)
		a.auditSystem("agent.auto_upgrade", fmt.Sprintf(`{"server_id":%q,"from":%q,"to":%q}`, srv.ID, *srv.AgentVersion, info.Latest))
	}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func pickAgentAsset(assets []AgentUpdateAsset, osName, arch string) (AgentUpdateAsset, bool) {
	osName = strings.TrimSpace(strings.ToLower(osName))
	arch = strings.TrimSpace(strings.ToLower(arch))
	if osName == "" || arch == "" {
		return AgentUpdateAsset{}, false
	}
	for _, asset := range assets {
		if strings.EqualFold(asset.OS, osName) && strings.EqualFold(asset.Arch, arch) && strings.TrimSpace(asset.SHA256) != "" {
			return asset, true
		}
	}
	return AgentUpdateAsset{}, false
}
