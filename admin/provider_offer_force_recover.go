package admin

// provider_offer_force_recover.go — extracted from providers.go (2026-06-22
// audit §3 single-file-bloat remediation, sixth cut). Owns the model_offer
// mutation endpoints and the credential-level force-recover / manual-disable
// controls.
//
// Endpoints (model offer cluster):
//   POST   /api/providers/{id}/offers                    (dispatched in handleProviderModelOffer)
//   GET    /api/providers/{id}/offers/{oid}             (also via dispatcher)
//   PATCH  /api/providers/{id}/offers/{oid}             updateModelOffer
//   GET    /api/providers/{id}/offers/{oid}/suggestions getModelOfferSuggestions
//   POST   /api/providers/{id}/offers/{oid}/toggle      toggleModelOfferState
//
// Endpoints (force-recover / manual-disable cluster):
//   POST   /api/providers/credentials/{cid}/force-recover     handleForceRecover
//   POST   /api/providers/{id}/credentials/{cid}/disable       setCredentialManualDisabled
//   POST   /api/providers/{id}/disable                        setProviderManualDisabled
//   POST   /api/providers/{id}/credentials/{cid}/probe-model  setDefaultProbeModel / pickDefaultProbeModel
//   GET    /api/providers/{id}/routable-summary               getRoutableSummary
//
// Helpers:
//   maxInt — integer max used by getRoutableSummary's health-score aggregation.
//
// Self-contained: only stdlib + same-package helpers (writeJSON / writeError /
// h.db / h.bgTasks / h.decryptCredStr / modelRowLite / upsertModelForProvider /
// updateCredHealth). No internal/* deps.

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

