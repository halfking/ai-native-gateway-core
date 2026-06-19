package admin

import (
	"encoding/json"
	"net/http"
)

type clientConfigAuditRequest struct {
	Action     string `json:"action"`
	Tool       string `json:"tool"`
	OS         string `json:"os"`
	KeyID      int    `json:"key_id"`
	ModelCount int    `json:"model_count"`
	ModelScope string `json:"model_scope"`
}

func (h *Handler) handleClientConfigAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	var req clientConfigAuditRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	validActions := map[string]bool{
		"generate":        true,
		"download_script": true,
		"copy_config":    true,
		"manual_view":     true,
	}
	validTools := map[string]bool{
		"zcode":         true,
		"opencode":      true,
		"cursor":        true,
		"cherry_studio": true,
		"roocode":       true,
	}
	validOS := map[string]bool{
		"macos":   true,
		"windows": true,
		"linux":   true,
	}

	if !validActions[req.Action] {
		writeError(w, http.StatusBadRequest, "invalid action")
		return
	}
	if !validTools[req.Tool] {
		writeError(w, http.StatusBadRequest, "invalid tool")
		return
	}
	if !validOS[req.OS] {
		writeError(w, http.StatusBadRequest, "invalid os")
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
	detailsJSON, _ := json.Marshal(details)
	h.writeAuditLog(r, "client_config_"+req.Action, "client_config", 0, string(detailsJSON))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
