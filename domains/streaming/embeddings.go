package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
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
}

func NewEmbeddingsHandler(providerResolver embeddingProviderResolver, upstreamClient *upstream.Client) *EmbeddingsHandler {
	return &EmbeddingsHandler{provider: providerResolver, upstream: upstreamClient}
}

func (h *EmbeddingsHandler) SetAuth(keyVerifier *authentication.KeyVerifier, rateLimiter ratelimit.RPMLimiter) {
	h.keyVerifier = keyVerifier
	h.rateLimiter = rateLimiter
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

	var lastErr string
	for _, candidate := range candidates {
		if !candidate.IsAvailable() || candidate.APIKey == "" || candidate.Protocol == "anthropic-messages" {
			continue
		}
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
			lastErr = upstreamErr.Error()
			continue
		}
		responseBody, readErr := readLimitedResponse(resp.Body, maxEmbeddingsResponseBytes)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr.Error()
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError {
			lastErr = fmt.Sprintf("upstream HTTP %d", resp.StatusCode)
			continue
		}
		copyEmbeddingResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(responseBody)
		return
	}

	slog.Warn("embedding candidates exhausted", "request_id", requestID, "model", model, "error", lastErr)
	writeErrorJSON(w, http.StatusBadGateway, requestID, "All embedding providers failed", "server_error", "upstream_error")
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
		writeErrorJSON(w, http.StatusTooManyRequests, requestID, "API key throttled", "rate_limit_error", "key_throttled")
		return nil, false
	}
	if outcome := checkGatewayRateLimit(keyInfo, h.rateLimiter); !outcome.Skipped {
		writeRateLimitHeaders(w, outcome)
		if outcome.Blocked {
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
