package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/store"
)

// 访问账号字段约束。复用服务器档案的自定义字段限制，避免两套规格。
const (
	maxAccountAddressRunes  = 512
	maxAccountUsernameRunes = 128
	maxAccountPasswordBytes = 4096
)

// serverAccountPayload 是访问账号的对外表示。
//
// 安全红线：列表/详情不回显密码；密码单独走 GET .../secret（审计后返回）。
type serverAccountPayload struct {
	ID             string          `json:"id"`
	ServerID       string          `json:"server_id"`
	Name           string          `json:"name"`
	Address        string          `json:"address"`
	Username       string          `json:"username"`
	Note           string          `json:"note"`
	CustomFields   json.RawMessage `json:"custom_fields"`
	LoginScript    string          `json:"login_script"`
	HasPassword    bool            `json:"has_password"`
	HasLoginScript bool            `json:"has_login_script"`
	CreatedAt      int64           `json:"created_at"`
	UpdatedAt      int64           `json:"updated_at"`
}

type createServerAccountRequest struct {
	Name         string            `json:"name"`
	Address      string            `json:"address"`
	Username     string            `json:"username"`
	Password     string            `json:"password"`
	Note         string            `json:"note"`
	CustomFields map[string]string `json:"custom_fields"`
	LoginScript  string            `json:"login_script"`
}

type patchServerAccountRequest struct {
	Name         *string            `json:"name"`
	Address      *string            `json:"address"`
	Username     *string            `json:"username"`
	Password     *string            `json:"password"`
	Note         *string            `json:"note"`
	CustomFields *map[string]string `json:"custom_fields"`
	LoginScript  *string            `json:"login_script"`
}

type serverAccountSecretPayload struct {
	Password string `json:"password"`
}

type serverAccountLoginPayload struct {
	AccountID string `json:"account_id"`
	serverLoginPayload
}

func (a *API) serverAccountPayload(item store.ServerAccount) serverAccountPayload {
	return serverAccountPayload{
		ID:             item.ID,
		ServerID:       item.ServerID,
		Name:           item.Name,
		Address:        item.Address,
		Username:       item.Username,
		Note:           item.Note,
		CustomFields:   customFieldsRaw(item.CustomFieldsJSON),
		LoginScript:    item.LoginScript,
		HasPassword:    len(item.PasswordEnc) > 0,
		HasLoginScript: strings.TrimSpace(item.LoginScript) != "",
		CreatedAt:      item.CreatedAt,
		UpdatedAt:      item.UpdatedAt,
	}
}

// loadServerAccount 读取账号并保证它确实属于该服务器；不存在按 404 处理。
func (a *API) loadServerAccount(w http.ResponseWriter, r *http.Request, serverID, accountID string) (*store.ServerAccount, bool) {
	item, err := a.st.GetServerAccount(r.Context(), serverID, accountID)
	if err != nil {
		a.writeStoreError(w, r, err)
		return nil, false
	}
	return item, true
}

// handleListServerAccounts 处理 GET /api/v1/servers/{serverID}/accounts。
func (a *API) handleListServerAccounts(w http.ResponseWriter, r *http.Request) {
	serverID := strings.TrimSpace(chi.URLParam(r, "serverID"))
	if serverID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}
	ctx := r.Context()
	if _, err := a.st.GetServer(ctx, serverID); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	items, err := a.st.ListServerAccounts(ctx, serverID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]serverAccountPayload, 0, len(items))
	for _, item := range items {
		out = append(out, a.serverAccountPayload(item))
	}
	writeData(w, http.StatusOK, out)
}