func (h *Handler) handleProviderModelOffer(w http.ResponseWriter, r *http.Request, providerID int, offerPath string) {
	if offerPath == "" {
		writeError(w, http.StatusBadRequest, "offer_id required")
		return
	}

	parts := splitPath(offerPath)
	offerID, err := strconv.Atoi(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid offer_id")
		return
	}

	if len(parts) > 1 {
		switch parts[1] {
		case "suggestions":
			h.getModelOfferSuggestions(w, r, providerID, offerID)
			return
		case "state":
			h.toggleModelOfferState(w, r, providerID, offerID)
			return
		}
	}

	if r.Method == http.MethodPatch {
		h.updateModelOffer(w, r, providerID, offerID)
	} else {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) updateModelOffer(w http.ResponseWriter, r *http.Request, providerID, offerID int) {
	var req struct {
		StandardizedName *string `json:"standardized_name"`
		CanonicalID      *int    `json:"canonical_id"`
		// OutboundModelName is the upstream-side model identifier — e.g. a
		// Volcano Ark endpoint ID like "ep-20241227XXXX".  When set, the
		// gateway uses this instead of raw_model_name when calling the
		// provider's chat completions / messages endpoint.  Pass an empty
		// string to clear it (revert to raw_model_name).
		OutboundModelName *string `json:"outbound_model_name"`
		// ContextWindow (522) calibrates this credential×model's context
		// window. nil/omitted = leave unchanged. A non-nil pointer sets the
		// override: a positive value replaces it, a zero/negative value
		// clears it (falls back to models_canonical). This is the per-credential
		// lever the operator needs when a provider's real context window
		// diverges from the standardized catalog value.
		ContextWindow        *int     `json:"context_window"`
		UnitPriceInPer1M     *float64 `json:"unit_price_in_per_1m"`
		UnitPriceOutPer1M    *float64 `json:"unit_price_out_per_1m"`
		CacheReadPricePer1M  *float64 `json:"cache_read_price_per_1m"`
		CacheWritePricePer1M *float64 `json:"cache_write_price_per_1m"`
		BillingMode          *string  `json:"billing_mode"`
	}

	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var currentID int
	var rawName string
	err := h.db.QueryRow(ctx, `
		SELECT mo.id, mo.raw_model_name FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		WHERE mo.id = $1 AND c.provider_id = $2
	`, offerID, providerID).Scan(&currentID, &rawName)
	if err != nil {
		writeError(w, http.StatusNotFound, "offer not found")
		return
	}

	if req.CanonicalID != nil {
		var canonName string
		err := h.db.QueryRow(ctx, `SELECT canonical_name FROM models_canonical WHERE id = $1`, *req.CanonicalID).Scan(&canonName)
		if err != nil {
			writeError(w, http.StatusBadRequest, "canonical model not found")
			return
		}
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE model_offers SET canonical_id = $1 WHERE id = $2`, *req.CanonicalID, offerID)
		if req.StandardizedName == nil {
			//nolint:errcheck // best-effort exec, non-critical
			h.db.Exec(ctx, `UPDATE model_offers SET standardized_name = $1 WHERE id = $2`, canonName, offerID)
			// 2026-06-19 audit: mirror the write to the underlying
			// provider_models table.  model_offers is a VIEW with an
			// INSTEAD OF UPDATE trigger that re-derives standardized_name
			// from raw_model_name on conflict, which was clobbering the
			// admin's manual edit ("一会就被刷新").  Writing both sides
			// keeps the view and the base table in lockstep.
			//nolint:errcheck // best-effort exec, non-critical
			h.db.Exec(ctx, `
				UPDATE provider_models
				SET standardized_name = $1
				WHERE id = (
					SELECT pm.id FROM provider_models pm
					JOIN model_offers mo ON mo.raw_model_name = pm.raw_model_name
					JOIN credentials c ON c.id = pm.provider_id AND c.id = mo.credential_id
					WHERE mo.id = $2
					LIMIT 1
				)
			`, canonName, offerID)
		}
	}

	if req.StandardizedName != nil {
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE model_offers SET standardized_name = $1 WHERE id = $2`, *req.StandardizedName, offerID)
		// 2026-06-19 audit: mirror to provider_models so the view's
		// INSTEAD OF UPDATE trigger cannot re-clobber the value.
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `
			UPDATE provider_models
			SET standardized_name = $1
			WHERE id = (
				SELECT pm.id FROM provider_models pm
				JOIN model_offers mo ON mo.raw_model_name = pm.raw_model_name
				JOIN credentials c ON c.id = pm.provider_id AND c.id = mo.credential_id
				WHERE mo.id = $2
				LIMIT 1
			)
		`, *req.StandardizedName, offerID)
	}

	// outbound_model_name is independent of canonical_id / standardized_name;
	// a model can have an endpoint ID without being mapped to a canonical row.
	// Use NULLIF('') so that an explicit empty string clears the column
	// (reverting to raw_model_name at query time).
	if req.OutboundModelName != nil {
		if _, err := h.db.Exec(ctx, `
			UPDATE model_offers
			SET outbound_model_name = NULLIF($1, ''),
			    updated_at = NOW()
			WHERE id = $2
		`, *req.OutboundModelName, offerID); err != nil {
			writeError(w, http.StatusInternalServerError, "update outbound_model_name failed: "+err.Error())
			return
		}
		slog.Info("model_offers.outbound_model_name updated",
			"offer_id", offerID,
			"raw_model_name", rawName,
			"provider_id", providerID,
			"new_value", *req.OutboundModelName,
		)
	}

	if req.UnitPriceInPer1M != nil || req.UnitPriceOutPer1M != nil || req.CacheReadPricePer1M != nil || req.CacheWritePricePer1M != nil || req.BillingMode != nil {
		if req.BillingMode != nil && *req.BillingMode != "" && !isValidBillingMode(*req.BillingMode) {
			writeError(w, http.StatusBadRequest, "invalid billing_mode")
			return
		}
		if _, err := h.db.Exec(ctx, `
				UPDATE credential_model_bindings
				SET unit_price_in_per_1m = COALESCE($1, unit_price_in_per_1m),
				    unit_price_out_per_1m = COALESCE($2, unit_price_out_per_1m),
				    cache_read_price_per_1m = COALESCE($3, cache_read_price_per_1m),
				    cache_write_price_per_1m = COALESCE($4, cache_write_price_per_1m),
				    billing_mode = COALESCE(NULLIF($5, ''), billing_mode), updated_at = now()
				WHERE id = $6
			`, req.UnitPriceInPer1M, req.UnitPriceOutPer1M, req.CacheReadPricePer1M, req.CacheWritePricePer1M, req.BillingMode, offerID); err != nil {
			writeError(w, http.StatusInternalServerError, "update pricing failed")
			return
		}
		invalidateRoutingCaches(r.Context(), h.db, "credential_model_bindings", offerID)
	}

	// credential_model_bindings row directly (not via the model_offers view)
	// because the INSTEAD OF UPDATE trigger uses COALESCE() and cannot express
	// "clear the override to NULL" — the only way to fall back to the canonical
	// value is to set the column to NULL, which COALESCE would mask. A
	// zero/negative context_window clears the override; a positive value sets
	// it. source is tagged 'manual' so discovery probes won't silently clobber
	// an operator decision.
	if req.ContextWindow != nil {
		if *req.ContextWindow > 0 {
			if _, err := h.db.Exec(ctx, `
				UPDATE credential_model_bindings
				SET context_window_override = $1,
				    context_window_source = 'manual',
				    context_window_updated_at = now(),
				    updated_at = now()
				WHERE id = $2
			`, *req.ContextWindow, offerID); err != nil {
				writeError(w, http.StatusInternalServerError, "update context_window failed: "+err.Error())
				return
			}
		} else {
			if _, err := h.db.Exec(ctx, `
				UPDATE credential_model_bindings
				SET context_window_override = NULL,
				    context_window_source = 'catalog',
				    context_window_updated_at = now(),
				    updated_at = now()
				WHERE id = $1
			`, offerID); err != nil {
				writeError(w, http.StatusInternalServerError, "clear context_window failed: "+err.Error())
				return
			}
		}
		slog.Info("credential_model_bindings.context_window_override updated",
			"offer_id", offerID,
			"raw_model_name", rawName,
			"provider_id", providerID,
			"context_window", *req.ContextWindow,
		)
		// 522/523 audit: a context-window override feeds the runtime candidate
		// SQL and therefore the compression trigger thresholds (mechanical
		// trim ThresholdBytes, session-compressor TOKEN trigger, 4xx
		// smart-window recovery cut point). Without this wakeup other gateway
		// instances keep a stale candCache and trim off the old window —
		// exactly the multi-layer-cache hazard the review flagged.
		// Migration 524 widens the DB trigger to NOTIFY on the column, but we
		// also fire an explicit NOTIFY here (matching the other manual-override
		// endpoints) so a missed DB-event path can't strand sibling processes.
		invalidateRoutingCaches(r.Context(), h.db, "credential_model_bindings", offerID)
	}

	var result struct {
		ID                    int      `json:"id"`
		RawModelName          string   `json:"raw_model_name"`
		StandardizedName      *string  `json:"standardized_name"`
		CanonicalID           *int     `json:"canonical_id"`
		CanonicalName         *string  `json:"canonical_name"`
		OutboundModelName     *string  `json:"outbound_model_name"`
		ContextWindow         *int     `json:"context_window"`
		ContextWindowOverride *int     `json:"context_window_override"`
		UnitPriceInPer1M      *float64 `json:"unit_price_in_per_1m"`
		UnitPriceOutPer1M     *float64 `json:"unit_price_out_per_1m"`
		CacheReadPricePer1M   *float64 `json:"cache_read_price_per_1m"`
		CacheWritePricePer1M  *float64 `json:"cache_write_price_per_1m"`
		BillingMode           *string  `json:"billing_mode"`
	}
	//nolint:errcheck // scan error non-critical
	h.db.QueryRow(ctx, `
		SELECT mo.id, mo.raw_model_name, mo.standardized_name, mo.canonical_id,
		       mc.canonical_name, mo.outbound_model_name,
		       COALESCE(mo.context_window_override, mc.context_window_override, mc.context_window) AS context_window,
		       mo.context_window_override, cmb.unit_price_in_per_1m, cmb.unit_price_out_per_1m,
		       cmb.cache_read_price_per_1m, cmb.cache_write_price_per_1m, cmb.billing_mode
		FROM model_offers mo
		LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
		LEFT JOIN credential_model_bindings cmb ON cmb.id = mo.id
		WHERE mo.id = $1
	`, offerID).Scan(&result.ID, &result.RawModelName, &result.StandardizedName,
		&result.CanonicalID, &result.CanonicalName, &result.OutboundModelName,
		&result.ContextWindow, &result.ContextWindowOverride, &result.UnitPriceInPer1M,
		&result.UnitPriceOutPer1M, &result.CacheReadPricePer1M, &result.CacheWritePricePer1M,
		&result.BillingMode)

	// 2026-06-19 audit: any PATCH that touches standardized_name /
	// canonical_id / outbound_model_name can change the data
	// /api/routing/available-models aggregates.  Invalidate the
	// process-wide cache so the next page render re-reads the DB.
	// 522: context_window also feeds the candidate trim threshold, so a
	// calibration change must flush the cache too.
	if req.CanonicalID != nil || req.StandardizedName != nil || req.OutboundModelName != nil || req.ContextWindow != nil {
		InvalidateAvailableModelsCache()
	}

	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) getModelOfferSuggestions(w http.ResponseWriter, r *http.Request, providerID, offerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var rawName string
	err := h.db.QueryRow(ctx, `
		SELECT mo.raw_model_name FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		WHERE mo.id = $1 AND c.provider_id = $2
	`, offerID, providerID).Scan(&rawName)
	if err != nil {
		writeError(w, http.StatusNotFound, "offer not found")
		return
	}

	rows, err := h.db.Query(ctx, `
		SELECT id, canonical_name, COALESCE(display_name,''), COALESCE(family,'')
		FROM models_canonical WHERE status = 'active' ORDER BY canonical_name
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type canonicalOption struct {
		ID            int    `json:"id"`
		CanonicalName string `json:"canonical_name"`
		DisplayName   string `json:"display_name"`
		Family        string `json:"family"`
	}
	options := make([]canonicalOption, 0)
	for rows.Next() {
		var o canonicalOption
		if err := rows.Scan(&o.ID, &o.CanonicalName, &o.DisplayName, &o.Family); err != nil {
			continue
		}
		options = append(options, o)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"offer_id":          offerID,
		"raw_model_name":    rawName,
		"rule_based":        rawName,
		"canonical_options": options,
	})
}

func (h *Handler) toggleModelOfferState(w http.ResponseWriter, r *http.Request, providerID, offerID int) {
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		Available bool `json:"available"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var credID int
	err := h.db.QueryRow(ctx, `
		SELECT mo.credential_id FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		WHERE mo.id = $1 AND c.provider_id = $2
	`, offerID, providerID).Scan(&credID)
	if err != nil {
		writeError(w, http.StatusNotFound, "offer not found")
		return
	}

	if req.Available {
		// Admin re-enable: clear unavailable_reason (so v_routable views the
		// binding as routable) and set admin_protected=TRUE so
		// expireStaleModels skips this binding on subsequent discovery runs.
		// The admin_protected column (added by 905) is the authoritative
		// protection flag — unavailable_reason is kept semantically clean.
		_, err = h.db.Exec(ctx, `
			UPDATE model_offers SET available = TRUE,
			unavailable_reason = NULL, unavailable_at = NULL,
			admin_protected = TRUE
			WHERE id = $1
		`, offerID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "update failed")
			return
		}
	} else {
		// Admin disable: set reason='manual' and clear protection.
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `
			UPDATE model_offers SET available = FALSE,
			unavailable_reason = 'manual', unavailable_at = now(),
			admin_protected = FALSE
			WHERE id = $1
		`, offerID)
	}

	//nolint:errcheck // best-effort exec, non-critical
	h.db.Exec(ctx, `
		UPDATE credentials SET availability_state = 'ready',
		availability_recover_at = NULL, state_reason_code = NULL,
		state_reason_detail = NULL, state_updated_at = now()
		WHERE id = $1 AND lifecycle_status = 'active'
		AND availability_state IN ('cooling', 'rate_limited', 'unreachable')
	`, credID)

	writeJSON(w, http.StatusOK, map[string]any{
		"message":   "offer state updated",
		"offer_id":  offerID,
		"available": req.Available,
	})
}

