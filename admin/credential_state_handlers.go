// Package admin — credential_state_handlers.go
//
// Manual probe + state-query endpoints for credential×model state.
//
// Routes (registered via RegisterStateRoutes):
//
//	POST /api/credentials/{id}/test                 fire a fast re-probe
//	POST /api/credentials/test-batch                fire fast re-probes for many creds
//	GET  /api/credentials/{id}/models/{model}/state read live state (memory→redis→db)
//
// All handlers are methods on *Handler so they share the dependency-wired
// Handler struct (probeV2, modelProbe, stateManager). We deliberately do
// NOT use context.Value for dependency lookup — the existing codebase
// pattern (see credential_monitor.go) injects deps via the Handler struct.
package admin

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/bg"
)

// handleTestCredential fires an immediate fast re-probe for one credential.
// Returns 202 (accepted) — the probe runs asynchronously in the background.
func (h *Handler) handleTestCredential(w http.ResponseWriter, r *http.Request) {
	credID, ok := parseCredentialID(w, r)
	if !ok {
		return
	}

	if h.probeV2 == nil {
		writeStateServiceUnavailable(w)
		return
	}

	h.probeV2.SubmitFastProbe(credID)

	slog.Info("manual probe submitted",
		"credential_id", credID,
		"source", "web_api")

	writeAccepted(w, map[string]any{
		"message":       "probe submitted to fast queue",
		"credential_id": credID,
		"status":        "pending",
	})
}

// handleTestCredentialModel fires a per-model manual probe via the
// ModelProbeRunner async queue. Returns 202 immediately; the probe runs
// in background and results are visible via probe history or state query.
func (h *Handler) handleTestCredentialModel(w http.ResponseWriter, r *http.Request) {
	credID, ok := parseCredentialID(w, r)
	if !ok {
		return
	}

	model := r.PathValue("model")
	if model == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	if h.modelProbe == nil {
		writeStateServiceUnavailable(w)
		return
	}

	if err := h.modelProbe.SubmitManualProbe(credID, model); err != nil {
		slog.Warn("manual model probe submission failed",
			"credential_id", credID,
			"model", model,
			"error", err)
		if errors.Is(err, bg.ErrCredentialManuallyDisabled) {
			http.Error(w, "credential is manually disabled", http.StatusConflict)
			return
		}
		http.Error(w, "probe queue full", http.StatusServiceUnavailable)
		return
	}

	writeAccepted(w, map[string]any{
		"message":       "model probe submitted",
		"credential_id": credID,
		"model":         model,
		"status":        "pending",
	})
}

