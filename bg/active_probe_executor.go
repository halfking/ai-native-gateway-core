// bg/active_probe_executor.go — direct-to-provider HTTP probe for the
// error-triggered active probe workflow.
//
// Unlike the periodic credential_probe_v2 / model_probe workers which run
// on a fixed cadence, this executor is invoked synchronously from
// ActiveProbeWorker whenever a (credential, model) pair accumulates
// `consecutive_threshold` failures. The probe goes DIRECTLY to the
// provider's base URL using the decrypted secret, bypassing the gateway —
// this isolates "upstream issue" from "gateway issue".
//
// Each call produces a single ProbeResult. The caller (ActiveProbeWorker)
// is responsible for:
//   - emitting the result to request_logs (so it lands in the live stream)
//   - feeding it back into CredentialStateManager.UpdateFromProbe
//
// HTTP-timeout: 10s by default, overridable via config.
package bg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// ActiveProbeExecutor loads (credential, model) → provider target and
// fires a minimal chat-completion ping directly to the provider.
type ActiveProbeExecutor struct {
	db         *pgxpool.Pool
	keyring    *secret.Keyring
	encKey     []byte
	httpClient *http.Client
}

// ProbeTarget is the resolved "where to send the probe" record.
type ProbeTarget struct {
	CredentialID  int
	ProviderID    int
	RawModel      string
	OutboundModel string
	BaseURL       string
	Protocol      string
	APIKey        string // already decrypted
}

// ProbeStatus enumerates the categorised outcomes of a single probe.
// Values mirror credential_probe_v2 / probe_http conventions so the
// downstream dashboard can render the same kind of error pill.
type ProbeStatus string

const (
	ProbeStatusSuccess  ProbeStatus = "success"
	ProbeStatusTimeout  ProbeStatus = "timeout"
	ProbeStatusNetwork  ProbeStatus = "network"
	ProbeStatusAuth     ProbeStatus = "auth"
	ProbeStatusRate     ProbeStatus = "rate_limit"
	ProbeStatusHTTP5xx  ProbeStatus = "http_5xx"
	ProbeStatusHTTP4xx  ProbeStatus = "http_4xx"
	ProbeStatusFailed   ProbeStatus = "failed"
	ProbeStatusSkipped  ProbeStatus = "skipped"
	ProbeStatusCanceled ProbeStatus = "canceled"
)

// ProbeResult captures everything the caller needs to persist + report.
type ProbeResult struct {
	Status      ProbeStatus
	HTTPStatus  int
	ErrCode     string
	ErrMsg      string
	LatencyMs   int
	RespPreview string // truncated to 500 chars
	StartedAt   time.Time
	CompletedAt time.Time
	Target      ProbeTarget
}

// NewActiveProbeExecutor constructs an executor.
// timeoutMs <= 0 falls back to 10s (matches spec §3.1).
func NewActiveProbeExecutor(db *pgxpool.Pool, keyring *secret.Keyring, encKey []byte, timeoutMs int) *ActiveProbeExecutor {
	if timeoutMs <= 0 {
		timeoutMs = 10000
	}
	return &ActiveProbeExecutor{
		db:      db,
		keyring: keyring,
		encKey:  encKey,
		httpClient: &http.Client{
			Timeout: time.Duration(timeoutMs) * time.Millisecond,
		},
	}
}

