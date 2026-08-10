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
		SELECT ck.kid_index, COALESCE(ck.label,''), ck.status, ck.secret_ciphertext,
		       ck.last_used_at, ck.last_failed_at, ck.created_at
		FROM credential_keys ck
		JOIN credentials c ON c.id = ck.credential_id
		WHERE ck.credential_id = $1 AND c.provider_id = $2
		ORDER BY ck.kid_index
	`, credID, providerID)
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

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "begin transaction failed")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // harmless after successful Commit

	var credTenant string
	err = tx.QueryRow(ctx, `
		SELECT tenant_id
		FROM credentials
		WHERE id = $1 AND provider_id = $2
		FOR UPDATE
	`, credID, providerID).Scan(&credTenant)
	if err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}

	// Find the next free kid_index while holding a row lock on the parent
	// credential. This avoids MAX(kid_index)+1 races where concurrent POSTs can
	// select the same kid and silently overwrite each other.
	var maxKid int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(kid_index), 0) FROM credential_keys WHERE credential_id = $1
	`, credID).Scan(&maxKid); err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	newKid := maxKid + 1

	_, err = tx.Exec(ctx, `
		INSERT INTO credential_keys (credential_id, kid_index, secret_ciphertext, status, label, tenant_id)
		VALUES ($1, $2, $3, 'active', NULLIF($4,''), $5)
	`, credID, newKid, encrypted, req.Label, credTenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "insert failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "commit failed")
		return
	}

	// invalidate candidate cache and reset in-memory rotator so the next request
	// re-enriches/rebuilds from the current DB key set.
	provider.InvalidateCandidateCacheForCredential(credID)
	provider.ResetKeyRotatorForCredential(credID)
	writeJSON(w, http.StatusOK, map[string]any{"kid_index": newKid, "message": "ok"})
}

// deleteCredentialKey removes an extra key by kid_index.
func (h *Handler) deleteCredentialKey(w http.ResponseWriter, r *http.Request, providerID, credID, kid int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.db.Exec(ctx, `
		DELETE FROM credential_keys ck
		USING credentials c
		WHERE ck.credential_id = c.id AND c.provider_id = $1 AND ck.credential_id = $2 AND ck.kid_index = $3
	`, providerID, credID, kid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	provider.InvalidateCandidateCacheForCredential(credID)
	provider.ResetKeyRotatorForCredential(credID)
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
		UPDATE credential_keys ck
		SET status = $4
		FROM credentials c
		WHERE ck.credential_id = c.id AND c.provider_id = $1 AND ck.credential_id = $2 AND ck.kid_index = $3
	`, providerID, credID, kid, req.Status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	// Invalidate candidate cache and reset the shared in-memory KeyRotator. Cache
	// invalidation alone does not clear per-key terminal/invalid state.
	provider.InvalidateCandidateCacheForCredential(credID)
	provider.ResetKeyRotatorForCredential(credID)
	slog.Info("credential key status reset",
		"credential_id", credID, "kid_index", kid, "status", req.Status)
	writeJSON(w, http.StatusOK, map[string]any{"message": "updated", "status": req.Status})
}
