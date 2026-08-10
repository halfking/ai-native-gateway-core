package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"log/slog"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// handleCredentialKeys manages the extra keys on a credential (migration 076).
//
//	GET    /api/providers/{id}/credentials/{cid}/keys
//	       → list extra keys (masked, never plaintext)
//	POST   /api/providers/{id}/credentials/{cid}/keys
//	       body: {"api_key": "...", "label": "..."} → add an extra key
//	DELETE /api/providers/{id}/credentials/{cid}/keys/{kid}
//	       → remove an extra key by kid_index
//	PATCH  /api/providers/{id}/credentials/{cid}/keys/{kid}
//	       body: {"status": "active"} → reset an invalid/terminal key to active
//
// The primary key (kid_index 0) lives in credentials.secret_ciphertext and is
// NOT managed here — it's revealed/rotated via the existing reveal/lifecycle
// endpoints. Only kid_index >= 1 (extras) are mutable here.
func (h *Handler) handleCredentialKeys(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	// Parse optional /keys/{kid} suffix from the URL to distinguish
	// collection ops (GET/POST) from item ops (DELETE/PATCH on {kid}).
	kidStr := ""
	if idx := strings.Index(r.URL.Path, "/keys/"); idx >= 0 {
		kidStr = r.URL.Path[idx+len("/keys/"):]
	}

	switch {
	case kidStr == "" && r.Method == http.MethodGet:
		h.listCredentialKeys(w, r, providerID, credID)
	case kidStr == "" && r.Method == http.MethodPost:
		h.addCredentialKey(w, r, providerID, credID)
	case kidStr != "" && r.Method == http.MethodDelete:
		kid, err := strconv.Atoi(kidStr)
		if err != nil || kid < 1 {
			writeError(w, http.StatusBadRequest, "invalid kid_index (must be >= 1)")
			return
		}
		h.deleteCredentialKey(w, r, providerID, credID, kid)
	case kidStr != "" && r.Method == http.MethodPatch:
		kid, err := strconv.Atoi(kidStr)
		if err != nil || kid < 1 {
			writeError(w, http.StatusBadRequest, "invalid kid_index (must be >= 1)")
			return
		}
		h.resetCredentialKey(w, r, providerID, credID, kid)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// listCredentialKeys returns the extra keys for a credential, masked.
func (h *Handler) listCredentialKeys(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT kid_index, COALESCE(label,''), status, secret_ciphertext,
		       last_used_at, last_failed_at, created_at
		FROM credential_keys
		WHERE credential_id = $1
		ORDER BY kid_index
	`, credID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	type keyInfo struct {
		KidIndex     int     `json:"kid_index"`
		Label        string  `json:"label"`
		Masked       string  `json:"masked"`
		Status       string  `json:"status"`
		LastUsedAt   *string `json:"last_used_at,omitempty"`
		LastFailedAt *string `json:"last_failed_at,omitempty"`
		CreatedAt    string  `json:"created_at"`
	}
	var keys []keyInfo
	for rows.Next() {
		var ki keyInfo
		var ciphertext []byte
		var lastUsed, lastFailed *time.Time
		var createdAt time.Time
		if err := rows.Scan(&ki.KidIndex, &ki.Label, &ki.Status, &ciphertext,
			&lastUsed, &lastFailed, &createdAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed: "+err.Error())
			return
		}
		// decrypt for masking only — never return plaintext
		if pt, _, derr := h.decryptCred(string(ciphertext)); derr == nil {
			ki.Masked = maskAPIKey(pt)
		}
		if lastUsed != nil {
			s := lastUsed.UTC().Format(time.RFC3339)
			ki.LastUsedAt = &s
		}
		if lastFailed != nil {
			s := lastFailed.UTC().Format(time.RFC3339)
			ki.LastFailedAt = &s
		}
		ki.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		keys = append(keys, ki)
	}
	if keys == nil {
		keys = []keyInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

// addCredentialKey appends a new extra key to a credential.
func (h *Handler) addCredentialKey(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	var req struct {
		APIKey string `json:"api_key"`
		Label  string `json:"label"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.APIKey = strings.TrimSpace(req.APIKey)
	if req.APIKey == "" {
		writeError(w, http.StatusBadRequest, "api_key required")
		return
	}

	encrypted, err := h.encryptCred([]byte(req.APIKey))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encryption failed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// find the next free kid_index (max + 1).
	var maxKid int
	_ = h.db.QueryRow(ctx, `
		SELECT COALESCE(MAX(kid_index), 0) FROM credential_keys WHERE credential_id = $1
	`, credID).Scan(&maxKid)
	newKid := maxKid + 1

	_, err = h.db.Exec(ctx, `
		INSERT INTO credential_keys (credential_id, kid_index, secret_ciphertext, status, label, tenant_id)
		VALUES ($1, $2, $3, 'active', NULLIF($4,''), public.get_current_tenant())
		ON CONFLICT (credential_id, kid_index) DO UPDATE SET secret_ciphertext = EXCLUDED.secret_ciphertext, status = 'active', label = EXCLUDED.label
	`, credID, newKid, encrypted, req.Label)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "insert failed: "+err.Error())
		return
	}

	// invalidate candidate cache so the next request re-enriches with the new key.
	provider.InvalidateCandidateCacheForCredential(credID)
	writeJSON(w, http.StatusOK, map[string]any{"kid_index": newKid, "message": "ok"})
}

// deleteCredentialKey removes an extra key by kid_index.
func (h *Handler) deleteCredentialKey(w http.ResponseWriter, r *http.Request, providerID, credID, kid int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.db.Exec(ctx, `
		DELETE FROM credential_keys WHERE credential_id = $1 AND kid_index = $2
	`, credID, kid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	provider.InvalidateCandidateCacheForCredential(credID)
	writeJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}

// resetCredentialKey marks an invalid/terminal key active again (e.g. after
// an operator tops up balance). Also best-effort resets the in-memory
// KeyRotator state via cache invalidation so the next request re-registers.
func (h *Handler) resetCredentialKey(w http.ResponseWriter, r *http.Request, providerID, credID, kid int) {
	var req struct {
		Status string `json:"status"`
	}
	// body optional; default to "active"
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Status == "" {
		req.Status = "active"
	}
	if req.Status != "active" && req.Status != "invalid" {
		writeError(w, http.StatusBadRequest, "status must be 'active' or 'invalid'")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.db.Exec(ctx, `
		UPDATE credential_keys SET status = $3 WHERE credential_id = $1 AND kid_index = $2
	`, credID, kid, req.Status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	// invalidate cache so the KeyRotator re-registers from fresh DB state
	// on the next request (EnsureCred + the rotator's in-memory state reset).
	provider.InvalidateCandidateCacheForCredential(credID)
	slog.Info("credential key status reset",
		"credential_id", credID, "kid_index", kid, "status", req.Status)
	writeJSON(w, http.StatusOK, map[string]any{"message": "updated", "status": req.Status})
}
