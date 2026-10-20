package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/maliang/leadstack/internal/config"
	"github.com/maliang/leadstack/internal/store"
)

// 网络相关设置键（契约 §49，复用 settings 表，无 schema 变更）。
const (
	settingNetworkListen    = "network.listen"
	settingNetworkPublicURL = "network.public_url"
	settingDefaultSSHPort   = "servers.default_ssh_port"

	// 配置来源标识。
	sourceSettings = "settings"
	sourceEnv      = "env"
	sourceDefault  = "default"

	// defaultSSHPort 是未配置时的目标机 SSH 端口。
	defaultSSHPort = 22
)

// networkSettings 是 /system/network 的响应体（契约 49.1）。
type networkSettings struct {
	Listen            string `json:"listen"`
	ListenSource      string `json:"listen_source"`
	ActiveListen      string `json:"active_listen"`
	PublicURL         string `json:"public_url"`
	PublicURLSource   string `json:"public_url_source"`
	DefaultSSHPort    int    `json:"default_ssh_port"`
	DefaultSSHPortSrc string `json:"default_ssh_port_source"`
	RestartRequired   bool   `json:"restart_required"`
}

// ------------------------------------------------ 解析：settings > env > default ------------------------------------------------

// ResolveNetworkListen 解析控制台监听地址及其来源。
//
// 优先级：settings.network.listen > 环境变量 LEADSTACK_LISTEN > 兜底值（配置默认）。
func ResolveNetworkListen(ctx context.Context, st *store.Store, fallback string) (string, string, error) {
	if st != nil {
		if raw, ok, err := st.GetSetting(ctx, settingNetworkListen); err != nil {
			return "", "", fmt.Errorf("读取网络设置 %s: %w", settingNetworkListen, err)
		} else if ok && strings.TrimSpace(raw) != "" {
			return strings.TrimSpace(raw), sourceSettings, nil
		}
	}
	if env := strings.TrimSpace(os.Getenv(config.EnvListen)); env != "" {
		return env, sourceEnv, nil
	}
	if strings.TrimSpace(fallback) == "" {
		return config.DefaultListen, sourceDefault, nil
	}
	return strings.TrimSpace(fallback), sourceDefault, nil
}

// ResolvePublicURL 解析控制台对外地址及其来源。
//
// 优先级：settings.network.public_url > 环境变量 LEADSTACK_PUBLIC_URL > 空（调用方回落到请求 Host）。
func ResolvePublicURL(ctx context.Context, st *store.Store) (string, string, error) {
	if st != nil {
		if raw, ok, err := st.GetSetting(ctx, settingNetworkPublicURL); err != nil {
			return "", "", fmt.Errorf("读取网络设置 %s: %w", settingNetworkPublicURL, err)
		} else if ok && strings.TrimSpace(raw) != "" {
			return strings.TrimSuffix(strings.TrimSpace(raw), "/"), sourceSettings, nil
		}
	}
	if env := strings.TrimSpace(os.Getenv(config.EnvPublicURL)); env != "" {
		return strings.TrimSuffix(env, "/"), sourceEnv, nil
	}
	return "", sourceDefault, nil
}

// ResolveDefaultSSHPort 解析目标机默认 SSH 端口及其来源。
//
// 优先级：settings.servers.default_ssh_port > 环境变量 LEADSTACK_DEFAULT_SSH_PORT > 22。
func ResolveDefaultSSHPort(ctx context.Context, st *store.Store) (int, string, error) {
	if st != nil {
		if raw, ok, err := st.GetSetting(ctx, settingDefaultSSHPort); err != nil {
			return 0, "", fmt.Errorf("读取网络设置 %s: %w", settingDefaultSSHPort, err)
		} else if ok && strings.TrimSpace(raw) != "" {
			port, perr := strconv.Atoi(strings.TrimSpace(raw))
			if perr != nil || port < 1 || port > 65535 {
				// 库里的值被人工改坏时不让整个服务不可用：回落并继续。
				return defaultSSHPort, sourceDefault, nil
			}
			return port, sourceSettings, nil
		}
	}
	if env := strings.TrimSpace(os.Getenv(config.EnvDefaultSSHPort)); env != "" {
		if port, err := strconv.Atoi(env); err == nil && port >= 1 && port <= 65535 {
			return port, sourceEnv, nil
		}
	}
	return defaultSSHPort, sourceDefault, nil
}

