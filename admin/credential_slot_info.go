package admin

// credential_slot_info.go — V3.1 (2026-06-26) admin API for SlotInfoV3.
//
// GET /api/credentials/{id}/slots returns per-slot fingerprint + inflight details
// for admin dashboards. Uses the new SlotInfoV3 method from credentialfpslot.Manager.

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleCredentialSlots handles GET /api/credentials/{id}/slots
// V3.1 (2026-06-26): Returns detailed slot information for a credential.
func (h *Handler) handleCredentialSlots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Extract credential ID from path
	idStr := r.PathValue("id")
	if idStr == "" {
		// Fallback: try to extract from URL path manually
		// Expected pattern: /api/credentials/{id}/slots
		idStr = extractPathSegment(r.URL.Path, "/api/credentials/", "/slots")
	}
	
	credID, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid credential id")
		return
	}

	// Check if fp slot manager is available
	if h.fpSlots == nil || !h.fpSlots.Enabled() {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"credential_id": credID,
			"enabled":       false,
			"slots":         []interface{}{},
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Query credential's fp_slot_limit from database
	var fpSlotLimit *int
	err = h.db.QueryRow(ctx, `
		SELECT fp_slot_limit 
		FROM credentials 
		WHERE id = $1
	`, credID).Scan(&fpSlotLimit)
	
	if err != nil {
		slog.Error("failed to query credential fp_slot_limit", "cred_id", credID, "error", err)
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}

	// Get slot info from manager
	slots, err := h.fpSlots.SlotInfoV3(ctx, credID, fpSlotLimit)
	if err != nil {
		slog.Error("failed to get slot info", "cred_id", credID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to retrieve slot information")
		return
	}

	// Calculate summary stats
	totalSlots := len(slots)
	activeSlots := 0
	totalInflight := 0
	for _, s := range slots {
		if s.Holder != "" && !s.Expired {
			activeSlots++
		}
		totalInflight += s.Inflight
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"credential_id":   credID,
		"enabled":         true,
		"fp_slot_limit":   fpSlotLimit,
		"total_slots":     totalSlots,
		"active_slots":    activeSlots,
		"total_inflight":  totalInflight,
		"slots":           slots,
	})
}

// extractPathSegment is a helper to extract a path segment between two markers.
// Used as fallback when r.PathValue is not available (Go < 1.22).
func extractPathSegment(path, prefix, suffix string) string {
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	if suffix != "" && strings.HasSuffix(rest, suffix) {
		rest = strings.TrimSuffix(rest, suffix)
	}
	return rest
}