// LoadTarget queries DB for the (credential, model) binding and decrypts
// the secret. Returns an error if the binding is missing, inactive, or
// the secret cannot be decrypted.
//
// Skips manual_disabled=true / lifecycle_status != 'active' credentials
// so we never probe something the operator has explicitly retired.
func (e *ActiveProbeExecutor) LoadTarget(ctx context.Context, credID int, model string) (*ProbeTarget, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var (
		providerID     int
		outboundModel  string
		baseURL        string
		protocol       string
		ciphertext     []byte
		lifecycle      string
		manualDisabled bool
	)
	err := e.db.QueryRow(queryCtx, `
		SELECT c.provider_id,
		       COALESCE(pm.outbound_model_name, ''),
		       COALESCE(p.base_url, ''),
		       COALESCE(p.protocol, 'openai-completions'),
		       c.secret_ciphertext,
		       COALESCE(c.lifecycle_status, ''),
		       COALESCE(c.manual_disabled, FALSE)
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE c.id = $1 AND pm.raw_model_name = $2
		LIMIT 1
	`, credID, model).Scan(&providerID, &outboundModel, &baseURL, &protocol, &ciphertext, &lifecycle, &manualDisabled)
	if err != nil {
		return nil, fmt.Errorf("load target: %w", err)
	}
	if lifecycle != "active" {
		return nil, fmt.Errorf("credential lifecycle_status=%q, not active", lifecycle)
	}
	if manualDisabled {
		return nil, fmt.Errorf("credential manual_disabled=true")
	}
	if baseURL == "" {
		return nil, fmt.Errorf("empty base_url")
	}

	plain, decErr := decryptCiphertext(ciphertext, e.keyring, e.encKey)
	if decErr != nil {
		return nil, fmt.Errorf("decrypt credential: %w", decErr)
	}

	return &ProbeTarget{
		CredentialID:  credID,
		ProviderID:    providerID,
		RawModel:      model,
		OutboundModel: outboundModel,
		BaseURL:       baseURL,
		Protocol:      protocol,
		APIKey:        plain,
	}, nil
}

// Run executes one direct probe and returns a categorised ProbeResult.
// The returned result is always non-nil — even for transport-level
// failures we synthesise a result with Status=network/timeout so the
// caller can still write a request_log row.
//
// The function is single-shot: it does NOT retry. Backoff retries are
// the caller's responsibility (ActiveProbeWorker.processOne).
func (e *ActiveProbeExecutor) Run(ctx context.Context, t *ProbeTarget) *ProbeResult {
	start := time.Now()
	res := &ProbeResult{Target: *t, StartedAt: start}

	desc := providercap.Resolve(t.Protocol, "")
	endpoint, err := e.buildEndpoint(t, desc)
	if err != nil {
		res.Status = ProbeStatusFailed
		res.ErrCode = "endpoint_build"
		res.ErrMsg = err.Error()
		res.LatencyMs = int(time.Since(start).Milliseconds())
		res.CompletedAt = time.Now()
		return res
	}

	body, err := buildProbePingBody(t.OutboundModel, t.Protocol)
	if err != nil {
		res.Status = ProbeStatusFailed
		res.ErrCode = "body_build"
		res.ErrMsg = err.Error()
		res.LatencyMs = int(time.Since(start).Milliseconds())
		res.CompletedAt = time.Now()
		return res
	}

	reqCtx, cancel := context.WithTimeout(ctx, e.httpClient.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		res.Status = ProbeStatusNetwork
		res.ErrCode = "request_build"
		res.ErrMsg = err.Error()
		res.LatencyMs = int(time.Since(start).Milliseconds())
		res.CompletedAt = time.Now()
		return res
	}
	req.Header.Set("Content-Type", "application/json")
	providercap.ApplyAuthHeaders(req, desc, t.APIKey)

	resp, err := e.httpClient.Do(req)
	latency := time.Since(start)
	res.LatencyMs = int(latency.Milliseconds())
	res.CompletedAt = start.Add(latency)

	if err != nil {
		// Distinguish timeout vs network so the dashboard can show the
		// correct pill. context.DeadlineExceeded on its own can mean
		// either client-cancel (worker shutdown) or upstream-too-slow
		// (network timeout); the client.Timeout error wraps the latter.
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Client.Timeout") || strings.Contains(err.Error(), "context deadline exceeded") {
			res.Status = ProbeStatusTimeout
			res.ErrCode = "probe_timeout"
		} else if errors.Is(err, context.Canceled) {
			res.Status = ProbeStatusCanceled
			res.ErrCode = "probe_canceled"
		} else {
			res.Status = ProbeStatusNetwork
			res.ErrCode = "network_error"
		}
		res.ErrMsg = err.Error()
		return res
	}
	defer func() { _ = resp.Body.Close() }()

	res.HTTPStatus = resp.StatusCode
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	res.RespPreview = truncatePreview(string(bodyBytes), 500)

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		res.Status = ProbeStatusSuccess
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		res.Status = ProbeStatusAuth
		res.ErrCode = http.StatusText(resp.StatusCode)
	case resp.StatusCode == 429:
		res.Status = ProbeStatusRate
		res.ErrCode = "rate_limited"
	case resp.StatusCode >= 500:
		res.Status = ProbeStatusHTTP5xx
		res.ErrCode = http.StatusText(resp.StatusCode)
	default:
		res.Status = ProbeStatusHTTP4xx
		res.ErrCode = http.StatusText(resp.StatusCode)
	}
	res.ErrMsg = truncatePreview(string(bodyBytes), 500)
	return res
}

