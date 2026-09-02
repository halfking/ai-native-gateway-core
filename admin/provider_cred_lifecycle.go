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
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/modelresponse"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/provider"
)

var credentialLifecycleStatuses = map[string]struct{}{
	"active":    {},
	"disabled":  {},
	"suspended": {},
	"retired":   {},
}

func isCredentialLifecycleStatus(value string) bool {
	_, ok := credentialLifecycleStatuses[value]
	return ok
}

func setCredentialLifecycleStatus(ctx context.Context, db dbExec, providerID, credID int, status string) (bool, error) {
	// 2026-08-31: status='deleted' 是终态，lifecycle 通道不允许再翻回
	// active（UI 抽屉的 lifecycle 切换会走这里，不拦会削弱删除语义；
	// status 过滤本身仍兜底，这里是双保险）。
	tag, err := db.Exec(ctx, `UPDATE credentials SET lifecycle_status = $1 WHERE id = $2 AND provider_id = $3 AND status <> 'deleted'`, status, credID, providerID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (h *Handler) revealCredential(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var ciphertext []byte
	// 2026-08-31: 已删（deleted）凭据与已停用（disabled）一样不允许
	// reveal —— 删除是终态，明文密钥不应再可被取出。
	err := h.db.QueryRow(ctx, `
		SELECT secret_ciphertext FROM credentials
		WHERE id = $1 AND provider_id = $2 AND status NOT IN ('disabled', 'deleted')
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
	if !isCredentialLifecycleStatus(req.LifecycleStatus) {
		writeError(w, http.StatusBadRequest, "invalid lifecycle_status")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	updated, err := setCredentialLifecycleStatus(ctx, h.db, providerID, credID, req.LifecycleStatus)
	if err != nil {
		slog.Error("update credential lifecycle failed", "provider_id", providerID, "credential_id", credID, "lifecycle_status", req.LifecycleStatus, "error", err)
		writeError(w, http.StatusInternalServerError, "update credential lifecycle failed")
		return
	}
	if !updated {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
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
	model := strings.TrimSpace(r.URL.Query().Get("model"))

	taskID, err := insertBackgroundTask(ctx, h.db, "health_check", &providerID, &credID, map[string]any{"provider_id": providerID, "credential_id": credID, "model": model})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create task: "+err.Error())
		return
	}

	go h.runHealthCheck(providerID, credID, model, taskID)

	writeJSON(w, http.StatusAccepted, map[string]any{"task_id": taskID, "status": "running"})
}

func (h *Handler) runHealthCheck(providerID, credID int, model string, taskID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := h.doHealthCheck(ctx, providerID, credID, model)
	if err != nil {
		slog.Error("health check failed", "provider_id", providerID, "credential_id", credID, "error", err)
		failBackgroundTask(ctx, h.db, taskID, "health check failed: "+err.Error())
		return
	}
	completeBackgroundTask(ctx, h.db, taskID, result)
}

func (h *Handler) doHealthCheck(ctx context.Context, providerID, credID int, model string) (map[string]any, error) {
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
	var routingModelsUpserted int
	var sampleModels []string
	var apiModelsErr *string
	var effectiveSource string
	var modelsStatus int
	var probeError string
	var probeHTTPStatus int
	var probeLatencyMs int
	// 2026-09-02: typed model-error kind + redacted preview so the admin UI
	// can render a tailored hint instead of leaking the raw "<html>…"
	// payload into the credential detail drawer (see CredsTab.vue
	// health_error_kind branch).
	var modelsErrorKind string
	var modelsErrorPreview string

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
			// 2026-09-02: previously we piped the raw modelresponse.Error
			// string straight into health_error. When an upstream
			// (e.g. sunyun-china2) returns an HTML error page on
			// /v1/models, the string was "parse models response failed:
			// invalid character '<' looking for beginning of value
			// (context: body_bytes=1726)" — completely opaque to the
			// operator and made the drawer look broken. Now we surface a
			// short tag + a clean preview so the UI can render a
			// tailored hint. The original error remains available via
			// slog below for debugging.
			var mrErr *modelresponse.Error
			switch {
			case errors.As(fetchErr, &mrErr) && mrErr.Kind == modelresponse.ErrorKindNonJSONBody:
				healthError = "upstream_non_json"
				preview := modelresponse.Preview(mrErr)
				msg := "上游 /v1/models 返回了非 JSON 响应（疑似 HTML 错误页）"
				if preview != "" {
					msg = msg + "; preview=" + preview
				}
				apiModelsErr = &msg
				modelsErrorKind = modelresponse.ErrorKindNonJSONBody
				modelsErrorPreview = preview
				slog.Warn("credential health: upstream returned non-JSON on /v1/models",
					"credential_id", credID,
					"provider_id", providerID,
					"body_bytes", len(mrErr.Body),
					"preview", preview,
					"underlying", mrErr.Err,
				)
			default:
				healthError = fetchErr.Error()
				msg := healthError
				apiModelsErr = &msg
				if mrErr != nil {
					modelsErrorKind = mrErr.Kind
					modelsErrorPreview = modelresponse.Preview(mrErr)
				}
			}
			modelsStatus = -1
		} else if len(models) == 0 {
			healthStatus = "unreachable"
			healthError = fmt.Sprintf("no models returned (source=%s)", source)
			msg := healthError
			apiModelsErr = &msg
			modelsStatus = -1
		} else {
			healthStatus = "healthy"
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
			if probeModelsEligibleForRouting(source, models) {
				upserted, failed := h.enrollCredentialModels(ctx, cred.id, models)
				routingModelsUpserted = upserted
				if upserted > 0 {
					provider.InvalidateAllCandidateCache()
				}
				if failed > 0 {
					slog.Warn("health check: some discovered models were not enrolled for routing",
						"credential_id", cred.id,
						"upserted", upserted,
						"failed", failed)
				}
			}
		}

		if model != "" {
			start = time.Now()
			chatResult, chatErr := doChatProbe(ctx, upstreamurl.ChatCompletionsURL(cred.baseURL), apiKey, model)
			probeLatencyMs = int(time.Since(start).Milliseconds())
			if chatErr != nil {
				probeError = chatErr.Error()
			} else {
				probeHTTPStatus = chatResult.statusCode
				probeOk = chatResult.statusCode == http.StatusOK
				if probeOk {
					healthStatus = "healthy"
					if !modelsOk {
						healthError = "chat succeeded; models endpoint format was not recognized"
					}
				} else {
					healthStatus = "degraded"
					probeError = fmt.Sprintf("chat endpoint returned %d: %s", chatResult.statusCode, chatResult.errorMessage)
				}
			}
		}
	}

	if sampleModels == nil {
		sampleModels = []string{}
	}

	//nolint:errcheck // best-effort exec, non-critical
	h.db.Exec(ctx, `
		UPDATE credentials SET health_status = $1, health_checked_at = now(), health_error = $2,
		    health_latency_ms = $3, health_source = 'probe', api_models_ok = $4,
		    api_models_last_checked_at = now(), api_models_error = $5
		WHERE id = $6
	`, healthStatus, healthError, healthLatencyMs, modelsOk, apiModelsErr, credID)

	return map[string]any{
		"credential_id":            credID,
		"health_status":            healthStatus,
		"probe_ok":                 probeOk,
		"models_ok":                modelsOk,
		"models_count":             modelsCount,
		"health_latency_ms":        healthLatencyMs,
		"health_error":             healthError,
		"models_failure_reason":    apiModelsErr,
		"models_error":             apiModelsErr,
		"models_status":            modelsStatus,
		"probe_http_status":        probeHTTPStatus,
		"probe_latency_ms":         probeLatencyMs,
		"probe_error":              probeError,
		"health_probe_model":       model,
		"sample_models":            sampleModels,
		"effective_source":         effectiveSource,
		"routing_models_upserted":  routingModelsUpserted,
		"discovery_strategy":       cred.discoveryStrategy,
		"models_endpoint_template": cred.modelsEndpointTpl,
		// 2026-09-02: see modelsErrorKind / modelsErrorPreview above.
		"models_error_kind":    modelsErrorKind,
		"models_error_preview": modelsErrorPreview,
	}, nil
}

func (h *Handler) checkCredentialHealth(w http.ResponseWriter, r *http.Request, providerID, credID int) { //nolint:unused
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := h.doHealthCheck(ctx, providerID, credID, strings.TrimSpace(r.URL.Query().Get("model")))
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
		  AND availability_state IN ('cooling','unreachable')
		  AND lifecycle_status = 'active'
		  -- 2026-08-31: 'deleted' 终态凭据不参与批量恢复
		  AND status = 'active'
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
		      -- 2026-08-31: 已删凭据的 offer 不得被批量恢复复活
		      AND status = 'active'
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
	// 2026-07 partition-aware: 当 days <= 7 时查询 *_hot（heap 热数据，性能最优），
	// 当 days > 7 时跨月查询父表（聚合所有分区）。符合
	// docs/partition/partition-standards.md 查询规范。
	usageTable := "usage_ledger"
	if days <= 7 {
		usageTable = "usage_ledger_hot"
	}
	h.db.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
		       COALESCE(SUM(cost_usd),0)::float8, COALESCE(AVG(latency_ms),0)::float8,
		       COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END)::FLOAT / NULLIF(COUNT(*),0), 1.0)
		FROM `+usageTable+` WHERE credential_id = $1 AND tenant_id = 'default' AND ts >= now() - ($2 * INTERVAL '1 day')
	`, credID, days).Scan(&reqCount, &promptTok, &compTok, &cost, &avgLatency, &successRate)

	writeJSON(w, http.StatusOK, map[string]any{
		"credential_id":     credID,
		"label":             label,
		"status":            status,
		"provider_name":     providerName,
		"days":              days,
		"request_count":     reqCount,
		"prompt_tokens":     promptTok,
		"completion_tokens": compTok,
		"cost_usd":          cost,
		"avg_latency_ms":    avgLatency,
		"success_rate":      successRate,
	})
}
