package admin

// provider_diagnose.go — extracted from providers.go (2026-06-21 audit §3
// single-file-bloat remediation, second cut after provider_probe.go).
// The diagnose cluster runs a 30s deep-health check on a single provider:
// SELECT credentials, decrypt each secret, probe /v1/models and /v1/chat
// in parallel, classify errors, compute health scores, and surface the
// whole snapshot to the admin UI in one round-trip.
//
// Self-contained: it only needs stdlib + the helpers already split into
// provider_probe.go (doProbeRequest / doChatProbe) and the original
// providers.go (modelsURLCandidatesForBase / writeError / h.db / h.decryptCredStr).
//
// Pulled out of a 4395-line monolith so the diagnose logic has its own home
// for the inevitable "ModelsTab.vue 6/21 single-day 6 commits" follow-ups
// — diagnose was one of the areas touched most often in Round 48 audit.

import (
	"context"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/probeutil"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
)

func (h *Handler) diagnoseProvider(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var providerCode, baseURL, protocol string
	var enabled bool
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(code,''), COALESCE(base_url,''), COALESCE(protocol,''), enabled
		FROM providers WHERE id = $1 AND tenant_id = 'default'
	`, providerID).Scan(&providerCode, &baseURL, &protocol, &enabled)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}

	type modelsProbe struct {
		StatusCode  int      `json:"status_code"`
		LatencyMs   int      `json:"latency_ms"`
		Error       string   `json:"error,omitempty"`
		ModelsCount int      `json:"models_count"`
		SampleModels []string `json:"sample_models"`
	}

	type chatProbe struct {
		StatusCode       int    `json:"status_code"`
		LatencyMs        int    `json:"latency_ms"`
		Error            string `json:"error,omitempty"`
		ErrorCode        string `json:"error_code,omitempty"`
		ModelInResponse  string `json:"model_in_response,omitempty"`
	}

	type credDiag struct {
		CredentialID       int         `json:"credential_id"`
		Label              string      `json:"label"`
		Status             string      `json:"status"`
		CircuitState       string      `json:"circuit_state"`
		AvailabilityState  string      `json:"availability_state"`
		HealthStatus       string      `json:"health_status"`
		ConsecutiveFailures int        `json:"consecutive_failures"`
		ModelsProbe        modelsProbe `json:"models_probe"`
		ChatProbe          chatProbe   `json:"chat_probe"`
	}

	rows, err := h.db.Query(ctx, `
		SELECT c.id, COALESCE(c.label,''), COALESCE(c.status,'active'),
		       COALESCE(c.circuit_state,'closed'), COALESCE(c.availability_state,'ready'),
		       COALESCE(c.health_status,'unknown'), COALESCE(c.consecutive_failures,0),
		       c.secret_ciphertext
		FROM credentials c
		WHERE c.provider_id = $1 AND c.status = 'active'
		ORDER BY c.id
	`, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	var credDiags []credDiag
	var totalLatencyMs int
	latencyCount := 0

	for rows.Next() {
		var cd credDiag
		// secret_ciphertext is a bytea column; scanning into a string fails
		// in pgx and the error is swallowed by the `continue` below, which
		// makes diagnose silently report 0 credentials for a provider whose
		// only credential has a non-null secret. Scan into []byte (the same
		// shape routing.go uses) and convert to string for decryption.
		var ciphertext []byte
		if err := rows.Scan(&cd.CredentialID, &cd.Label, &cd.Status,
			&cd.CircuitState, &cd.AvailabilityState, &cd.HealthStatus,
			&cd.ConsecutiveFailures, &ciphertext); err != nil {
			continue
		}

	apiKey, decErr := h.decryptCredStr(string(ciphertext))
		if decErr != nil {
			cd.ModelsProbe = modelsProbe{Error: "decrypt failed"}
			cd.ChatProbe = chatProbe{Error: "decrypt failed"}
			credDiags = append(credDiags, cd)
			continue
		}

		modelsURLs := modelsURLCandidatesForBase(baseURL)

		start := time.Now()
		modelsResp, modelsErr := doProbeRequest(ctx, modelsURLs, apiKey)
		modelsLatency := int(time.Since(start).Milliseconds())
		totalLatencyMs += modelsLatency
		latencyCount++

		if modelsErr != nil {
			cd.ModelsProbe = modelsProbe{
				LatencyMs: modelsLatency,
				Error:     modelsErr.Error(),
			}
		} else {
			cd.ModelsProbe = modelsProbe{
				StatusCode:   modelsResp.statusCode,
				LatencyMs:    modelsLatency,
				ModelsCount:  modelsResp.modelCount,
				SampleModels: modelsResp.sampleModels,
			}
		}

		var testModel string
		err := h.db.QueryRow(ctx, `
			SELECT COALESCE(NULLIF(mo.outbound_model_name, ''), mo.raw_model_name) AS probe_model
			FROM model_offers mo
			JOIN credentials c ON c.id = mo.credential_id
			WHERE c.provider_id = $1 AND mo.available = TRUE
			ORDER BY mo.id
			LIMIT 1
		`, providerID).Scan(&testModel)
		if err != nil {
			testModel = "gpt-4o-mini"
		}

		chatURL := upstreamurl.ChatCompletionsURL(baseURL)
		start = time.Now()
		chatResp, chatErr := doChatProbe(ctx, chatURL, apiKey, testModel)
		chatLatency := int(time.Since(start).Milliseconds())
		totalLatencyMs += chatLatency
		latencyCount++

		if chatErr != nil {
			cd.ChatProbe = chatProbe{
				LatencyMs: chatLatency,
				Error:     chatErr.Error(),
			}
		} else {
			cp := chatProbe{
				StatusCode:      chatResp.statusCode,
				LatencyMs:       chatLatency,
				ModelInResponse: chatResp.modelInResponse,
				ErrorCode:       chatResp.errorCode,
			}
			if chatResp.errorCode == probeutil.EndpointIDRequiredErrCode {
				cp.Error = "此模型（如火山方舟 minimax-m3 / glm-5.1）需要 endpoint ID（outbound_model_name），请在 Models 标签页设置"
			}
			cd.ChatProbe = cp
		}

		credDiags = append(credDiags, cd)
	}

	healthy := 0
	degraded := 0
	unreachable := 0
	cooling := 0
	disabled := 0
	for _, cd := range credDiags {
		switch {
		case cd.HealthStatus == "healthy":
			healthy++
		case cd.AvailabilityState == "unreachable":
			unreachable++
		case cd.CircuitState == "open":
			cooling++
		case cd.HealthStatus == "warning" || cd.HealthStatus == "degraded":
			degraded++
		default:
			if cd.Status != "active" {
				disabled++
			}
		}
	}

	var availableCount, totalOffers int
	//nolint:errcheck // scan error non-critical
	h.db.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE mo.available = TRUE), COUNT(*)
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		WHERE c.provider_id = $1
	`, providerID).Scan(&availableCount, &totalOffers)

	coveragePct := 0.0
	if totalOffers > 0 {
		coveragePct = float64(availableCount) / float64(totalOffers) * 100
	}

	avgLatency := 0.0
	if latencyCount > 0 {
		avgLatency = float64(totalLatencyMs) / float64(latencyCount)
	}

	type errorClassification struct {
		AuthErrors         int `json:"auth_errors"`
		RateLimitErrors    int `json:"rate_limit_errors"`
		TimeoutErrors      int `json:"timeout_errors"`
		ModelNotFoundErrors int `json:"model_not_found_errors"`
		OtherErrors        int `json:"other_errors"`
	}

	var ec errorClassification
	ecRows, err := h.db.Query(ctx, `
		SELECT COALESCE(error_kind,'other'), COUNT(*)
		FROM request_logs
		WHERE provider_id = $1 AND ts >= now() - interval '24 hours'
		GROUP BY error_kind
	`, providerID)
	if err == nil {
		for ecRows.Next() {
			var kind string
			var cnt int
			if ecRows.Scan(&kind, &cnt) != nil {
				continue
			}
			switch {
			case strings.Contains(kind, "auth") || strings.Contains(kind, "401") || strings.Contains(kind, "403"):
				ec.AuthErrors += cnt
			case strings.Contains(kind, "rate") || strings.Contains(kind, "429"):
				ec.RateLimitErrors += cnt
			case strings.Contains(kind, "timeout") || strings.Contains(kind, "deadline"):
				ec.TimeoutErrors += cnt
			case strings.Contains(kind, "model") || strings.Contains(kind, "404"):
				ec.ModelNotFoundErrors += cnt
			default:
				ec.OtherErrors += cnt
			}
		}
		ecRows.Close()
	}

	type healthScore struct {
		CredentialID int     `json:"credential_id"`
		Score        float64 `json:"score"`
	}
	scores := make([]healthScore, 0, len(credDiags))
	for _, cd := range credDiags {
		score := 100.0
		if cd.CircuitState != "closed" {
			score -= 30
		}
		if cd.HealthStatus != "healthy" {
			score -= 20
		}
		if cd.ConsecutiveFailures > 0 {
			score -= 10
			score -= math.Min(20, float64(cd.ConsecutiveFailures)*5)
		}
		score = math.Max(0, math.Min(100, score))
		scores = append(scores, healthScore{CredentialID: cd.CredentialID, Score: score})
	}

	totalCreds := len(credDiags)
	result := map[string]any{
		"provider_id":  providerID,
		"provider_code": providerCode,
		"enabled":      enabled,
		"base_url":     baseURL,
		"protocol":     protocol,
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
		"credentials":  credDiags,
		"summary": map[string]any{
			"total_credentials":    totalCreds,
			"healthy":              healthy,
			"degraded":             degraded,
			"unreachable":          unreachable,
			"cooling":              cooling,
			"disabled":             disabled,
			"models_coverage_pct":  coveragePct,
			"avg_latency_ms":       avgLatency,
		},
		"error_classification": ec,
		"health_scores":        scores,
	}

	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) startDiagnose(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	taskID, err := insertBackgroundTask(ctx, h.db, "diagnose", &providerID, nil, map[string]any{"provider_id": providerID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create task: "+err.Error())
		return
	}

	go h.runDiagnose(providerID, taskID)

	writeJSON(w, http.StatusAccepted, map[string]any{"task_id": taskID, "status": "running"})
}

func (h *Handler) getDiagnoseResult(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	result, err := getLatestDiagnoseResult(ctx, h.db, providerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "no completed diagnose result found")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) runDiagnose(providerID int, taskID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	result := h.doDiagnose(ctx, providerID)
	if ctx.Err() != nil {
		failBackgroundTask(ctx, h.db, taskID, "timeout")
		return
	}
	completeBackgroundTask(ctx, h.db, taskID, result)
}

func (h *Handler) doDiagnose(ctx context.Context, providerID int) map[string]any {
	var providerCode, baseURL, protocol string
	var enabled bool
	//nolint:errcheck // scan error non-critical
	h.db.QueryRow(ctx, `
		SELECT COALESCE(code,''), COALESCE(base_url,''), COALESCE(protocol,''), enabled
		FROM providers WHERE id = $1 AND tenant_id = 'default'
	`, providerID).Scan(&providerCode, &baseURL, &protocol, &enabled)

	type modelsProbe struct {
		StatusCode   int      `json:"status_code"`
		LatencyMs    int      `json:"latency_ms"`
		Error        string   `json:"error,omitempty"`
		ModelsCount  int      `json:"models_count"`
		SampleModels []string `json:"sample_models"`
	}
	type chatProbe struct {
		StatusCode      int    `json:"status_code"`
		LatencyMs       int    `json:"latency_ms"`
		Error           string `json:"error,omitempty"`
		ModelInResponse string `json:"model_in_response,omitempty"`
	}
	type credDiag struct {
		CredentialID      int         `json:"credential_id"`
		Label             string      `json:"label"`
		Status            string      `json:"status"`
		CircuitState      string      `json:"circuit_state"`
		AvailabilityState string      `json:"availability_state"`
		HealthStatus      string      `json:"health_status"`
		ConsecutiveFailures int       `json:"consecutive_failures"`
		ModelsProbe       modelsProbe `json:"models_probe"`
		ChatProbe         chatProbe   `json:"chat_probe"`
	}

	rows, err := h.db.Query(ctx, `
		SELECT c.id, COALESCE(c.label,''), COALESCE(c.status,'active'),
		       COALESCE(c.circuit_state,'closed'), COALESCE(c.availability_state,'ready'),
		       COALESCE(c.health_status,'unknown'), COALESCE(c.consecutive_failures,0),
		       c.secret_ciphertext
		FROM credentials c WHERE c.provider_id = $1 AND c.status = 'active' ORDER BY c.id
	`, providerID)

	var credDiags []credDiag
	var totalLatencyMs int
	latencyCount := 0

	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var cd credDiag
			// secret_ciphertext is bytea — must scan into []byte, not string
			// (see the matching note in diagnoseProvider above).
			var ciphertext []byte
			if rows.Scan(&cd.CredentialID, &cd.Label, &cd.Status, &cd.CircuitState,
				&cd.AvailabilityState, &cd.HealthStatus, &cd.ConsecutiveFailures, &ciphertext) != nil {
				continue
			}

	apiKey, decErr := h.decryptCredStr(string(ciphertext))
			if decErr != nil {
				cd.ModelsProbe = modelsProbe{Error: "decrypt failed"}
				cd.ChatProbe = chatProbe{Error: "decrypt failed"}
				credDiags = append(credDiags, cd)
				continue
			}

			modelsURLs := modelsURLCandidatesForBase(baseURL)
			start := time.Now()
			modelsResp, modelsErr := doProbeRequest(ctx, modelsURLs, apiKey)
			modelsLatency := int(time.Since(start).Milliseconds())
			totalLatencyMs += modelsLatency
			latencyCount++

			if modelsErr != nil {
				cd.ModelsProbe = modelsProbe{LatencyMs: modelsLatency, Error: modelsErr.Error()}
			} else {
				cd.ModelsProbe = modelsProbe{StatusCode: modelsResp.statusCode, LatencyMs: modelsLatency, ModelsCount: modelsResp.modelCount, SampleModels: modelsResp.sampleModels}
			}

			var testModel string
			//nolint:errcheck // scan error non-critical
			h.db.QueryRow(ctx, `SELECT mo.raw_model_name FROM model_offers mo JOIN credentials c ON c.id = mo.credential_id WHERE c.provider_id = $1 AND mo.available = TRUE LIMIT 1`, providerID).Scan(&testModel)
			if testModel == "" { testModel = "gpt-4o-mini" }

			chatURL := upstreamurl.ChatCompletionsURL(baseURL)
			start = time.Now()
			chatResp, chatErr := doChatProbe(ctx, chatURL, apiKey, testModel)
			chatLatency := int(time.Since(start).Milliseconds())
			totalLatencyMs += chatLatency
			latencyCount++

			if chatErr != nil {
				cd.ChatProbe = chatProbe{LatencyMs: chatLatency, Error: chatErr.Error()}
			} else {
				cd.ChatProbe = chatProbe{StatusCode: chatResp.statusCode, LatencyMs: chatLatency, ModelInResponse: chatResp.modelInResponse}
			}
			credDiags = append(credDiags, cd)
		}
	}

	healthy, degraded, unreachable, cooling, disabled := 0, 0, 0, 0, 0
	for _, cd := range credDiags {
		switch {
		case cd.HealthStatus == "healthy": healthy++
		case cd.AvailabilityState == "unreachable": unreachable++
		case cd.CircuitState == "open": cooling++
		case cd.HealthStatus == "warning" || cd.HealthStatus == "degraded": degraded++
		default:
			if cd.Status != "active" { disabled++ }
		}
	}

	var availableCount, totalOffers int
	//nolint:errcheck // scan error non-critical
	h.db.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE mo.available = TRUE), COUNT(*) FROM model_offers mo JOIN credentials c ON c.id = mo.credential_id WHERE c.provider_id = $1`, providerID).Scan(&availableCount, &totalOffers)
	coveragePct := 0.0
	if totalOffers > 0 { coveragePct = float64(availableCount) / float64(totalOffers) * 100 }
	avgLatency := 0.0
	if latencyCount > 0 { avgLatency = float64(totalLatencyMs) / float64(latencyCount) }

	type errorClassification struct {
		AuthErrors int `json:"auth_errors"`; RateLimitErrors int `json:"rate_limit_errors"`; TimeoutErrors int `json:"timeout_errors"`; ModelNotFoundErrors int `json:"model_not_found_errors"`; OtherErrors int `json:"other_errors"`
	}
	var ec errorClassification
	ecRows, _ := h.db.Query(ctx, `SELECT COALESCE(error_kind,'other'), COUNT(*) FROM request_logs WHERE provider_id = $1 AND ts >= now() - interval '24 hours' GROUP BY error_kind`, providerID)
	if ecRows != nil {
		for ecRows.Next() {
			var kind string; var cnt int
			if ecRows.Scan(&kind, &cnt) != nil { continue }
			switch {
			case strings.Contains(kind, "auth") || strings.Contains(kind, "401") || strings.Contains(kind, "403"): ec.AuthErrors += cnt
			case strings.Contains(kind, "rate") || strings.Contains(kind, "429"): ec.RateLimitErrors += cnt
			case strings.Contains(kind, "timeout") || strings.Contains(kind, "deadline"): ec.TimeoutErrors += cnt
			case strings.Contains(kind, "model") || strings.Contains(kind, "404"): ec.ModelNotFoundErrors += cnt
			default: ec.OtherErrors += cnt
			}
		}
		ecRows.Close()
	}

	type healthScore struct { CredentialID int `json:"credential_id"`; Score float64 `json:"score"` }
	scores := make([]healthScore, 0, len(credDiags))
	for _, cd := range credDiags {
		score := 100.0
		if cd.CircuitState != "closed" { score -= 30 }
		if cd.HealthStatus != "healthy" { score -= 20 }
		if cd.ConsecutiveFailures > 0 { score -= 10; score -= math.Min(20, float64(cd.ConsecutiveFailures)*5) }
		score = math.Max(0, math.Min(100, score))
		scores = append(scores, healthScore{CredentialID: cd.CredentialID, Score: score})
	}

	return map[string]any{
		"provider_id": providerID, "provider_code": providerCode, "enabled": enabled,
		"base_url": baseURL, "protocol": protocol, "timestamp": time.Now().UTC().Format(time.RFC3339),
		"credentials": credDiags, "summary": map[string]any{
			"total_credentials": len(credDiags), "healthy": healthy, "degraded": degraded,
			"unreachable": unreachable, "cooling": cooling, "disabled": disabled,
			"models_coverage_pct": coveragePct, "avg_latency_ms": avgLatency,
		},
		"error_classification": ec, "health_scores": scores,
	}
}
