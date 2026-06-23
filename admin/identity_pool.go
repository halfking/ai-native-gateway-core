package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// identity_pool.go — admin endpoints for the global identity pool cap.
//
// The identity pool implements Layer 0 of the three-layer architecture:
//   Layer 0 (this file)  — global cap on TOTAL distinct end-user identities
//   Layer 1 (credentialfpslot) — per-credential fingerprint pool size
//   Layer 2 (limiter) — per-credential in-flight REQUEST concurrency
//
// Admins can inspect the current cap and read live occupancy.
//
// Note: the actual Pool object lives in cmd/gateway/main.go and is wired in
// via Handler.SetIdentityPool. Until then, these endpoints return a 503.

func (h *Handler) getIdentityPoolStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.identityPool == nil {
		writeError(w, http.StatusServiceUnavailable, "identity pool not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	stats := h.identityPool.Stats(ctx)
	writeJSON(w, http.StatusOK, stats)
}

// setIdentityPoolMax updates the global identity cap. This is the
// system-wide total of distinct fingerprints allowed simultaneously.
func (h *Handler) setIdentityPoolMax(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.identityPool == nil {
		writeError(w, http.StatusServiceUnavailable, "identity pool not configured")
		return
	}

	var req struct {
		MaxIdentities *int `json:"max_identities"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.MaxIdentities == nil || *req.MaxIdentities < 0 {
		writeError(w, http.StatusBadRequest, "max_identities must be >= 0")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Persist to system_identity_pool so the value survives restart.
	newMax := *req.MaxIdentities
	if _, err := h.db.Exec(ctx, `
		INSERT INTO system_identity_pool (id, max_identities, updated_at)
		VALUES (1, $1, now())
		ON CONFLICT (id) DO UPDATE SET
			max_identities = EXCLUDED.max_identities,
			updated_at = now()
	`, newMax); err != nil {
		slog.Error("setIdentityPoolMax persist failed", "error", err)
		writeError(w, http.StatusInternalServerError, "persist failed: "+err.Error())
		return
	}

	// In-memory update for immediate effect (next Acquire uses new cap).
	h.identityPool.SetMaxIdentities(newMax)

	writeJSON(w, http.StatusOK, map[string]any{
		"max_identities": newMax,
		"message":        "updated",
	})
}

// ── Compatibility helpers ─────────────────────────────────────────────

// parseIdentityPoolMax is a small helper for callers that have a query string.
func parseIdentityPoolMax(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, nil
	}
	return n, nil
}
