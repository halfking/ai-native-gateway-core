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

	var result struct {
		ID                int     `json:"id"`
		RawModelName      string  `json:"raw_model_name"`
		StandardizedName  *string `json:"standardized_name"`
		CanonicalID       *int    `json:"canonical_id"`
		CanonicalName     *string `json:"canonical_name"`
		OutboundModelName *string `json:"outbound_model_name"`
	}
	//nolint:errcheck // scan error non-critical
	h.db.QueryRow(ctx, `
		SELECT mo.id, mo.raw_model_name, mo.standardized_name, mo.canonical_id,
		       mc.canonical_name, mo.outbound_model_name
		FROM model_offers mo
		LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
		WHERE mo.id = $1
	`, offerID).Scan(&result.ID, &result.RawModelName, &result.StandardizedName,
		&result.CanonicalID, &result.CanonicalName, &result.OutboundModelName)

	// 2026-06-19 audit: any PATCH that touches standardized_name /
	// canonical_id / outbound_model_name can change the data
	// /api/routing/available-models aggregates.  Invalidate the
	// process-wide cache so the next page render re-reads the DB.
	if req.CanonicalID != nil || req.StandardizedName != nil || req.OutboundModelName != nil {
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

	tag, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET availability_recover_at = now() - INTERVAL '1 second'
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

	writeJSON(w, http.StatusOK, map[string]any{
		"message":         "updated",
		"manual_disabled": req.ManualDisabled,
		"actor":           actor,
	})
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
