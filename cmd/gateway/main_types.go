// Adapters and helper types used by main() but defined outside it.
//
// Extracted from main.go as part of the P0 main.go split refactor.
// See docs/refactor-plans/main-go-split.md for the full plan.
//
// All declarations here are package-private; behaviour is unchanged
// from the original implementation in main.go.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	streaming "github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/pending"
)

// pendingStoreAdapter bridges the pending package's *Store to the
// sessions package's narrow PendingStore interface. The two-way
// import (sessions ← pending) would be a cycle; the adapter is the
// only place that can import both, so it lives here in main.go.
//
// All methods are thin shims; the heavy lifting stays in
// pending.Store where the Redis access is.
type pendingStoreAdapter struct{ s *pending.Store }

func newPendingStoreAdapter(s *pending.Store) session.PendingStore {
	return &pendingStoreAdapter{s: s}
}

func (a *pendingStoreAdapter) Get(ctx context.Context, sessionID, requestID string) (*session.PendingEntry, bool, error) {
	r, ok, err := a.s.Get(ctx, sessionID, requestID)
	if err != nil || !ok {
		return nil, false, err
	}
	return a.toEntry(r), true, nil
}

func (a *pendingStoreAdapter) GetLatest(ctx context.Context, sessionID string) (*session.PendingEntry, string, bool, error) {
	r, requestID, ok, err := a.s.GetLatest(ctx, sessionID)
	if err != nil || !ok {
		return nil, requestID, false, err
	}
	return a.toEntry(r), requestID, true, nil
}

func (a *pendingStoreAdapter) toEntry(r *pending.Response) *session.PendingEntry {
	if r == nil {
		return nil
	}
	return &session.PendingEntry{
		SessionID:    r.SessionID,
		TenantID:     r.TenantID,
		RequestID:    r.RequestID,
		Status:       string(r.Status),
		Body:         r.Body,
		ContentType:  r.ContentType,
		ProviderID:   r.ProviderID,
		CredentialID: r.CredentialID,
		IsStream:     r.IsStream,
		CompletedAt:  r.CompletedAt,
		ErrorMessage: r.ErrorMessage,
	}
}

// sessionAuthAdapter bridges the live authentication.KeyVerifier
// (which returns *authentication.KeyInfo) to the session.KeyVerifier
// interface (which returns session.KeyInfo).
type sessionAuthAdapter struct {
	kv *authentication.KeyVerifier
}

func (a sessionAuthAdapter) Enabled() bool { return a.kv != nil && a.kv.Enabled() }
func (a sessionAuthAdapter) Verify(ctx context.Context, rawKey string) (session.KeyInfo, error) {
	ki, err := a.kv.Verify(ctx, rawKey)
	if err != nil {
		return session.KeyInfo{}, err
	}
	return session.KeyInfo{ID: ki.ID, TenantID: ki.TenantID}, nil
}

// extractTenantIDFromUpstreamResp extracts tenantID from the upstream request
// context. For session requests, the executor propagates session headers to
// upstream (see executor_chat.go:359-362), and also injects TenantID into the
// request context (executor.go:1097). This helper retrieves it for pending
// store isolation.
func extractTenantIDFromUpstreamResp(resp *http.Response) string {
	if resp == nil || resp.Request == nil {
		return ""
	}
	return session.GetTenantIDFromContext(resp.Request.Context())
}

// markCapturedPendingInProgress creates the durable placeholder before the
// stream starts. This makes a reconnect observe in_progress even if the
// original HTTP client disconnects before the upstream produces a chunk.
// 2026-07-19 audit fix: accepts tenantID as explicit parameter instead of
// reading from resp.Request.Context() (which is the upstream context and
// lacks tenant info after upstreamContext decoupling).
func markCapturedPendingInProgress(store *pending.Store, resp *http.Response, tenantID string) {
	if store == nil || resp == nil || tenantID == "" {
		return
	}
	sessionID := streaming.SessionIDFromResp(resp)
	requestID := streaming.RequestIDFromResp(resp)
	if sessionID == "" || requestID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := store.MarkInProgress(ctx, &pending.Response{
		SessionID: sessionID,
		TenantID:  tenantID,
		RequestID: requestID,
		Status:    pending.StatusInProgress,
		IsStream:  true,
		CreatedAt: time.Now().Unix(),
	}); err != nil {
		slog.Warn("pending_mark_in_progress_failed", "session_id", sessionID, "request_id", requestID, "error", err)
	}
}