// defaultSSHPortForCreate 返回新建/导入服务器时的默认端口（改设置后立即生效）。
func (a *API) defaultSSHPortForCreate(ctx context.Context) int {
	port, _, err := ResolveDefaultSSHPort(ctx, a.st)
	if err != nil {
		a.logger.Warn("读取默认 SSH 端口失败，使用 22", "err", err)
		return defaultSSHPort
	}
	return port
}

// publicURLOrEmpty 返回当前生效的对外地址（可能为空，调用方回落到请求 Host）。
func (a *API) publicURL(ctx context.Context) string {
	value, _, err := ResolvePublicURL(ctx, a.st)
	if err != nil {
		a.logger.Warn("读取对外地址失败", "err", err)
		return ""
	}
	return value
}

// ------------------------------------------------ 校验 ------------------------------------------------

// ValidateListen 校验监听地址：必须 host:port，端口 1-65535。
//
// probe 为 true 时尝试 bind 一次（避免用户保存一个被占用/不可绑定的地址后重启失联）。
func ValidateListen(addr string, probe bool) error {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return errors.New("listen 不能为空")
	}
	host, portStr, err := net.SplitHostPort(trimmed)
	if err != nil {
		return errors.New("listen 必须形如 host:port（例如 0.0.0.0:8080）")
	}
	port, perr := strconv.Atoi(portStr)
	if perr != nil || port < 1 || port > 65535 {
		return errors.New("listen 的端口必须是 1-65535 的整数")
	}
	// host 允许为空（等价于全部地址）或任意 IP/主机名；只拒绝明显非法的空白。
	if strings.TrimSpace(host) != host {
		return errors.New("listen 的主机部分不能包含首尾空白")
	}
	if !probe {
		return nil
	}
	ln, lerr := net.Listen("tcp", trimmed)
	if lerr != nil {
		return fmt.Errorf("该地址无法绑定（可能已被占用）：%v，请换一个端口", lerr)
	}
	if cerr := ln.Close(); cerr != nil {
		return fmt.Errorf("释放试探监听失败：%v", cerr)
	}
	return nil
}

// ValidatePublicURL 校验对外地址：必须 http(s)，且不能是通配地址（目标机连不上）。
func ValidatePublicURL(raw string) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		return errors.New("public_url 不能为空；如需清空请使用「恢复默认」（POST /api/v1/system/network/reset）")
	}
	lower := strings.ToLower(value)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return errors.New("public_url 必须以 http:// 或 https:// 开头")
	}
	rest := value[strings.Index(value, "://")+3:]
	hostPart := rest
	if idx := strings.IndexAny(hostPart, "/?#"); idx >= 0 {
		hostPart = hostPart[:idx]
	}
	hostOnly := hostPart
	if h, _, herr := net.SplitHostPort(hostPart); herr == nil {
		hostOnly = h
	}
	hostOnly = strings.Trim(hostOnly, "[]")
	switch hostOnly {
	case "", "0.0.0.0", "::":
		return errors.New("public_url 不能使用 0.0.0.0 或 ::，目标机无法回连，请填控制台的对外 IP 或域名")
	}
	return nil
}

// validateListenForSave 是「保存 listen」场景的校验：格式始终校验，bind 探测仅在地址变化时执行。
//
// 为什么跳过：如果用户把当前正在生效的地址保存进 settings（例如从 env 切到控制台设置），
// 这个端口正被本进程占用，探测必然失败——但那不是错误，也不该阻止保存。
func validateListenForSave(addr, activeListen string) error {
	if err := ValidateListen(addr, false); err != nil {
		return err
	}
	if activeListen != "" && sameListen(addr, activeListen) {
		return nil
	}
	return ValidateListen(addr, true)
}

// ValidatePort 校验端口范围。
func ValidatePort(port int) error {
	if port < 1 || port > 65535 {
		return errors.New("端口必须是 1-65535 的整数")
	}
	return nil
}

// ------------------------------------------------ REST ------------------------------------------------

// handleGetNetwork 处理 GET /api/v1/system/network。
func (a *API) handleGetNetwork(w http.ResponseWriter, r *http.Request) {
	payload, err := a.currentNetworkSettings(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, payload)
}

