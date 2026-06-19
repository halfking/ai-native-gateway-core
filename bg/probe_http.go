// bg/probe_http.go — v5 (2026-06-20) GET-based probe helpers
//
// Replaces the chat-ping model probe with GET /v1/models for free,
// zero-token validation. Includes:
//   - 3-attempt retry with 10s/15s/30s backoff for transient errors
//   - 402/429 error classifier (quota, rate_limit_5h, rate_limit_weekly, etc.)
//   - Model existence piggy-back check
//   - Chat ping variant for Layer 4 featured models
package bg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/probeutil"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
)

// ProbeMode selects which probe endpoint to call.
type ProbeMode int

const (
	// ProbeModeModelsList: GET /v1/models (free, Layer 1+2 main probe)
	ProbeModeModelsList ProbeMode = iota
	// ProbeModeChatPing: POST /v1/chat/completions with max_tokens=1 (Layer 4)
	ProbeModeChatPing
	// ProbeModeMessages: POST /v1/messages (Anthropic-specific)
	ProbeModeMessages
)

// probeBackoff is the retry schedule for Layer 1 (model list) probe.
// Spec: 0s (immediate), 10s, 15s, 30s — total 4 attempts within ~70s.
var probeBackoff = []time.Duration{0, 10 * time.Second, 15 * time.Second, 30 * time.Second}

// httpProbeResult is the structured outcome of a single HTTP probe attempt.
type httpProbeResult struct {
	status         string             // ok, network, auth, http_4xx, http_5xx, skipped
	category       probeCategory
	httpStatus     int
	errCode        string
	errMsg         string
	latencyMs      int
	modelListed    bool   // model was found in the response body (piggy-back)
	modelIDs       []string // all model IDs from the response (used to evaluate modelListed)
}

// singleGet does ONE GET /v1/models request with 5s timeout, returns parsed result.
func singleGet(ctx context.Context, endpoint, apiKey string, desc providercap.Descriptor) httpProbeResult {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return httpProbeResult{
			status: "network", category: probeCategoryProviderError,
			errCode: "request_build", errMsg: err.Error(),
			latencyMs: int(time.Since(start).Milliseconds()),
		}
	}
	providercap.ApplyAuthHeaders(req, desc, apiKey)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return httpProbeResult{
			status: "network", category: probeCategoryProviderError,
			errCode: "request_error", errMsg: err.Error(),
			latencyMs: int(time.Since(start).Milliseconds()),
		}
	}
	defer resp.Body.Close()
	latencyMs := int(time.Since(start).Milliseconds())
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 65536)) // 64KB to capture full model list
	return classifyHTTPResponse(resp.StatusCode, string(body), latencyMs)
}