// pinAdminProtectedOffers flags the bindings for (credentialID, rawModelNames)
// as admin-protected so batch/auto paths (expireStaleModels, vendor re-fetch,
// probes, health checks, credential recovery) never update or expire them.
//
// The model_offers INSERT trigger propagates admin_protected on fresh rows;
// this call also covers the ON CONFLICT case where the row already existed
// unprotected (the trigger's conflict branch leaves admin_protected as-is).
func (h *Handler) pinAdminProtectedOffers(ctx context.Context, credentialID int, rawModelNames []string) {
	if h == nil || h.db == nil || len(rawModelNames) == 0 {
		return
	}
	if _, err := h.db.Exec(ctx, `
		UPDATE credential_model_bindings cmb
		SET admin_protected = TRUE
		FROM provider_models pm
		WHERE cmb.provider_model_id = pm.id
		  AND cmb.credential_id = $1
		  AND pm.raw_model_name = ANY($2)
	`, credentialID, rawModelNames); err != nil {
		slog.Warn("pin admin_protected failed",
			"credential_id", credentialID,
			"model_count", len(rawModelNames),
			"error", err)
	}
}

func (h *Handler) handleForceRecover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	path := r.URL.Path
	stripPrefix := "/api/providers/credentials/"
	remaining := path[len(stripPrefix):]

	parts := splitPath(remaining)
	if len(parts) < 2 || parts[1] != "force-recover" {
		http.NotFound(w, r)
		return
	}

	credID, err := strconv.Atoi(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid credential id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 2026-07-03 fix: Bug #14 - force-recover should also clear suspended/auth_failed state
	// When admin manually recovers a credential (e.g., after fixing revoked keys),
	// we need to reset availability_state to 'ready' so the background recovery
	// worker can pick it up (bg/credential_recovery.go skips suspended/auth_failed).
	tag, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET availability_state = CASE
		        WHEN availability_state IN ('suspended', 'auth_failed') THEN 'ready'
		        ELSE availability_state
		    END,
		    availability_recover_at = now() - INTERVAL '1 second',
		    state_reason_code = NULL,
		    state_reason_detail = 'admin force-recover',
		    state_updated_at = now()
		WHERE id = $1 AND lifecycle_status = 'active'
	`, credID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "credential not found or not active")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"triggered":     true,
		"credential_id": credID,
	})
}

// =============================================================================
// 900-series endpoints: Manual disable + default probe model
// Spec: docs/superpowers/specs/2026-06-12-credential-availability-audit-design.md
// =============================================================================

// setProviderManualDisabled toggles providers.manual_disabled
// Audit: writes a synthetic model_offer_events row with raw_model_name=NULL
// (provider-level disable does not target a specific model)
func (h *Handler) setProviderManualDisabled(w http.ResponseWriter, r *http.Request, providerID int) {
	var req struct {
		ManualDisabled bool   `json:"manual_disabled"`
		Reason         string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	// 2026-06-23: reason is required (matches handleSetManualDisabled).
	// The legacy 900-series endpoint previously accepted empty reasons
	// and produced audit rows like reason_detail="admin: " with no
	// business context, which made the minimax-prod-1 06-23 incident
	// root-cause work much harder. Validation runs before any DB work.
	if strings.TrimSpace(req.Reason) == "" {
		writeError(w, http.StatusBadRequest, "reason is required")
		return
	}
	actor := r.Header.Get("X-Admin-User")
	if actor == "" {
		actor = "admin"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.db.Exec(ctx, `
		UPDATE providers
		SET manual_disabled = $1, updated_at = now()
		WHERE id = $2 AND tenant_id = 'default'
	`, req.ManualDisabled, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}

	action := "disable"
	if !req.ManualDisabled {
		action = "enable"
	}
	reasonCode := "provider_manual_disabled"
	if !req.ManualDisabled {
		reasonCode = "provider_manual_enabled"
	}
	actorCopy := actor
	reqCopy := req
	//nolint:errcheck // best-effort exec, non-critical
	h.db.Exec(ctx, `
		INSERT INTO model_offer_events
		    (source, action, credential_id, provider_id, raw_model_name, reason_code, reason_detail)
		VALUES ('admin', $1, 0, $2, '', $3, $4)
	`, action, providerID, reasonCode, actorCopy+": "+reqCopy.Reason)

	// 2026-06-28: providers has no auto_route_refresh trigger at all, and
	// even if it did, the existing trg_notify_auto_route_creds does not
	// watch manual_disabled. Wake the listener + clear candCache so the
	// (re)enabled provider's credentials show up within ~5s rather than
	// waiting up to 5 min for the periodic refresh.
	invalidateRoutingCaches(r.Context(), h.db, "providers", providerID)

	writeJSON(w, http.StatusOK, map[string]any{
		"message":         "updated",
		"manual_disabled": req.ManualDisabled,
		"actor":           actor,
	})
}

// setCredentialManualDisabled toggles credentials.manual_disabled
// Audit: writes a model_offer_events row per affected binding (raw_model_name=” is
// treated as "applies to all models under this credential")
func (h *Handler) setCredentialManualDisabled(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	var req struct {
		ManualDisabled bool   `json:"manual_disabled"`
		Reason         string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	// 2026-06-23: reason is required (matches handleSetManualDisabled).
	// The legacy 900-series endpoint previously accepted empty reasons
	// and produced audit rows like reason_detail="admin: " with no
	// business context, which made the minimax-prod-1 06-23 incident
	// root-cause work much harder. Validation runs before any DB work.
	if strings.TrimSpace(req.Reason) == "" {
		writeError(w, http.StatusBadRequest, "reason is required")
		return
	}
	actor := r.Header.Get("X-Admin-User")
	if actor == "" {
		actor = "admin"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET manual_disabled = $1, updated_at = now()
		WHERE id = $2 AND provider_id = $3
	`, req.ManualDisabled, credID, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}

	action := "disable"
	reasonCode := "credential_manual_disabled"
	if !req.ManualDisabled {
		action = "enable"
		reasonCode = "credential_manual_enabled"
	}
	actorCopy := actor
	reqCopy := req
	//nolint:errcheck // best-effort exec, non-critical
	h.db.Exec(ctx, `
		INSERT INTO model_offer_events
		    (source, action, credential_id, provider_id, raw_model_name, reason_code, reason_detail)
		VALUES ('admin', $1, $2, $3, '', $4, $5)
	`, action, credID, providerID, reasonCode, actorCopy+": "+reqCopy.Reason)

	// 2026-06-28: legacy 900-series endpoint — same wakeup as the unified
	// handleSetManualDisabled in credential_monitor.go. PG trigger does
	// not watch manual_disabled, so we must NOTIFY + clear candCache
	// ourselves to keep the in-memory routing layer in sync.
	invalidateRoutingCaches(r.Context(), h.db, "credentials", credID)

	// 2026-08-13: mirror the manual_disabled flag into URSM v2 so the Redis
	// manual_hold stays consistent with PostgreSQL under URSM_v2_MODE=
	// authoritative. The emergency-repair force_disable/force_enable path
	// already does this; the Settings-tab checkbox previously flipped only the
	// DB column, so a checkbox disable could lag the Redis hold. Fan out across
	// every bound raw_model because this endpoint is credential-scoped (no
	// model). Best-effort: failures are logged, never block the HTTP response.
	ursmApplied := h.applyURSMManualDisabled(ctx, credID, req.ManualDisabled, req.Reason, actor)

	writeJSON(w, http.StatusOK, map[string]any{
		"message":         "updated",
		"manual_disabled": req.ManualDisabled,
		"actor":           actor,
		"ursm_v2_applied": ursmApplied.applied,
		"ursm_v2_models":  ursmApplied.models,
		"ursm_v2_errors":  ursmApplied.errors,
	})
}

