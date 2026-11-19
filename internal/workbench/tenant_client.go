package workbench

import (
	"net/http"

	"github.com/sylvinus/agent-vm/internal/remotehost"
)

func tenantClientRequest(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Portal   string `json:"portal"`
		Username string `json:"username"`
		Password string `json:"password"`
		Name     string `json:"name"`
		SystemID string `json:"system_id"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	password := []byte(input.Password)
	input.Password = ""
	defer clear(password)
	w.Header().Set("Cache-Control", "no-store")
	ctx := r.Context()
	switch path {
	case "tenant/login":
		catalog, err := remotehost.BrowseTenant(ctx, input.Portal, input.Username, password)
		if err != nil {
			requestError(w, err)
			return
		}
		jsonReply(w, catalog)
	case "tenant/vms":
		vm, err := remotehost.CreateTenantVM(ctx, input.Portal, input.Username, password, input.Name, input.SystemID)
		if err != nil {
			requestError(w, err)
			return
		}
		jsonReply(w, map[string]any{"vm": vm})
	default:
		http.NotFound(w, r)
	}
}
