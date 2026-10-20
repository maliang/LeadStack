package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/hostimport"
	"github.com/maliang/leadstack/internal/store"
)

// 批量导入上限（契约第 45 节）。
const maxImportLines = 500

// importRequest 是批量导入请求体。
type importRequest struct {
	Text         string   `json:"text"`
	DefaultPort  *int     `json:"default_port"`
	CredentialID string   `json:"credential_id"`
	Group        string   `json:"group"`
	Tags         []string `json:"tags"`
	Mode         string   `json:"mode"`
}

// importCreated 是成功导入的一条。
type importCreated struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	ServerID string `json:"server_id"`
}

// importFailed 是导入失败的一条。
type importFailed struct {
	Host  string `json:"host"`
	Error string `json:"error"`
}

// importResponse 是逐条导入结果。
type importResponse struct {
	Total   int             `json:"total"`
	Created []importCreated `json:"created"`
	Failed  []importFailed  `json:"failed"`
}

// handleImportServers 处理 POST /api/v1/servers/import（operator 及以上）。
func (a *API) handleImportServers(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "text 不能为空")
		return
	}
	// 未显式给 default_port 时用设置里的默认 SSH 端口（改设置后立即生效）。
	port := a.defaultSSHPortForCreate(r.Context())
	if req.DefaultPort != nil && *req.DefaultPort > 0 {
		port = *req.DefaultPort
	}
	if port < 1 || port > 65535 {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "default_port 必须在 1-65535 之间")
		return
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = store.ModeSSH
	}
	if mode != store.ModeAgent && mode != store.ModeSSH {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "mode 只能是 agent|ssh")
		return
	}
	ctx := r.Context()
	var credentialID *string
	if strings.TrimSpace(req.CredentialID) != "" {
		cid := strings.TrimSpace(req.CredentialID)
		if _, err := a.st.GetCredential(ctx, cid); err != nil {
			a.writeStoreError(w, r, err)
			return
		}
		credentialID = &cid
	}
	tags, err := normalizeTags(req.Tags)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		a.internalError(w, r, fmt.Errorf("序列化标签: %w", err))
		return
	}
	var group *string
	if g := strings.TrimSpace(req.Group); g != "" {
		if err := validateText("group", g, maxGroupRunes, false); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		group = &g
	}

	entries, err := hostimport.Parse(req.Text, port)
	if err != nil {
		if errors.Is(err, hostimport.ErrTooManyLines) {
			writeError(w, http.StatusBadRequest, codeInvalidArgument,
				fmt.Sprintf("单次最多导入 %d 行", maxImportLines))
			return
		}
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "解析导入内容失败: "+err.Error())
		return
	}

	// 已存在的 host:port 集合（用于去重说明）。
	existing, err := a.st.ListServers(ctx)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	seen := make(map[string]string, len(existing)) // host:port → server name
	for _, srv := range existing {
		seen[fmt.Sprintf("%s:%d", srv.Host, srv.Port)] = srv.Name
	}

	resp := importResponse{Created: []importCreated{}, Failed: []importFailed{}}
	for _, entry := range entries {
		resp.Total++
		if strings.TrimSpace(entry.Err) != "" {
			resp.Failed = append(resp.Failed, importFailed{Host: entry.Raw, Error: entry.Err})
			continue
		}
		key := fmt.Sprintf("%s:%d", entry.Host, entry.Port)
		if name, dup := seen[key]; dup {
			resp.Failed = append(resp.Failed, importFailed{
				Host: key, Error: fmt.Sprintf("该主机已存在（服务器「%s」）", name),
			})
			continue
		}
		srv := &store.Server{
			ID: uuid.NewString(), Name: entry.Host, Host: entry.Host, Port: entry.Port,
			Mode: mode, TagsJSON: string(tagsJSON), Status: "unknown", CreatedAt: a.nowMS(),
			CredentialID: credentialID, Group: group, HostKeyPolicy: store.HostKeyPolicyAcceptNew,
		}
		if err := a.st.CreateServer(ctx, srv); err != nil {
			resp.Failed = append(resp.Failed, importFailed{Host: key, Error: "创建服务器失败: " + err.Error()})
			continue
		}
		seen[key] = srv.Name
		resp.Created = append(resp.Created, importCreated{Host: entry.Host, Port: entry.Port, ServerID: srv.ID})
	}

	a.logger.Info("批量导入主机",
		"total", resp.Total, "created", len(resp.Created), "failed", len(resp.Failed))
	writeData(w, http.StatusOK, resp)
}
