package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

const (
	maxEmbeddingsRequestBytes  = 8 << 20
	maxEmbeddingsResponseBytes = 64 << 20
)

type embeddingProviderResolver interface {
	GetCandidatesByModality(ctx context.Context, model, profile, tenantID, modality string) ([]provider.Candidate, *provider.Policy, error)
}

// EmbeddingsHandler proxies OpenAI-compatible embedding requests.
type EmbeddingsHandler struct {
	provider    embeddingProviderResolver
	upstream    *upstream.Client
	keyVerifier *authentication.KeyVerifier
	rateLimiter ratelimit.RPMLimiter
	telemetry   *telemetry.Client
	// autoIndex enables model="auto" for embeddings (22 章 §22.2).
	// When nil, model="auto" is passed through unchanged.
	autoIndex *autoroute.Index
	// failureLogger feeds each failed candidate into the shared
	// candidate_failure_logs_hot ledger (plus the supplier_errors_hot
	// projection) — same writer the chat executor uses. Optional wiring;
	// nil disables the ledger writes (2026-09-14 audit #8).
	failureLogger *executors.CandidateFailureWriter
}

func NewEmbeddingsHandler(providerResolver embeddingProviderResolver, upstreamClient *upstream.Client) *EmbeddingsHandler {
	return &EmbeddingsHandler{provider: providerResolver, upstream: upstreamClient}
}

// SetFailureLogger wires the shared candidate-failure ledger writer. Pass nil
// to disable (default) — every call site is nil-safe.
func (h *EmbeddingsHandler) SetFailureLogger(writer *executors.CandidateFailureWriter) {
	h.failureLogger = writer
}

func (h *EmbeddingsHandler) SetAuth(keyVerifier *authentication.KeyVerifier, rateLimiter ratelimit.RPMLimiter) {
	h.keyVerifier = keyVerifier
	h.rateLimiter = rateLimiter
}

func (h *EmbeddingsHandler) SetTelemetry(tc *telemetry.Client) {
	h.telemetry = tc
}

// SetAutoIndex wires the autoroute index for model="auto" resolution.
// When set AND FeatureFlags.AutoOnEmbeddings is true, a model="auto"
// request is resolved to the best embedding candidate via
// RecommendByModality. Pass nil to disable.
func (h *EmbeddingsHandler) SetAutoIndex(idx *autoroute.Index) {
	h.autoIndex = idx
}

func (h *EmbeddingsHandler) recordRateLimited(requestID string, keyInfo *authentication.KeyInfo, model, errCode string) {
	if h.telemetry == nil || !h.telemetry.Enabled() {
		return
	}
	entry := &telemetry.RequestLogEntry{
		Op:            telemetry.RequestLogInsert,
		RequestID:     requestID,
		EventAt:       func() *time.Time { now := time.Now().UTC(); return &now }(),
		TenantID:      "default",
		Success:       false,
		RequestStatus: strPtr(telemetry.RequestStatusRateLimited),
		ErrorKind:     strPtr(errCode),
		FailureStage:  strPtr("gateway"),
		ClientModel:   strPtr(model),
		RequestMode:   strPtr("embeddings"),
	}
	if keyInfo != nil {
		entry.TenantID = keyInfo.TenantID
		entry.APIKeyID = intPtr(keyInfo.ID)
	}
	h.telemetry.EmitRequestLogInsert(entry)
}