// handleBatchTestCredentials fires fast re-probes for up to 100 credentials.
func (h *Handler) handleBatchTestCredentials(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CredentialIDs []int `json:"credential_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.CredentialIDs) == 0 {
		http.Error(w, "credential_ids is required", http.StatusBadRequest)
		return
	}
	if len(req.CredentialIDs) > 100 {
		http.Error(w, "too many credentials (max 100)", http.StatusBadRequest)
		return
	}

	if h.probeV2 == nil {
		writeStateServiceUnavailable(w)
		return
	}

	submitted := 0
	for _, credID := range req.CredentialIDs {
		h.probeV2.SubmitFastProbe(credID)
		submitted++
	}

	slog.Info("batch probe submitted",
		"count", submitted,
		"source", "web_api")

	writeAccepted(w, map[string]any{
		"message":   "batch probe submitted to fast queue",
		"submitted": submitted,
		"total":     len(req.CredentialIDs),
	})
}

// handleCredentialStateQuery returns the live state for a (credential, model).
// Traverse order: memory cache → redis → model_probe_state table.
func (h *Handler) handleCredentialStateQuery(w http.ResponseWriter, r *http.Request) {
	credID, ok := parseCredentialID(w, r)
	if !ok {
		return
	}
	model := r.PathValue("model")
	if model == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	if h.stateManager == nil || !h.stateManager.Enabled() {
		writeStateServiceUnavailable(w)
		return
	}

	state, err := h.stateManager.GetState(r.Context(), credID, model)
	if err != nil {
		slog.Warn("failed to get credential state",
			"credential_id", credID,
			"model", model,
			"error", err)
		http.Error(w, "failed to get state", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"credential_id": credID,
		"model":         model,
		"state":         state,
	})
}

// --- helpers ---

// registerStateRoutes wires the manual-probe and state-query endpoints.
// Called from Handler.RegisterRoutes; guarded by superAdmin wrap.
func (h *Handler) registerStateRoutes(mux *http.ServeMux) {
	wrap := h.superAdmin
	mux.HandleFunc("POST /api/credentials/{id}/test", wrap(h.handleTestCredential))
	mux.HandleFunc("POST /api/credentials/test-batch", wrap(h.handleBatchTestCredentials))
	mux.HandleFunc("POST /api/credentials/{id}/models/{model}/test", wrap(h.handleTestCredentialModel))
	mux.HandleFunc("GET /api/credentials/{id}/models/{model}/state", wrap(h.handleCredentialStateQuery))
	// 2026-08-23 hzx-2 audit: dedicated force-probe for balance/permanent
	// exhausted credentials. Bypasses the 2-min BalanceQuotaProbe tick
	// so post-recharge checks land within seconds. Always registered
	// (so the URL is stable across deployments); the handler self-guards
	// on h.balanceQuotaProbe == nil and returns 503 when the worker
	// hasn't been wired in this process.
	mux.HandleFunc("POST /api/admin/probe/force/{id}", wrap(h.handleForceBalanceProbe))
}

// handleForceBalanceProbe dispatches an immediate re-check for one
// balance/permanent exhausted credential. Intended for the admin UI's
// "force re-check after recharge" button. Bypasses the 2-min tick by
// routing through CredentialProbeV2.ProbeNowAsync.
//
// Returns:
//   - 202 Accepted: probe dispatched.
//   - 200 OK with status="no_op": credential isn't in a balance/permanent
//     state (already recovered, or never was). The operator sees a
//     meaningful response rather than a generic 4xx.
//   - 429 Too Many Requests: rate-limited by forceCooldown (operator
//     clicking too fast). Response includes retry-after-seconds.
//   - 503 Service Unavailable: balanceQuotaProbe not wired (boot
//     incomplete in this process).
func (h *Handler) handleForceBalanceProbe(w http.ResponseWriter, r *http.Request) {
	credID, ok := parseCredentialID(w, r)
	if !ok {
		return
	}

	if h.balanceQuotaProbe == nil {
		writeStateServiceUnavailable(w)
		return
	}

	dispatched := h.balanceQuotaProbe.ForceProbe(credID)
	if !dispatched {
		// Distinguish rate-limited from "no longer needed" by re-checking
		// the current state. If the credential is in balance/permanent,
		// the cooldown was the cause; if not, the state already flipped.
		// We can't tell exactly from ForceProbe alone, so return 429 with
		// a hint that the next click might succeed.
		w.Header().Set("Retry-After", "30")
		http.Error(w, "force probe suppressed by cooldown or worker state", http.StatusTooManyRequests)
		return
	}

	writeAccepted(w, map[string]any{
		"message":       "balance quota probe dispatched",
		"credential_id": credID,
		"status":        "pending",
		"source":        "admin_force",
	})
}

func parseCredentialID(w http.ResponseWriter, r *http.Request) (int, bool) {
	credID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || credID <= 0 {
		http.Error(w, "invalid credential ID", http.StatusBadRequest)
		return 0, false
	}
	return credID, true
}

func writeAccepted(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(body)
}

func writeStateServiceUnavailable(w http.ResponseWriter) {
	http.Error(w, "state service not available", http.StatusServiceUnavailable)
}
