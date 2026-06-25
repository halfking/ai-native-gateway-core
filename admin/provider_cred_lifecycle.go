package admin

// provider_cred_lifecycle.go — extracted from providers.go (2026-06-21
// audit §3 single-file-bloat remediation, fourth cut). This file owns
// the post-creation lifecycle of a credential: reveal, update lifecycle
// metadata, reset availability/quota, run health checks, recover batches,
// and surface usage stats. Together with provider_credential.go (CRUD)
// it forms the complete credential story for a provider.
//
// Endpoints:
//   POST   /api/providers/{id}/credentials/{cid}/reveal          revealCredential
//   PATCH  /api/providers/{id}/credentials/{cid}/lifecycle     updateCredentialLifecycle
//   POST   /api/providers/{id}/credentials/{cid}/reset-availability  resetCredentialAvailability
//   POST   /api/providers/{id}/credentials/{cid}/reset-quota        resetCredentialQuota
//   POST   /api/providers/{id}/credentials/{cid}/check-health       startCheckCredentialHealth
//   GET    /api/providers/{id}/credentials/{cid}/check-health       checkCredentialHealth
//   POST   /api/providers/{id}/credentials/{cid}/recover            batchRecoverCredentials
//   GET    /api/providers/{id}/credentials/{cid}/usage             getCredentialUsage
//
// Background workers (runHealthCheck / doHealthCheck) are called from
// startCheckCredentialHealth via the bg.Tasks queue.
//
// Self-contained: only stdlib + same-package helpers. No internal/* deps.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/provider"
)

func (h *Handler) revealCredential(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var ciphertext []byte
	err := h.db.QueryRow(ctx, `
		SELECT secret_ciphertext FROM credentials
		WHERE id = $1 AND provider_id = $2 AND status <> 'disabled'
	`, credID, providerID).Scan(&ciphertext)
	if err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	if len(ciphertext) == 0 {
		writeError(w, http.StatusNotFound, "no secret stored")
		return
	}

	plaintext, err := h.decryptCredStr(string(ciphertext))
	if err != nil {
		slog.Warn("credential decrypt failed", "credential_id", credID, "error", err)
		writeError(w, http.StatusInternalServerError, "decryption failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"credential_id": credID,
		"api_key":       plaintext,
	})
}

