package api

import "net/http"

// onboardingResponse 是首次使用引导状态（契约第 47 节）。
type onboardingResponse struct {
	NeedsSetup     bool `json:"needs_setup"`
	HasServers     bool `json:"has_servers"`
	HasCredentials bool `json:"has_credentials"`
	HasChecks      bool `json:"has_checks"`
}

// handleOnboarding 处理 GET /api/v1/system/onboarding。
func (a *API) handleOnboarding(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	servers, err := a.st.ListServers(ctx)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	credentials, err := a.st.ListCredentials(ctx)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	checks, err := a.st.ListChecks(ctx)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	resp := onboardingResponse{
		HasServers:     len(servers) > 0,
		HasCredentials: len(credentials) > 0,
		HasChecks:      len(checks) > 0,
	}
	resp.NeedsSetup = !resp.HasServers
	writeData(w, http.StatusOK, resp)
}