// saveCapturedPending persists the capturer's buffered SSE body to the
// pending store so a client that disconnects mid-stream can pick up
// the response via GET /v1/sessions/{id}/pending-response (Track C C5,
// 2026-06-21). Shared by the OpenAI and both Anthropic (Q3 + Q4) stream
// wrappers.
// 2026-07-19 audit fix: accepts tenantID as explicit parameter instead of
// reading from resp.Request.Context() (which is the upstream context).
func saveCapturedPending(store *pending.Store, pc *streaming.PendingCapturer, resp *http.Response, tenantID string) {
	if store == nil || pc == nil || resp == nil || tenantID == "" {
		return
	}
	body, state, ok := pc.Snapshot()
	if !ok {
		return
	}
	if state.Overflowed {
		slog.Warn("pending_capture_overflow",
			"session_id", streaming.SessionIDFromResp(resp),
			"request_id", streaming.RequestIDFromResp(resp),
			"captured_bytes", len(body),
		)
	}
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer saveCancel()
	if err := store.Save(saveCtx, &pending.Response{
		SessionID:    streaming.SessionIDFromResp(resp),
		TenantID:     tenantID,
		RequestID:    streaming.RequestIDFromResp(resp),
		Status:       pending.Status(state.Status),
		Body:         string(body),
		ContentType:  "text/event-stream",
		IsStream:     true,
		CreatedAt:    time.Now().Unix(),
		CompletedAt:  state.CompletedAt,
		ErrorMessage: state.ErrMessage,
	}); err != nil {
		slog.Warn("pending_save_failed",
			"session_id", streaming.SessionIDFromResp(resp),
			"request_id", streaming.RequestIDFromResp(resp),
			"error", err,
		)
	}
}

// buildAutoLLMCaller returns the LLMCaller to use for the auto-route
// fallback classifier.
//
// Selection logic:
//  1. If LLMGatewayAutoLLMEndpoint env var is set:
//     HTTPLlmCaller (OpenAI-compatible POST /chat/completions)
//     wrapped in CircuitBreakerCaller (5-failure / 30s cooldown)
//     wrapped in InstrumentedCaller (per-call metrics)
//  2. Otherwise:
//     DisabledCaller (no LLM call; decider falls back to the
//     heuristic result at low confidence)
//
// Environment variables consumed (all optional except Endpoint):
//
//	LLMGatewayAutoLLMEndpoint  base URL (e.g. "https://llmgateway.internal.example.com/v1")
//	LLMGatewayAutoLLMApiKey   bearer token
//	LLMGatewayAutoLLMModel    model name (default "gpt-4o-mini")
//	LLMGatewayAutoLLMTimeout  seconds (default 3)
func buildAutoLLMCaller() autoroute.LLMCaller {
	caller, enabled := autoroute.BuildHTTPLlmCallerFromEnv(os.Getenv)
	if !enabled {
		return autoroute.DisabledCaller{}
	}
	// Wrap the real caller in: circuit breaker → instrumented metrics.
	// Order matters: instrumented wraps circuit breaker so metrics
	// see the outcome AFTER the breaker decides to short-circuit.
	return &autoroute.InstrumentedCaller{
		Inner:   autoroute.NewCircuitBreakerCaller(caller),
		Metrics: &autoroute.CallerMetrics{},
	}
}

// irAdapter implements routing.IRConverter by wrapping the ir package functions.
// Used when LLM_GATEWAY_IR_CONVERTER=true to enable the Phase B Parse→IR→Serialize
// pipeline, reducing protocol conversion complexity from O(N²) to O(N).
type irAdapter struct{}

func (a *irAdapter) ParseOpenAI(body []byte) (*ir.InternalRequest, error) {
	return ir.ParseOpenAI(body)
}

func (a *irAdapter) ParseAnthropic(body []byte) (*ir.InternalRequest, error) {
	return ir.ParseAnthropic(body)
}

func (a *irAdapter) SerializeOpenAI(req *ir.InternalRequest) ([]byte, error) {
	return ir.SerializeOpenAI(req)
}

func (a *irAdapter) SerializeAnthropic(req *ir.InternalRequest) ([]byte, error) {
	return ir.SerializeAnthropic(req)
}

// Phase D (2026-06-22): Response direction methods
func (a *irAdapter) ParseAnthropicResponse(body []byte) (*ir.InternalResponse, error) {
	return ir.ParseAnthropicResponse(body)
}

func (a *irAdapter) ParseOpenAIResponse(body []byte) (*ir.InternalResponse, error) {
	return ir.ParseOpenAIResponse(body)
}

func (a *irAdapter) SerializeOpenAIResponse(irResp *ir.InternalResponse, clientModel string) ([]byte, error) {
	return ir.SerializeOpenAIResponse(irResp, clientModel)
}

func (a *irAdapter) SerializeAnthropicResponse(irResp *ir.InternalResponse, clientModel string) ([]byte, error) {
	return ir.SerializeAnthropicResponse(irResp, clientModel)
}

// Phase E (2026-07-01): Responses API serializer. Implements streaming.IRConverter
// for the Responses API client target (ClientProtocol == "openai-responses").
// The stream serializer is per-chunk and emits ONE OR MORE Responses SSE events;
// the response serializer produces the complete non-stream body.
func (a *irAdapter) SerializeResponses(chunk *ir.StreamChunk, itemID string) string {
	return chunk.SerializeResponses(itemID)
}

func (a *irAdapter) SerializeResponsesResponse(irResp *ir.InternalResponse, clientModel string) ([]byte, error) {
	return ir.SerializeResponsesResponse(irResp, clientModel)
}