// urmsManualDisableResult reports the URSM v2 manual-hold fan-out outcome for
// setCredentialManualDisabled. Used only to surface diagnostics in the response.
type urmsManualDisableResult struct {
	applied bool // true if at least one model's ApplyAdmin succeeded
	models  int  // number of bound raw_models attempted
	errors  int  // number of per-model ApplyAdmin failures
}

// applyURSMManualDisabled fans an URSM v2 manual-hold (or release) across every
// raw_model bound to the credential. No-op when URSM v2 is not configured. The
// tenant is read from the credentials row; missing tenant is treated as the
// default ("") tenant, matching handleEmergencyRepair.
func (h *Handler) applyURSMManualDisabled(ctx context.Context, credID int, disabled bool, reason, actor string) urmsManualDisableResult {
	out := urmsManualDisableResult{}
	if h.ursmV2 == nil {
		return out
	}
	var tenant string
	if err := h.db.QueryRow(ctx,
		"SELECT COALESCE(tenant_id,'') FROM credentials WHERE id = $1", credID,
	).Scan(&tenant); err != nil {
		slog.Warn("manual_disabled: tenant lookup failed", "cred", credID, "error", err)
		return out
	}
	rows, err := h.db.Query(ctx, `
		SELECT pm.raw_model_name
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE cmb.credential_id = $1
	`, credID)
	if err != nil {
		slog.Warn("manual_disabled: bound models query failed", "cred", credID, "error", err)
		return out
	}
	defer rows.Close()
	disabledPtr := disabled
	issuedAt := time.Now().UnixMilli()
	for rows.Next() {
		var rawModel string
		if err := rows.Scan(&rawModel); err != nil {
			continue
		}
		out.models++
		action := api.AdminAction{
			Scope:          api.ScopeNode,
			CredentialID:   credID,
			RawModel:       rawModel,
			TenantID:       tenant,
			ManualDisabled: &disabledPtr,
			Reason:         reason,
			Actor:          actor,
			IssuedAtMs:     issuedAt,
		}
		if err := h.ursmV2.ApplyAdmin(ctx, action); err != nil {
			out.errors++
			slog.Warn("manual_disabled: ursm.v2 apply_admin failed",
				"cred", credID, "model", rawModel, "error", err)
			continue
		}
		out.applied = true
	}
	return out
}

