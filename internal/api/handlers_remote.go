package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/maliang/leadstack/internal/remote"
)

// 日志/进程/端口参数（契约第 43 节）。
const (
	remoteOutputMaxBytes = 256 << 10
	defaultLogLines      = 200
	maxLogLines          = 5000
	defaultProcLimit     = 50
	maxProcLimit         = 500
)

// logsPayload 是日志查看响应。
type logsPayload struct {
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
	Path      string   `json:"path"`
}

// processesPayload 是进程列表响应。
type processesPayload struct {
	Processes []remote.Process `json:"processes"`
}

// portsPayload 是端口列表响应。
type portsPayload struct {
	Ports []remote.Port `json:"ports"`
}

// remoteClient 为某台服务器构造 remote.Client（os 优先取 host_info.os）。
func (a *API) remoteClient(r *http.Request, serverID string) (*remote.Client, error) {
	if a.remoteExec == nil {
		return nil, errNoRemoteExecutor
	}
	ctx := r.Context()
	srv, err := a.st.GetServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	osName := ""
	if snap, herr := a.st.GetServerHostInfo(ctx, serverID); herr == nil && snap != nil {
		osName = extractOSName(snap.InfoJSON)
	}
	_ = srv
	return remote.New(a.remoteExec, serverID, osName), nil
}

// extractOSName 从 host_info JSON 里取 os 字段（宽松解析，失败返回空）。
func extractOSName(infoJSON string) string {
	const marker = `"os"`
	idx := strings.Index(infoJSON, marker)
	if idx < 0 {
		return ""
	}
	rest := infoJSON[idx+len(marker):]
	start := strings.Index(rest, `"`)
	if start < 0 {
		return ""
	}
	rest = rest[start+1:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// handleServerLogs 处理 GET /api/v1/servers/{id}/logs?path=&lines=&grep=。
func (a *API) handleServerLogs(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	remotePath, err := sanitizeRemotePath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	lines := defaultLogLines
	if raw := strings.TrimSpace(r.URL.Query().Get("lines")); raw != "" {
		v, perr := strconv.Atoi(raw)
		if perr != nil || v <= 0 || v > maxLogLines {
			writeError(w, http.StatusBadRequest, codeInvalidArgument,
				"lines 必须是 1-5000 的整数")
			return
		}
		lines = v
	}
	client, err := a.remoteClient(r, serverID)
	if err != nil {
		a.writeRemoteError(w, r, err, "准备远程执行通道")
		return
	}
	out, truncated, err := client.TailLog(r.Context(), remotePath, lines,
		strings.TrimSpace(r.URL.Query().Get("grep")), remoteOutputMaxBytes)
	if err != nil {
		a.writeRemoteError(w, r, err, "读取日志")
		return
	}
	writeData(w, http.StatusOK, logsPayload{Lines: out, Truncated: truncated, Path: remotePath})
}

// handleServerProcesses 处理 GET /api/v1/servers/{id}/processes?sort=&limit=。
func (a *API) handleServerProcesses(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	limit := defaultProcLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		v, perr := strconv.Atoi(raw)
		if perr != nil || v <= 0 || v > maxProcLimit {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "limit 必须是 1-500 的整数")
			return
		}
		limit = v
	}
	client, err := a.remoteClient(r, serverID)
	if err != nil {
		a.writeRemoteError(w, r, err, "准备远程执行通道")
		return
	}
	procs, err := client.ListProcesses(r.Context())
	if err != nil {
		a.writeRemoteError(w, r, err, "读取进程列表")
		return
	}
	if len(procs) > limit {
		procs = procs[:limit]
	}
	if procs == nil {
		procs = []remote.Process{}
	}
	writeData(w, http.StatusOK, processesPayload{Processes: procs})
}

// fallbackListPorts 用 netstat / lsof 回退采集监听端口（目标机无 ss 时）。
func (a *API) fallbackListPorts(ctx context.Context, serverID string) ([]remote.Port, error) {
	if a.remoteExec == nil {
		return nil, errNoRemoteExecutor
	}
	// 不能依赖退出码串联：macOS 的 netstat -tulpn 会"成功"地打印 UNIX socket。
	// 因此全部尝试一遍（忽略各自错误），再统一 grep LISTEN；重复项由 seen 去重。
	cmd := "sh -c '{ ss -tulpn; netstat -tulpn; netstat -an -p tcp; " +
		"lsof -nP -iTCP -sTCP:LISTEN; } 2>/dev/null | grep -i listen'"
	out, stderr, code, err := a.remoteExec.Exec(ctx, serverID, cmd, 30)
	if err != nil || code != 0 {
		return nil, fmt.Errorf("端口回退采集失败: %v %s", err, strings.TrimSpace(stderr))
	}
	ports := make([]remote.Port, 0, 8)
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "listen") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		proto := "tcp"
		if strings.HasPrefix(lower, "udp") || strings.Contains(lower, " udp") {
			proto = "udp"
		}
		// 定位「本地地址:端口」字段：Linux `0.0.0.0:22`、macOS `127.0.0.1.8080` / `*.8080`。
		local := ""
		for _, f := range fields {
			if strings.HasPrefix(f, "tcp") || strings.HasPrefix(f, "udp") || strings.HasPrefix(f, "-") {
				continue
			}
			if strings.Contains(f, ":") && !strings.Contains(f, "->") {
				local = strings.TrimSuffix(f, " (LISTEN)")
				break
			}
			// macOS netstat：以点分隔的最后一段是端口
			if dot := strings.LastIndex(f, "."); dot > 0 {
				portPart := f[dot+1:]
				if _, perr := strconv.Atoi(portPart); perr == nil {
					host := strings.ReplaceAll(f[:dot], ".", ".")
					local = host + ":" + portPart
					break
				}
			}
		}
		if local == "" || strings.HasSuffix(local, ":*") || strings.HasSuffix(local, ":0") {
			continue
		}
		key := proto + "|" + local
		if seen[key] {
			continue
		}
		seen[key] = true
		// 进程名：lsof 的第 1 列是 COMMAND；netstat 的第 1 列是协议族（无进程信息）。
		proc := ""
		pid := 0
		first := strings.ToLower(fields[0])
		if !strings.HasPrefix(first, "tcp") && !strings.HasPrefix(first, "udp") && first != "active" {
			proc = fields[0]
			if len(fields) > 1 {
				if v, perr := strconv.Atoi(fields[1]); perr == nil {
					pid = v
				}
			}
		}
		ports = append(ports, remote.Port{Proto: proto, Local: local, PID: pid, Process: proc})
	}
	return ports, nil
}

// handleServerPorts 处理 GET /api/v1/servers/{id}/ports。
func (a *API) handleServerPorts(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	client, err := a.remoteClient(r, serverID)
	if err != nil {
		a.writeRemoteError(w, r, err, "准备远程执行通道")
		return
	}
	ports, err := client.ListPorts(r.Context())
	if err != nil || len(ports) == 0 {
		// 目标机可能没有 ss（容器/BSD/macOS）：用 netstat/lsof 回退采集。
		if fb, fbErr := a.fallbackListPorts(r.Context(), serverID); fbErr == nil && len(fb) > 0 {
			ports, err = fb, nil
		}
	}
	if err != nil {
		a.writeRemoteError(w, r, err, "读取端口列表")
		return
	}
	if ports == nil {
		ports = []remote.Port{}
	}
	writeData(w, http.StatusOK, portsPayload{Ports: ports})
}
