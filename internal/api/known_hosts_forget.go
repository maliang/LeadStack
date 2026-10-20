package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// forgetKnownHost 清除某台服务器在 known_hosts 中的记录。
//
// host:port 变更后旧主机密钥不再可信：清掉对应行，下次连接会按 host_key_policy 重新学习/校验。
// 失败只记日志（不阻断 PATCH —— 用户改端口这个动作本身必须成功）。
func (a *API) forgetKnownHost(ctx context.Context, serverID, host string, port int) {
	if a == nil || a.st == nil {
		return
	}
	srv, err := a.st.GetServer(ctx, serverID)
	if err != nil {
		a.logger.Debug("清除主机密钥前查询服务器失败", "server_id", serverID, "err", err)
		return
	}
	path := a.knownHostsPath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			a.logger.Debug("读取 known_hosts 失败", "path", path, "err", err)
		}
		return
	}
	// SSHJ 的 known_hosts 行形如 "host:port ssh-ed25519 AAAA..." / "[host]:port ..."。
	targets := []string{
		strings.ToLower(host) + ":" + fmt.Sprint(port),
		"[" + strings.ToLower(host) + "]:" + fmt.Sprint(port),
		strings.ToLower(host),
	}
	kept := make([]string, 0, 64)
	removed := 0
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		matched := false
		for _, target := range targets {
			if strings.HasPrefix(strings.ToLower(trimmed), target+" ") {
				matched = true
				break
			}
		}
		if matched {
			removed++
			continue
		}
		kept = append(kept, line)
	}
	if removed == 0 {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(kept, "\n")), 0o600); err != nil {
		a.logger.Warn("写回 known_hosts 失败", "path", path, "err", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		a.logger.Warn("替换 known_hosts 失败", "path", path, "err", err)
		_ = os.Remove(tmp)
		return
	}
	_ = srv // 保留参数以便将来按 serverID 记录更精细的主机密钥归属
	a.logger.Info("已清除旧主机密钥记录",
		"server_id", serverID, "host", host, "port", port, "removed", removed)
}

// knownHostsPath 返回 known_hosts 文件路径（数据目录下）。
func (a *API) knownHostsPath() string {
	if a == nil || a.cfg == nil {
		return ""
	}
	if a.cfg.KnownHostsPath() != "" {
		return a.cfg.KnownHostsPath()
	}
	if a.cfg.DataDir != "" {
		return filepath.Join(a.cfg.DataDir, "known_hosts")
	}
	return ""
}