// handleCreateServerAccount 处理 POST /api/v1/servers/{serverID}/accounts。
func (a *API) handleCreateServerAccount(w http.ResponseWriter, r *http.Request) {
	serverID := strings.TrimSpace(chi.URLParam(r, "serverID"))
	if serverID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}
	var req createServerAccountRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := validateText("name", name, maxServerNameRunes, true); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	address := strings.TrimSpace(req.Address)
	if err := validateText("address", address, maxAccountAddressRunes, false); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	username := strings.TrimSpace(req.Username)
	if err := validateText("username", username, maxAccountUsernameRunes, false); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	note := strings.TrimSpace(req.Note)
	if err := validateServerNotes(note); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	loginScript := strings.TrimSpace(req.LoginScript)
	if err := validateLoginScript(loginScript); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	fieldsJSON, _, err := normalizeCustomFields(req.CustomFields)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}

	ctx := r.Context()
	if _, err := a.st.GetServer(ctx, serverID); err != nil {
		a.writeStoreError(w, r, err)
		return
	}

	var encrypted []byte
	if req.Password != "" {
		if len(req.Password) > maxAccountPasswordBytes {
			writeError(w, http.StatusBadRequest, codeInvalidArgument,
				fmt.Sprintf("password 不能超过 %d 字节", maxAccountPasswordBytes))
			return
		}
		if a.cipher == nil {
			writeError(w, http.StatusServiceUnavailable, codeInternal, "凭证加密未启用（缺少主密钥）")
			return
		}
		encrypted, err = a.cipher.Encrypt([]byte(req.Password))
		if err != nil {
			a.internalError(w, r, fmt.Errorf("加密访问账号密码失败: %w", err))
			return
		}
	}

	now := a.nowMS()
	item := &store.ServerAccount{
		ID:               uuid.NewString(),
		ServerID:         serverID,
		Name:             name,
		Address:          address,
		Username:         username,
		PasswordEnc:      encrypted,
		Note:             note,
		CustomFieldsJSON: fieldsJSON,
		LoginScript:      loginScript,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := a.st.CreateServerAccount(ctx, item); err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, a.serverAccountPayload(*item))
}

// handlePatchServerAccount 处理 PATCH /api/v1/servers/{serverID}/accounts/{accountID}。
func (a *API) handlePatchServerAccount(w http.ResponseWriter, r *http.Request) {
	serverID := strings.TrimSpace(chi.URLParam(r, "serverID"))
	accountID := strings.TrimSpace(chi.URLParam(r, "accountID"))
	if serverID == "" || accountID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器或账号 ID")
		return
	}
	var req patchServerAccountRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	if req.Name == nil && req.Address == nil && req.Username == nil && req.Password == nil &&
		req.Note == nil && req.CustomFields == nil && req.LoginScript == nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			"至少需要提供 name/address/username/password/note/custom_fields/login_script 之一")
		return
	}

	upd := store.ServerAccountUpdate{UpdatedAt: a.nowMS()}
	if req.Name != nil {
		v := strings.TrimSpace(*req.Name)
		if err := validateText("name", v, maxServerNameRunes, true); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		upd.Name = &v
	}
	if req.Address != nil {
		v := strings.TrimSpace(*req.Address)
		if err := validateText("address", v, maxAccountAddressRunes, false); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		upd.Address = &v
	}
	if req.Username != nil {
		v := strings.TrimSpace(*req.Username)
		if err := validateText("username", v, maxAccountUsernameRunes, false); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		upd.Username = &v
	}
	if req.Note != nil {
		v := strings.TrimSpace(*req.Note)
		if err := validateServerNotes(v); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		upd.Note = &v
	}
	if req.LoginScript != nil {
		v := strings.TrimSpace(*req.LoginScript)
		if err := validateLoginScript(v); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		upd.LoginScript = &v
	}
	if req.CustomFields != nil {
		encoded, _, err := normalizeCustomFields(*req.CustomFields)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		upd.CustomFieldsJSON = &encoded
	}
	if req.Password != nil {
		upd.PasswordSet = true
		if *req.Password != "" {
			if len(*req.Password) > maxAccountPasswordBytes {
				writeError(w, http.StatusBadRequest, codeInvalidArgument,
					fmt.Sprintf("password 不能超过 %d 字节", maxAccountPasswordBytes))
				return
			}
			if a.cipher == nil {
				writeError(w, http.StatusServiceUnavailable, codeInternal, "凭证加密未启用（缺少主密钥）")
				return
			}
			encrypted, err := a.cipher.Encrypt([]byte(*req.Password))
			if err != nil {
				a.internalError(w, r, fmt.Errorf("加密访问账号密码失败: %w", err))
				return
			}
			upd.PasswordEnc = encrypted
		}
	}

	updated, err := a.st.UpdateServerAccount(r.Context(), serverID, accountID, upd)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, a.serverAccountPayload(*updated))
}