func (h *EmbeddingsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := r.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = generateRequestID()
	}
	w.Header().Set("X-Request-Id", requestID)

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErrorJSON(w, http.StatusMethodNotAllowed, requestID, "Method not allowed", "invalid_request_error", "method_not_allowed")
		return
	}
	if h.provider == nil || h.upstream == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, "Embeddings service unavailable", "server_error", "service_unavailable")
		return
	}

	keyInfo, ok := h.authenticate(w, r, requestID)
	if !ok {
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEmbeddingsRequestBytes))
	if err != nil {
		writeErrorJSON(w, http.StatusRequestEntityTooLarge, requestID, "Request body too large", "invalid_request_error", "request_too_large")
		return
	}
	defer func() { _ = r.Body.Close() }()

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "Invalid JSON body", "invalid_request_error", "invalid_json")
		return
	}
	model, err := requiredJSONString(payload, "model")
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, requestID, err.Error(), "invalid_request_error", "invalid_model")
		return
	}
	if !validEmbeddingInput(payload["input"]) {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "input must be a non-empty string or array", "invalid_request_error", "invalid_input")
		return
	}

	// model="auto": resolve to the best embedding candidate (22 章 §22.2).
	// Bypasses task classification — embeddings have no chat taxonomy.
	autoDecision := ""
	if model == "auto" && h.autoIndex != nil {
		if flags := autoroute.GetFeatureFlags(); flags != nil && flags.AutoOnEmbeddings {
			if scored := h.autoIndex.RecommendByModality("embedding", 1); len(scored) > 0 {
				autoDecision = scored[0].Candidate.RawModel
				model = scored[0].Candidate.CanonicalName
				payload["model"], _ = json.Marshal(model)
			}
		}
	}

	tenantID, profile := "", ""
	if keyInfo != nil {
		tenantID = keyInfo.TenantID
		if keyInfo.DefaultClientProfile != nil {
			profile = *keyInfo.DefaultClientProfile
		}
	}
	candidates, _, err := h.provider.GetCandidatesByModality(r.Context(), model, profile, tenantID, "embedding")
	if err != nil || len(candidates) == 0 {
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, "No embedding provider available", "server_error", "no_provider")
		return
	}

	// Typed trailing state for the exhaustion response: Kind decides 429 vs
	// 502, and Retry-After is kept per Kind family (R27 #11-3) so a window
	// quoted by one family can never leak onto another family's terminal
	// response. Same-family windows collapse to the max (worst constraint).
	var lastKind errorsx.ErrorKind
	retryAfterByKind := make(map[errorsx.ErrorKind]time.Duration)
	var lastErr string
	attempt := 0
	for _, candidate := range candidates {
		if !candidate.IsAvailable() || candidate.APIKey == "" || candidate.Protocol == "anthropic-messages" {
			continue
		}
		attemptStart := time.Now()
		attempt++
		upstreamBody, marshalErr := rewriteEmbeddingModel(payload, candidate.RawModel)
		if marshalErr != nil {
			lastErr = marshalErr.Error()
			continue
		}
		upstreamReq, requestErr := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamurl.EmbeddingsURL(candidate.BaseURL), bytes.NewReader(upstreamBody))
		if requestErr != nil {
			lastErr = requestErr.Error()
			continue
		}
		upstreamReq.Header.Set("Authorization", "Bearer "+candidate.APIKey)
		upstreamReq.Header.Set("Content-Type", "application/json")
		upstreamReq.Header.Set("Accept", "application/json")
		upstreamReq.Header.Set("X-Gateway-Internal-Purpose", "embedding")

		resp, upstreamErr := h.upstream.Do(upstreamReq)
		if upstreamErr != nil {
			// 5xx / network path: Do already classified (status+body) and
			// captured the body; harvest Kind + per-family Retry-After for
			// exhaustion, then fail over to the next candidate.
			h.logCandidateFailure(requestID, keyInfo, candidate, attempt, upstreamErr.Kind, upstreamErr, attemptStart)
			lastKind = upstreamErr.Kind
			if upstreamErr.RetryAfter > retryAfterByKind[upstreamErr.Kind] {
				retryAfterByKind[upstreamErr.Kind] = upstreamErr.RetryAfter
			}
			lastErr = upstreamErr.Error()
			continue
		}
		// 2026-09-13 audit fix + 2026-09-14 audit #8: only 2xx responses are
		// relayed verbatim. Everything else is classified via
		// ErrorFromResponse (the chat executor's lossy-signal fix) BEFORE the
		// body is drained, then either fails over (retryable /
		// credential-fatal / anything not client-determined) or renders a
		// terminal client-determined family as a gateway-shaped error.
		if !isEmbeddingPassthroughStatus(resp.StatusCode) {
			typedErr := upstream.ErrorFromResponse(resp)
			_ = resp.Body.Close()
			if typedErr == nil {
				lastErr = fmt.Sprintf("upstream HTTP %d", resp.StatusCode)
				continue
			}
			h.logCandidateFailure(requestID, keyInfo, candidate, attempt, typedErr.Kind, typedErr, attemptStart)
			if isEmbeddingTerminalClientKind(typedErr.Kind) {
				// Client-determined terminal family: every sibling credential
				// would answer identically, so no failover — surface the
				// gateway shape immediately.
				h.writeTerminalEmbeddingError(w, requestID, typedErr)
				return
			}
			lastKind = typedErr.Kind
			if typedErr.RetryAfter > retryAfterByKind[typedErr.Kind] {
				retryAfterByKind[typedErr.Kind] = typedErr.RetryAfter
			}
			lastErr = typedErr.Message
			continue
		}
		responseBody, readErr := readLimitedResponse(resp.Body, maxEmbeddingsResponseBytes)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr.Error()
			continue
		}
		if autoDecision != "" {
			// Minimal X-Gw-Auto-Decision for embeddings (no task taxonomy).
			w.Header().Set("X-Gw-Auto-Decision", `{"task_type":"embedding","chosen_model":"`+autoDecision+`"}`)
		}
		copyEmbeddingResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(responseBody)
		return
	}

	slog.Warn("embedding candidates exhausted", "request_id", requestID, "model", model, "error", lastErr)
	// Contract: exhaustion status follows the semantic family of the last
	// error Kind (429 throttled / 503 overloaded / 504 timeout / 502 dead).
	// Retry-After is read from the terminating family's own bucket only
	// (R27 #11-3): a 120s window quoted by an earlier 429 candidate must not
	// ride on a 503 exhaustion response. 429 terminal with hint=0 sends no
	// header (unchanged).
	status, errType, code := http.StatusBadGateway, "server_error", "upstream_error"
	if lastKind != "" && errorsx.HTTPStatusForKind(lastKind) != http.StatusBadGateway {
		if hint := retryAfterByKind[lastKind]; hint > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(hint.Seconds()))))
		}
		switch errorsx.HTTPStatusForKind(lastKind) {
		case http.StatusTooManyRequests:
			status, errType, code = http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exhausted"
		case http.StatusServiceUnavailable:
			status, errType, code = http.StatusServiceUnavailable, "overloaded_error", "overloaded_exhausted"
		case http.StatusGatewayTimeout:
			status, errType, code = http.StatusGatewayTimeout, "timeout_error", "timeout_exhausted"
		}
	}
	writeErrorJSON(w, status, requestID, "All embedding providers failed", errType, code)
}