// buildEndpoint returns the chat-completions URL for the protocol.
// Anthropic uses /v1/messages; everything else (OpenAI-compatible) uses
// /v1/chat/completions.
func (e *ActiveProbeExecutor) buildEndpoint(t *ProbeTarget, desc providercap.Descriptor) (string, error) {
	if t.BaseURL == "" {
		return "", fmt.Errorf("empty base_url")
	}
	if strings.HasPrefix(t.Protocol, "anthropic") {
		return upstreamurl.MessagesURL(t.BaseURL), nil
	}
	return upstreamurl.ChatCompletionsURL(t.BaseURL), nil
}

// buildProbePingBody constructs the minimal chat-completion request body.
// max_tokens=1 keeps the probe cheap (< 10 output tokens). Temperature=0
// makes the response deterministic (where supported) so we can do simple
// "non-empty content" success checks downstream if needed.
func buildProbePingBody(model, protocol string) (string, error) {
	if model == "" {
		return "", fmt.Errorf("empty model name")
	}
	payload := map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
	}
	if strings.HasPrefix(protocol, "anthropic") {
		payload["max_tokens"] = 1
	} else {
		payload["temperature"] = 0
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal probe body: %w", err)
	}
	return string(b), nil
}

// truncatePreview keeps at most maxChars characters of a probe response
// for inclusion in request_logs.response_preview.
func truncatePreview(s string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	if len(s) <= maxChars {
		return s
	}
	return s[:maxChars]
}

// Log emits a single structured slog line summarising the probe outcome.
// Called by the worker (NOT by Run itself) so the executor stays a pure
// function and is trivially unit-testable.
func (r *ProbeResult) Log(credID int, model string, attempt int) {
	slog.Info("active_probe: result",
		"cred_id", credID,
		"model", model,
		"attempt", attempt,
		"status", string(r.Status),
		"http_status", r.HTTPStatus,
		"err_code", r.ErrCode,
		"latency_ms", r.LatencyMs,
	)
}

// classifyProbeErrorKind maps a ProbeResult.Status into an error_kind
// string suitable for request_logs.error_kind. Kept in this file (rather
// than emitter.go) so the executor + emitter share the same vocabulary.
func classifyProbeErrorKind(r *ProbeResult) string {
	switch r.Status {
	case ProbeStatusSuccess:
		return ""
	case ProbeStatusTimeout:
		return "probe_direct_timeout"
	case ProbeStatusNetwork:
		return "probe_direct_network_error"
	case ProbeStatusAuth:
		return "probe_direct_auth_failed"
	case ProbeStatusRate:
		return "probe_direct_rate_limited"
	case ProbeStatusHTTP5xx:
		if r.HTTPStatus > 0 {
			return fmt.Sprintf("probe_direct_http_%d", r.HTTPStatus)
		}
		return "probe_direct_http_5xx"
	case ProbeStatusHTTP4xx:
		if r.HTTPStatus > 0 {
			return fmt.Sprintf("probe_direct_http_%d", r.HTTPStatus)
		}
		return "probe_direct_http_4xx"
	case ProbeStatusCanceled:
		return "probe_direct_canceled"
	default:
		return "probe_direct_failed"
	}
}
