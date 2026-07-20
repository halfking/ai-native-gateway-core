package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// validModalities is the canonical allow-list for models_canonical.modality.
// Mirrors the SQL CHECK constraint models_canonical_modality_check:
//
//	CHECK (modality = ANY (ARRAY['text','vision','audio','multimodal','embedding']))
//
// Keep this in sync with sql/schema/01-schema.sql line ~2520.
var validModalities = map[string]bool{
	"text":       true,
	"vision":     true,
	"audio":      true,
	"multimodal": true,
	"embedding":  true,
}

// updateModelModality handles PATCH /api/models/:id/modality.
//
// Body: {"modality": "vision", "reason": "manual override"}
//
// Effect: Updates models_canonical.modality to the provided value (within
// the SQL CHECK allow-list) and records a structured audit log entry.
//
// Layer 3 of the modality-detection pipeline:
//
//	Layer 1 (modelname.InferModality): zero-cost rule-based seed.
//	Layer 2 (bg.ProbeModality):        lightweight upstream verification.
//	Layer 3 (this handler):             super_admin manual override.
//
// Use case: when rule inference + probe verification both misclassify a
// model (e.g. a new experimental model not in any pattern table), a
// super_admin can correct the modality manually.
func (h *Handler) updateModelModality(w http.ResponseWriter, r *http.Request, id int) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	// Write operations require super_admin (matches updateModel / tag handlers).
	if RequireSuperAdminForWrite(w, r) {
		return
	}

	var req struct {
		Modality string `json:"modality"`
		Reason   string `json:"reason,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if req.Modality == "" {
		writeError(w, http.StatusBadRequest, "modality field required")
		return
	}
	if !validModalities[req.Modality] {
		writeError(w, http.StatusBadRequest,
			"invalid modality: must be one of text/vision/audio/multimodal/embedding")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Verify the model exists and capture the old modality for the audit trail.
	var oldModality string
	err := h.db.QueryRow(ctx,
		`SELECT COALESCE(modality, '') FROM models_canonical WHERE id = $1`, id).
		Scan(&oldModality)
	if err != nil {
		writeError(w, http.StatusNotFound, "model not found: id="+strconv.Itoa(id))
		return
	}

	// Update modality. RETURNING gives us the new value without a second round-trip.
	var newModality string
	err = h.db.QueryRow(ctx,
		`UPDATE models_canonical
		 SET modality = $1
		 WHERE id = $2
		 RETURNING modality`, req.Modality, id).Scan(&newModality)
	if err != nil {
		slog.Error("update modality failed",
			"model_id", id,
			"requested_modality", req.Modality,
			"error", err)
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}

	// Structured audit log — surfaces in journald and correlates with any
	// downstream routing anomalies triggered by the change.
	slog.Info("modality override applied",
		"model_id", id,
		"old_modality", oldModality,
		"new_modality", newModality,
		"reason", req.Reason,
		"remote_addr", r.RemoteAddr)

	writeJSON(w, http.StatusOK, map[string]any{
		"id":           id,
		"modality":     newModality,
		"old_modality": oldModality,
		"reason":       req.Reason,
		"updated_at":   time.Now().UTC().Format(time.RFC3339),
	})
}