// isEmbeddingPassthroughStatus reports whether an upstream embedding response
// may be relayed verbatim. 2026-09-14 audit #8: tightened to 2xx only
// (200/201/206) — the previous pass-through of every other status leaked
// vendor bodies (402/408/422/...) straight to the client.
func isEmbeddingPassthroughStatus(status int) bool {
	switch status {
	case http.StatusOK, http.StatusCreated, http.StatusPartialContent:
		return true
	default:
		return false
	}
}

// isEmbeddingTerminalClientKind reports the client-determined terminal
// families: the request itself (input content, size, model identity) is the
// problem, so sibling candidates would fail identically and failover is
// wasted fan-out (2026-09-14 audit #8).
func isEmbeddingTerminalClientKind(kind errorsx.ErrorKind) bool {
	switch kind {
	case errorsx.KindContentFilter, errorsx.KindContextLength, errorsx.KindClientBug,
		errorsx.KindModelNotFound, errorsx.KindModelDeprecated, errorsx.KindUnsupportedFeature:
		return true
	default:
		return false
	}
}

// writeTerminalEmbeddingError renders a terminal client-determined family as
// a gateway-shaped error: 400 invalid_request_error / 404 model_not_found /
// 410 gone. The message carries only the sanitized upstream reason — vendor
// bodies and status codes are never relayed verbatim.
func (h *EmbeddingsHandler) writeTerminalEmbeddingError(w http.ResponseWriter, requestID string, typedErr *upstream.Error) {
	status, errType, code := http.StatusBadRequest, "invalid_request_error", "invalid_request"
	switch typedErr.Kind {
	case errorsx.KindModelNotFound:
		status, code = http.StatusNotFound, "model_not_found"
	case errorsx.KindModelDeprecated:
		status, code = http.StatusGone, "model_deprecated"
	case errorsx.KindContextLength:
		code = "context_length_exceeded"
	case errorsx.KindContentFilter:
		code = "content_filter"
	case errorsx.KindUnsupportedFeature:
		code = "unsupported_feature"
	case errorsx.KindClientBug:
		code = "client_bug"
	}
	reason := strings.TrimSpace(string(errorsx.SanitizeErrorText([]byte(typedErr.Message), 256)))
	if reason == "" {
		reason = string(typedErr.Kind)
	}
	writeErrorJSON(w, status, requestID, "Embedding request rejected by upstream ("+reason+")", errType, code)
}

