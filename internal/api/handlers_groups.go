package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/store"
)

// 分组字段约束（契约 §53.1）。
const (
	maxGroupNameRunes        = 32
	maxGroupDescriptionRunes = 200
)

// handleListGroups 处理 GET /api/v1/groups：由 servers 聚合出的「自由标签分组」与数量。
//
// 这是**筛选口径**（servers.group 文本列）；授权口径是 /server-groups（一等公民分组）。
func (a *API) handleListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := a.st.ListGroups(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if groups == nil {
		groups = []store.GroupCount{}
	}
	writeData(w, http.StatusOK, groups)
}

// handleListServerGroups 处理 GET /api/v1/server-groups（server.read）。
//
// 受限范围的角色只看到自己范围内的分组（避免泄露无关分组名）。
func (a *API) handleListServerGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := a.st.ListServerGroups(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if az, ok := authzFromRequest(r); ok && !az.scopeAll {
		allowed := make(map[string]struct{}, len(az.Scope.GroupIDs))
		for _, id := range az.Scope.GroupIDs {
			allowed[id] = struct{}{}
		}
		filtered := make([]store.ServerGroup, 0, len(groups))
		for _, g := range groups {
			if _, ok := allowed[g.ID]; ok {
				filtered = append(filtered, g)
			}
		}
		groups = filtered
	}
	if groups == nil {
		groups = []store.ServerGroup{}
	}
	writeData(w, http.StatusOK, groups)
}

// handleCreateServerGroup 处理 POST /api/v1/server-groups（group.write + 全局范围）。
func (a *API) handleCreateServerGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := validateText("name", name, maxGroupNameRunes, true); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	description := strings.TrimSpace(req.Description)
	if len([]rune(description)) > maxGroupDescriptionRunes {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			fmt.Sprintf("描述不能超过 %d 个字符", maxGroupDescriptionRunes))
		return
	}
	now := a.nowMS()
	group := &store.ServerGroup{
		ID: uuid.NewString(), Name: name, Description: description, CreatedAt: now, UpdatedAt: now,
	}
	if err := a.st.CreateServerGroup(r.Context(), group); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.auditManual(r, "group.create", "server_group", group.ID, fmt.Sprintf(`{"name":%q}`, group.Name))
	stored, err := a.st.GetServerGroup(r.Context(), group.ID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, stored)
}

// handlePatchServerGroup 处理 PATCH /api/v1/server-groups/{groupID}。
func (a *API) handlePatchServerGroup(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "groupID"))
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	upd := store.GroupUpdate{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if err := validateText("name", name, maxGroupNameRunes, true); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		upd.Name = &name
	}
	if req.Description != nil {
		description := strings.TrimSpace(*req.Description)
		if len([]rune(description)) > maxGroupDescriptionRunes {
			writeError(w, http.StatusBadRequest, codeInvalidArgument,
				fmt.Sprintf("描述不能超过 %d 个字符", maxGroupDescriptionRunes))
			return
		}
		upd.Description = &description
	}
	group, err := a.st.UpdateServerGroup(r.Context(), id, upd, a.nowMS())
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.auditManual(r, "group.update", "server_group", id, "")
	writeData(w, http.StatusOK, group)
}

// handleDeleteServerGroup 处理 DELETE /api/v1/server-groups/{groupID}。
func (a *API) handleDeleteServerGroup(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "groupID"))
	if err := a.st.DeleteServerGroup(r.Context(), id); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	// 分组被删后，引用它的角色范围随之变化，必须失效授权缓存。
	a.invalidateAuthz()
	a.auditManual(r, "group.delete", "server_group", id, "")
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSetServerGroupMembers 处理 PUT /api/v1/server-groups/{groupID}/servers。
func (a *API) handleSetServerGroupMembers(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "groupID"))
	var req struct {
		ServerIDs []string `json:"server_ids"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	serverIDs := dedupeTrimmed(req.ServerIDs)
	if len(serverIDs) > 0 {
		missing, err := a.st.ServersExist(r.Context(), serverIDs)
		if err != nil {
			a.internalError(w, r, err)
			return
		}
		if len(missing) > 0 {
			writeError(w, http.StatusBadRequest, codeInvalidArgument,
				"服务器不存在: "+strings.Join(missing, ", "))
			return
		}
	}
	if err := a.st.SetServerGroupMembers(r.Context(), id, serverIDs); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.invalidateAuthz()
	a.auditManual(r, "group.members", "server_group", id, fmt.Sprintf(`{"servers":%d}`, len(serverIDs)))
	group, err := a.st.GetServerGroup(r.Context(), id)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, group)
}

// handleListServerGroupMembers 处理 GET /api/v1/server-groups/{groupID}/servers。
func (a *API) handleListServerGroupMembers(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "groupID"))
	servers, err := a.st.ListServerGroupMembers(r.Context(), id)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	servers = a.filterServersByScope(r, servers)
	nowMS := a.nowMS()
	ids := make([]string, 0, len(servers))
	for _, srv := range servers {
		ids = append(ids, srv.ID)
	}
	metrics, err := a.st.LatestMetrics(r.Context(), ids)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]serverPayload, 0, len(servers))
	for _, srv := range servers {
		out = append(out, toServerPayload(srv, nowMS, metrics[srv.ID], nil))
	}
	if err := a.attachServerGroups(r.Context(), out); err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, out)
}

// filterServersByScope 按调用者的资源范围过滤服务器列表；范围=all 时原样返回。
func (a *API) filterServersByScope(r *http.Request, servers []store.Server) []store.Server {
	allowed := a.allowedServers(r)
	if allowed == nil {
		return servers
	}
	out := make([]store.Server, 0, len(servers))
	for _, srv := range servers {
		if _, ok := allowed[srv.ID]; ok {
			out = append(out, srv)
		}
	}
	return out
}

// attachServerGroups 给服务器载荷补上分组 ID 与名称。
func (a *API) attachServerGroups(ctx context.Context, payloads []serverPayload) error {
	if len(payloads) == 0 {
		return nil
	}
	ids := make([]string, 0, len(payloads))
	for _, p := range payloads {
		ids = append(ids, p.ID)
	}
	groupIDs, groupNames, err := a.st.ServerGroupRefs(ctx, ids)
	if err != nil {
		return err
	}
	for i := range payloads {
		payloads[i].GroupIDs = nonNilStrings(groupIDs[payloads[i].ID])
		payloads[i].GroupNames = nonNilStrings(groupNames[payloads[i].ID])
	}
	return nil
}

// applyServerGroups 校验并整包替换服务器的一等公民分组归属；nil 表示不改。
// 校验失败时已写好错误响应，返回 false 让调用方直接 return。
func (a *API) applyServerGroups(ctx context.Context, w http.ResponseWriter, r *http.Request, serverID string, groupIDs *[]string) bool {
	if groupIDs == nil {
		return true
	}
	ids := dedupeTrimmed(*groupIDs)
	if len(ids) > 0 {
		missing, err := a.st.GroupsExist(ctx, ids)
		if err != nil {
			a.internalError(w, r, err)
			return false
		}
		if len(missing) > 0 {
			writeError(w, http.StatusBadRequest, codeInvalidArgument,
				"分组不存在: "+strings.Join(missing, ", "))
			return false
		}
	}
	if err := a.st.SetServerGroups(ctx, serverID, ids); err != nil {
		a.writeStoreError(w, r, err)
		return false
	}
	return true
}