// handlePutNetwork 处理 PUT /api/v1/system/network（部分更新）。
func (a *API) handlePutNetwork(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Listen         *string `json:"listen"`
		PublicURL      *string `json:"public_url"`
		DefaultSSHPort *int    `json:"default_ssh_port"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	if req.Listen == nil && req.PublicURL == nil && req.DefaultSSHPort == nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			"至少需要提供 listen/public_url/default_ssh_port 之一")
		return
	}

	ctx := r.Context()
	changes := make([]string, 0, 3)

	if req.Listen != nil {
		value := strings.TrimSpace(*req.Listen)
		if err := validateListenForSave(value, a.activeListen); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		if err := a.st.SetSetting(ctx, settingNetworkListen, value); err != nil {
			a.internalError(w, r, err)
			return
		}
		changes = append(changes, fmt.Sprintf(`"listen":%q`, value))
	}
	if req.PublicURL != nil {
		value := strings.TrimSuffix(strings.TrimSpace(*req.PublicURL), "/")
		if err := ValidatePublicURL(value); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		if err := a.st.SetSetting(ctx, settingNetworkPublicURL, value); err != nil {
			a.internalError(w, r, err)
			return
		}
		changes = append(changes, fmt.Sprintf(`"public_url":%q`, value))
	}
	if req.DefaultSSHPort != nil {
		if err := ValidatePort(*req.DefaultSSHPort); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		if err := a.st.SetSetting(ctx, settingDefaultSSHPort, strconv.Itoa(*req.DefaultSSHPort)); err != nil {
			a.internalError(w, r, err)
			return
		}
		changes = append(changes, fmt.Sprintf(`"default_ssh_port":%d`, *req.DefaultSSHPort))
	}

	payload, err := a.currentNetworkSettings(ctx)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	a.logger.Info("网络设置已更新",
		"listen", payload.Listen, "listen_source", payload.ListenSource,
		"public_url", payload.PublicURL, "default_ssh_port", payload.DefaultSSHPort,
		"restart_required", payload.RestartRequired)
	if len(changes) > 0 {
		detail := "{" + strings.Join(changes, ",") + "}"
		a.setAuditDetail(r, "system.network_update", detail)
	}
	writeData(w, http.StatusOK, payload)
}

// handleResetNetwork 处理 POST /api/v1/system/network/reset（清空设置，回落 env/默认）。
func (a *API) handleResetNetwork(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	for _, key := range []string{settingNetworkListen, settingNetworkPublicURL, settingDefaultSSHPort} {
		if err := a.st.DeleteSetting(ctx, key); err != nil {
			a.internalError(w, r, err)
			return
		}
	}
	payload, err := a.currentNetworkSettings(ctx)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	a.logger.Info("网络设置已重置",
		"listen", payload.Listen, "listen_source", payload.ListenSource,
		"public_url", payload.PublicURL, "default_ssh_port", payload.DefaultSSHPort)
	a.setAuditDetail(r, "system.network_reset", "")
	writeData(w, http.StatusOK, payload)
}

// currentNetworkSettings 组装当前网络设置与来源。
func (a *API) currentNetworkSettings(ctx context.Context) (networkSettings, error) {
	listen, listenSource, err := ResolveNetworkListen(ctx, a.st, a.cfg.Listen)
	if err != nil {
		return networkSettings{}, err
	}
	publicURL, publicSource, err := ResolvePublicURL(ctx, a.st)
	if err != nil {
		return networkSettings{}, err
	}
	sshPort, sshSource, err := ResolveDefaultSSHPort(ctx, a.st)
	if err != nil {
		return networkSettings{}, err
	}
	active := a.activeListen
	if active == "" {
		active = a.cfg.Listen
	}
	return networkSettings{
		Listen:            listen,
		ListenSource:      listenSource,
		ActiveListen:      active,
		PublicURL:         publicURL,
		PublicURLSource:   publicSource,
		DefaultSSHPort:    sshPort,
		DefaultSSHPortSrc: sshSource,
		RestartRequired:   !sameListen(listen, active),
	}, nil
}

// sameListen 比较监听地址是否等价（主机名/IP 差异按字面比较，端口必须相同）。
func sameListen(a, b string) bool {
	normalize := func(v string) string {
		v = strings.TrimSpace(v)
		if h, p, err := net.SplitHostPort(v); err == nil {
			return net.JoinHostPort(strings.Trim(h, "[]"), p)
		}
		return v
	}
	return normalize(a) == normalize(b)
}