// singleChatPing does ONE POST chat completion with max_tokens=1 (Layer 4).
func singleChatPing(ctx context.Context, endpoint, apiKey, modelField string, desc providercap.Descriptor) httpProbeResult {
	start := time.Now()
	body, _ := json.Marshal(map[string]any{
		"model":       modelField,
		"messages":    []map[string]string{{"role": "user", "content": "."}},
		"max_tokens":  1,
		"temperature": 0,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return httpProbeResult{
			status: "network", category: probeCategoryProviderError,
			errCode: "request_build", errMsg: err.Error(),
			latencyMs: int(time.Since(start).Milliseconds()),
		}
	}
	req.Header.Set("Content-Type", "application/json")
	providercap.ApplyAuthHeaders(req, desc, apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return httpProbeResult{
			status: "network", category: probeCategoryProviderError,
			errCode: "request_error", errMsg: err.Error(),
			latencyMs: int(time.Since(start).Milliseconds()),
		}
	}
	defer resp.Body.Close()
	latencyMs := int(time.Since(start).Milliseconds())
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return classifyHTTPResponse(resp.StatusCode, string(respBody), latencyMs)
}

// classifyHTTPResponse maps an HTTP response (status + body) to probeCategory.
// Used by both Layer 1 (GET /v1/models) and Layer 4 (chat ping).
func classifyHTTPResponse(httpStatus int, body string, latencyMs int) httpProbeResult {
	switch {
	case httpStatus >= 200 && httpStatus < 300:
		// 200 OK: parse body for model list (Layer 1 piggy-back)
		ids := parseModelList(body)
		return httpProbeResult{
			status: "ok", category: probeCategoryOK,
			httpStatus: httpStatus, latencyMs: latencyMs,
			modelIDs: ids,
		}
	case httpStatus == 401 || httpStatus == 403:
		// Auth errors are provider-side (bad key), not model availability.
		return httpProbeResult{
			status: "auth", category: probeCategoryProviderError,
			httpStatus: httpStatus,
			errCode: http.StatusText(httpStatus),
			errMsg: truncateProbeBody(body, 500),
			latencyMs: latencyMs,
		}
	case httpStatus == 402:
		// Payment required — quota exhausted.
		// Parse error.code / error.message if present.
		errCode, errMsg := parseErrorCodeAndMessage(body)
		quotaKind := detectQuotaKind(body, errCode)
		return httpProbeResult{
			status: quotaKind, category: probeCategoryProviderError,
			httpStatus: httpStatus,
			errCode:   errCode,
			errMsg:    errMsg,
			latencyMs: latencyMs,
		}
	case httpStatus == 404:
		// Could be endpoint_id_required (Volcano Ark) or genuine model_not_found.
		if probeutil.IsEndpointIDRequiredError(body) {
			return httpProbeResult{
				status: "skipped", category: probeCategorySkipped,
				httpStatus: httpStatus,
				errCode: probeutil.EndpointIDRequiredErrCode,
				errMsg:  "model requires endpoint ID: " + truncateProbeBody(body, 300),
				latencyMs: latencyMs,
			}
		}
		// For Layer 1 GET /v1/models: 404 means the URL is wrong (not model unavailable).
		// For Layer 4 chat ping: 404 means model_not_found (genuine model missing).
		return httpProbeResult{
			status: "model_not_found", category: probeCategoryModelUnavailable,
			httpStatus: httpStatus,
			errCode:   "model_not_found",
			errMsg:    truncateProbeBody(body, 500),
			latencyMs: latencyMs,
		}
	case httpStatus == 429:
		// Rate limited: classify into 5h/weekly/monthly based on body.
		errCode, errMsg := parseErrorCodeAndMessage(body)
		rlKind := detectRateLimitKind(body, errCode)
		return httpProbeResult{
			status: rlKind, category: probeCategoryProviderError,
			httpStatus: httpStatus,
			errCode:   errCode,
			errMsg:    errMsg,
			latencyMs: latencyMs,
		}
	case httpStatus >= 400 && httpStatus < 500:
		// Other 4xx (400, 422, etc.) — genuine model problem for chat ping,
		// or malformed endpoint for models list.
		errCode, errMsg := parseErrorCodeAndMessage(body)
		return httpProbeResult{
			status: "http_4xx", category: probeCategoryModelUnavailable,
			httpStatus: httpStatus,
			errCode:   errCode,
			errMsg:    errMsg,
			latencyMs: latencyMs,
		}
	case httpStatus >= 500:
		// Server errors are provider-side, not model availability.
		return httpProbeResult{
			status: "http_5xx", category: probeCategoryProviderError,
			httpStatus: httpStatus,
			errCode:   http.StatusText(httpStatus),
			errMsg:    truncateProbeBody(body, 500),
			latencyMs: latencyMs,
		}
	default:
		return httpProbeResult{
			status: "unknown", category: probeCategoryProviderError,
			httpStatus: httpStatus,
			errCode:   http.StatusText(httpStatus),
			errMsg:    truncateProbeBody(body, 500),
			latencyMs: latencyMs,
		}
	}
}

// parseModelList extracts model IDs from common response shapes:
//   - OpenAI:    {"data": [{"id": "..."}]}
//   - Anthropic: {"data": [{"id": "..."}]}
//   - Volcano Ark: {"data": [{"id": "ep-..."}]}
//   - Gemini:    {"models": [{"name": "models/..."}]}
//   - Raw:       ["model-a", "model-b"] (fallback)
func parseModelList(body string) []string {
	if body == "" {
		return nil
	}
	var out []string
	// Try OpenAI/Anthropic shape
	var openAIShape struct {
		Data []struct {
			ID    string `json:"id"`
			Model string `json:"model"`
		} `json:"data"`
		Models []struct {
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(body), &openAIShape); err == nil {
		for _, m := range openAIShape.Data {
			if m.ID != "" {
				out = append(out, m.ID)
			}
		}
		for _, m := range openAIShape.Models {
			id := m.Name
			if id == "" {
				id = m.ID
			}
			// Strip "models/" prefix from Gemini
			id = strings.TrimPrefix(id, "models/")
			if id != "" {
				out = append(out, id)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	// Try raw array
	var rawArr []string
	if err := json.Unmarshal([]byte(body), &rawArr); err == nil {
		return rawArr
	}
	return nil
}

// parseErrorCodeAndMessage extracts {"error":{"code":"...","message":"..."}} if present.
func parseErrorCodeAndMessage(body string) (string, string) {
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &e); err == nil {
		code := e.Error.Code
		if code == "" {
			code = e.Error.Type
		}
		if code != "" || e.Error.Message != "" {
			return code, e.Error.Message
		}
	}
	return "", ""
}

// 5h / week / month patterns for rate limit classification.
var (
	re5h     = regexp.MustCompile(`(?i)(5\s*h(our)?|5\s*小时|per\s*5\s*h)`)
	reWeek   = regexp.MustCompile(`(?i)(week|周)`)
	reMonth  = regexp.MustCompile(`(?i)(month|月|30\s*day)`)
	reMinute = regexp.MustCompile(`(?i)(per\s*minute|per\s*min|tpm|rpm|requests\s*per\s*minute|tokens\s*per\s*minute)`)
	reBalance = regexp.MustCompile(`(?i)(balance|insufficient|quota|exceeded|payment)`)
)

// detectQuotaKind maps a 402 body to a specific quota subtype.
func detectQuotaKind(body, errCode string) string {
	combined := strings.ToLower(body + " " + errCode)
	switch {
	case reBalance.MatchString(combined):
		return "quota_exhausted"
	default:
		return "quota_exhausted"
	}
}

// detectRateLimitKind maps a 429 body to rate_limit_5h / _weekly / _monthly / _short.
func detectRateLimitKind(body, errCode string) string {
	combined := strings.ToLower(body + " " + errCode)
	switch {
	case re5h.MatchString(combined):
		return "rate_limit_5h"
	case reMonth.MatchString(combined):
		return "rate_limit_monthly"
	case reWeek.MatchString(combined):
		return "rate_limit_weekly"
	case reMinute.MatchString(combined):
		return "rate_limit_short"
	default:
		return "rate_limit"
	}
}

func truncateProbeBody(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// probeWithRetry performs up to 4 attempts with backoff. Non-retryable
// errors (401/403/404) return immediately on the first attempt.
func probeWithRetry(
	ctx context.Context,
	desc providercap.Descriptor,
	t probeTarget,
	mode ProbeMode,
) httpProbeResult {
	endpoint := resolveProbeEndpoint(t, desc, mode)
	if endpoint == "" {
		return httpProbeResult{
			status: "skipped", category: probeCategorySkipped,
			errCode: "endpoint_unresolved", errMsg: "empty base_url",
		}
	}

	// Retryable error check: status in [401, 403, 404] should not retry.
	for attempt, backoff := range probeBackoff {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return httpProbeResult{
					status: "network", category: probeCategoryProviderError,
					errCode: "ctx_canceled", errMsg: ctx.Err().Error(),
				}
			case <-time.After(backoff):
			}
		}
		var result httpProbeResult
		switch mode {
		case ProbeModeChatPing:
			result = singleChatPing(ctx, endpoint, t.APIKey, t.RawModel, desc)
		case ProbeModeMessages:
			// Same as chat ping for now (uses POST /v1/messages); uses outbound model.
			modelField := t.OutboundModel
			if modelField == "" {
				modelField = t.RawModel
			}
			result = singleChatPing(ctx, endpoint, t.APIKey, modelField, desc)
		default: // ProbeModeModelsList
			result = singleGet(ctx, endpoint, t.APIKey, desc)
		}
		// Piggy-back: was the model in the response?
		if len(result.modelIDs) > 0 && mode == ProbeModeModelsList {
			result.modelListed = probeContainsString(result.modelIDs, t.RawModel) ||
				(t.OutboundModel != "" && probeContainsString(result.modelIDs, t.OutboundModel))
			// For models-list probe, "200 but model not listed" = model_not_found.
			if !result.modelListed {
				result.status = "model_not_found"
				result.category = probeCategoryModelUnavailable
				result.errCode = "model_not_listed"
				result.errMsg = fmt.Sprintf("model %q not in /v1/models response (%d models found)", t.RawModel, len(result.modelIDs))
			}
		}
		// Non-retryable conditions: success or auth/404 (definitive errors).
		if result.category == probeCategoryOK ||
			result.httpStatus == 401 || result.httpStatus == 403 || result.httpStatus == 404 ||
			result.httpStatus == 400 || result.httpStatus == 422 {
			return result
		}
		// Otherwise (network/5xx/429) — continue to next attempt.
		if attempt == len(probeBackoff)-1 {
			return result // exhausted retries
		}
	}
	return httpProbeResult{
		status: "network", category: probeCategoryProviderError,
		errCode: "retry_exhausted",
	}
}

// resolveProbeEndpoint returns the full URL for the given probe mode.
func resolveProbeEndpoint(t probeTarget, desc providercap.Descriptor, mode ProbeMode) string {
	if t.BaseURL == "" {
		return ""
	}
	switch mode {
	case ProbeModeModelsList:
		// Use ModelsURLCandidates to handle /v1, /v3, /api/paas/v4 etc.
		candidates := providercap.ModelsURLCandidates(t.BaseURL, nil, desc)
		if len(candidates) == 0 {
			return ""
		}
		return candidates[0]
	case ProbeModeChatPing:
		return upstreamurl.Build(t.BaseURL, desc.ChatProbeEndpoint)
	case ProbeModeMessages:
		return upstreamurl.Build(t.BaseURL, upstreamurl.EpMessages)
	}
	return ""
}

func probeContainsString(s []string, target string) bool {
	for _, v := range s {
		if v == target {
			return true
		}
	}
	return false
}
