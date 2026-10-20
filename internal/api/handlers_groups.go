package api

import (
	"net/http"

	"github.com/maliang/leadstack/internal/store"
)

// handleListGroups 处理 GET /api/v1/groups：由 servers 聚合出的分组名与数量（纳管 不建独立表）。
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