// setDefaultProbeModel manually pins a credential's probe model
// Audit: writes both model_offer_events and credential_probe_model_log
func (h *Handler) setDefaultProbeModel(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	var req struct {
		Model  *string `json:"model"`
		Reason string  `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	actor := r.Header.Get("X-Admin-User")
	if actor == "" {
		actor = "admin"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var oldModel string
	if err := h.db.QueryRow(ctx,
		`SELECT COALESCE(default_probe_model, '') FROM credentials WHERE id = $1 AND provider_id = $2`,
		credID, providerID,
	).Scan(&oldModel); err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}

	var newModel string
	var source string
	if req.Model != nil {
		newModel = *req.Model
		source = "manual"
	} else {
		source = "cleared"
	}

	if _, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET default_probe_model = $1, default_probe_model_source = $2, default_probe_model_picked_at = now()
		WHERE id = $3 AND provider_id = $4
	`, newModel, source, credID, providerID); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}

	actorCopy := actor
	reqCopy := req
	//nolint:errcheck // best-effort exec, non-critical
	h.db.Exec(ctx, `
		INSERT INTO credential_probe_model_log
		    (tenant_id, credential_id, source, old_model, new_model, actor, reason)
		VALUES ('default', $1, $2, $3, $4, $5, $6)
	`, credID, source, oldModel, newModel, actorCopy, reqCopy.Reason)

	writeJSON(w, http.StatusOK, map[string]any{
		"message":   "updated",
		"old_model": oldModel,
		"new_model": newModel,
		"source":    source,
		"actor":     actor,
	})
}