// logCandidateFailure feeds one failed embedding candidate into the shared
// candidate-failure ledger (LogFailureWithKind usage mirrors the chat
// executor's dispatch path). Best-effort and nil-safe: without wiring, the
// ledger write is skipped.
func (h *EmbeddingsHandler) logCandidateFailure(requestID string, keyInfo *authentication.KeyInfo, candidate provider.Candidate, attempt int, kind errorsx.ErrorKind, execErr error, startedAt time.Time) {
	if h.failureLogger == nil || execErr == nil {
		return
	}
	tenantID := ""
	if keyInfo != nil {
		tenantID = keyInfo.TenantID
	}
	perAttemptMs := int(time.Since(startedAt).Milliseconds())
	h.failureLogger.LogFailureWithKind(
		requestID, tenantID, "",
		candidate.CredentialID, candidate.ProviderID,
		candidate.RawModel, attempt,
		execErr, kind, nil, &perAttemptMs,
		map[string]any{
			"supplier":      candidate.CatalogCode,
			"failure_stage": "upstream",
		},
	)
}

func (h *EmbeddingsHandler) authenticate(w http.ResponseWriter, r *http.Request, requestID string) (*authentication.KeyInfo, bool) {
	if h.keyVerifier == nil || !h.keyVerifier.Enabled() {
		return nil, true
	}
	rawKey := extractBearerToken(r)
	if rawKey == "" {
		writeErrorJSON(w, http.StatusUnauthorized, requestID, "Missing API key", "authentication_error", "missing_key")
		return nil, false
	}
	keyInfo, err := h.keyVerifier.Verify(r.Context(), rawKey)
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeErrorJSON(w, http.StatusUnauthorized, requestID, "Invalid or expired API key", "authentication_error", "invalid_key")
		return nil, false
	}
	if keyInfo.Status == "throttled" {
		h.recordRateLimited(requestID, keyInfo, "<unknown>", "key_throttled")
		writeErrorJSON(w, http.StatusTooManyRequests, requestID, "API key throttled", "rate_limit_error", "key_throttled")
		return nil, false
	}
	if outcome := checkGatewayRateLimit(r.Context(), keyInfo, h.rateLimiter, nil); !outcome.Skipped {
		writeRateLimitHeaders(w, outcome)
		if outcome.Blocked {
			h.recordRateLimited(requestID, keyInfo, "<unknown>", "rate_limit_exceeded")
			writeErrorJSON(w, http.StatusTooManyRequests, requestID, "Rate limit exceeded", "rate_limit_error", "rate_limit_exceeded")
			return nil, false
		}
	}
	if err := h.keyVerifier.CheckBudget(r.Context(), keyInfo.ID); err != nil {
		if _, exceeded := err.(*authentication.BudgetExceededError); exceeded {
			writeErrorJSON(w, http.StatusPaymentRequired, requestID, "Budget exhausted", "insufficient_quota", "budget_exhausted")
			return nil, false
		}
	}
	return keyInfo, true
}

func requiredJSONString(payload map[string]json.RawMessage, field string) (string, error) {
	raw, ok := payload[field]
	if !ok {
		return "", fmt.Errorf("%s is required", field)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must be a non-empty string", field)
	}
	return value, nil
}

func validEmbeddingInput(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text) != ""
	}
	var inputs []json.RawMessage
	if json.Unmarshal(raw, &inputs) != nil || len(inputs) == 0 {
		return false
	}
	for _, input := range inputs {
		if len(input) == 0 || string(input) == "null" {
			return false
		}
	}
	return true
}

func rewriteEmbeddingModel(payload map[string]json.RawMessage, model string) ([]byte, error) {
	modelJSON, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	copyPayload := make(map[string]json.RawMessage, len(payload))
	for key, value := range payload {
		copyPayload[key] = value
	}
	copyPayload["model"] = modelJSON
	return json.Marshal(copyPayload)
}

func readLimitedResponse(body io.Reader, limit int64) ([]byte, error) {
	limited := io.LimitReader(body, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("upstream response exceeds %d bytes", limit)
	}
	return data, nil
}

func copyEmbeddingResponseHeaders(dst, src http.Header) {
	for _, name := range []string{"Content-Type", "Retry-After", "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
		if value := src.Get(name); value != "" {
			dst.Set(name, value)
		}
	}
	if dst.Get("Content-Type") == "" {
		dst.Set("Content-Type", "application/json")
	}
}
