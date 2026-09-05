// 2026-08-23 hzx-2 audit: focused credential state reset endpoint.
//
// Background: the 2026-08-15 force_enable handler (see
// PATCH /api/routing/emergency-repair) covers the full reset chain but is
// overloaded: 4 actions × optional raw_model × optional reason × multiple
// side-effects (force_disable, clear_circuit, reset_errors, force_enable).
// Operators hitting it with the wrong payload got 4xx or partial resets.
//
// POST /api/routing/credentials/{id}/reset-state is a focused shortcut:
//   - Mandatory body { reason, raw_model? }
//   - Always runs the full DB + in-memory + URSM v2 reset chain
//   - Writes audit log (routing_audit_log via h.logAudit)
//   - Increments metrics.RoutingCredentialResetTotal per surface
//   - Optional trigger: probes (NodeProbeWorker.Submit) so the operator
//     can request an immediate self-check after reset
//
// Authorization: super_admin only (see handler.go registration).
package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	met "github.com/kaixuan/llm-gateway-go/metrics" //nolint:depguard // routing credential observability
)

// routingResetStateRequest is the body for POST /api/routing/credentials/{id}/reset-state.
type routingResetStateRequest struct {
	Reason string `json:"reason"`
	// RawModel is optional. Empty string means whole-credential reset
	// (covers every binding model of the credential).
	RawModel string `json:"raw_model,omitempty"`
	// TriggerProbe kicks off NodeProbeWorker.Submit for the (cred, raw_model)
	// pair after the reset completes, so the operator doesn't have to wait
	// for the next 5-min cycle to see whether the upstream is back. No-op
	// when the probe submitter is nil (older builds / tests).
	TriggerProbe bool `json:"trigger_probe,omitempty"`
}

// handleResetCredentialState is the hot-fix endpoint for the hzx-2 audit.
//
// It runs the same applyForceEnable chain the emergency-repair force_enable
// action runs, but with a smaller surface (no other actions to confuse with),
// mandatory reason (audit trail), and optional immediate probe submission.
//
// On success the response mirrors the force_enable beforeAfter map plus a
// human-friendly `message` so on-call engineers can grep their curl output.
func (h *Handler) handleResetCredentialState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	credID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || credID <= 0 {
		writeError(w, http.StatusBadRequest, "id path param must be a positive integer")
		return
	}

	var req routingResetStateRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if req.Reason == "" {
		writeError(w, http.StatusBadRequest, "reason is required for audit trail")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// Look up tenant for URSM v2 scope. Mirrors the emergency-repair handler.
	ursmTenantID := ""
	if h.ursmV2 != nil {
		if err := h.db.QueryRow(ctx,
			"SELECT COALESCE(tenant_id, '') FROM credentials WHERE id = $1",
			credID,
		).Scan(&ursmTenantID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "credential not found")
			} else {
				writeError(w, http.StatusInternalServerError, "tenant lookup failed: "+err.Error())
			}
			return
		}
	}

	actor := r.RemoteAddr
	if auth := GetAuthContext(r); auth != nil && auth.Username != "" {
		actor = auth.Username
	}

	beforeAfter := map[string]any{
		"credential_id": credID,
		"raw_model":     req.RawModel,
		"reason":        req.Reason,
		"endpoint":      "reset-state",
		"actor":         actor,
	}

	if err := h.applyForceEnable(ctx, credID, req.RawModel, req.Reason, actor, ursmTenantID, beforeAfter); err != nil {
		if errors.Is(err, errForceEnableCredNotFound) {
			writeError(w, http.StatusNotFound, "credential not found")
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	// Optional: trigger an immediate self-check probe so we don't wait for
	// the next 5-min NodeProbeWorker cycle to learn whether the upstream
	// has actually recovered. Best-effort; NodeProbeWorker.Submit is
	// fire-and-forget so we count the call but don't propagate errors.
	if req.TriggerProbe && h.probeSubmitter != nil {
		for _, m := range extractModels(req.RawModel) {
			h.probeSubmitter(credID, m, ursmTenantID, actor)
			met.RoutingAutoHealSubmitTotal.WithLabelValues("submitted").Inc()
		}
	}

	// Always-on follow-up: kick off the bg.CredentialAutoHealWorker for
	// this credential so the next probe cycle has a fresh trigger even
	// when the operator didn't pass trigger_probe=true. Skipped when the
	// worker hasn't been initialized (older builds / tests).
	if h.autoHealOneShot != nil {
		if n := h.autoHealOneShot(ctx, credID); n > 0 {
			beforeAfter["auto_heal_pairs_submitted"] = n
		}
	}

	// Audit. Same channel as emergency-repair; consumers can filter by
	// endpoint == "reset-state" in beforeAfter.endpoint.
	h.logAudit(r, "routing_credential_reset_state", beforeAfter)

	writeJSON(w, http.StatusOK, map[string]any{
		"message":         "credential state reset",
		"credential_id":   credID,
		"raw_model":       req.RawModel,
		"actor":           actor,
		"probe_triggered": req.TriggerProbe && h.probeSubmitter != nil,
		"details":         beforeAfter,
	})
}

// extractModels returns the list of (cred, raw_model) pairs the reset
// actually covered, for downstream probe submission. When rawModel is empty
// (whole-credential reset) we don't re-derive the binding list here — the
// NodeProbeWorker.Submit path for whole-cred is intentionally left to the
// bg/credential_autoheal worker (Day 3 deliverable). Today the operator
// can pass raw_model explicitly or rely on the next autoheal tick.
func extractModels(rawModel string) []string {
	if rawModel != "" {
		return []string{rawModel}
	}
	return []string{}
}