// pickDefaultProbeModel triggers an immediate auto-pick (skips daily cron)
// Looks up via request_logs (7d most-used client_model) and falls back to
// domestic provider random pick. Manual source is preserved.
func (h *Handler) pickDefaultProbeModel(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	actor := r.Header.Get("X-Admin-User")
	if actor == "" {
		actor = "admin"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	result, err := bgPickProbeModel(ctx, h.db, credID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pick failed: "+err.Error())
		return
	}

	if result.Model == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"message": "no candidate found",
			"model":   "",
			"source":  "",
		})
		return
	}

	var oldModel string
	if err := h.db.QueryRow(ctx,
		`SELECT COALESCE(default_probe_model, '') FROM credentials WHERE id = $1`,
		credID,
	).Scan(&oldModel); err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	if _, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET default_probe_model = $1, default_probe_model_source = $2, default_probe_model_picked_at = now()
		WHERE id = $3
	`, result.Model, result.Source, credID); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}

	actorCopy := actor
	//nolint:errcheck // best-effort exec, non-critical
	h.db.Exec(ctx, `
		INSERT INTO credential_probe_model_log
		    (tenant_id, credential_id, source, old_model, new_model, actor, reason)
		VALUES ('default', $1, $2, $3, $4, $5, $6)
	`, credID, result.Source, oldModel, result.Model, actorCopy, "admin immediate pick")

	writeJSON(w, http.StatusOK, map[string]any{
		"message":   "picked",
		"model":     result.Model,
		"source":    result.Source,
		"old_model": oldModel,
	})
}

// getRoutableSummary returns aggregate counts of routable vs unavailable bindings
// for a provider, grouped by unavailable_reason. Backed by v_routable_credential_models.
func (h *Handler) getRoutableSummary(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT
		    COALESCE(unavailable_reason, 'routable') AS reason,
		    count(*) AS cnt
		FROM v_routable_credential_models
		WHERE provider_id = $1 AND tenant_id = 'default'
		GROUP BY unavailable_reason
		ORDER BY cnt DESC
	`, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	breakdown := make(map[string]int)
	var total, routable int
	for rows.Next() {
		var reason string
		var cnt int
		if err := rows.Scan(&reason, &cnt); err != nil {
			continue
		}
		breakdown[reason] = cnt
		total += cnt
		if reason == "routable" {
			routable = cnt
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"provider_id":           providerID,
		"total_bindings":        total,
		"routable_bindings":     routable,
		"unavailable_bindings":  total - routable,
		"unavailable_breakdown": breakdown,
		"routable_ratio":        float64(routable) / float64(maxInt(total, 1)),
	})
}
