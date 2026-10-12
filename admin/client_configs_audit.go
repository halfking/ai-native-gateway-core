package admin

import "net/http"

type clientConfigAuditRequest struct {
	Action     string `json:"action"`
	Tool       string `json:"tool"`
	OS         string `json:"os"`
	KeyID      int    `json:"key_id"`
	ModelCount int    `json:"model_count"`
	ModelScope string `json:"model_scope"`
}

func (h *Handler) handleClientConfigAudit(w http.ResponseWriter, r *http.Request) {
	if !requireUserManager(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req clientConfigAuditRequest
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if message := validateClientConfigAudit(req); message != "" {
		writeError(w, http.StatusBadRequest, message)
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if !h.assertKeyTenantScope(w, r, req.KeyID) {
		return
	}
	details := map[string]any{
		"tool":        req.Tool,
		"os":          req.OS,
		"key_id":      req.KeyID,
		"model_count": req.ModelCount,
		"model_scope": req.ModelScope,
		"action":      req.Action,
		"ip":          r.RemoteAddr,
		"user_agent":  r.UserAgent(),
	}
	h.writeAuditLog(r, "client_config_"+req.Action, "client_config", 0, details)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func validateClientConfigAudit(entry clientConfigAuditRequest) string {
	validActions := map[string]bool{"generate": true, "download_script": true, "copy_config": true, "manual_view": true}
	validTools := map[string]bool{"zcode": true, "opencode": true, "minimax_code": true, "cursor": true, "cherry_studio": true, "roocode": true}
	validOS := map[string]bool{"macos": true, "windows": true, "linux": true}
	if !validActions[entry.Action] {
		return "invalid action"
	}
	if !validTools[entry.Tool] {
		return "invalid tool"
	}
	if !validOS[entry.OS] {
		return "invalid os"
	}
	if entry.KeyID <= 0 || entry.ModelCount <= 0 || (entry.ModelScope != "featured" && entry.ModelScope != "all") {
		return "invalid key or model scope"
	}
	return ""
}
