package admin

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ContextWindowCalibrationHandler handles manual context window override
// (docs/omni-ref3 A4 Phase 1).
type ContextWindowCalibrationHandler struct {
	db *pgxpool.Pool
}

// NewContextWindowCalibrationHandler creates a new handler.
func NewContextWindowCalibrationHandler(db *pgxpool.Pool) *ContextWindowCalibrationHandler {
	return &ContextWindowCalibrationHandler{db: db}
}

// RegisterRoutes registers the context window calibration routes.
func (h *ContextWindowCalibrationHandler) RegisterRoutes(mux *http.ServeMux, adminWrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/models/context-window/", adminWrap(h.handleContextWindowCalibration))
}

// handleContextWindowCalibration handles PUT /api/admin/models/context-window/{canonical_id}
// to manually override a model's context window.
//
// Request body:
//
//	{
//	  "context_window": 128000,
//	  "source": "manual",
//	  "reason": "provider虚标，实测窗口为128k"
//	}
//
// Response:
//
//	{
//	  "canonical_id": 42,
//	  "canonical_name": "gpt-4-turbo",
//	  "old_value": 8192,
//	  "new_value": 128000,
//	  "source": "manual",
//	  "updated_at": "2026-08-07T12:34:56Z"
//	}
func (h *ContextWindowCalibrationHandler) handleContextWindowCalibration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "only PUT is supported"})
		return
	}

	// Extract canonical_id from path: /api/admin/models/context-window/{canonical_id}
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/models/context-window/")
	canonicalID, err := strconv.Atoi(path)
	if err != nil || canonicalID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid canonical_id"})
		return
	}

	// Parse request body
	var req struct {
		ContextWindow int    `json:"context_window"`
		Source        string `json:"source"` // manual, discovery, probe
		Reason        string `json:"reason"` // audit reason
	}
	if err := readJSONRequired(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}

	if req.ContextWindow <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "context_window must be positive"})
		return
	}

	if req.Source == "" {
		req.Source = "manual"
	}
	if req.Source != "manual" && req.Source != "discovery" && req.Source != "probe" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "source must be manual, discovery, or probe"})
		return
	}

	// Fetch current values
	var canonicalName string
	var oldValue *int
	err = h.db.QueryRow(r.Context(), `
		SELECT canonical_name, COALESCE(context_window_override, context_window)
		FROM models_canonical
		WHERE id = $1
	`, canonicalID).Scan(&canonicalName, &oldValue)
	if err != nil {
		slog.ErrorContext(r.Context(), "failed to fetch model", "canonical_id", canonicalID, "error", err)
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "model not found"})
		return
	}

	// Update override
	now := time.Now()
	_, err = h.db.Exec(r.Context(), `
		UPDATE models_canonical
		SET context_window_override = $2,
		    context_window_source = $3,
		    context_window_updated_at = $4
		WHERE id = $1
	`, canonicalID, req.ContextWindow, req.Source, now)
	if err != nil {
		slog.ErrorContext(r.Context(), "failed to update context_window_override", "canonical_id", canonicalID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "update failed"})
		return
	}

	// Audit log
	auditMsg := fmt.Sprintf("A4: context_window calibration for %s (id=%d): %v → %d (source=%s, reason=%s)",
		canonicalName, canonicalID, oldValue, req.ContextWindow, req.Source, req.Reason)
	slog.InfoContext(r.Context(), auditMsg,
		"canonical_id", canonicalID,
		"canonical_name", canonicalName,
		"old_value", oldValue,
		"new_value", req.ContextWindow,
		"source", req.Source,
		"reason", req.Reason,
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"canonical_id":   canonicalID,
		"canonical_name": canonicalName,
		"old_value":      oldValue,
		"new_value":      req.ContextWindow,
		"source":         req.Source,
		"updated_at":     now.Format(time.RFC3339),
	})
}
