package autoroute

// http_llm_caller.go — HTTP-based LLMCaller implementation.
//
// Implements LLMCaller against an OpenAI-compatible /chat/completions
// endpoint. The auto-route classifier's "tiny classification prompt"
// is sent as a user message; the response.choices[0].message.content
// is treated as the raw task-type string and passed to normaliseLLMTaskType
// in classifier_llm.go.
//
// Why a separate file: keeps llm_caller.go focused on the abstractions
// (interface, breaker, instrumented) and isolates the HTTP/JSON
// details (which are much more verbose) here.
//
// Wiring:
//   buildAutoLLMCaller() in cmd/gateway/main.go reads:
//     LLMGatewayAutoLLMEndpoint   (e.g. "https://llm.kxpms.cn/v1")
//     LLMGatewayAutoLLMApiKey    (e.g. "sk-...")
//     LLMGatewayAutoLLMModel     (e.g. "claude-3-5-sonnet")
//   When Endpoint is set, returns HTTPLlmCaller wrapped in
//   CircuitBreakerCaller + InstrumentedCaller. When unset, returns
//   DisabledCaller (the default, safe no-op).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// HTTPLlmCallerConfig configures the HTTP LLM caller. Zero value is
// safe but unusable; callers should populate at least Endpoint and
// APIKey before use.
type HTTPLlmCallerConfig struct {
	// Endpoint is the base URL of an OpenAI-compatible /chat/completions
	// endpoint. The caller POSTs to {Endpoint}/chat/completions.
	Endpoint string

	// APIKey is the bearer token sent in the Authorization header.
	APIKey string

	// Model is the model name passed in the request body. Defaults
	// to "gpt-4o-mini" (cheap + low-latency, suitable for classification).
	Model string

	// Timeout is the wall-clock deadline for a single HTTP call.
	// Defaults to 3 seconds.
	Timeout time.Duration

	// HTTPClient is the *http.Client used for requests. If nil, a
	// client with a 5-second connection timeout is used. Override
	// in tests with httptest.NewServer's URL.
	HTTPClient *http.Client

	// ExtraHeaders are added to every request (useful for org-level
	// routing headers, request IDs, etc.).
	ExtraHeaders map[string]string

	// MaxTokens is the max_tokens for the response. Defaults to 16
	// (we only need a one-word task type back).
	MaxTokens int
}

// HTTPLlmCaller is the production-grade LLMCaller implementation
// that calls a real OpenAI-compatible /chat/completions endpoint.
//
// Thread-safe: a single instance can be shared across the gateway
// (the wrapped http.Client is safe for concurrent use).
type HTTPLlmCaller struct {
	cfg HTTPLlmCallerConfig
}

// NewHTTPLlmCaller constructs an HTTP caller with sensible defaults
// applied to the config (zero-value fields filled in).
func NewHTTPLlmCaller(cfg HTTPLlmCallerConfig) *HTTPLlmCaller {
	if cfg.Timeout == 0 {
		cfg.Timeout = 3 * time.Second
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-4o-mini"
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 16
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			Timeout: 5 * time.Second,
		}
	}
	return &HTTPLlmCaller{cfg: cfg}
}

// Call implements LLMCaller. Sends a tiny classification prompt to
// the configured /chat/completions endpoint and returns the raw
// response text. Errors are categorised:
//
//   context.DeadlineExceeded  — request timed out
//   ErrLLMHTTPNonOK (wrapped)  — server returned non-2xx
//   other                      — network / parse error
func (h *HTTPLlmCaller) Call(ctx context.Context, prompt string) (string, error) {
	if h.cfg.Endpoint == "" {
		return "", errors.New("autoroute: HTTPLlmCaller endpoint not configured")
	}

	reqBody := map[string]any{
		"model": h.cfg.Model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens":  h.cfg.MaxTokens,
		"temperature": 0.0, // deterministic for classification
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("autoroute: marshal request: %w", err)
	}

	url := strings.TrimRight(h.cfg.Endpoint, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("autoroute: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if h.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+h.cfg.APIKey)
	}
	for k, v := range h.cfg.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}

	// Apply per-call timeout
	callCtx, cancel := context.WithTimeout(ctx, h.cfg.Timeout)
	defer cancel()
	httpReq = httpReq.WithContext(callCtx)

	resp, err := h.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		// Distinguish timeout from other errors
		if callCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			RecordLLMHTTPStatus("timeout")
			return "", context.DeadlineExceeded
		}
		RecordLLMHTTPStatus("network_error")
		return "", fmt.Errorf("autoroute: HTTP call: %w", err)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	RecordLLMHTTPStatus(classifyHTTPStatus(resp.StatusCode))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Capture a short body excerpt for debugging
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("autoroute: HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Parse OpenAI response
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("autoroute: decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("autoroute: empty choices in response")
	}
	return parsed.Choices[0].Message.Content, nil
}

// ── Helpers ─────────────────────────────────────────────────────

// BuildHTTPLlmCallerFromEnv reads LLMGatewayAutoLLM* env vars and
// returns the configured caller plus a bool indicating whether
// LLM fallback is enabled. When disabled, returns (DisabledCaller{}, false).
//
// Env vars (all optional except Endpoint):
//   LLMGatewayAutoLLMEndpoint  — base URL, e.g. https://llm.kxpms.cn/v1
//   LLMGatewayAutoLLMApiKey   — bearer token (or "env:OPENAI_API_KEY" to use $OPENAI_API_KEY)
//   LLMGatewayAutoLLMModel    — model name (default: gpt-4o-mini)
//   LLMGatewayAutoLLMTimeout  — seconds, default 3
func BuildHTTPLlmCallerFromEnv(envLookup func(string) string) (LLMCaller, bool) {
	if envLookup == nil {
		envLookup = func(k string) string { return "" }
	}
	endpoint := strings.TrimSpace(envLookup("LLMGatewayAutoLLMEndpoint"))
	if endpoint == "" {
		return DisabledCaller{}, false
	}

	cfg := HTTPLlmCallerConfig{
		Endpoint: endpoint,
		APIKey:   strings.TrimSpace(envLookup("LLMGatewayAutoLLMApiKey")),
		Model:    strings.TrimSpace(envLookup("LLMGatewayAutoLLMModel")),
	}
	if t := strings.TrimSpace(envLookup("LLMGatewayAutoLLMTimeout")); t != "" {
		if secs, err := parseFloatSeconds(t); err == nil {
			cfg.Timeout = time.Duration(secs * float64(time.Second))
		}
	}

	slog.Info("autoroute: LLM fallback enabled",
		"endpoint", cfg.Endpoint,
		"model", cfg.Model,
		"timeout", cfg.Timeout.String())
	return NewHTTPLlmCaller(cfg), true
}

// RecordLLMHTTPStatus is an indirection for the telemetry package to
// observe HTTP status code distribution on the LLM endpoint. Defaults
// to a no-op so the autoroute package doesn't import telemetry.
// Wired by main.go via telemetry's package init.
var RecordLLMHTTPStatus = func(statusClass string) {}

// parseFloatSeconds is a tiny helper to avoid importing strconv just
// for the env var timeout parser. Accepts "3", "3.0", "0.5".
func parseFloatSeconds(s string) (float64, error) {
	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err != nil {
		return 0, err
	}
	return f, nil
}

// classifyHTTPStatus maps an HTTP status code to a coarse class for
// metrics: "2xx", "3xx", "4xx", "5xx", "other".
// metrics: "2xx", "3xx", "4xx", "5xx", "other".
func classifyHTTPStatus(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500 && code < 600:
		return "5xx"
	default:
		return "other"
	}
}