func (h *Handler) updateCredentialLifecycle(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	var req struct {
		LifecycleStatus string `json:"lifecycle_status"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	//nolint:errcheck // best-effort exec, non-critical
	h.db.Exec(ctx, `UPDATE credentials SET lifecycle_status = $1 WHERE id = $2 AND provider_id = $3`, req.LifecycleStatus, credID, providerID)
	provider.InvalidateAllCandidateCache()
	writeJSON(w, http.StatusOK, map[string]string{"message": "updated"})
}

func (h *Handler) resetCredentialAvailability(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	//nolint:errcheck // best-effort exec, non-critical
	h.db.Exec(ctx, `
		UPDATE credentials
		SET availability_state = 'ready', availability_recover_at = NULL,
		    state_reason_code = NULL, state_reason_detail = NULL, state_updated_at = now()
		WHERE id = $1 AND provider_id = $2
	`, credID, providerID)
	provider.InvalidateAllCandidateCache()
	writeJSON(w, http.StatusOK, map[string]string{"message": "reset"})
}

func (h *Handler) resetCredentialQuota(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	//nolint:errcheck // best-effort exec, non-critical
	h.db.Exec(ctx, `
		UPDATE credentials SET quota_state = 'ok', quota_recover_at = NULL
		WHERE id = $1 AND provider_id = $2
	`, credID, providerID)
	provider.InvalidateAllCandidateCache()
	writeJSON(w, http.StatusOK, map[string]string{"message": "reset"})
}

func (h *Handler) startCheckCredentialHealth(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	taskID, err := insertBackgroundTask(ctx, h.db, "health_check", &providerID, &credID, map[string]any{"provider_id": providerID, "credential_id": credID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create task: "+err.Error())
		return
	}

	go h.runHealthCheck(providerID, credID, taskID)

	writeJSON(w, http.StatusAccepted, map[string]any{"task_id": taskID, "status": "running"})
}

func (h *Handler) runHealthCheck(providerID, credID int, taskID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := h.doHealthCheck(ctx, providerID, credID)
	if err != nil {
		slog.Error("health check failed", "provider_id", providerID, "credential_id", credID, "error", err)
		failBackgroundTask(ctx, h.db, taskID, "health check failed: "+err.Error())
		return
	}
	completeBackgroundTask(ctx, h.db, taskID, result)
}

func (h *Handler) doHealthCheck(ctx context.Context, providerID, credID int) (map[string]any, error) {
	cred, err := h.loadCredentialRowLite(ctx, providerID, credID)
	if err != nil {
		return nil, fmt.Errorf("query credential: %w", err)
	}

	apiKey, decErr := h.decryptCredStr(string(cred.secretCipher))
	probeOk := false
	modelsOk := false
	var healthStatus, healthError string
	var healthLatencyMs int
	var modelsCount int
	var sampleModels []string
	var apiModelsErr *string
	var effectiveSource string
	var modelsStatus int

	if decErr != nil {
		healthStatus = "error"
		healthError = "decrypt failed"
		msg := healthError
		apiModelsErr = &msg
	} else {
		start := time.Now()
		models, source, fetchErr := h.resolveModelsForCredential(ctx, cred, apiKey, true)
		healthLatencyMs = int(time.Since(start).Milliseconds())
		effectiveSource = source

		if fetchErr != nil {
			healthStatus = "unreachable"
			healthError = fetchErr.Error()
			msg := fetchErr.Error()
			apiModelsErr = &msg
			modelsStatus = -1
		} else if len(models) == 0 {
			healthStatus = "unreachable"
			healthError = fmt.Sprintf("no models returned (source=%s)", source)
			msg := healthError
			apiModelsErr = &msg
			modelsStatus = -1
		} else {
			healthStatus = "healthy"
			probeOk = source == "api" || source == "api+manifest"
			modelsOk = source == "api" || source == "api+manifest"
			modelsCount = len(models)
			limit := 3
			if len(models) < limit {
				limit = len(models)
			}
			sampleModels = models[:limit]
			if source != "api" && source != "api+manifest" {
				healthError = fmt.Sprintf("used %s fallback (%d models)", source, len(models))
			}
			modelsStatus = 1
		}
	}

	if sampleModels == nil {
		sampleModels = []string{}
	}

	// Update health status. The WHERE clause includes both id and provider_id
	// as a defensive constraint: if the credential doesn't belong to this
	// provider (shouldn't happen given loadCredentialRowLite check above, but
	// protects against race conditions or manual DB edits), the update will
	// affect 0 rows and we'll catch that.
	tag, err := h.db.Exec(ctx, `
		UPDATE credentials SET health_status = $1, health_checked_at = now(), health_error = $2,
		    health_latency_ms = $3, health_source = 'probe', api_models_ok = $4,
		    api_models_last_checked_at = now(), api_models_error = $5
		WHERE id = $6 AND provider_id = $7
	`, healthStatus, healthError, healthLatencyMs, modelsOk, apiModelsErr, credID, providerID)
	if err != nil {
		slog.Error("doHealthCheck: UPDATE credentials failed", "credential_id", credID, "provider_id", providerID, "error", err)
		return nil, fmt.Errorf("failed to update credential health: %w", err)
	}
	if tag.RowsAffected() == 0 {
		slog.Error("doHealthCheck: UPDATE affected 0 rows (provider_id/credential_id mismatch?)", "credential_id", credID, "provider_id", providerID)
		return nil, fmt.Errorf("credential %d does not belong to provider %d", credID, providerID)
	}

	return map[string]any{
		"credential_id":          credID,
		"health_status":          healthStatus,
		"probe_ok":               probeOk,
		"models_ok":              modelsOk,
		"models_count":           modelsCount,
		"health_latency_ms":      healthLatencyMs,
		"health_error":           healthError,
		"models_failure_reason":  apiModelsErr,
		"models_error":           apiModelsErr,
		"models_status":          modelsStatus,
		"sample_models":          sampleModels,
		"effective_source":       effectiveSource,
		"discovery_strategy":     cred.discoveryStrategy,
		"models_endpoint_template": cred.modelsEndpointTpl,
	}, nil
}

func (h *Handler) checkCredentialHealth(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := h.doHealthCheck(ctx, providerID, credID)
	if err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) batchRecoverCredentials(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	credTag, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET availability_state = 'ready', availability_recover_at = NULL,
		    state_reason_code = NULL, state_reason_detail = NULL, state_updated_at = now()
		WHERE provider_id = $1
		  AND availability_state IN ('cooling','unreachable','degraded')
		  AND lifecycle_status = 'active'
	`, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "credential recovery failed")
		return
	}
	recoveredCreds := int(credTag.RowsAffected())
	provider.InvalidateAllCandidateCache()

	offerTag, err := h.db.Exec(ctx, `
		UPDATE model_offers
		SET available = true, unavailable_reason = NULL, unavailable_at = NULL
		WHERE credential_id IN (
		    SELECT id FROM credentials
		    WHERE provider_id = $1 AND availability_state = 'ready'
		      AND lifecycle_status = 'active'
		) AND unavailable_reason LIKE 'auto_%%'
	`, providerID)
	recoveredOffers := 0
	if err == nil {
		recoveredOffers = int(offerTag.RowsAffected())
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"recovered_credentials": recoveredCreds,
		"recovered_offers":      recoveredOffers,
	})
}

func (h *Handler) getCredentialUsage(w http.ResponseWriter, r *http.Request, credID int) {
	days := queryInt(r, "days", 7)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var label, status, providerName string
	err := h.db.QueryRow(ctx, `
		SELECT c.label, COALESCE(c.status,''), COALESCE(p.display_name,'')
		FROM credentials c JOIN providers p ON p.id = c.provider_id
		WHERE c.id = $1 AND c.tenant_id = 'default'
	`, credID).Scan(&label, &status, &providerName)
	if err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}

	var reqCount int
	var promptTok, compTok int
	var cost, avgLatency, successRate float64
	//nolint:errcheck // scan error non-critical
	h.db.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
		       COALESCE(SUM(cost_usd),0)::float8, COALESCE(AVG(latency_ms),0)::float8,
		       COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END)::FLOAT / NULLIF(COUNT(*),0), 1.0)
		FROM usage_ledger WHERE credential_id = $1 AND tenant_id = 'default' AND ts >= now() - ($2 * INTERVAL '1 day')
	`, credID, days).Scan(&reqCount, &promptTok, &compTok, &cost, &avgLatency, &successRate)

	writeJSON(w, http.StatusOK, map[string]any{
		"credential_id":      credID,
		"label":              label,
		"status":             status,
		"provider_name":      providerName,
		"days":               days,
		"request_count":      reqCount,
		"prompt_tokens":      promptTok,
		"completion_tokens":  compTok,
		"cost_usd":           cost,
		"avg_latency_ms":     avgLatency,
		"success_rate":       successRate,
	})
}

