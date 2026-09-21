package admin

import (
	"net/http"
	"strconv"
	"strings"
)

// canManageCredentialSecrets is the single backend permission predicate for
// viewing or changing an upstream credential secret.
func canManageCredentialSecrets(r *http.Request) bool {
	auth := GetAuthContext(r)
	if auth == nil {
		return false
	}
	if auth.Role == "super_admin" || auth.Role == "admin_key" {
		return true
	}
	return auth.Role == "tenant_admin" && auth.TenantID == "default"
}

func (h *Handler) handleUnifiedCredentialSecrets(w http.ResponseWriter, r *http.Request) {
	if !canManageCredentialSecrets(r) {
		writeError(w, http.StatusForbidden, "credential secret management requires a default-tenant administrator")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		writeError(w, http.StatusBadRequest, "invalid credential path")
		return
	}
	credID, err := strconv.Atoi(parts[2])
	if err != nil || credID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid credential id")
		return
	}
	var providerID int
	var tenantID string
	if err := h.db.QueryRow(r.Context(), `
		SELECT c.provider_id, COALESCE(p.tenant_id, 'default')
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.id = $1 AND c.status <> 'deleted' AND p.deleted_at IS NULL
	`, credID).Scan(&providerID, &tenantID); err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	if IsTenantAdmin(r) && tenantID != "default" {
		writeError(w, http.StatusForbidden, "credential is outside the default tenant")
		return
	}
	switch parts[3] {
	case "reveal":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.revealCredential(w, r, providerID, credID)
	case "set-key":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.rotateCredentialPrimaryKeyWithOptions(w, r, providerID, credID, true)
	default:
		writeError(w, http.StatusNotFound, "unknown credential secret operation")
	}
}
