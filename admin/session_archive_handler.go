package admin

import (
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domains/sessionarchive"
)

// SessionArchiveHandler handles session_summaries archival operations (M5).
type SessionArchiveHandler struct {
	archiver *sessionarchive.Archiver
}

// NewSessionArchiveHandler creates a new handler.
func NewSessionArchiveHandler(archiver *sessionarchive.Archiver) *SessionArchiveHandler {
	return &SessionArchiveHandler{archiver: archiver}
}

// RegisterRoutes registers session archive routes.
func (h *SessionArchiveHandler) RegisterRoutes(mux *http.ServeMux, adminWrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/session-archive/trigger", adminWrap(h.handleTrigger))
	mux.HandleFunc("/api/admin/session-archive/stats", adminWrap(h.handleStats))
}

// handleTrigger triggers manual archival of old inactive summaries.
//
// POST /api/admin/session-archive/trigger
//
// Response:
//
//	{
//	  "archived_count": 1234,
//	  "cutoff": "2025-12-08T12:34:56Z",
//	  "duration_ms": 456
//	}
func (h *SessionArchiveHandler) handleTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "only POST is supported"})
		return
	}

	result, err := h.archiver.Archive(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "archival failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"archived_count": result.ArchivedCount,
		"cutoff":         result.Cutoff.Format("2006-01-02T15:04:05Z07:00"),
		"duration_ms":    result.Duration.Milliseconds(),
	})
}

// handleStats returns current archival statistics.
//
// GET /api/admin/session-archive/stats
//
// Response:
//
//	{
//	  "total_summaries": 100000,
//	  "archived_summaries": 45000,
//	  "active_summaries": 55000,
//	  "archival_rate": 0.45
//	}
func (h *SessionArchiveHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "only GET is supported"})
		return
	}

	stats, err := h.archiver.GetStats(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "stats query failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total_summaries":    stats.TotalSummaries,
		"archived_summaries": stats.ArchivedSummaries,
		"active_summaries":   stats.ActiveSummaries,
		"archival_rate":      stats.ArchivalRate,
	})
}