// handleDeleteServerAccount 处理 DELETE /api/v1/servers/{serverID}/accounts/{accountID}。
func (a *API) handleDeleteServerAccount(w http.ResponseWriter, r *http.Request) {
	serverID := strings.TrimSpace(chi.URLParam(r, "serverID"))
	accountID := strings.TrimSpace(chi.URLParam(r, "accountID"))
	if serverID == "" || accountID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器或账号 ID")
		return
	}
	if err := a.st.DeleteServerAccount(r.Context(), serverID, accountID); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"deleted": true})
}

// handleServerAccountSecret 处理 GET .../accounts/{accountID}/secret：
// 返回明文密码并写审计（与 GET /servers/{id}/access 同一安全模型）。
func (a *API) handleServerAccountSecret(w http.ResponseWriter, r *http.Request) {
	serverID := strings.TrimSpace(chi.URLParam(r, "serverID"))
	accountID := strings.TrimSpace(chi.URLParam(r, "accountID"))
	if serverID == "" || accountID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器或账号 ID")
		return
	}
	item, ok := a.loadServerAccount(w, r, serverID, accountID)
	if !ok {
		return
	}
	if len(item.PasswordEnc) == 0 {
		writeError(w, http.StatusConflict, codeConflict, "该访问账号未保存密码")
		return
	}
	if a.cipher == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "凭证加密未启用（缺少主密钥）")
		return
	}
	plain, err := a.cipher.Decrypt(item.PasswordEnc)
	if err != nil {
		a.internalError(w, r, fmt.Errorf("解密访问账号 %s 的密码失败: %w", accountID, err))
		return
	}
	a.auditManual(r, auditActionServerAccountAccess, "server_account", accountID,
		fmt.Sprintf(`{"server_id":%q}`, serverID))
	writeData(w, http.StatusOK, serverAccountSecretPayload{Password: string(plain)})
}

// handleServerAccountLogin 处理 POST .../accounts/{accountID}/login：
// 在目标机执行该账号配置的一键登录脚本，按 JSON 返回 url/terminal/output。
func (a *API) handleServerAccountLogin(w http.ResponseWriter, r *http.Request) {
	serverID := strings.TrimSpace(chi.URLParam(r, "serverID"))
	accountID := strings.TrimSpace(chi.URLParam(r, "accountID"))
	if serverID == "" || accountID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器或账号 ID")
		return
	}
	item, ok := a.loadServerAccount(w, r, serverID, accountID)
	if !ok {
		return
	}
	script := strings.TrimSpace(item.LoginScript)
	if script == "" {
		writeError(w, http.StatusConflict, codeConflict, "该访问账号未配置登录脚本")
		return
	}
	runner, ok := a.deploy.(loginScriptRunner)
	if !ok {
		writeUnavailable(w, "发布执行器未启用，无法执行登录脚本")
		return
	}
	raw, err := runner.RunLoginScript(r.Context(), serverID, script)
	if err != nil {
		if errors.Is(err, errOrchestratorDisabled) {
			writeUnavailable(w, "发布执行器未启用，无法执行登录脚本")
			return
		}
		a.writeRemoteError(w, r, err, "执行登录脚本")
		return
	}
	payload := parseServerLoginOutput(serverID, raw)
	a.setAuditDetail(r, auditActionServerAccountLogin,
		fmt.Sprintf(`{"server_id":%q,"account_id":%q,"script_bytes":%d,"has_url":%t}`,
			serverID, accountID, len(script), payload.URL != ""))
	writeData(w, http.StatusOK, serverAccountLoginPayload{
		AccountID:          accountID,
		serverLoginPayload: payload,
	})
}
