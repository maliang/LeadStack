package api

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/store"
)

// localServerResponse 是「接入本机」的结果：服务器 + 一次性令牌 + 安装命令。
type localServerResponse struct {
	Server    serverPayload `json:"server"`
	Token     string        `json:"token"`
	ExpiresAt int64         `json:"expires_at"`
	Command   string        `json:"command"`
}

// handleCreateLocalServer 处理 POST /api/v1/servers/local：
// 创建一台「本机」服务器（mode=agent, host=127.0.0.1）并返回可直接粘贴执行的 agent 安装命令。
//
// 为什么走 agent 而不是 SSH：本机部署最怕两件事——给控制端开 sshd、以及 SSH 轮询拿不到实时状态。
// agent 常驻本机后，状态/指标是推送的（心跳 20s、指标 15s、离线阈值 45s），部署任务也走长连下发。
func (a *API) handleCreateLocalServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		// ServerURL 可覆盖安装命令里的控制端地址（默认自动推导为 http://127.0.0.1:<端口>）。
		ServerURL string `json:"server_url"`
		// TTL 是安装令牌有效期（秒），默认 1 小时。
		TTLS int `json:"ttl_s"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
			return
		}
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		if host, herr := os.Hostname(); herr == nil && strings.TrimSpace(host) != "" {
			name = "本机（" + strings.TrimSpace(host) + "）"
		} else {
			name = "本机"
		}
	}

	nowMS := a.nowMS()
	srv := &store.Server{
		ID:        uuid.NewString(),
		Name:      name,
		Host:      "127.0.0.1",
		Port:      0, // agent 模式不用于连入
		Mode:      store.ModeAgent,
		Status:    "unknown",
		CreatedAt: nowMS,
	}
	if err := a.st.CreateServer(r.Context(), srv); err != nil {
		a.writeStoreError(w, r, err)
		return
	}

	ttl := req.TTLS
	if ttl <= 0 {
		ttl = defaultEnrollTTLS
	}
	if ttl < 60 || ttl > maxEnrollTTLS {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, fmt.Sprintf("ttl_s 必须在 60-%d 秒之间", maxEnrollTTLS))
		return
	}
	plain, err := newEnrollToken()
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	serverID := srv.ID
	token := &store.EnrollToken{
		ID:        uuid.NewString(),
		TokenHash: hashEnrollToken(plain),
		Note:      "本机接入",
		ExpiresAt: nowMS + int64(ttl)*1000,
		ServerID:  &serverID,
	}
	if err := a.st.CreateEnrollToken(r.Context(), token); err != nil {
		a.writeStoreError(w, r, err)
		return
	}

	base, err := a.localServerURL(r, req.ServerURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	command := fmt.Sprintf("curl -fsSL '%s/install.sh' | sh -s -- --server '%s' --token '%s'", base, base, plain)

	payload := toServerPayload(*srv, nowMS, nil, nil)
	// 新服务器还没有分组：返回空数组而不是 null，避免前端要额外容错。
	if payload.GroupIDs == nil {
		payload.GroupIDs = []string{}
	}
	if payload.GroupNames == nil {
		payload.GroupNames = []string{}
	}

	a.logger.Info("创建本机服务器", "server_id", srv.ID, "name", srv.Name, "token_id", token.ID)
	writeData(w, http.StatusCreated, localServerResponse{
		Server:    payload,
		Token:     plain,
		ExpiresAt: token.ExpiresAt,
		Command:   command,
	})
}

// localServerURL 推导本机 agent 应连接的控制端地址：显式指定 > 请求 Host（带端口时）> 默认 127.0.0.1:8080。
func (a *API) localServerURL(r *http.Request, explicit string) (string, error) {
	if raw := strings.TrimSpace(explicit); raw != "" {
		return normalizeServerURL(raw)
	}
	host := strings.TrimSpace(r.Host)
	// 请求可能来自反向代理（Host 是外部域名）；本机 agent 一律走回环，只保留端口。
	port := ""
	if idx := strings.LastIndex(host, ":"); idx >= 0 && idx < len(host)-1 {
		if _, perr := strconv.Atoi(host[idx+1:]); perr == nil {
			port = host[idx+1:]
		}
	}
	if port == "" {
		port = "8080"
	}
	return normalizeServerURL("http://127.0.0.1:" + port)
}
