package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/cache/prefix"
	"github.com/kaixuan/llm-gateway-go/domains/attachments"    //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/autocombo"
	"github.com/kaixuan/llm-gateway-go/domains/credential"                          //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"                            //nolint:depguard // V3.2 state-transition logger
	"github.com/kaixuan/llm-gateway-go/domains/freeresource"                        //nolint:depguard // OmniFree quota tracker
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"                         //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"                   //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/goal"                          //nolint:depguard // Goal retry outcome observer
	"github.com/kaixuan/llm-gateway-go/domains/hooks/handoff"                       //nolint:depguard // request-side session handoff hook
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"       //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"                      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	sessionaudithook "github.com/kaixuan/llm-gateway-go/domains/hooks/sessionaudit" //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/identity"                            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/session"                             //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/transformation"                      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/i18n"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
	"github.com/kaixuan/llm-gateway-go/internal/modelpolicy"
	"github.com/kaixuan/llm-gateway-go/internal/observability"
	"github.com/kaixuan/llm-gateway-go/internal/streamretry" //nolint:depguard // trusted tenant propagation to outer retry wrapper
	gwtrace "github.com/kaixuan/llm-gateway-go/internal/trace"
	"github.com/kaixuan/llm-gateway-go/maas"
	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/modelname"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/registry"
	"github.com/kaixuan/llm-gateway-go/resolve"
	"github.com/kaixuan/llm-gateway-go/security/armor"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const maxBodySize = 128 << 20 // 128MB - increased for large context models like claude-opus-4-8 (1M context)

func MaxBodySize() int { return maxBodySize }

type preStreamKeepalive struct {
	w       http.ResponseWriter
	flusher http.Flusher
	stopCh  chan struct{}
	doneCh  chan struct{}
	mu      sync.Mutex
	once    sync.Once
	// paused is set by the executor when entering the synchronous
	// no-candidate probe hold. While paused, the loop() goroutine
	// skips writing SSE keepalive comments so the client does not
	// interpret a stale comment as a response-start signal during
	// the hold. Resumed by resume(); the goroutine checks the flag
	// on every tick so the resume latency is at most one keepalive
	// interval (default 15s — but the executor's 5s probe timeout
	// means we resume long before that regardless).
	paused atomic.Bool
}

type retryCommitWriter struct {
	http.ResponseWriter
	wrote atomic.Bool
}

func (w *retryCommitWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.wrote.Store(true)
	}
	return w.ResponseWriter.Write(p)
}

func (w *retryCommitWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// interceptingStreamWriter applies the response interceptor chain to complete
// SSE events before they reach the client. Upstream bridges may split an SSE
// event across multiple Write calls, so the writer buffers until the event
// delimiter (\n\n) is present.
type interceptingStreamWriter struct {
	w        http.ResponseWriter
	flusher  http.Flusher
	chain    ResponseInterceptor
	ctx      context.Context
	meta     response.StreamMeta
	pending  []byte
	writeErr error
}

func newInterceptingStreamWriter(w http.ResponseWriter, chain ResponseInterceptor, ctx context.Context, meta response.StreamMeta) *interceptingStreamWriter {
	writer := &interceptingStreamWriter{w: w, chain: chain, ctx: ctx, meta: meta}
	if f, ok := w.(http.Flusher); ok {
		writer.flusher = f
	}
	return writer
}

func (w *interceptingStreamWriter) Header() http.Header        { return w.w.Header() }
func (w *interceptingStreamWriter) WriteHeader(statusCode int) { w.w.WriteHeader(statusCode) }

func (w *interceptingStreamWriter) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	w.pending = append(w.pending, p...)
	w.drain()
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return len(p), nil
}

func (w *interceptingStreamWriter) Flush() {
	_ = w.FlushError()
}

func (w *interceptingStreamWriter) FlushError() error {
	w.drain()
	if w.flusher == nil {
		return nil
	}
	if errorFlusher, ok := w.flusher.(interface{ FlushError() error }); ok {
		return errorFlusher.FlushError()
	}
	w.flusher.Flush()
	return nil
}

func (w *interceptingStreamWriter) finish() {
	w.drain()
	if len(w.pending) > 0 && w.writeErr == nil {
		_, w.writeErr = w.w.Write(w.pending)
		w.pending = nil
	}
}

func (w *interceptingStreamWriter) drain() {
	for w.writeErr == nil {
		idx := bytes.Index(w.pending, []byte("\n\n"))
		if idx < 0 {
			return
		}
		frameEnd := idx + 2
		frame := append([]byte(nil), w.pending[:frameEnd]...)
		w.pending = w.pending[frameEnd:]
		w.writeFrame(frame)
	}
}

func (w *interceptingStreamWriter) writeFrame(frame []byte) {
	final := frame
	if w.chain != nil {
		result, err := w.chain.InterceptStreamChunk(w.ctx, frame, &w.meta)
		if err == nil && result != nil {
			if result.ShouldBlock {
				return
			}
			if len(result.ModifiedChunk) > 0 {
				final = result.ModifiedChunk
			}
			if len(result.InjectAfter) > 0 {
				final = append(append([]byte(nil), final...), result.InjectAfter...)
			}
		}
	}
	if _, err := w.w.Write(final); err != nil {
		w.writeErr = err
	}
}

func startPreStreamKeepalive(w http.ResponseWriter, interval time.Duration, requestID string) (*preStreamKeepalive, bool) {
	if _, ok := w.(http.Flusher); !ok {
		return nil, false
	}
	if interval <= 0 {
		interval = 15 * time.Second
	}
	// 2026-08-15 (A-P2-6): the keepalive loop is one of several producers on
	// this connection — the bridges write the same ResponseWriter once the
	// stream starts. Wrap the connection in a serializedResponseWriter so
	// keepalive comments and stream frames can never interleave; the handler
	// reassigns its local writer to psk.Writer() so later writes join the
	// same channel. The survival path (survival_wiring.go) installs its own
	// SerializedStreamWriter after stopping this loop, so the two never
	// overlap on one connection.
	sw := NewSerializedResponseWriter(w)
	psk := &preStreamKeepalive{
		w:       sw,
		flusher: sw,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// 2026-08-04: set X-Accel-Buffering here so nginx/reverse proxies disable
	// response buffering from the very first byte. Previously this header was
	// only set inside each protocol bridge (stream.go/anthropic_bridge.go/
	// responses_bridge.go) AFTER WriteHeader — too late for the prewarmed
	// path, where keepalive comments would be buffered and never reach the
	// client, defeating the whole purpose.
	w.Header().Set("X-Accel-Buffering", "no")
	// 2026-08-04: stamp X-Request-Id here so the response carries the
	// request id on the prewarmed path. Each protocol bridge sets it again
	// later, but that happens AFTER its own WriteHeader — too late once we
	// have already committed headers. The caller passes the already-resolved
	// id as a parameter (see preStreamKeepalive call site) rather than
	// re-reading r.Header, which may not be populated yet.
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(http.StatusOK)
	psk.writeComment(sseKeepaliveComment)
	go psk.loop(interval)
	return psk, true
}

func (p *preStreamKeepalive) loop(interval time.Duration) {
	defer close(p.doneCh)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			if p.paused.Load() {
				continue
			}
			p.writeComment(sseKeepaliveComment)
		}
	}
}

func (p *preStreamKeepalive) writeComment(line string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	safeWriteSSE(p.w, line)
	safeFlush(p.flusher)
}

// writeThinking sends a thinking event to the client (SSE event: thinking).
// Used to display node failover status without entering the conversation.
//
// Format compatibility (2026-07-23 research):
//   - SSE comment format: `: text\n\n` — all SSE parsers treat comments as
//     no-ops; no event/data dispatched to any event handler or onmessage.
//   - The opencode client parses ALL data: lines through a Zod union
//     (choices[] | error{} | ...). ANY data: line that doesn't match
//     triggers "Type validation failed" / "invalid_union" — regardless of
//     event: type. The previous approach (event: thinking + data: {...})
//     relied on incorrect assumption that Zod skips unknown event types.
//   - SSE comment format avoids data: entirely, keeping the connection
//     alive without any client-side parsing risk.
func (p *preStreamKeepalive) writeThinking(message string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	escaped, _ := json.Marshal(message)
	fmt.Fprintf(p.w, ": thinking: %s\n\n", escaped)
	safeFlush(p.flusher)
}

// pause suspends future keepalive comments. Idempotent.
func (p *preStreamKeepalive) pause() {
	if p == nil {
		return
	}
	p.paused.Store(true)
}

// resume re-enables keepalive comment writes. Idempotent.
func (p *preStreamKeepalive) resume() {
	if p == nil {
		return
	}
	p.paused.Store(false)
}

// Writer returns the serialized connection view (the serializedResponseWriter
// this keepalive installed). The handler reassigns its local ResponseWriter
// to it so bridge writes share the keepalive's serialized channel instead of
// racing the keepalive loop on the raw connection (doc 20 A-P2-6).
func (p *preStreamKeepalive) Writer() http.ResponseWriter { return p.w }

func (p *preStreamKeepalive) stop() {
	if p == nil {
		return
	}
	p.once.Do(func() { close(p.stopCh) })
	<-p.doneCh
}

func writePrewarmedStreamError(w http.ResponseWriter, message, errType, code string) {
	writePrewarmedStreamErrorWithKind(w, message, errType, code, "")
}

// writePrewarmedStreamErrorWithKind is the variant that carries the real
// upstream error kind so the SDK can distinguish "the model does not
// exist" from "the relay is overloaded". When kind is empty the envelope
// is byte-identical to the historical 3-field shape, keeping the seven
// existing call sites backwards-compatible.
//
// The reasoning behind exposing kind only here (and not on the non-prewarmed
// JSON path, which already includes it via writeErrorJSONWithKind) is that
// the prewarmed path is the only one where the existing code field is a
// lie: the underlying failure is not "model not found" but an exhausted
// upstream, and the SDK benefits from knowing that. The other six models
// that did not have a code-vs-kind mismatch before the fix continue to
// write kind="", so nobody sees a new field they did not expect.
func writePrewarmedStreamErrorWithKind(w http.ResponseWriter, message, errType, code, kind string) {
	if errType == "" {
		errType = "server_error"
	}
	if code == "" {
		code = "provider_error"
	}
	body := fmt.Sprintf("data: {\"error\":{\"message\":%q,\"type\":%q,\"code\":%q", message, errType, code)
	if kind != "" {
		body += fmt.Sprintf(",\"kind\":%q", kind)
	}
	body += "}}\n\n"
	safeWriteSSE(w, body)
	if flusher, ok := w.(http.Flusher); ok {
		safeFlush(flusher)
	}
}

// RequestIdentity contains immutable correlation fields derived at the HTTP boundary.
type RequestIdentity struct {
	RequestID       string
	ClientRequestID string
	TenantID        string
	APIKeyID        int
	ClientType      string
	ClientModel     string
	SessionID       string
	UserKey         string
}

// initializeRequestIdentity establishes one stable request and provisional
// gateway-session identity even when middleware is bypassed by direct tests.
func initializeRequestIdentity(r *http.Request) RequestIdentity {
	identity := RequestIdentity{TenantID: "default"}
	if r == nil {
		identity.RequestID = generateRequestID()
		identity.SessionID = generateSystemSessionID()
		return identity
	}
	identity.RequestID = strings.TrimSpace(r.Header.Get("X-Request-Id"))
	if identity.RequestID == "" {
		identity.RequestID = generateRequestID()
		r.Header.Set("X-Request-Id", identity.RequestID)
	}
	identity.ClientRequestID = strings.TrimSpace(r.Header.Get("X-Gw-Client-Request-Id"))
	if identity.ClientRequestID == "" {
		identity.ClientRequestID = strings.TrimSpace(r.Header.Get("X-Client-Request-Id"))
	}
	identity.SessionID = sanitizeGwSessionHeader(r.Header.Get("X-Gw-Session-Id"))
	if identity.SessionID == "" {
		identity.SessionID = generateSystemSessionID()
		r.Header.Set("X-Gw-Session-Id", identity.SessionID)
	}
	identity.ClientType = strings.TrimSpace(r.Header.Get("X-Gw-Client-Type"))
	return identity
}

func sanitizeGwSessionHeader(v string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return ""
	}
	// Accept the three gateway-namespaced session-id prefixes:
	//   gw_<uuid>  — user main session
	//   gt_<...>   — auto-title branch session (admin/auto_title_generator.go)
	//   gs_<...>   — auto-summary branch session (admin/auto_summary_generator.go)
	// Treat plain UUID-style values as client metadata/session identifiers,
	// not gateway session IDs. Branch prefixes MUST stay in sync with the
	// X-Gw-Session-Id header set by the auto-title / auto-summary loopback
	// callers; otherwise the loopback's session_id is silently dropped and
	// the resulting request_logs_hot row shows a fresh gw_<uuid>.
	if !strings.HasPrefix(s, "gw_") && !strings.HasPrefix(s, "gt_") && !strings.HasPrefix(s, "gs_") {
		return ""
	}
	return s
}

// isBranchSessionID reports whether the already-sanitized session id belongs to
// an auto-generated background branch rather than a user-facing main session.
// The two namespaces are written by the auto-title / auto-summary loopback
// (admin/auto_title_generator.go, admin/auto_summary_generator.go) and MUST be
// preserved verbatim on the request log row so operators can SQL:
//
//	WHERE gw_session_id LIKE 'gt\_%' ESCAPE '\'   -- every auto-title row
//	WHERE gw_session_id LIKE 'gs\_%' ESCAPE '\'   -- every auto-summary row
//
// They must NOT be auto-created/migrated into a fresh gw_<uuid> by the chat
// handler's ErrSessionNotFound fallback, nor written into the no-session
// "last system session" resume pointer. The prefixes MUST stay in sync with
// sanitizeGwSessionHeader above.
func isBranchSessionID(sessionID string) bool {
	return strings.HasPrefix(sessionID, "gt_") || strings.HasPrefix(sessionID, "gs_")
}

// InitializeRequestIdentity is the exported helper that establishes one
// stable request identity (server-issued request_id, optional client
// request_id, and a provisional gateway session id) at the HTTP
// boundary. It mirrors the contract used by the production ChatHandler
// pipeline so that new callers (and tests that bypass the full
// ChatHandler) get identical behaviour:
//
//   - request_id: prefers the inbound X-Request-Id (set by the
//     RequestIDMiddleware); if absent, generates a fresh value and
//     writes it back to BOTH the request header (so downstream
//     middleware / audit / telemetry see it) AND the response header
//     (so the client can correlate on the way back).
//   - client_request_id: reads X-Gw-Client-Request-Id first, then
//     falls back to the legacy X-Client-Request-Id; if non-empty and
//     different from request_id, writes the canonical
//     X-Client-Request-Id back to the response so legacy clients
//     still see the value they sent.
//   - session_id: reuses ensureSessionID semantics — it RETURNS the
//     resolved id but does NOT mutate the response header. The full
//     session assignment pipeline decides whether to surface the id
//     to the client (after body parsing + DB lookup).
//   - tenant_id: defaults to "default" when no keyInfo context is
//     available; ChatHandler overrides this after API key
//     authentication.
//
// Why this is a separate function (2026-07-28, request-flow audit
// spec §10 Step 2):
//
//   - The internal initializeRequestIdentity(r) helper exists to feed
//     the Messages/Responses/Gemini sub-handlers, which never see
//     http.ResponseWriter directly. They only mutate r.Header for
//     downstream observability, so they don't need the response-side
//     writes.
//   - InitializeRequestIdentity is the version new code SHOULD call:
//     it writes the canonical response headers (X-Request-Id,
//     X-Client-Request-Id) so external clients and proxies can
//     correlate requests even when the upstream middleware is
//     skipped (e.g. integration tests that bypass the router).
//
// This helper MUST stay within the ChatHandler package — it touches
// the package-private RequestIdentity type and reuses the same
// generation functions. Do not move to a shared package without
// lifting RequestIdentity with it.
func InitializeRequestIdentity(r *http.Request, w http.ResponseWriter) RequestIdentity {
	identity := RequestIdentity{TenantID: "default"}

	// request_id: prefer inbound X-Request-Id; generate + write back
	// if missing. Mirror the value to the response header so external
	// clients see the server-issued id even on direct (non-proxied)
	// connections.
	identity.RequestID = strings.TrimSpace(getRequestHeader(r, "X-Request-Id"))
	if identity.RequestID == "" {
		identity.RequestID = generateRequestID()
		setRequestHeader(r, "X-Request-Id", identity.RequestID)
	}
	if w != nil {
		w.Header().Set("X-Request-Id", identity.RequestID)
	}

	// client_request_id: canonical header first, legacy fallback.
	// When non-empty and different from request_id, surface it on the
	// response so legacy clients can still echo the value back.
	identity.ClientRequestID = strings.TrimSpace(getRequestHeader(r, "X-Gw-Client-Request-Id"))
	if identity.ClientRequestID == "" {
		identity.ClientRequestID = strings.TrimSpace(getRequestHeader(r, "X-Client-Request-Id"))
	}
	if identity.ClientRequestID != "" && w != nil && identity.ClientRequestID != identity.RequestID {
		w.Header().Set("X-Client-Request-Id", identity.ClientRequestID)
	}

	// provisional session_id: reuse ensureSessionID semantics. The id
	// is returned for early-failure logging / request_log_context but
	// is NOT written to the response header — the full session
	// assignment pipeline (after body parsing + DB lookup) decides
	// what to surface.
	identity.SessionID = ensureSessionIDIdentity(r)

	identity.ClientType = strings.TrimSpace(getRequestHeader(r, "X-Gw-Client-Type"))
	return identity
}

// getRequestHeader is a nil-safe wrapper around r.Header.Get.
func getRequestHeader(r *http.Request, key string) string {
	if r == nil {
		return ""
	}
	return r.Header.Get(key)
}

// setRequestHeader is a nil-safe wrapper around r.Header.Set.
func setRequestHeader(r *http.Request, key, value string) {
	if r == nil {
		return
	}
	r.Header.Set(key, value)
}

// ensureSessionIDIdentity mirrors the provisional-only behaviour of
// (*ChatHandler).ensureSessionID without taking a *ChatHandler
// receiver — used by InitializeRequestIdentity when called outside
// the full handler pipeline.
//
// Contract (2026-07-28):
//   - returns the client-supplied X-Gw-Session-Id when present and
//     gw_-prefixed
//   - otherwise returns a fresh gw_<uuid> so request_logs.gw_session_id
//     is never empty
//   - NEVER writes X-Gw-Session-Id back to the response — the
//     canonical session id is decided after body parsing
func ensureSessionIDIdentity(r *http.Request) string {
	if id := sanitizeGwSessionHeader(getRequestHeader(r, "X-Gw-Session-Id")); id != "" {
		return id
	}
	return generateSystemSessionID()
}

// stripLegacyToolCallText removes the legacy "[Tool Call: <name>]\n"
// markers (and the bare arguments JSON that immediately follows them)
// from a stream_text_content blob when the same tool_calls are also
// available as structured data via audit.StreamCapture.ToolCalls.
//
// audit/stream.go ObserveChunk appends both a structured entry
// (sc.ToolCalls, via mergeToolCall) AND a free-text rendering into
// sc.textContent. The latter is preserved for any consumer that reads
// stream_text_content as a unified text preview. When emitTelemetry
// synthesizes a final response_body, however, the structured entries
// must be the SOLE source of truth for tool_calls — otherwise the same
// data appears twice in different shapes and OpenAI Chat Completions
// clients (which expect content to be plain assistant prose and
// tool_calls to be a separate array) reject the response.
//
// The marker format emitted by audit/stream.go is:
//
//	"\n[Tool Call: <name>]\n<arguments-json>"
//
// We strip every "[Tool Call: ...]" marker plus the JSON value that
// follows it on the same logical block. We are deliberately conservative:
// the marker text is a fixed-prefix sentinel that no upstream LLM emits
// in practice, so false-positive stripping is not a concern.
var legacyToolCallMarkerRE = regexp.MustCompile(`(?s)\[Tool Call:[^\]]*\]\n?`)

func stripLegacyToolCallText(s string) string {
	if s == "" {
		return s
	}
	return legacyToolCallMarkerRE.ReplaceAllString(s, "")
}

// ServiceID maps an API key to a (providerID, credentialID) pair.
type ServiceID struct {
	ProviderID   int
	CredentialID int
}

type chatRequestBody struct {
	Model    string          `json:"model"`
	Stream   bool            `json:"stream"`
	Messages json.RawMessage `json:"messages,omitempty"`
	User     string          `json:"user,omitempty"`
	// Tools is the optional function/tool definitions array.
	// Used by autoroute (v2.0) to detect multi-tool agent requests.
	Tools json.RawMessage `json:"tools,omitempty"`
	// ToolIDs (Phase 3, 2026-06-21) is the optional tool ID array.
	// Format: ["filesystem.*", "network.http_get"]
	// Expands to full tool definitions via toolRegistry.
	ToolIDs []string `json:"tool_ids,omitempty"`
}

//-----------------------------------------------------------------------------

// Phase 1: Retry logic helpers for Goal mode error retry
// Added: 2026-07-19

// isRetriableError determines if an error should trigger a retry attempt.
// Returns true for transient errors (network, timeout, 5xx, 429, no_candidates),
// false for permanent errors (4xx client errors, auth failures, content filters).
func isRetriableError(err error) bool {
	if err == nil {
		return false
	}

	// Check ExecuteError type for error kind classification
	if execErr, ok := err.(*executors.ExecuteError); ok {
		kind := execErr.LastKind

		// Network-related errors (transient)
		if kind == errorsx.KindNetwork ||
			kind == errorsx.KindTimeout ||
			kind == errorsx.KindUpstreamDown ||
			kind == errorsx.KindUpstreamOverloaded {
			return true
		}

		// Rate limiting (transient, should retry with backoff)
		if kind == errorsx.KindRateLimit {
			return true
		}

		// Transient errors (general)
		if kind == errorsx.KindTransient {
			return true
		}

		// No available model (may recover on retry with different routing)
		if kind == errorsx.KindModelNotFound {
			return true
		}

		// Concurrency limit exceeded (transient)
		if kind == errorsx.KindConcurrent {
			return true
		}

		// Permanent errors - do NOT retry
		if kind == errorsx.KindAuth ||
			kind == errorsx.KindAuthRevoked ||
			kind == errorsx.KindContentFilter ||
			kind == errorsx.KindContextLength ||
			kind == errorsx.KindModelDeprecated ||
			kind == errorsx.KindQuotaPeriodic ||
			kind == errorsx.KindQuotaPermanent {
			return false
		}
	}

	// Check upstream HTTP status code via extractUpstreamError
	if ue, ok := extractUpstreamError(err); ok {
		// 429 Too Many Requests - retriable
		// 5xx Server Error - retriable
		if ue.StatusCode == 429 || ue.StatusCode >= 500 {
			return true
		}
	}

	// Default: not retriable (conservative approach)
	return false
}

// calculateRetryDelay computes the delay before the next retry attempt using
// exponential backoff with random jitter to prevent thundering herd.
//
// Formula:
//
//	delayMs = baseDelayMs * (2 ^ attempt)
//	delayMs = min(delayMs, maxDelayMs)
//	jitter = delayMs * 0.2 * random(-1, 1)  // ±20%
//	finalDelay = delayMs + jitter
//
// Example progression with baseDelayMs=100, maxDelayMs=5000:
//
//	attempt=0: 100ms ± 20% = 80-120ms
//	attempt=1: 200ms ± 40% = 160-240ms
//	attempt=2: 400ms ± 80% = 320-480ms
//	attempt=3: 800ms ± 160ms = 640-960ms
//	attempt=4+: 5000ms ± 1000ms = 4000-6000ms (capped)
func calculateRetryDelay(attempt int, baseDelayMs int, maxDelayMs int) time.Duration {
	if baseDelayMs <= 0 {
		baseDelayMs = 100 // default 100ms
	}
	if maxDelayMs <= 0 {
		maxDelayMs = 5000 // default max 5s
	}

	// Exponential backoff: 100ms -> 200ms -> 400ms -> 800ms -> ...
	delayMs := baseDelayMs * (1 << attempt)
	if delayMs > maxDelayMs {
		delayMs = maxDelayMs
	}

	// Add ±20% random jitter to prevent retry storms
	jitter := float64(delayMs) * 0.2
	jitterMs := int(jitter * (2*rand.Float64() - 1)) // range: -20% to +20%
	delayMs += jitterMs

	// Ensure non-negative
	if delayMs < 0 {
		delayMs = baseDelayMs
	}

	return time.Duration(delayMs) * time.Millisecond
}

// Chat handler — integrates circuit breaker + concurrency limiter
//-----------------------------------------------------------------------------

type providerResolver interface {
	Enabled() bool
	GetCandidates(ctx context.Context, model, profile, tenantID string) ([]provider.Candidate, *provider.Policy, error)
	GetCandidatesByModality(ctx context.Context, model, profile, tenantID, modality string) ([]provider.Candidate, *provider.Policy, error)
	ModelKnown(ctx context.Context, model string) bool
}

// requestKeyVerifier is the authorization surface used by request handlers.
// *authentication.KeyVerifier is the production implementation; the interface
// keeps endpoint-level recovery tests on the same authentication contract.
type requestKeyVerifier interface {
	Enabled() bool
	Verify(ctx context.Context, rawKey string) (*authentication.KeyInfo, error)
	VerifyByID(ctx context.Context, id int) (*authentication.KeyInfo, error)
	CheckBudget(ctx context.Context, keyID int) error
	LookupKeyMeta(ctx context.Context, rawKey string) (*authentication.KeyLookupMeta, error)
}

// ChatHandler handles chat completions with circuit breaker and concurrency control.
type ChatHandler struct {
	circuit    *credential.Manager
	limiter    *credential.Limiter
	matrix     *transformation.Matrix
	pools      *pool.PoolManager
	resolver   *resolve.Resolver
	auditor    audit.Sink
	client     *upstreampkg.Client
	normalizer *Normalizer
	executor   *executors.Executor
	// SR-W2 request survival (doc 18 §6): nil gate keeps the survival branch
	// inert; armed via SetRequestSurvival from main.go.
	survivalTenantAllowed func(tenantID string) bool
	survivalOptions       SurvivalOptions
	// SR-12 durable execution (doc 18 §11.2): nil store keeps the durable
	// handoff inert; armed via SetDurableExecution from main.go.
	durableStore         DurableForegroundStore
	durableTenantAllowed func(tenantID string) bool
	durableExecOptions   DurableExecutionOptions
	provider             providerResolver
	sticky               *executors.StickyCache
	keyVerifier          requestKeyVerifier
	survivalAttemptExec  AttemptExecutor
	rateLimiter          ratelimit.RPMLimiter
	telemetryClient      *telemetry.Client
	// profileEmitter (2026-07-15) 把请求/会话事件投到 clientprofile 画像聚合。
	// nil 禁用画像聚合；调用方负责 graceful 注入（main.go SetupClientProfileIntegration）。
	profileEmitter interface {
		EmitRequestCompleted(ctx context.Context, sc *session.SessionContext, identityHash string, success bool, tokensUsed int, latencyMs int64) error
		EmitSessionStarted(ctx context.Context, sc *session.SessionContext, identityHash string) error
	}
	// decider (v2.0) is the optional autoroute.Decider. When non-nil,
	// requests with model="auto" trigger task classification + 6-dim
	// scoring. When nil, model="auto" falls back to default chat model.
	decider *autoroute.Decider
	// altFinder (2026-08-09) supplies the "you could use these instead" list
	// on the zero-candidate exit. Optional: nil means that exit keeps its
	// historical bare 503, so a deployment that has not wired it loses the
	// suggestion but nothing else.
	altFinder *ModelAlternativesFinder
	// requestLogHook is an optional test sink.  When set, every
	// request_logs row the gateway emits is also passed to the hook
	// function so unit tests can assert on the safety-net coverage.
	// See SetRequestLogHook.
	requestLogHook func(*telemetry.RequestLogEntry)
	// sessionAuditHook (2026-06-28, session-audit feature) is the
	// pre-routing chat-time hook.  When non-nil, every chat request
	// goes through FastDetector before hitting GetCandidates(); Block /
	// NeedApproval decisions short-circuit the request with 403 / 202.
	// See SetSessionAuditHook.
	sessionAuditHook *sessionaudithook.SessionAuditHook
	maasSvc          *maas.Service
	sessionGetter    interface {
		Get(ctx context.Context, id string) (*session.Session, error)
		Touch(ctx context.Context, id string) error
		CreateV2(ctx context.Context, apiKeyID int, tenantID, deviceSeed, taskID string) (*session.Session, error)
		BindAPIKey(ctx context.Context, sessionID string, apiKeyID int, tenantID string) error
	}
	// idempotentCache (Track C C5, 2026-06-18) deduplicates
	// re-sent requests within a 5-minute window. When a client
	// retries (network glitch, double-click), the handler
	// returns 202 + X-Gw-Pending immediately rather than
	// re-executing the full routing + vendor path. nil disables
	// the dedup (every request is treated as new).
	idempotentCache *IdempotentCache

	// sessionCompressor (v3, 2026-06-19) is the session-level
	// intelligent compression. When non-nil, each request runs a
	// message-level LCS delta-append + optional proactive sliding-window
	// LLM summary before forwarding to the upstream. nil disables v3
	// (every request forwards the client body as-is, matching v7 behaviour).
	sessionCompressor *compression.SessionCompressor

	// promptCacheStabilize (rtk borrowing, 2026-07-06) reorders the request
	// messages by stability class (system → tools → history → tail) so the
	// upstream provider's KV-prefix-cache hits maximise. Idempotent + always
	// fail-open (Stabilize returns the original bytes on any unrecognised
	// shape). Toggle with LLM_GATEWAY_PROMPT_CACHE_STABILIZE=0. Default true.
	promptCacheStabilize bool

	// cacheInjector (rtk borrowing, 2026-07-06) injects cache_control markers
	// (Anthropic ephemeral / OpenAI checkpoint) onto the stabilized prefix
	// boundary when the resolved candidate supports prompt caching. nil or
	// promptCacheInject=false disables injection. Opt-in via
	// LLM_GATEWAY_PROMPT_CACHE_INJECT=1; default off (data-accuracy first).
	cacheInjector     *session.CacheInjector
	promptCacheInject bool

	// metaToolInterceptor (Phase 2, 2026-06-20) handles meta-tool calls

	// anomalyRecorder (2026-06-28) tracks response format anomalies to detect
	// provider API changes and improve token estimation logic. nil disables.
	anomalyRecorder *FormatAnomalyRecorder

	// integrityDetector (2026-07-28) emits model-integrity events
	// (model_mismatch, finish_refusal, finish_truncation, empty_response,
	// repeated_content). Runs once per request inside emitTelemetry and
	// also once per OpenAI non-stream success path in the executor.
	// nil disables (legacy behavior).
	integrityDetector executors.IntegrityDetector
	// (list_categories, load_tools) locally without forwarding to upstream.
	// nil disables Phase 2 meta-tools.
	metaToolInterceptor *MetaToolInterceptor

	// toolRegistry (Phase 3, 2026-06-21) provides centralized tool definitions.
	// When non-nil, requests with tool_ids expand to full tool definitions.
	// nil disables Phase 3 tool registry (tool_ids are ignored).
	toolRegistry ToolRegistryService

	// modelPolicy (Round 48, 2026-06-21) is the tenant-scoped model
	// denylist.  nil disables enforcement (every model allowed).
	// Wire from main.go after constructing the Checker.
	modelPolicy *modelpolicy.Checker

	// requestLogger (Request WAL) provides synchronous initial logging
	// at request arrival and asynchronous stage updates. nil disables.
	requestLogger *telemetry.RequestLogger

	// traceRecorder (2026-07-17) 注入请求链路追踪器。
	// nil 禁用(降级为 NoopRecorder 等价)。调用方负责 best-effort 注入。
	traceRecorder gwtrace.Recorder

	// liveActions (2026-08-15, V3.3-OBS OBS-B1) 注入请求生命周期动作事件
	// 发射器（arrive/route_resolved/first_byte 等，见 docs/会话优化v3/24 §2）。
	// nil 安全（*liveactions.Emitter 的 Emit 对 nil receiver 是 no-op）；
	// 旁路异步、满即丢，不阻塞请求热路径。
	liveActions *liveactions.Emitter

	// autoTitleGenerator (2026-06-22) automatically generates session titles
	// after the first successful request. nil disables auto-title generation.
	autoTitleGenerator interface {
		// 2026-08-05: requestBody is the full (redacted) inbound body so the
		// title LLM gets the actual user message. requestPreview is the
		// 320-byte summary used as fallback.
		// 2026-08-06: parentRequestID is the user request_id that triggered
		// this title generation; forwarded as X-Gw-Parent-Request-Id so
		// request_logs_hot.parent_request_id makes the loopback joinable.
		// 2026-08-06: taskID is the request's gw_task_id so the stored title
		// row matches request_logs on (task_id, scoped_session_id).
		MaybeGenerateTitle(sessionID, tenantID, taskID, requestBody, requestPreview, parentRequestID, requestID string)
	}

	// autoSummaryGenerator (2026-08-06) incrementally rolls session
	// summaries via map-reduce over the request path. nil disables
	// auto-summary generation; the v2 dispatch background worker
	// (domains/sessionsummary) still runs for session.closed events.
	autoSummaryGenerator interface {
		MaybeGenerateSummary(sessionID, tenantID, requestBody, requestPreview, parentRequestID string)
	}

	// armorJudge (Track A B1-5, 2026-06-25) scores prompts for security risks.
	// When non-nil, handler calls Judge.Score before executing LLM requests.
	// nil disables armor checks (every request proceeds without judgment).
	armorJudge armor.Judge

	// armorLogger (Track A B1-5, 2026-06-25) writes armor judgments to audit table.
	// When non-nil, handler calls Logger.Log after armor scoring.
	// nil disables armor audit (judgments are not persisted).
	armorLogger *armor.Logger
	// lastSystemSession enables 5-minute no-id session reuse.
	lastSystemSession *session.LastSystemSessionIndex
	// sessionPref tracks session -> credential preference for model switch handling.
	sessionPref *session.SessionPreference
	// rotationHook (2026-07-06) detects credential rotation and updates
	// session state (turns, tokens, cost, cred-history). nil disables.
	rotationHook *session.RotationHook
	// sessionReuseWindow (2026-06-26) is the look-back window for
	// FindRecentGatewaySession. Defaults to 5 * time.Minute; can be
	// overridden via LLM_GATEWAY_SESSION_REUSE_WINDOW env. 0 means
	// "always create new" (no recent-session reuse).
	sessionReuseWindow time.Duration

	// responseInterceptor (2026-06-29, auto-control feature) intercepts
	// LLM responses before forwarding to clients. Goal and output-compliance
	// use this path; handoff executes before provider dispatch via handoffHook.
	responseInterceptor ResponseInterceptor

	// sanitizeInputMiddleware (2026-08-07, SmartSaniGuard) wraps the
	// chatHandler entry point to replace sensitive info in the request body
	// with {SENSITIVE:type:index} placeholders before the request reaches
	// the executor. The reverse mapping is persisted to Redis and consumed
	// by SanitizeRestoreInterceptor on the response path. nil = disabled.
	sanitizeInputMiddleware func(http.Handler) http.Handler

	// handoffHook may return an explicit resume packet before provider dispatch;
	// it must never inject gateway control data into the provider request body.
	handoffHook          *handoff.TriggerHook
	handoffSessionGetter interface {
		Get(ctx context.Context, id string) (*session.Session, error)
	}

	// attachmentExtractor (2026-07-01) extracts base64/data-URI attachments
	// from incoming requests and saves them to the filesystem before forwarding.
	// nil disables attachment extraction (attachments remain inline in request body).
	attachmentExtractor *attachments.Extractor

	// handoffFallbackAuth (2026-07-11, handoff self-call fix)
	handoffFallbackAuth string

	// dispatchFollowUpRequest (2026-07-11) is the inner dispatch seam used
	// by injectFollowUpRequest to send a synthetic /v1/chat/completions
	// request. nil falls back to defaultDispatchFollowUp, which loops the
	// request through h.ServeHTTP. Tests can overwrite the field to stub
	// out the network/auth path without spinning up the full pipeline.
	dispatchFollowUpRequest dispatchFollowUpFunc

	// goalRetryPolicyResolver (2026-07-23) resolves tenant-scoped retry policy.
	// When non-nil, retry loop reads per-request policy from tenant settings
	// instead of fixed boot-time config. nil falls back to minimal defaults.
	goalRetryPolicyResolver GoalRetryPolicyResolver

	// goalRetryRecorder (2026-07-23) persists actual retry count to goal_sessions.
	// When non-nil, handler writes retry_count after each request. nil disables
	// persistence (fail-open: retry behavior unchanged, only stats missing).
	goalRetryRecorder   GoalRetryRecorder
	goalOutcomeObserver goal.OutcomeObserver

	// formatDetector (2026-07-26) automatically detects client request format patterns.
	// When non-nil, handler identifies format (OpenAI, OpenCode, etc.) and applies
	// known fixes before validation. nil disables format detection (strict validation only).
	formatDetector *FormatDetector

	// formatFixer (2026-07-26) applies automatic fixes to common format issues.
	// Works with formatDetector to repair malformed requests (empty objects, wrong types).
	// nil disables auto-fix (requests must be valid on arrival).
	formatFixer *FormatFixer

	// formatCache (2026-07-26) caches detected format patterns per session in Redis.
	// Avoids repeated detection for same client. nil disables caching (detect every request).
	formatCache FormatCache

	// OmniFree (Phase 4, 2026-08-07) - optional virtual auto/* routing.
	// When autoComboResolver + autoComboFactory are wired, requests with
	// client model starting with "auto/" (excluding the exact "auto"
	// magic handled by maybeResolveAuto) are routed through VirtualFactory.
	// quotaTracker records per-credential request usage for free-tier providers.
	autoComboResolver *autocombo.Resolver
	autoComboFactory  *autocombo.VirtualFactory
	quotaTracker      QuotaRecorder
	omniFreeMu        sync.RWMutex

	// round 4 审计补充修复 (L3): quotaRecordQueue + quotaWorkersDone 为
	// recordOmniFreeQuota 提供 bounded 异步队列, 防止无限制启动 goroutine
	// (旧实现每个请求都 go func() 直接 fork, 高并发时可能积压上万个未完成
	// goroutine + 数据库连接, 导致 pgx pool 耗尽 / context 泄漏).
	// SetOmniFree 时启动固定数量的 worker, quotaRecordQueue 缓冲 N 个待处理
	// 任务; 溢出时 recordOmniFreeQuota 非阻塞丢弃 + 打 WARN 日志.
	quotaRecordQueue chan quotaRecordTask
	quotaWorkersDone sync.WaitGroup
	quotaQueueMu     sync.Mutex
	quotaAccepting   bool
}

// ToolRegistryService is the interface for tool registry access.
type ToolRegistryService interface {
	Get(ctx context.Context, tenantID, toolID string) (*registry.ToolDef, error)
	GetCategory(ctx context.Context, tenantID, category string) ([]*registry.ToolDef, error)
	ExpandToolIDs(ctx context.Context, tenantID string, toolIDs []string) []string
}

// ResponseInterceptor is the interface for response interception hooks.
// Alias to domains/hooks/response.ResponseInterceptor for type unification.
type ResponseInterceptor = response.ResponseInterceptor

// ResponseInterceptRequest is an alias to response.InterceptRequest.
type ResponseInterceptRequest = response.InterceptRequest

// ResponseInterceptResult is an alias to response.InterceptResult.
type ResponseInterceptResult = response.InterceptResult

// ResponseStreamMeta is an alias to response.StreamMeta.
type ResponseStreamMeta = response.StreamMeta

// dispatchFollowUpFunc (2026-07-11) is the seam used by injectFollowUpRequest
type dispatchFollowUpFunc func(h *ChatHandler, ctx context.Context, sessionID string, body []byte, action string, authHeader string, attempt int) (status int, bodySnippet string)

// ResponseChunkResult is an alias to response.ChunkResult.
type ResponseChunkResult = response.ChunkResult

// ResponseEndResult is an alias to response.EndResult.
type ResponseEndResult = response.EndResult

func NewChatHandler(cm *credential.Manager, l *credential.Limiter, matrix *transformation.Matrix, pools *pool.PoolManager, resolver *resolve.Resolver, auditor audit.Sink) *ChatHandler {
	if auditor == nil {
		auditor = &audit.LogSink{}
	}
	return &ChatHandler{circuit: cm, limiter: l, matrix: matrix, pools: pools, resolver: resolver, auditor: auditor, client: upstreampkg.New(), normalizer: NewNormalizer()}
}

func (h *ChatHandler) SetExecutor(exec *executors.Executor, prov providerResolver, sticky *executors.StickyCache) {
	h.executor = exec
	h.provider = prov
	h.sticky = sticky
}

func (h *ChatHandler) SetSessionRouting(lastSystemSession *session.LastSystemSessionIndex, sessionPref *session.SessionPreference) {
	h.lastSystemSession = lastSystemSession
	h.sessionPref = sessionPref
}

// SetRotationHook (2026-07-06) wires the session rotation hook.
func (h *ChatHandler) SetRotationHook(hook *session.RotationHook) {
	h.rotationHook = hook
}

// SetTraceRecorder (2026-07-17) 注入请求链路追踪器。
// 传 nil 等价于禁用(内部 trace.Recorder 接口自身为 nil-safe)。
func (h *ChatHandler) SetTraceRecorder(rec gwtrace.Recorder) {
	h.traceRecorder = rec
}

// SetLiveActions (2026-08-15, V3.3-OBS OBS-B1) 注入请求生命周期动作事件
// 发射器。传 nil 等价于禁用（Emit 对 nil receiver 是 no-op）。
func (h *ChatHandler) SetLiveActions(e *liveactions.Emitter) {
	h.liveActions = e
}

// clientProtocolFromPath infers the inbound wire protocol from the URL path
// for the arrive action event (the authoritative ir.DetectProtocol runs later,
// after the body is parsed; arrive fires before that).
func clientProtocolFromPath(path string) string {
	switch {
	case strings.Contains(path, "/v1/messages"):
		return "anthropic-messages"
	case strings.Contains(path, "/v1/responses"):
		return "openai-responses"
	default:
		return "openai-completions"
	}
}

// emitAction 是 liveactions 注入的薄包装（同 emitTrace 的做法）。
// Detail 只允许放 id/模型名/错误 kind 等元数据 —— 正文、API key、系统
// prompt 严禁进入（23 号 §2 安全红线）。
func (h *ChatHandler) emitAction(ctx context.Context, requestID string, action liveactions.Action, detail map[string]string) {
	if h == nil || h.liveActions == nil || requestID == "" {
		return
	}
	h.liveActions.Emit(ctx, liveactions.ActionEvent{
		RequestID: requestID,
		Action:    action,
		Detail:    detail,
	})
}

// emitTrace 是 trace 注入的薄包装,避免在 6 处 hot path 中重复写
// if h.traceRecorder != nil { ... } 代码。
func (h *ChatHandler) emitTrace(ctx context.Context, requestID string, ev gwtrace.EventBuilder) {
	if h == nil || h.traceRecorder == nil || requestID == "" {
		return
	}
	ev.Append(ctx, h.traceRecorder, requestID)
}

// SetSessionReuseWindow configures the look-back window used by
// FindRecentGatewaySession. 0 disables recent-session reuse (every
// request creates a new gw_<uuid>). Negative values are clamped to 0.
func (h *ChatHandler) SetSessionReuseWindow(d time.Duration) {
	if d < 0 {
		d = 0
	}
	h.sessionReuseWindow = d
}

// sessionReuseWindowOrDefault returns the configured window, falling
// back to session.LastSystemSessionTTL (5m) when the handler has not
// been wired via SetSessionReuseWindow.
func (h *ChatHandler) sessionReuseWindowOrDefault() time.Duration {
	if h == nil {
		return session.LastSystemSessionTTL
	}
	if h.sessionReuseWindow <= 0 {
		return session.LastSystemSessionTTL
	}
	return h.sessionReuseWindow
}

// SetModelPolicy wires the tenant-scoped model denylist checker
// (Round 48).  nil disables enforcement.  Production wires a
// non-nil Checker from cmd/gateway/main.go.
func (h *ChatHandler) SetModelPolicy(mp *modelpolicy.Checker) {
	h.modelPolicy = mp
}

// SetIdempotentCache (Track C C5, 2026-06-18) wires the
// duplicate-request detector. nil disables dedup; every
// request is treated as new. Production wiring in
// cmd/gateway/main.go calls this with a non-nil cache so
// that double-clicks and network retries get an instant
// 202 + X-Gw-Pending response.
func (h *ChatHandler) SetIdempotentCache(c *IdempotentCache) {
	h.idempotentCache = c
}

// SetSessionCompressor wires the v3 session-level intelligent compression.
// When set, each request performs message-level delta-append + optional
// proactive sliding-window LLM summary before forwarding to the upstream.
func (h *ChatHandler) SetSessionCompressor(sc *compression.SessionCompressor) {
	h.sessionCompressor = sc
}

// SetHandoffHook wires the request-side handoff hook. The hook is evaluated
// after candidate resolution (when the model context window is known) and
// before session compression sends the request upstream.
func (h *ChatHandler) SetHandoffHook(hook *handoff.TriggerHook) {
	h.handoffHook = hook
}

// SetSanitizeInputMiddleware wires the SmartSaniGuard input-side sanitizer.
// The middleware runs at the chatHandler entry, replacing sensitive info
// in the request body with placeholders and persisting the reverse map to
// Redis. Pass nil to disable.
func (h *ChatHandler) SetSanitizeInputMiddleware(mw func(http.Handler) http.Handler) {
	h.sanitizeInputMiddleware = mw
}

// SetPromptCacheStabilize toggles request-body prefix stabilization
// (cache/prefix.Stabilize). When true (default), each request's messages are
// reordered by stability class to maximise upstream KV-prefix-cache hits.
// Read once at startup from LLM_GATEWAY_PROMPT_CACHE_STABILIZE (default "1").
func (h *ChatHandler) SetPromptCacheStabilize(on bool) {
	h.promptCacheStabilize = on
}

// SetCacheInjector wires the prompt-cache-control injector. When set AND
// promptCacheInject is true, the request body gets cache_control markers
// placed on the stabilized prefix boundary for candidates that declare
// SupportsPromptCache. Opt-in only (LLM_GATEWAY_PROMPT_CACHE_INJECT=1).
func (h *ChatHandler) SetCacheInjector(ci *session.CacheInjector) {
	h.cacheInjector = ci
}

// SetPromptCacheInject enables/disables cache_control marker injection.
// Read once at startup from LLM_GATEWAY_PROMPT_CACHE_INJECT (default "0").
func (h *ChatHandler) SetPromptCacheInject(on bool) {
	h.promptCacheInject = on
}

// SetMetaToolInterceptor wires the Phase 2 meta-tool interceptor.
// When set, requests containing meta-tool calls (list_categories, load_tools)
// are handled locally without forwarding to upstream LLM providers.
func (h *ChatHandler) SetMetaToolInterceptor(i *MetaToolInterceptor) {
	h.metaToolInterceptor = i
}

// SetToolRegistry wires the Phase 3 tool registry.
// When set, requests containing tool_ids expand to full tool definitions.
func (h *ChatHandler) SetToolRegistry(tr ToolRegistryService) {
	h.toolRegistry = tr
}

// expandToolIDs (Phase 3, 2026-06-21) expands tool_ids to full tool definitions.
// Supports wildcards (filesystem.*, *) and exact matches (network.http_get).
// Returns expanded tools as JSON array, or nil if no tool_ids provided.
func (h *ChatHandler) expandToolIDs(ctx context.Context, tenantID string, toolIDs []string) ([]byte, error) {
	if len(toolIDs) == 0 || h.toolRegistry == nil {
		return nil, nil
	}

	// Use ExpandToolIDs for unified wildcard handling (supports *, category.*, exact)
	expandedIDs := h.toolRegistry.ExpandToolIDs(ctx, tenantID, toolIDs)
	if len(expandedIDs) == 0 {
		return nil, nil
	}

	// Fetch full tool definitions for expanded IDs
	var tools []json.RawMessage
	for _, toolID := range expandedIDs {
		tool, err := h.toolRegistry.Get(ctx, tenantID, toolID)
		if err != nil {
			slog.Warn("failed to get expanded tool",
				"tenant", tenantID,
				"tool_id", toolID,
				"error", err)
			continue
		}

		if tool != nil {
			tools = append(tools, json.RawMessage(tool.Definition))
		}
	}

	if len(tools) == 0 {
		return nil, nil
	}

	return json.Marshal(tools)
}

func (h *ChatHandler) SetAuth(kv *authentication.KeyVerifier, rl ratelimit.RPMLimiter) {
	h.keyVerifier = kv
	h.rateLimiter = rl
}

func (h *ChatHandler) AuthKeyVerifier() *authentication.KeyVerifier {
	if h == nil {
		return nil
	}
	verifier, _ := h.keyVerifier.(*authentication.KeyVerifier)
	return verifier
}

func (h *ChatHandler) setRequestKeyVerifierForTest(verifier requestKeyVerifier) {
	h.keyVerifier = verifier
}

func (h *ChatHandler) SetTelemetry(tc *telemetry.Client) {
	h.telemetryClient = tc
}

// SetProfileEmitter wires the client-profile event emitter (2026-07-15).
//
// When non-nil, request-completed / session-closed / failure events flow to
// clientprofile.EventEmitter → analysis 总线 → ProfileWorker → client_profiles。
// nil disables profile aggregation (no behavioural impact on request flow).
func (h *ChatHandler) SetProfileEmitter(emitter interface {
	EmitRequestCompleted(ctx context.Context, sc *session.SessionContext, identityHash string, success bool, tokensUsed int, latencyMs int64) error
	EmitSessionStarted(ctx context.Context, sc *session.SessionContext, identityHash string) error
}) {
	h.profileEmitter = emitter
}

func (h *ChatHandler) SetMaas(svc *maas.Service) {
	h.maasSvc = svc
}

// SetFormatDetection (2026-07-26) configures the format detection and auto-fix system.
// When detector and fixer are non-nil, the handler automatically detects client request
// formats and applies fixes before validation. cache is optional (nil disables caching).
func (h *ChatHandler) SetFormatDetection(detector *FormatDetector, fixer *FormatFixer, cache FormatCache) {
	h.formatDetector = detector
	h.formatFixer = fixer
	h.formatCache = cache
}

// SetRequestLogger wires the Request WAL logger.
// nil disables Request WAL (default).
func (h *ChatHandler) SetRequestLogger(rl *telemetry.RequestLogger) {
	h.requestLogger = rl
}

// SetIntegrityDetector (2026-07-28) wires the model-integrity detector.
// nil disables the per-request detection; the executor side keeps
// its own field because the non-stream path records from inside
// executeOpenAI (before result.ResponseBody is fully consumed by
// the response interceptor).
func (h *ChatHandler) SetIntegrityDetector(d executors.IntegrityDetector) {
	h.integrityDetector = d
}

// integrityObserverFactory is the optional interface an
// executors.IntegrityDetector implements to supply a per-request
// incremental stream observer. Declared here (rather than imported) so
// domains/streaming keeps its one-directional dependency on
// domains/streaming/integrity — see the note on
// streamRespModelForIntegrity.
type integrityObserverFactory interface {
	NewStreamTextObserver() audit.StreamTextObserver
}

// newStreamCapture builds a StreamCapture with the incremental integrity
// observer attached when the wired detector supplies one. Every stream
// entry point (chat / responses / messages) goes through here so the
// four transformer families are covered uniformly.
func (h *ChatHandler) newStreamCapture() *audit.StreamCapture {
	capture := audit.NewStreamCapture()
	if f, ok := h.integrityDetector.(integrityObserverFactory); ok && f != nil {
		if obs := f.NewStreamTextObserver(); obs != nil {
			capture.SetTextObserver(obs)
		}
	}
	return capture
}

// SetAutoTitleGenerator (2026-06-22) wires the auto title generator from admin package.
// 2026-08-06: signature extended with parentRequestID for request_logs_hot.parent_request_id linkage.
// 2026-08-06: signature extended with taskID so the stored title row matches
// request_logs on (task_id, scoped_session_id).
func (h *ChatHandler) SetAutoTitleGenerator(atg interface {
	MaybeGenerateTitle(sessionID, tenantID, taskID, requestBody, requestPreview, parentRequestID, requestID string)
}) {
	h.autoTitleGenerator = atg
}

// SetAutoSummaryGenerator (2026-08-06) wires the auto summary generator
// from the admin package. Symmetric contract with SetAutoTitleGenerator:
// the streaming handler fires MaybeGenerateSummary on every successful
// user request, gated by the rolling N-turn threshold inside the generator.
func (h *ChatHandler) SetAutoSummaryGenerator(asg interface {
	MaybeGenerateSummary(sessionID, tenantID, requestBody, requestPreview, parentRequestID string)
}) {
	h.autoSummaryGenerator = asg
}

// SetArmor wires armor judge and logger for prompt security checks.
// When both are non-nil, handler scores prompts before LLM execution
// and writes audit records to armor_judgments table. v1 observe-only mode.
func (h *ChatHandler) SetArmor(judge armor.Judge, logger *armor.Logger) {
	h.armorJudge = judge
	h.armorLogger = logger
}

// SetRequestLogHook installs an in-memory sink that records every
// request_logs row the safety-net (or the success path) emits.  It is
// used by unit tests in this package to assert that every error exit
// path still produces a row.  Passing nil clears the hook (the
// default is nil; production callers should never set a hook).
//
// The hook is best-effort: if it is set and the entry is nil, the
// hook does nothing.  Concurrent appends are guarded by a mutex so
// tests that fire many requests in parallel can inspect the
// collected slice without racing.
func (h *ChatHandler) SetRequestLogHook(hook func(*telemetry.RequestLogEntry)) {
	h.requestLogHook = hook
}

// SetSessionAuditHook installs the chat-time session-audit hook.
//
// When non-nil, the hook is consulted BEFORE the request reaches
// GetCandidates (routing). Block → 403; NeedApproval → 202 + approval_id
// + pending_approval response; Pass/Warn → continue.
//
// nil disables the hook (no chat-time audit). Production callers (cmd/gateway/main.go)
// should set this after constructing both SessionAuditHook and ApprovalManager.
//
// 2026-06-28: this is the v1 ChatHandler integration point that handoff
// round-1 had claimed to wire but actually missed (修复 G only landed in
// cmd/gateway-v2/main.go, the demo binary).
func (h *ChatHandler) SetSessionAuditHook(hook *sessionaudithook.SessionAuditHook) {
	h.sessionAuditHook = hook
}

// SetHandoffFallbackAPIKey (2026-07-11, handoff self-call fix)
func (h *ChatHandler) SetHandoffFallbackAPIKey(apiKey string) {
	h.handoffFallbackAuth = strings.TrimSpace(apiKey)
}

// SetResponseInterceptor wires the response interceptor for automatic
// handoff and goal mode. When set, the handler calls the interceptor
// after receiving LLM responses but before forwarding to clients.
// nil disables response interception (default).
//
// 2026-06-29: auto-control feature integration point.
func (h *ChatHandler) SetResponseInterceptor(interceptor ResponseInterceptor) {
	h.responseInterceptor = interceptor
}

// ResponseInterceptorForWire returns the current response interceptor
// as a *response.InterceptorChain when it is one. Returns nil otherwise.
//
// 2026-08-07: SmartSaniGuard needs to append a restore interceptor onto
// the existing chain (after output_compliance). Use this getter to inspect
// what is already wired without exposing the chain's internal slice.
func (h *ChatHandler) ResponseInterceptorForWire() *response.InterceptorChain {
	if h == nil {
		return nil
	}
	if chain, ok := h.responseInterceptor.(*response.InterceptorChain); ok {
		return chain
	}
	return nil
}

// SetGoalRetryPolicyResolver (2026-07-23) wires the tenant-scoped retry
// policy resolver. When set, the handler resolves a fresh policy per
// request based on tenant settings and cost-mode presets, replacing the
// fixed boot-time configuration. nil falls back to minimal defaults.
func (h *ChatHandler) SetGoalRetryPolicyResolver(resolver GoalRetryPolicyResolver) {
	h.goalRetryPolicyResolver = resolver
}

// SetGoalRetryRecorder (2026-07-23) wires the retry count recorder.
// When set, the handler persists actual retry attempts to goal_sessions
// after each request. Persistence errors are logged but do not block
// requests (fail-open). nil disables persistence.
func (h *ChatHandler) SetGoalRetryRecorder(recorder GoalRetryRecorder) {
	h.goalRetryRecorder = recorder
}

// SetGoalOutcomeObserver wires fail-open Goal lifecycle observations.
func (h *ChatHandler) SetGoalOutcomeObserver(observer goal.OutcomeObserver) {
	h.goalOutcomeObserver = observer
}

func (h *ChatHandler) SetSessionGetter(sg interface {
	Get(ctx context.Context, id string) (*session.Session, error)
	Touch(ctx context.Context, id string) error
	CreateV2(ctx context.Context, apiKeyID int, tenantID, deviceSeed, taskID string) (*session.Session, error)
	BindAPIKey(ctx context.Context, sessionID string, apiKeyID int, tenantID string) error
}) {
	h.sessionGetter = sg
	h.handoffSessionGetter = sg
}

// SetFormatAnomalyRecorder configures the anomaly recorder for tracking
// response format issues (used to detect provider API changes).
func (h *ChatHandler) SetFormatAnomalyRecorder(recorder *FormatAnomalyRecorder) {
	h.anomalyRecorder = recorder
}

// SetAttachmentExtractor (2026-07-01) wires the attachment extractor.
// When set, the handler extracts base64/data-URI attachments from incoming
// requests and saves them to the filesystem before forwarding to upstream.
// The extracted metadata is written to request_logs.attachments JSONB column.
// nil disables attachment extraction (attachments remain inline).
func (h *ChatHandler) SetAttachmentExtractor(extractor *attachments.Extractor) {
	h.attachmentExtractor = extractor
}

// SetOmniFree (2026-08-07) wires the optional OmniFree auto-combo stack:
// resolver + factory + quota tracker. Any argument may be nil to skip that
// component. With resolver nil, auto/* requests are treated as model_not_found.
//
// round 4 审计补充修复 (L3): 当 tracker != nil 时, 启动 bounded worker pool
// (默认 16 workers + 256 缓冲队列) 处理 recordOmniFreeQuota 的异步任务,
// 防止高并发时无限制 fork goroutine. SetOmniFree 可安全重复调用, 会先
// drain 并停止旧 worker, 再安装新 tracker.
func (h *ChatHandler) SetOmniFree(
	resolver *autocombo.Resolver,
	factory *autocombo.VirtualFactory,
	tracker QuotaRecorder,
) {
	h.ShutdownOmniFree()
	h.omniFreeMu.Lock()
	h.autoComboResolver = resolver
	h.autoComboFactory = factory
	h.omniFreeMu.Unlock()
	h.quotaQueueMu.Lock()
	h.quotaTracker = tracker
	if tracker == nil {
		h.quotaQueueMu.Unlock()
		return
	}

	const (
		quotaWorkers    = 16
		quotaQueueDepth = 256
	)
	queue := make(chan quotaRecordTask, quotaQueueDepth)
	h.quotaRecordQueue = queue
	h.quotaAccepting = true
	h.quotaWorkersDone.Add(quotaWorkers)
	h.quotaQueueMu.Unlock()
	for i := 0; i < quotaWorkers; i++ {
		go h.quotaRecordWorker(queue)
	}
}

// ShutdownOmniFree stops accepting new quota tasks, drains queued tasks, and
// waits for all quota workers to finish. It is idempotent and should run before
// the database pool used by QuotaRecorder is closed.
func (h *ChatHandler) ShutdownOmniFree() {
	h.quotaQueueMu.Lock()
	if !h.quotaAccepting {
		h.quotaQueueMu.Unlock()
		return
	}
	h.quotaAccepting = false
	queue := h.quotaRecordQueue
	if queue != nil {
		close(queue)
	}
	h.quotaQueueMu.Unlock()
	h.quotaWorkersDone.Wait()
}

func (h *ChatHandler) enqueueQuotaTask(task quotaRecordTask) bool {
	h.quotaQueueMu.Lock()
	defer h.quotaQueueMu.Unlock()
	if !h.quotaAccepting || h.quotaRecordQueue == nil {
		return false
	}
	select {
	case h.quotaRecordQueue <- task:
		return true
	default:
		return false
	}
}

// quotaRecordTask 是 quotaRecordQueue 中的任务单元: 包含一个 Record 或
// CorrectFromHeaders 调用的完整参数.
type quotaRecordTask struct {
	ctx        context.Context
	cancel     context.CancelFunc
	recorder   QuotaRecorder
	isRecord   bool // true=Record, false=CorrectFromHeaders
	recordReq  freeresource.RecordRequest
	correctReq freeresource.CorrectionRequest
}

// quotaRecordWorker 从 quotaRecordQueue 消费任务. 收到关闭信号后仍会排空
// 已经入队的任务, 确保 graceful shutdown 不丢弃已接受的 quota 记录.
func (h *ChatHandler) quotaRecordWorker(queue <-chan quotaRecordTask) {
	defer h.quotaWorkersDone.Done()
	for task := range queue {
		h.processQuotaRecordTask(task)
	}
}

func (h *ChatHandler) processQuotaRecordTask(task quotaRecordTask) {
	if task.cancel != nil {
		defer task.cancel()
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("omnifree: quota worker task panicked", "panic", recovered)
			metrics.OmniFreeQuotaRecordErrorsTotal.Inc()
		}
	}()
	if task.recorder == nil {
		return
	}
	if task.isRecord {
		if err := task.recorder.Record(task.ctx, task.recordReq); err != nil {
			metrics.OmniFreeQuotaRecordErrorsTotal.Inc()
			slog.Debug("omnifree: quota record failed (worker)",
				"error", err, "credential_id", task.recordReq.CredentialID)
		}
		return
	}
	if err := task.recorder.CorrectFromHeaders(task.ctx, task.correctReq); err != nil {
		metrics.OmniFreeQuotaRecordErrorsTotal.Inc()
		slog.Debug("omnifree: quota 429 correct failed (worker)",
			"error", err, "credential_id", task.correctReq.CredentialID)
	}
}

// QuotaRecorder 是 recordOmniFreeQuota 内部的隐式接口. *freeresource.QuotaTracker
// 隐式满足该接口 (Record + CorrectFromHeaders 两个方法). 让 handler 测试
// 可以注入 fake, 验证 RecordRequest / CorrectionRequest 字段是否被正确构造.
//
// round 4 L1+L2 修复: 旧实现 quotaTracker 是具体类型 *freeresource.QuotaTracker,
// 单元测试只能验证 nil 短路; 真实 Record 调用未覆盖. 接口化后 fake 可注入.
type QuotaRecorder interface {
	Record(ctx context.Context, req freeresource.RecordRequest) error
	CorrectFromHeaders(ctx context.Context, req freeresource.CorrectionRequest) error
}

// ServeHTTP handles /v1/chat/completions and /v1/completions.
//
// 2026-08-07 (SmartSaniGuard): the request-side input middleware (when set)
// wraps the real handler so sensitive info in r.Body is replaced with
// placeholders + persisted to Redis before any request processing.
func (h *ChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.sanitizeInputMiddleware != nil {
		h.sanitizeInputMiddleware(http.HandlerFunc(h.serveHTTPInner)).ServeHTTP(w, r)
		return
	}
	h.serveHTTPInner(w, r)
}

func (h *ChatHandler) serveHTTPInner(w http.ResponseWriter, r *http.Request) {
	// ── requestAttempt safety-net: every request that reaches this
	//    handler must produce exactly one request_logs row, regardless
	//    of which early-return path it takes.  attemptErrCode is
	//    populated by the inner functions when they exit without
	//    writing a row themselves; the deferred block at the end
	//    of this function writes the row using those fields.  The
	//    *attemptLogged bool is shared with the inner functions via
	//    pointer so success / explicit-failure paths can mark the
	//    row as already-written to avoid double-logging.
	var logCtx *RequestLogContext
	// 2026-06-26: server-side request id is ALWAYS freshly generated.
	// The middleware (RequestIDMiddleware) sets X-Request-Id to a new
	// UUID and X-Gw-Client-Request-Id to the client-supplied value.
	// We still defensively fall back to generateRequestID() here in
	// case the middleware chain was bypassed (e.g. direct unit-test
	// invocation), and we capture the client value for
	// request_logs.client_request_id so retries can be correlated.
	requestID := r.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = generateRequestID()
		w.Header().Set("X-Request-Id", requestID)
	}
	clientRequestID := r.Header.Get("X-Gw-Client-Request-Id")
	if clientRequestID == "" {
		// Backstop: read the original (pre-middleware) client value.
		clientRequestID = r.Header.Get("X-Client-Request-Id")
	}
	if clientRequestID != "" && clientRequestID != requestID {
		w.Header().Set("X-Client-Request-Id", clientRequestID)
	}
	startTime := time.Now()
	logCtx = h.NewRequestLogContext(r, requestID, startTime)
	logCtx.ClientRequestID = clientRequestID
	// 2026-06-30: 记录客户端请求端点 (migration 320)
	logCtx.SetClientEndpoint(r.URL.Path)
	if wt := strings.TrimSpace(r.Header.Get(autoWorkTypeHeader)); wt != "" {
		logCtx.SetWorkType(wt)
	}
	// 2026-08-05: 识别网关内部自动请求（auto title/summary 发起者设置的
	// X-Gw-Is-Auto）。此前入口侧从未读取此 header，导致 is_auto_request
	// 在日志里全为 NULL，也无法在标题生成触发点排除自身（链式自触发风险）。
	if strings.EqualFold(r.Header.Get(autoIsAutoHeader), "true") {
		logCtx.IsAutoRequest = true
	}
	// 2026-08-06: 读取父子关联 header。auto_title_generator 在 loopback
	// 请求中转发父请求的 request_id 与调用方 actor；handler 入口侧把它们
	// 写入 logCtx，再经 applyParentCorrelationFields 持久化到
	// request_logs_hot.parent_request_id / origin_actor。这是 "08aa2a8a →
	// 3a03f7db" 父子链路 SQL JOIN 的关键。
	if v := strings.TrimSpace(r.Header.Get(autoParentRequestIDHeader)); v != "" {
		logCtx.ParentRequestID = v
	}
	if v := strings.TrimSpace(r.Header.Get(autoSourceActorHeader)); v != "" {
		logCtx.OriginActor = v
	}

	// ── 2026-07-17: 请求链路追踪 — 注入 receive_request 事件 ───────────────
	// 在 requestID 生成后第一时刻记录,便于运维在 trace 视图里看到
	// 客户端真实 IP / path / X-Gw-Client-Request-Id 等关键信息。
	clientIP := r.Header.Get("X-Forwarded-For")
	if clientIP == "" {
		clientIP = r.RemoteAddr
	}
	h.emitTrace(r.Context(), requestID,
		gwtrace.ReceiveRequest(r.Method, r.URL.Path, clientIP).
			WithDetails(
				"client_request_id", clientRequestID,
				"user_agent", r.Header.Get("User-Agent"),
			))
	// ── 2026-08-15 (V3.3-OBS OBS-B1): arrive 动作事件（S1，24 号 §2）──────
	// 客户端请求到达网关。client_protocol 按路径推断；model(原始) 在 body
	// 解析之后才可知，route_resolved 事件会带上解析结果，此处不重复。
	h.emitAction(r.Context(), requestID, liveactions.ActionArrive, map[string]string{
		"client_protocol": clientProtocolFromPath(r.URL.Path),
		"method":          r.Method,
	})
	// ── Ensure every request has a gw_session_id (2026-06-26) ────────────
	// Even pre-keyInfo failures (missing_key, invalid_key, auth_unavailable)
	// emit a request_log row via the safety net. Without a session_id here
	// those rows would have empty gw_session_id, breaking /api/logs filtering
	// and session-summary grouping.
	//
	// 2026-07-27: Generate a provisional session id for early-failure
	// logging only. Do NOT write it to X-Gw-Session-Id here — doing so
	// would shadow any client-supplied or body-supplied session id once
	// the body is read below. The safety net records the provisional id
	// via applyProvisionalGatewaySessionHeader.
	provisionalSessionID := h.ensureSessionID(r.Context(), r, nil)
	logCtx.ProvisionalSessionID = provisionalSessionID

	// 2026-07-27 (L-1): write the request WAL row NOW, as early as possible,
	// so EVERY received request is traceable in request_wal_hot — including
	// pre-routing failures (missing_key, invalid_key, body_too_large,
	// json_parse_error, rate_limit_exceeded, model_forbidden) that previously
	// never reached the CreateInitial call at ~line 2371 (it runs AFTER auth,
	// body parse, session assign, and model resolution). Those failures got a
	// request_logs_hot row via the safety net but NO WAL row at all.
	//
	// This early write uses the minimal fields available at this point
	// (requestID + provisional session id); tenant_id and client_model are
	// enriched later (the request_logs_hot row carries the full detail via
	// the telemetry path). CreateInitial now conflicts on (request_id) — via
	// the unique index from migration 461 — and does an additive
	// COALESCE first-write-wins UPDATE, so the later, fuller CreateInitial at
	// ~2371 enriches this early row in place rather than creating a second
	// one (the old (request_id, created_at) target never collided across two
	// separate Execs because NOW() differed, orphaning the early row). For
	// pre-routing failures this is the only WAL row.
	if h.requestLogger != nil {
		earlyReq := &telemetry.InitialRequest{
			RequestID:   requestID,
			TenantID:    "default", // enriched in request_logs_hot; WAL just needs a row
			SessionID:   provisionalSessionID,
			Provisional: true,
		}
		if err := h.requestLogger.CreateInitial(r.Context(), earlyReq); err != nil {
			// Non-fatal: the safety net + request_logs_hot still record the
			// request. WAL is best-effort for fast operator drill-down.
			slog.Warn("request_logger: early CreateInitial failed",
				"request_id", requestID, "error", err)
		}
	}

	defer func() {
		slog.Info("safety_net_defer_fired",
			"request_id", requestID,
			"attempt_err_code", logCtx.ErrCode,
			"attempt_logged", logCtx.IsLogged())
		if rec := recover(); rec != nil {
			// 2026-07-20: Capture full stack trace for nil pointer panic debugging
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			stackTrace := string(buf[:n])
			slog.Error("chat handler panic",
				"panic", rec,
				"request_id", requestID,
				"stack_trace", stackTrace)
			logCtx.SetError("internal_panic", "internal server error")
			if len(logCtx.Body) == 0 {
				logCtx.EnsureCaptured()
			}
			if logCtx.ClientModel == "" {
				if len(logCtx.Body) > 0 {
					logCtx.SetClientModel(extractModelFromBody(logCtx.Body))
				}
				if logCtx.ClientModel == "" {
					logCtx.SetClientModel("<unknown>")
				}
			}
			logCtx.EmitFailure(logCtx.ErrCode, logCtx.ErrMsg, logCtx.ProviderID, logCtx.CredentialID)
			writeErrorJSON(w, http.StatusInternalServerError, requestID,
				"internal server error", "server_error", "internal_panic")
		} else if logCtx.ErrCode != "" && !logCtx.IsLogged() {
			slog.Info("safety_net: recording failed request",
				"request_id", requestID,
				"error_kind", logCtx.ErrCode,
				"client_model", logCtx.ClientModel)
			logCtx.EmitFailure(logCtx.ErrCode, logCtx.ErrMsg, logCtx.ProviderID, logCtx.CredentialID)
		}
		// ── 2026-06-22: Request WAL client-disconnect safety net ─────────
		// If the client disconnected before we logged completion, the WAL
		// record would otherwise stay at stage=0/pending forever. Mark it
		// as stage=13 (response_fail) so audit completeness stays at 100%.
		// Only runs if the request context is canceled (client disconnect)
		// AND we never successfully completed via the success path.
		// 2026-08-16 fix: the guard used to be only "context canceled" — a
		// request that had ALREADY completed successfully (stream flushed,
		// emitTelemetry wrote the row, IsLogged()=true) still tripped this
		// block when the client tore down its connection at the very end,
		// producing a spurious "client_cancel" probe in the live stream.
		// Add !IsLogged() so an already-recorded request never emits one.
		if h.requestLogger != nil && shouldEmitDisconnectProbe(r.Context(), logCtx) {
			// 2026-06-30: 标记客户端超时/断开连接 (migration 320)
			if errors.Is(r.Context().Err(), context.DeadlineExceeded) ||
				errors.Is(r.Context().Err(), context.Canceled) {
				logCtx.SetClientTimeout(true)
			}
			// ── 2026-07-09: 问题4 —— 客户端取消/超时探测记录 ──────────────
			// 在前端取消(context.Canceled)或超时(context.DeadlineExceeded)
			// 时，额外生成一条 probe 记录写入 request_logs_hot 并推入泳道，
			// 让运维在"实时请求流"里直接看到取消/超时事件及其凭据归属。
			// 用专用 probe- 前缀 + 时间戳的 request_id，避免与初始 in_progress
			// 行的 ON CONFLICT 冲突。携带 credential_id 以便泳道按供应商反查。
			h.emitClientDisconnectProbe(requestID, r, logCtx)
			// Use Background context since request context is already canceled
			update := &telemetry.LogUpdate{
				RequestID: requestID,
				Stage:     telemetry.StageResponseFail,
				Status:    telemetry.StatusFailure,
				Error:     "client_disconnect: " + r.Context().Err().Error(),
			}
			// Use 2-second timeout to avoid blocking the request lifecycle
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if err := h.requestLogger.UpdateSync(ctx, update); err != nil {
				slog.Warn("request_logger: client-disconnect UpdateSync failed", "request_id", requestID, "error", err)
			}
			cancel()
		}
		// ── 2026-07-17: 请求链路追踪 - 强制 Finalize + FlushToPG ─────────────
		// 把 trace 状态收尾,异步刷到 PG。即使 client_disconnect 已触发,
		// 这里仍写 finalize 让前端能看到这是次"在 client 端被取消"的请求。
		// 同时,失败事件携带上下文快照,便于事后定位客户端断连时的凭据/限流状态。
		if h.traceRecorder != nil {
			var (
				fs    gwtrace.FinalStatus
				failS gwtrace.Stage
			)
			if logCtx.ErrCode == "" {
				fs, failS = gwtrace.FinalSuccess, ""
			} else {
				fs = gwtrace.FinalFailed
				failS = gwtrace.ClassifyFailureToStage(logCtx.ErrCode)
			}
			// Finalize 必须使用独立 context；客户端断连后 r.Context 已取消。
			finalizeCtx, finalizeCancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = h.traceRecorder.Finalize(finalizeCtx, requestID, fs, failS)
			finalizeCancel()

			// FlushToPG 可能早于 telemetry worker 写入 request_logs；有限退避重试
			// 覆盖该竞态，且只有 UPDATE 命中行时 recorder 才会删除 Redis key。
			if h.telemetryClient != nil {
				if pool := h.telemetryClient.DBPool(); pool != nil {
					go func(rid string) {
						for attempt, delay := range []time.Duration{0, 100 * time.Millisecond, 500 * time.Millisecond, 2 * time.Second} {
							if delay > 0 {
								time.Sleep(delay)
							}
							flushCtx, flushCancel := context.WithTimeout(context.Background(), 2*time.Second)
							err := h.traceRecorder.FlushToPG(flushCtx, pool, rid)
							flushCancel()
							if err == nil {
								return
							}
							slog.Warn("request_trace: flush attempt failed",
								"request_id", rid, "attempt", attempt+1, "error", err)
						}
					}(requestID)
				}
			}
		}
	}()

	// GET probe — return 200 for client compatibility checks
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"message": "Chat completions endpoint is available. Use POST to send requests.",
		})
		return
	}
	if r.Method != http.MethodPost {
		logCtx.SetError("method_not_allowed", "method not allowed")
		// 2026-06-20 audit fix: capture the body + model so the
		// request_logs row records what the client actually sent
		// (not just "method not allowed"). Without this every
		// 405 row shows empty body + model="<unknown>" and the
		// operator can't tell which client / tool sent the
		// wrong method or which model it was trying to reach.
		// EmitFailure here (not relying on the safety net)
		// because the safety-net path runs only when the inner
		// pipeline never returned; an early 405 should produce
		// a single, fully-populated row.
		logCtx.EnsureCaptured()
		if logCtx.ClientModel == "" {
			if len(logCtx.Body) > 0 {
				logCtx.SetClientModel(extractModelFromBody(logCtx.Body))
			}
			if logCtx.ClientModel == "" {
				logCtx.SetClientModel("<unknown>")
			}
		}
		logCtx.EmitFailure(logCtx.ErrCode, logCtx.ErrMsg, nil, nil)
		logCtx.MarkLogged()
		http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request","code":"method_not_allowed"}}`, http.StatusMethodNotAllowed)
		return
	}

	if h.executor != nil && h.provider != nil && h.provider.Enabled() {
		h.serveWithExecutor(w, r, logCtx)
		return
	}
	logCtx.SetError("executor_unavailable", "routing executor not available; database connection required")
	logCtx.EnsureCaptured()
	// 2026-06-20 audit fix v3: ensure client_model is never
	// blank when body was captured but had no "model" field.
	if logCtx.ClientModel == "" {
		if len(logCtx.Body) > 0 {
			logCtx.SetClientModel(extractModelFromBody(logCtx.Body))
		}
		if logCtx.ClientModel == "" {
			logCtx.SetClientModel("<unknown>")
		}
	}
	logCtx.EmitFailure(logCtx.ErrCode, logCtx.ErrMsg, nil, nil)
	logCtx.MarkLogged()
	h.serveFallback(w, r)
}

// serveWithExecutor is the main chat-completions / completions pipeline.
// It receives pointers to the safety-net attempt state from ServeHTTP
// so that any exit path can populate them and the deferred logger in
// the caller will record exactly one request_logs row.  attemptLogged
// is set to true by any inner function that has already recorded the
// row (e.g. via recordFailedRequest or emitTelemetry on the success
// path) so the deferred safety net does not duplicate it.
func (h *ChatHandler) serveWithExecutor(
	w http.ResponseWriter,
	r *http.Request,
	logCtx *RequestLogContext,
) {
	//nolint:errcheck // best-effort close
	defer r.Body.Close()

	requestID := logCtx.RequestID
	startTime := logCtx.StartTime
	logCtx.EnsureCaptured()

	// ── 2026-07-17: trace.body_parse ──────────────────────────────────────
	// 在 EnsureCaptured 后 (body 已读取) 立即记录, body_size 用于分析上游
	// prompt cache 命中与上下文窗口风险。
	h.emitTrace(r.Context(), requestID,
		gwtrace.BodyParse(len(logCtx.Body), nil).
			WithDetails("client_model", logCtx.ClientModel))

	markLogged := func() { logCtx.MarkLogged() }

	// 2026-06-20 audit fix helper: capture body + model + emit failure
	// for early-exit error paths. Without this every 405/401/400/503
	// row shows empty body + model="<unknown>" and the operator cannot
	// tell which client sent the bad request or which model it was
	// trying to reach (the symptom that triggered the comprehensive audit).
	captureAndEmitFailure := func(errCode, errMsg string, providerID, credentialID *int) {
		logCtx.SetError(errCode, errMsg)
		logCtx.EnsureCaptured()
		// 2026-07-27: Surface the provisional session id on early-failure
		// branches so request_logs.gw_session_id stays non-empty without
		// overwriting a client-supplied or body-supplied session id (which
		// hasn't been parsed yet at this point).
		applyProvisionalGatewaySessionHeader(r, logCtx.ProvisionalSessionID)
		if logCtx.ClientModel == "" {
			if len(logCtx.Body) > 0 {
				logCtx.SetClientModel(extractModelFromBody(logCtx.Body))
			}
			if logCtx.ClientModel == "" {
				logCtx.SetClientModel("<unknown>")
			}
		}
		logCtx.EmitFailure(errCode, errMsg, providerID, credentialID)
		logCtx.MarkLogged()
	}
	// captureAndEmitRateLimited mirrors captureAndEmitFailure but records the
	// exit as request_status="rate_limited" instead of "failure". Used by the
	// gateway's own rate-limit / throttle rejections (RPM, per-key concurrent,
	// throttled key) so they don't pollute provider error counts while still
	// appearing in the dashboard denominator.
	captureAndEmitRateLimited := func(errCode, errMsg string, providerID, credentialID *int) {
		logCtx.SetError(errCode, errMsg)
		logCtx.EnsureCaptured()
		applyProvisionalGatewaySessionHeader(r, logCtx.ProvisionalSessionID)
		if logCtx.ClientModel == "" {
			if len(logCtx.Body) > 0 {
				logCtx.SetClientModel(extractModelFromBody(logCtx.Body))
			}
			if logCtx.ClientModel == "" {
				logCtx.SetClientModel("<unknown>")
			}
		}
		logCtx.EmitRateLimited(errCode, errMsg, providerID, credentialID)
		logCtx.MarkLogged()
	}

	// ── API key authentication ──────────────────────────────────────────
	var keyInfo *authentication.KeyInfo
	if h.keyVerifier != nil && h.keyVerifier.Enabled() {
		rawKey := extractBearerToken(r)
		if rawKey == "" {
			captureAndEmitFailure("missing_key", "missing api key", nil, nil)
			writeErrorJSONCtx(r.Context(), w, http.StatusUnauthorized, requestID, "authentication_error", i18n.MsgMissingKey, nil)
			return
		}
		ki, verifyErr := h.keyVerifier.Verify(r.Context(), rawKey)
		// ── 2026-07-17: trace.Authenticate ──────────────────────────────────
		// 无论成功/失败都记录, 让运维在 trace 视图里看到完整鉴权链路。
		if ki != nil {
			h.emitTrace(r.Context(), requestID,
				gwtrace.Authenticate(int64(ki.ID), ki.TenantID, true))
		} else {
			h.emitTrace(r.Context(), requestID,
				gwtrace.Authenticate(0, "", false).
					WithError(verifyErr))
		}
		if verifyErr != nil {
			if _, ok := verifyErr.(*authentication.InvalidKeyError); ok {
				captureAndEmitFailure("invalid_key", "invalid or expired api key", nil, nil)
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeErrorJSONCtx(r.Context(), w, http.StatusUnauthorized, requestID, "authentication_error", i18n.MsgInvalidKey, nil)
				return
			}
			slog.Error("key verification RPC failed, rejecting request", "error", verifyErr)
			captureAndEmitFailure("auth_unavailable", "authentication service temporarily unavailable", nil, nil)
			writeErrorJSON(w, http.StatusServiceUnavailable, requestID,
				"Authentication service temporarily unavailable", "server_error", "auth_unavailable")
			return
		}
		keyInfo = ki
		logCtx.SetKey(ki)
		streamretry.SetAuthenticatedTenant(r.Context(), ki.TenantID)

		// Round 38 (2026-06-16) — emit multi-tenant OTel span
		// attributes per docs/multi-tenant-otel-design.md §3.1.
		// llm-gateway-go is Pattern A (direct tenant_id from
		// authentication.KeyInfo). Every authenticated request now carries
		// tenant.id so production debugging can filter Jaeger.
		span := trace.SpanFromContext(r.Context())
		observability.SetTenantAttrs(span, keyInfo.TenantID, "api_key",
			fmt.Sprintf("key_%d", keyInfo.ID))
	}

	// ── Status checks (throttled key → hard rate-limit) ────────────────
	if keyInfo != nil && keyInfo.Status == "throttled" {
		captureAndEmitRateLimited("key_throttled", "api key throttled due to anomalous usage", nil, nil)
		writeErrorJSON(w, http.StatusTooManyRequests, requestID,
			"Your API key has been throttled due to anomalous usage. Contact admin.",
			"rate_limit_error", "key_throttled")
		return
	}

	// ── RPM rate limit (unified via checkGatewayRateLimit) ──────────────
	rlOutcome := checkGatewayRateLimit(keyInfo, h.rateLimiter)
	h.emitTrace(r.Context(), requestID,
		gwtrace.RateLimitCheck(!rlOutcome.Blocked, rateLimitOutcomeKind(rlOutcome), rlOutcome.Remaining).
			WithDetails("limit", rlOutcome.Limit, "reset_sec", rlOutcome.ResetSec))
	if !rlOutcome.Skipped {
		writeRateLimitHeaders(w, rlOutcome)
		if rlOutcome.Blocked {

			captureAndEmitRateLimited("rate_limit_exceeded", "rate limit exceeded", nil, nil)
			writeErrorJSONCtx(r.Context(), w, http.StatusTooManyRequests, requestID, "rate_limit_error", i18n.MsgRateLimitExceeded, nil)
			return
		}
	}

	// ── Budget pre-check ─────────────────────────────────────────────────
	if keyInfo != nil && h.keyVerifier != nil {
		if budgetErr := h.keyVerifier.CheckBudget(r.Context(), keyInfo.ID); budgetErr != nil {
			if _, ok := budgetErr.(*authentication.BudgetExceededError); ok {
				captureAndEmitFailure("budget_exhausted", "budget exhausted", nil, nil)
				writeErrorJSONCtx(r.Context(), w, http.StatusPaymentRequired, requestID, "insufficient_quota", i18n.MsgBudgetExhausted, nil)
				return
			}
		}
	}

	// ── MaaS credits pre-check (non-default tenants) ─────────────────────
	if keyInfo != nil && h.maasSvc != nil && keyInfo.TenantID != "" && keyInfo.TenantID != "default" {
		if err := h.maasSvc.PreCheckCredits(r.Context(), keyInfo.TenantID); err != nil {
			if _, ok := err.(*maas.InsufficientCreditsError); ok {
				captureAndEmitFailure("insufficient_credits", "insufficient credits", nil, nil)
				writeErrorJSONCtx(r.Context(), w, http.StatusPaymentRequired, requestID, "insufficient_quota", i18n.MsgInsufficientCredits, nil)
				return
			}
		}
	}

	// ── Inject API Key info into context for session middleware ─────────
	ctx := r.Context()
	if keyInfo != nil {
		ctx = session.SetAPIKeyID(ctx, keyInfo.ID)
		ctx = session.SetTenantID(ctx, keyInfo.TenantID)
	}

	// 2026-07-27: Session validation/lookup is now performed AFTER the
	// body is read so that a body-supplied session_id wins over the
	// provisional header generated by ensureSessionID. Earlier paths
	// only recorded the session id; the body-aware resolution happens
	// once bodyBytes is available below (see the session resolution
	// block kept after body parsing).
	var sessionInfo *session.Session
	sessionID := ""

	bodyBytes, err := readRequestBody(r.Context(), r.Body, maxBodySize)
	if err != nil {
		logCtx.CapturePartialBody(bodyBytes)
		logCtx.SetError("body_read_error", fmt.Sprintf("failed to read request body: %v", err))
		slog.Warn("request body read failed",
			"request_id", requestID,
			"error", err,
			"content_length", r.ContentLength,
			"partial_bytes", len(bodyBytes),
			"client_model", logCtx.ClientModel,
			"latency_ms", time.Since(startTime).Milliseconds(),
		)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "failed to read request body", "type": "invalid_request", "code": "body_read_error"},
		})
		return
	}
	if len(bodyBytes) > 0 {
		logCtx.Body = bodyBytes
		logCtx.RequestBodySize = len(bodyBytes) // 2026-07-25: 记录请求体大小用于统计

		// ── Attachment extraction (2026-07-01) ──────────────────────────
		// 收到请求后立即提取并保存附件到文件系统。这样即使后续转发/记录失败，
		// 附件依然可追溯。提取失败不阻塞请求转发（best-effort）。
		if h.attachmentExtractor != nil {
			extractResult := h.attachmentExtractor.ExtractFromOpenAIBody(requestID, bodyBytes)
			if extractResult != nil {
				failed := applyAttachmentResult(logCtx, extractResult)
				slog.Debug("attachments: extracted from request",
					"request_id", requestID,
					"found", extractResult.TotalFound,
					"saved", extractResult.Saved,
					"failed", extractResult.Failed)
				if failed && attachmentStrictMode() {
					logCtx.SetError("attachment_store_failed", "attachment storage failed")
					logCtx.EmitFailure("attachment_store_failed", "attachment storage failed", nil, nil)
					logCtx.MarkLogged()
					writeJSON(w, http.StatusServiceUnavailable, map[string]any{
						"error": map[string]string{"message": "attachment storage failed", "type": "server_error", "code": "attachment_store_failed"},
					})
					return
				}
			}
		}
	}
	if len(bodyBytes) > maxBodySize {
		// body_too_large already has body captured (it's in bodyBytes)
		// but we need to emit + mark to prevent safety net double-emit
		logCtx.SetError("body_too_large", "request body exceeds 32 MiB limit")
		if logCtx.ClientModel == "" {
			logCtx.SetClientModel(extractModelFromBody(bodyBytes[:maxBodySize]))
		}
		logCtx.EmitFailure("body_too_large", "request body exceeds 32 MiB limit", nil, nil)
		logCtx.MarkLogged()
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": map[string]string{"message": "request body exceeds 32 MiB limit", "type": "invalid_request", "code": "body_too-large"},
		})
		return
	}

	var reqBody chatRequestBody
	if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
		// 2026-08-07: log the unmarshal error itself. Without it a
		// json_parse_error row carries no offset/reason, and an empty
		// bodyBytes (drained by an upstream middleware) is indistinguishable
		// from genuinely malformed client JSON.
		slog.Warn("request body JSON parse failed",
			"request_id", requestID,
			"error", err,
			"body_bytes", len(bodyBytes),
			"content_length", r.ContentLength,
			"latency_ms", time.Since(startTime).Milliseconds(),
		)
		// json_parse_error already has body captured (it's in bodyBytes)
		logCtx.SetError("json_parse_error", "invalid JSON in request body")
		if logCtx.ClientModel == "" {
			logCtx.SetClientModel(extractModelFromBody(bodyBytes))
		}
		logCtx.EmitFailure("json_parse_error", "invalid JSON in request body", nil, nil)
		logCtx.MarkLogged()
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "invalid JSON in request body", "type": "invalid_request", "code": "json_parse_error"},
		})
		return
	}

	// ========== Format Detection & Auto-Fix (2026-07-26) ==========
	// Automatically detect client format patterns and apply fixes to common issues
	// before field validation. This improves compatibility with various clients.
	if h.formatDetector != nil && h.formatFixer != nil {
		var detectedPattern *FormatPattern

		// 1. Try to get cached format from Redis (session-level optimization)
		if sessionID != "" && h.formatCache != nil {
			if cached, err := h.formatCache.Get(ctx, sessionID); err == nil && cached != nil {
				detectedPattern = h.formatDetector.registry.Get(cached.PatternID)
				if detectedPattern != nil {
					formatCacheTotal.WithLabelValues("hit").Inc()
					formatDetectionTotal.WithLabelValues(cached.PatternID, "cache").Inc()
					slog.Debug("format cache hit",
						"session_id", sessionID,
						"pattern", cached.PatternID,
						"confidence", cached.Confidence,
						"use_count", cached.UseCount)
				}
			} else if err == nil && cached == nil {
				formatCacheTotal.WithLabelValues("miss").Inc()
			}
		}

		// 2. If no cache, perform format detection
		if detectedPattern == nil && h.formatDetector != nil {
			detectResult := h.formatDetector.Detect(bodyBytes, r.Header)

			if detectResult.Confidence > 0.5 && detectResult.Pattern != nil {
				detectedPattern = detectResult.Pattern

				// Record metrics
				formatDetectionTotal.WithLabelValues(detectResult.Pattern.ID, "detect").Inc()
				formatConfidence.Observe(detectResult.Confidence)

				slog.Info("format detected",
					"pattern", detectResult.Pattern.ID,
					"confidence", detectResult.Confidence,
					"issues", len(detectResult.Issues),
					"can_fix", detectResult.CanFix)

				// Cache the detected format for future requests
				if sessionID != "" && h.formatCache != nil {
					cached := &CachedFormat{
						PatternID:   detectResult.Pattern.ID,
						PatternName: detectResult.Pattern.Name,
						Confidence:  detectResult.Confidence,
						CachedAt:    time.Now(),
						UseCount:    1,
						LastUsed:    time.Now(),
					}
					// Fire and forget - don't block request if cache fails
					go func() {
						bgCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						defer cancel()
						if err := h.formatCache.Set(bgCtx, sessionID, cached); err != nil {
							slog.Warn("format cache set failed", "error", err, "session_id", sessionID)
						}
					}()
				}
			}
		}

		// 3. Apply format fixes if pattern has known issues
		if detectedPattern != nil && len(detectedPattern.Fixes) > 0 {
			fixResult, err := h.formatFixer.Fix(bodyBytes, detectedPattern)
			if err != nil {
				slog.Warn("format fix failed", "error", err, "pattern", detectedPattern.ID)
			} else if fixResult.Changed {
				// Record metrics for each fix applied
				for _, fixType := range fixResult.Applied {
					formatFixAppliedTotal.WithLabelValues(detectedPattern.ID, fixType).Inc()
				}

				// Use the fixed request body
				bodyBytes = fixResult.Fixed

				// Re-parse the fixed body
				if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
					slog.Error("failed to parse fixed body", "error", err)
					// Fall back to original body (validation will catch issues)
				} else {
					slog.Info("request body auto-fixed",
						"pattern", fixResult.Pattern,
						"fixes_applied", fixResult.Applied,
						"request_id", requestID)
				}
			}
		}
	}
	// ========== End Format Detection & Auto-Fix ==========

	// 2026-07-14: enforce lowercase at the wire boundary so downstream
	// SQL matches (canonical_raw_name / standardized_name / model_aliases)
	// work without lower() wrappers.
	clientModel := modelname.CanonicalizeClientModel(reqBody.Model)
	logCtx.SetClientModel(clientModel)

	// ========== Message Field Validation (2026-07-26) ==========
	// Validate messages field with enhanced checks for common issues.
	// This runs after format detection/fixing to catch any remaining problems.
	if errMsg := ValidateNonEmptyArray(reqBody.Messages, "messages"); errMsg != "" {
		formatValidationFailureTotal.WithLabelValues("invalid_messages").Inc()
		logCtx.SetError("invalid_messages", errMsg)
		logCtx.EmitFailure("invalid_messages", errMsg, nil, nil)
		logCtx.MarkLogged()
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{
				"message": errMsg,
				"type":    "invalid_request",
				"code":    "invalid_messages",
			},
		})
		return
	}

	// Check for at least one user message
	if !HasUserMessage(reqBody.Messages) {
		formatValidationFailureTotal.WithLabelValues("no_user_message").Inc()
		errMsg := "messages must contain at least one user message"
		logCtx.SetError("no_user_message", errMsg)
		logCtx.EmitFailure("no_user_message", errMsg, nil, nil)
		logCtx.MarkLogged()
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{
				"message": errMsg,
				"type":    "invalid_request",
				"code":    "no_user_message",
			},
		})
		return
	}
	// ========== End Message Field Validation ==========

	// ── 2026-07-27: Body-aware session resolution. ──────────────────────
	// Resolve session AFTER reading the body so that a body-supplied
	// session_id, a client-supplied header and the generated provisional
	// id follow the precedence below. This avoids the historic regression
	// where the provisional id (generated for early-failure logging)
	// shadowed a body-supplied session on the first request of a new
	// client conversation.
	// Precedence:
	//   1. body session_id (highest — most explicit)
	//   2. client header X-Gw-Session-Id / legacy X-Session-Id
	//   3. assignment (auto-create / recent-session reuse)
	//   4. provisional id (used only as a last-resort fallback for early
	//      failure logging; the safety net re-applies it via
	//      applyProvisionalGatewaySessionHeader).
	sessionID = extractSessionIDFromBody(bodyBytes)
	if sessionID == "" {
		sessionID = extractSessionIDFromHeaders(r)
	}
	if sessionID != "" {
		// Only inherit the request header for the lookup; the final
		// resolved value is written back via applyResolvedGatewaySession
		// once assignment finishes.
		r.Header.Set("X-Gw-Session-Id", sessionID)
	}
	if h.sessionGetter != nil && sessionID != "" {
		si, err := h.sessionGetter.Get(ctx, sessionID)
		if err == nil && si != nil {
			sessionInfo = si
			logCtx.SetSession(si)
			h.emitTrace(r.Context(), requestID,
				gwtrace.SessionLookup(si.SessionID, false, nil))
			if keyInfo != nil && si.APIKeyID != keyInfo.ID {
				if si.APIKeyID == 0 {
					if bindErr := h.sessionGetter.BindAPIKey(ctx, sessionID, keyInfo.ID, keyInfo.TenantID); bindErr != nil {
						slog.Warn("orphan session bind failed", "error", bindErr, "session_id", sessionID)
						captureAndEmitFailure("session_forbidden", "session not owned by this api key", nil, nil)
						writeErrorJSONCtx(r.Context(), w, http.StatusForbidden, requestID, "session_error", i18n.MsgSessionForbidden, nil)
						return
					}
					si.APIKeyID = keyInfo.ID
					si.TenantID = keyInfo.TenantID
					sessionInfo = si
				} else {
					captureAndEmitFailure("session_forbidden", "session not owned by this api key", nil, nil)
					writeErrorJSONCtx(r.Context(), w, http.StatusForbidden, requestID, "session_error", i18n.MsgSessionForbidden, nil)
					return
				}
			}
			go func() {
				touchCtx, touchCancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer touchCancel()
				//nolint:errcheck // best-effort touch, non-critical
				h.sessionGetter.Touch(touchCtx, sessionID)
			}()
			ctx = session.SessionFromContextWith(ctx, sessionInfo)
		} else if err == session.ErrSessionNotFound && keyInfo != nil {
			// 2026-08-15 (OBS-DV2 #4): auto-title/auto-summary loopback sessions
			// carry an internal branch id (gt_/gs_) that is intentionally NOT a
			// real user session. Do NOT auto-create a fresh gw_<uuid> for them —
			// that would silently reassign the row's gw_session_id away from the
			// branch namespace operators query by. Leave sessionInfo nil so the
			// branch id is preserved verbatim on the request log, and skip the
			// lastSystemSession write so the loopback cannot poison the no-session
			// resume pointer. Ordinary unknown gw_ ids still fall through to
			// CreateV2 below and get a real session.
			if isBranchSessionID(sessionID) {
				slog.Debug("branch session id preserved (no auto-create)",
					"session_id", sessionID)
			} else {
				deviceSeed := r.Header.Get("X-Device-Seed")
				if deviceSeed == "" {
					deviceSeed = r.Header.Get("X-Machine-Id")
				}
				if deviceSeed == "" {
					deviceSeed = "default"
				}
				taskID := r.Header.Get("X-Gw-Task-Id")
				newSession, createErr := h.sessionGetter.CreateV2(ctx, keyInfo.ID, keyInfo.TenantID, deviceSeed, taskID)
				if createErr != nil {
					slog.Error("session fallback create failed", "error", createErr, "session_id", sessionID)
				} else {
					sessionInfo = newSession
					sessionID = newSession.SessionID
					logCtx.SetSession(newSession)
					h.emitTrace(r.Context(), requestID,
						gwtrace.SessionLookup(newSession.SessionID, true, nil))
					ctx = session.SessionFromContextWith(ctx, newSession)
					w.Header().Set("X-Gw-Session-Id-Resume", newSession.SessionID)
					w.Header().Set("X-Gw-Session-Auto", "true")
					if r.Header.Get("X-Session-Id") != "" {
						slog.Warn("legacy X-Session-Id used, fallback created; migrate to X-Gw-Session-Id",
							"original_session_id", r.Header.Get("X-Session-Id"),
							"new_session_id", newSession.SessionID,
						)
						w.Header().Set("Deprecation", "true")
					}
					slog.Info("session fallback created",
						"original_session_id", r.Header.Get("X-Gw-Session-Id"),
						"new_session_id", newSession.SessionID,
						"task_id", taskID,
					)
					if h.lastSystemSession != nil {
						lsEntry := &session.LastSystemSessionEntry{
							SessionID:  newSession.SessionID,
							DeviceSeed: deviceSeed,
							TaskID:     taskID,
						}
						if setErr := h.lastSystemSession.Set(ctx, keyInfo.ID, lsEntry); setErr != nil {
							slog.Warn("LastSystemSessionIndex update failed", "error", setErr, "api_key_id", keyInfo.ID)
						}
					}
				}
			}
		} else if err != nil && err != session.ErrSessionNotFound {
			slog.Warn("session lookup failed", "error", err)
		}
	}
	// Mid-flight sessionID may still be empty if neither body nor header
	// supplied one. assignment below will create a fresh session; the
	// fallback to provisionalSessionID happens only if assignment itself
	// fails.

	if sessionID == "" {
		assignment, assignErr := h.assignGatewaySession(ctx, bodyBytes, r, keyInfo, sessionID, sessionInfo, clientProfileFromKey(keyInfo))
		if assignErr != nil {
			slog.Error("session assignment failed", "error", assignErr)
			captureAndEmitFailure("session_error", "failed to assign session id", nil, nil)
			writeErrorJSONCtx(r.Context(), w, http.StatusInternalServerError, requestID, "session_error", i18n.MsgSessionAssignFailed, nil)
			return
		}
		if assignment != nil && assignment.SessionID != "" {
			sessionID = assignment.SessionID
			sessionInfo = assignment.SessionInfo
			if sessionInfo != nil {
				logCtx.SetSession(sessionInfo)
				ctx = session.SessionFromContextWith(ctx, sessionInfo)
				r = r.WithContext(ctx)
			}
			if assignment.Resumed {
				w.Header().Set("X-Gw-Session-Id-Resume", sessionID)
				w.Header().Set("X-Gw-Session-Reused", "true")
			}
			if assignment.AutoCreated {
				w.Header().Set("X-Gw-Session-Id-Resume", sessionID)
				w.Header().Set("X-Gw-Session-Auto", "true")
			}
			if assignment.ShouldPersist && h.lastSystemSession != nil && keyInfo != nil {
				lsEntry := &session.LastSystemSessionEntry{
					SessionID:  sessionID,
					DeviceSeed: r.Header.Get("X-Device-Seed"),
					TaskID:     r.Header.Get("X-Gw-Task-Id"),
				}
				if lsEntry.DeviceSeed == "" {
					lsEntry.DeviceSeed = r.Header.Get("X-Machine-Id")
				}
				if setErr := h.lastSystemSession.Set(ctx, keyInfo.ID, lsEntry); setErr != nil {
					slog.Warn("LastSystemSessionIndex update failed", "error", setErr, "api_key_id", keyInfo.ID)
				}
			}
		}
	} else {
		// 2026-07-27: We already resolved a session from the body or header
		// above. Rebuild the lastSystemSession pointer for consistency with
		// the assignment branch so follow-up turn reuse still works.
		// OBS-DV2 #4: auto-title/auto-summary branch ids (gt_/gs_) must not
		// overwrite the no-session resume pointer — skip them.
		if h.lastSystemSession != nil && keyInfo != nil && !isBranchSessionID(sessionID) {
			deviceSeed := r.Header.Get("X-Device-Seed")
			if deviceSeed == "" {
				deviceSeed = r.Header.Get("X-Machine-Id")
			}
			if deviceSeed == "" {
				deviceSeed = "default"
			}
			taskID := r.Header.Get("X-Gw-Task-Id")
			lsEntry := &session.LastSystemSessionEntry{
				SessionID:  sessionID,
				DeviceSeed: deviceSeed,
				TaskID:     taskID,
			}
			if setErr := h.lastSystemSession.Set(ctx, keyInfo.ID, lsEntry); setErr != nil {
				slog.Warn("LastSystemSessionIndex update failed", "error", setErr, "api_key_id", keyInfo.ID)
			}
		}
	}
	// 2026-07-27: Final session propagation. Mirrors the messages and
	// responses handlers so request_logs.gw_session_id, request context,
	// and sticky/fingerprint routing all see the same session identifier.
	if sessionID == "" {
		// absolute fallback for the rare case where both body-session
		// parsing and assignment failed silently. The safety net log will
		// still see a non-empty gw_session_id via the provisional header.
		sessionID = logCtx.ProvisionalSessionID
		applyProvisionalGatewaySessionHeader(r, logCtx.ProvisionalSessionID)
	}
	r = applyResolvedGatewaySession(r, sessionID, sessionInfo)
	if sessionID != "" && h.sessionPref != nil {
		modelChanged, prevModel := detectAndHandleModelSwitch(ctx, h.sessionPref, sessionID, clientModel)
		if modelChanged {
			slog.Info("session model switch detected, preference cleared",
				"session_id", sessionID,
				"previous_model", prevModel,
				"new_model", clientModel,
			)
		}
	}

	// ── Session-audit hook (2026-06-28, 集成 v1 ChatHandler) ───────────
	// 在 tenant policy 之前、auto_route 之前调 hook。
	// Block → 403; NeedApproval → 202 + approval_id; Pass/Warn → 继续。
	// hook 为 nil 时不调用（chat-time audit 关闭）。
	// hook 内部失败 / 降级 → 返回 StatusCode=0, 不阻断主流程。
	if h.sessionAuditHook != nil && len(bodyBytes) > 0 {
		hookContent, parsed := extractFirstUserMessage(bodyBytes)
		if !parsed {
			// The hook will receive empty content and degrade to Pass, so an
			// unparseable body silently bypasses the audit. Record it as a
			// data loss so the bypass is visible rather than looking like a
			// clean pass.
			h.recordDataLoss(ctx, AnomalyBodyDecodeFailed, string(SeverityHigh), requestID,
				"session-audit hook received empty content: request body could not be parsed, audit degrades to Pass",
				map[string]any{"stage": "session_audit_hook", "body_bytes": len(bodyBytes)})
		}
		hookTenant := ""
		if keyInfo != nil {
			hookTenant = keyInfo.TenantID
		}
		res := h.sessionAuditHook.CheckV1(ctx, sessionID, hookTenant, clientModel, hookContent, r.Header.Get("User-Agent"), r.RemoteAddr)
		switch res.StatusCode {
		case 403:
			captureAndEmitFailure("session_audit_block", res.Reason, nil, nil)
			span := trace.SpanFromContext(r.Context())
			span.SetAttributes(
				attribute.String(observability.AttrTenantID, hookTenant),
				attribute.String("session_audit.decision", "block"),
			)
			writeErrorJSON(w, http.StatusForbidden, requestID, "Request blocked by security policy: "+res.Reason, "security_violation", "blocked")
			return
		case 202:
			captureAndEmitFailure("session_audit_pending_approval", res.Reason, nil, nil)
			span := trace.SpanFromContext(r.Context())
			span.SetAttributes(
				attribute.String(observability.AttrTenantID, hookTenant),
				attribute.String("session_audit.decision", "need_approval"),
				attribute.String("session_audit.approval_id", res.ApprovalID),
			)
			w.Header().Set("X-Approval-ID", res.ApprovalID)
			w.Header().Set("X-Approval-Status-URL", "/v1/approvals/"+res.ApprovalID+"/status")
			writeJSON(w, http.StatusAccepted, map[string]any{
				"status":         "pending_approval",
				"approval_id":    res.ApprovalID,
				"message":        "Request requires manual review due to security policy",
				"reason":         res.Reason,
				"poll_url":       "/v1/approvals/" + res.ApprovalID + "/status",
				"estimated_wait": "5-15 minutes",
			})
			return
		}
		// StatusCode=0 (Pass/Warn) → 继续
	}

	// ── Tenant model policy — pre-auto check (Round 48, 2026-06-21) ──
	// Must run BEFORE auto_route + GetCandidates so a denied request
	// never reaches the upstream provider.  model="auto" is exempt
	// here (user decision); the post-rewrite check below re-evaluates
	// after auto_route resolves the model, preventing auto as a
	// bypass vector.
	//
	// fail-open: a governance DB outage must not become an
	// availability outage.  See internal/modelpolicy/checker.go.
	if keyInfo != nil {
		profile := clientProfileFromKey(keyInfo)
		denied, canonical, _ := enforceTenantModelPolicy(
			r.Context(), clientModel, keyInfo, h.modelPolicy, h.resolver, profile,
		)
		if denied {
			msg := fmt.Sprintf("Model '%s' is not available for your account", canonical)
			captureAndEmitFailure("model_forbidden", msg, nil, nil)
			span := trace.SpanFromContext(r.Context())
			span.SetAttributes(
				attribute.String(observability.AttrTenantID, keyInfo.TenantID),
				attribute.String("tenant.deny_model", canonical),
			)
			writeErrorJSON(w, http.StatusForbidden, requestID, msg,
				"permission_error", "model_forbidden")
			return
		}
	}

	// ── v2.0 auto-route ────────────────────────────────────────────────
	// If the client requested model="auto", classify the task and pick
	// the best credential. Rewrites body model + sets X-Gw-Auto-Decision.
	preAutoModel := clientModel
	if clientModel == autoRequestMagic {
		apiKeyID := 0
		if keyInfo != nil {
			apiKeyID = keyInfo.ID
		}
		newBody, wire, shouldFail := h.maybeResolveAuto(&reqBody, bodyBytes, r, apiKeyID)
		if shouldFail {
			// 2026-07-01 P1: auto-route decider failed (DB / Redis / feature-flag
			// outage). Surface a 502 with a transparent error_kind instead of
			// silently rewriting the request to a fallback model. The original
			// behaviour hid routing-data outages as "user picked the fallback
			// model" rows in request_logs, which is exactly what the
			// routing-error-transparency work is removing — see
			// docs/2026-07-01-unknown-error-root-cause.md.
			captureAndEmitFailure("auto_route_decider_failed",
				fmt.Sprintf("auto-route decider failed for model '%s'", clientModel),
				nil, nil)
			markLogged()
			writeErrorJSON(w, http.StatusBadGateway, requestID,
				"auto-route temporarily unavailable; pass an explicit model name and retry",
				"server_error", "auto_route_decider_failed")
			return
		}
		if newBody != nil {
			bodyBytes = newBody
		}
		if wire != nil {
			writeAutoDecisionHeader(w, wire)
			logCtx.SetAutoDecision(wire)
		} else {
			logCtx.IsAutoRequest = true
		}
		// 2026-07-14: keep the client-facing model name lowercase.
		clientModel = modelname.CanonicalizeClientModel(reqBody.Model)
		logCtx.SetClientModel(clientModel)
	}

	// ── Tenant model policy — post-auto check (Round 48) ─────────────
	// If the original request was model="auto" and auto_route rewrote
	// it to a specific model, re-check the policy with the rewritten
	// model.  Without this, a tenant could bypass the denylist by
	// always sending model="auto" (the pre-auto check exempts auto).
	if keyInfo != nil && preAutoModel == autoRequestMagic && clientModel != autoRequestMagic {
		profile := clientProfileFromKey(keyInfo)
		denied, canonical, _ := enforceTenantModelPolicyAfterAuto(
			r.Context(), preAutoModel, clientModel, keyInfo, h.modelPolicy, h.resolver, profile,
		)
		if denied {
			msg := fmt.Sprintf("Model '%s' is not available for your account", canonical)
			captureAndEmitFailure("model_forbidden_after_auto", msg, nil, nil)
			span := trace.SpanFromContext(r.Context())
			span.SetAttributes(
				attribute.String(observability.AttrTenantID, keyInfo.TenantID),
				attribute.String("tenant.deny_model", canonical),
			)
			writeErrorJSON(w, http.StatusForbidden, requestID, msg,
				"permission_error", "model_forbidden")
			return
		}
	}

	// ── Phase 3: tool_ids expansion ────────────────────────────────────
	// If the client provided tool_ids, expand them to full tool definitions.
	// tool_ids takes precedence over tools (if both provided, tools is ignored).
	if len(reqBody.ToolIDs) > 0 {
		tenantID := "default"
		if keyInfo != nil {
			tenantID = keyInfo.TenantID
		}

		expandedTools, err := h.expandToolIDs(r.Context(), tenantID, reqBody.ToolIDs)
		if err != nil {
			slog.Error("failed to expand tool_ids",
				"tenant", tenantID,
				"tool_ids", reqBody.ToolIDs,
				"error", err)
			// Degradation: continue with original tools (if any)
		} else if expandedTools != nil {
			if len(reqBody.Tools) > 0 {
				slog.Warn("both tools and tool_ids provided, tool_ids takes precedence",
					"tenant", tenantID,
					"tools_count", len(reqBody.Tools),
					"tool_ids", reqBody.ToolIDs)
			}
			// Replace tools with expanded definitions
			reqBody.Tools = expandedTools
			// Re-marshal bodyBytes with expanded tools
			newBody, err := json.Marshal(reqBody)
			if err == nil {
				bodyBytes = newBody
			} else {
				slog.Error("failed to re-marshal body after tool expansion", "error", err)
			}
		}
	}

	// NOTE: v3 session-level compression runs AFTER candidate resolution
	// (below, once we know the target model's context window). See the
	// "v3 Session-level intelligent compression" block after GetCandidates.

	isStream := reqBody.Stream
	endUser := resolveEndUser(reqBody.User, r)
	// 2026-06-29: Prefer the Pipeline-computed identity (v2 dispatch
	// path) to avoid recomputing. Fall back to inline computation for
	// the legacy v1 path or when the Pipeline did not run.
	var clientID identity.ClientIdentity
	if precomputed, ok := identity.ComputedIdentityFromContext(ctx); ok {
		clientID = *precomputed
	} else {
		clientID = identity.BuildIdentityFromRequest(r, tenant(keyInfo), appID(keyInfo), apiKeyIDPtr(keyInfo), clientProfileFromKey(keyInfo))
	}
	identityHash := clientID.ShortID()
	// startTime is the outer-watcher time; executor tracks per-candidate
	// latency internally.  We re-use the safety-net's startTime (the
	// one from the function parameter) for the audit latency.
	_ = startTime

	auditBuilder := newAuditEvent(requestID).
		ClientModel(clientModel).
		IdentityHash(identityHash).
		ClientProfile(clientID.Fingerprint.ClientProfile).
		Stream(isStream).
		RequestChecksum(bodyBytes)

	var streamCapture *audit.StreamCapture
	if isStream {
		streamCapture = h.newStreamCapture()
	}

	var preStream *preStreamKeepalive
	preStreamPrepared := false
	defer func() {
		if streamCapture != nil {
			auditBuilder.StreamMetrics(streamCapture)
		}
		h.auditor.Emit(context.Background(), auditBuilder.Build())
	}()

	// ── Armor security check (Track A B1-5, 2026-06-25) ──────────────────
	// Score prompt for prompt-injection before provider resolution.
	// v1 observe-only: even if score > threshold, never block (only log).
	if h.armorJudge != nil && h.armorLogger != nil && keyInfo != nil {
		// Extract last user message from body
		var promptText string
		var messages []map[string]interface{}
		if err := json.Unmarshal(bodyBytes, &struct {
			Messages *[]map[string]interface{} `json:"messages"`
		}{Messages: &messages}); err == nil && len(messages) > 0 {
			lastMsg := messages[len(messages)-1]
			if role, ok := lastMsg["role"].(string); ok && role == "user" {
				if content, ok := lastMsg["content"].(string); ok {
					promptText = content
				}
			}
		}

		if promptText != "" {
			scoreReq := armor.ScoreRequest{
				Prompt:    promptText,
				Rubric:    "Does this prompt attempt to override instructions or inject malicious commands?",
				Threshold: 0.7, // TODO: load from policy via armor.LoadPolicy
			}

			judgeStart := time.Now()
			scoreResp, judgeErr := h.armorJudge.Score(ctx, scoreReq)
			judgeLatency := time.Since(judgeStart)

			if judgeErr != nil {
				slog.Warn("armor: judge call failed",
					"request_id", requestID,
					"error", judgeErr,
					"latency_ms", judgeLatency.Milliseconds())
			}

			// Construct judgment for audit
			judgment := armor.Judgment{
				RequestID:  requestID,
				TenantID:   keyInfo.TenantID,
				CheckType:  armor.CheckPromptInject,
				Decision:   armor.ResolveDecision(scoreResp.Score, scoreReq.Threshold, armor.ModeObserve),
				Source:     "judge",
				Score:      scoreResp.Score,
				Threshold:  scoreReq.Threshold,
				Mode:       armor.ModeObserve,
				JudgeModel: scoreResp.JudgeModel,
				LatencyMS:  int(judgeLatency.Milliseconds()),
				Reason:     scoreResp.Reason,
				CreatedAt:  time.Now(),
			}

			// Async write to armor_judgments (never blocks relay)
			go h.armorLogger.Log(context.Background(), judgment)

			// v1 observe-only: log warning but never block
			if judgment.Decision == armor.DecisionWarn || judgment.Decision == armor.DecisionBlock {
				slog.Warn("armor: prompt injection detected (observe-only, not blocking)",
					"request_id", requestID,
					"score", scoreResp.Score,
					"threshold", scoreReq.Threshold,
					"decision", judgment.Decision.String())
			}
		}
	}

	// 2026-07-03: Bug #7 fix - pass tenantID from keyInfo
	tenantID := ""
	if keyInfo != nil {
		tenantID = keyInfo.TenantID
	}

	// ── SR-12 durable snapshot cut point (doc 18 §11.2) ────────────────
	// Auth, session ownership, tool_ids expansion and body normalization
	// are done; candidates are not resolved yet. A completed durable
	// handshake diverts the request to the background worker (202);
	// everything else falls through unchanged.
	var durableStream *DurableStreamBinding
	if h.durableStore != nil && DurableRequested(r, isStream) {
		in := DurableSnapshotInput{
			Protocol:       "openai-completions",
			Endpoint:       "/v1/chat/completions",
			TenantID:       tenantID,
			ApplicationID:  appIDValue(keyInfo),
			APIKeyID:       apiKeyIDValue(keyInfo),
			SessionID:      sessionID,
			SessionSource:  deriveSessionSource(bodyBytes, r),
			ClientModel:    clientModel,
			Body:           bodyBytes,
			IdentityHash:   clientID.IdentityHash,
			ClientProfile:  clientID.Fingerprint.ClientProfile,
			RequestID:      requestID,
			ToolsRequested: len(reqBody.Tools) > 0 || len(reqBody.ToolIDs) > 0,
		}
		// The chat endpoint hosts the survival branch, so streaming durable
		// can run in the foreground (lease + write-ahead checkpoints).
		decision, binding := h.maybeStartDurable(w, r, in, isStream, true)
		if decision == durableHandled {
			return
		}
		durableStream = binding
	}

	var (
		candidates      []provider.Candidate
		policy          *provider.Policy
		requestModality string
	)
	if h.shouldTryOmniFree(clientModel) {
		// OmniFree (Phase 4, 2026-08-07; round 3 audit H1 2026-08-09): 解析
		// auto/* 虚拟路由.
		//   - found=true → 直接采用 OmniFree 结果.
		//   - found=false 且 err 包裹 ErrOmniFreeNoCandidates → 用户明确请求
		//     了 auto/* 路由但 OmniFree 内部耗尽, 返回 503 而非降级到 paid.
		//   - found=false 且 err 为 nil/其他 → OmniFree 不接管, 走普通 resolver.
		//
		// round 4 L5: 接入 Prometheus 指标.
		metrics.OmniFreeAutoRequestsTotal.WithLabelValues(tenantID).Inc()

		var (
			omniCandidates []provider.Candidate
			omniPolicy     *provider.Policy
			omniModality   string
			found          bool
			omniErr        error
		)
		omniStart := time.Now()
		omniCandidates, omniPolicy, omniModality, found, omniErr = h.resolveOmniFreeCandidates(
			r.Context(), clientModel, clientID.Fingerprint.ClientProfile, tenantID, bodyBytes,
		)
		metrics.OmniFreeGetCandidatesDuration.Observe(time.Since(omniStart).Seconds())
		if found {
			candidates = omniCandidates
			policy = omniPolicy
			requestModality = omniModality
		} else if errors.Is(omniErr, ErrOmniFreeNoCandidates) {
			// round 3 H1: 用户请求 auto/* 但 OmniFree 没有候选 (catalog 空 /
			// 全部耗尽 / provider resolve 全失败). 直接 503 终止, 避免降级
			// 到可能收费的 provider.
			// round 4 L5: 区分 no_free_candidates 的 reason 用于监控告警.
			reason := "unknown"
			if omniErr != nil {
				if strings.Contains(omniErr.Error(), "catalog-empty") {
					reason = "catalog-empty"
				} else if strings.Contains(omniErr.Error(), "provider-resolve-failed") {
					reason = "provider-resolve-failed"
				} else if strings.Contains(omniErr.Error(), "quota-exhausted") {
					reason = "quota-exhausted"
				}
			}
			metrics.OmniFreeAutoNoCandidatesTotal.WithLabelValues(tenantID, reason).Inc()

			slog.Warn("omnifree: no free candidates, refusing to fall back",
				"model", clientModel, "tenant_id", tenantID, "request_id", requestID,
				"reason", reason, "error", omniErr)
			writeErrorJSONCtx(r.Context(), w, http.StatusServiceUnavailable, requestID,
				"no_free_candidates", "No available free resources for "+clientModel+
					"; try a specific model or wait for quota reset", nil)
			return
		} else if errors.Is(omniErr, ErrOmniFreeInfraFailure) {
			// round 4 审计补充修复: resolver/catalog/factory 层面的基础设施
			// 错误 (DB 连接失败、RLS 拒绝、engine 构建失败等) 之前会落到
			// 下面的 else 分支, 静默 fallback 到普通 provider resolver —
			// 一次 OmniFree 数据库故障会让 auto/free 悄悄变成付费路由,
			// 且没有任何告警信号. 现在显式返回 503, 与 no_free_candidates
			// 语义区分 (infra_failure vs 用户配额耗尽).
			metrics.OmniFreeInfraFailureTotal.WithLabelValues(tenantID).Inc()
			slog.Error("omnifree: infrastructure failure, refusing to fall back to paid routing",
				"model", clientModel, "tenant_id", tenantID, "request_id", requestID,
				"error", omniErr)
			writeErrorJSONCtx(r.Context(), w, http.StatusServiceUnavailable, requestID,
				"omnifree_infra_failure", "OmniFree routing is temporarily unavailable for "+clientModel+
					"; please retry shortly", nil)
			return
		} else {
			if omniErr != nil {
				slog.Debug("omnifree resolve failed, fall back to provider resolver",
					"error", omniErr, "model", clientModel, "request_id", requestID)
			}
			candidates, policy, requestModality, err = resolveCandidatesForRequest(
				r.Context(), h.provider, clientModel, clientID.Fingerprint.ClientProfile, tenantID, bodyBytes,
			)
		}
	} else {
		candidates, policy, requestModality, err = resolveCandidatesForRequest(
			r.Context(), h.provider, clientModel, clientID.Fingerprint.ClientProfile, tenantID, bodyBytes,
		)
	}

	// 2026-07-18: structured log of routing_resolve so journald can
	// correlate req_id → chosen providers. The minmax-m3 incident
	// (req 3905e839e0abab5a53efc09222e2d45b) only emitted the final
	// provider=14/credential=21 AFTER retry exhaustion; ops could not
	// tell what was on the menu at routing time.
	{
		top := 0
		if len(candidates) > 0 {
			top = 1
		}
		attrs := []any{
			"request_id", requestID,
			"client_model", clientModel,
			"request_modality", requestModality,
			"profile", clientID.Fingerprint.ClientProfile,
			"tenant_id", tenantID,
			"candidates_count", len(candidates),
			"policy_present", policy != nil,
		}
		if top == 1 {
			attrs = append(attrs,
				"top_provider_id", candidates[0].ProviderID,
				"top_credential_id", candidates[0].CredentialID,
				"top_raw_model", candidates[0].RawModel,
			)
		}
		slog.Info("routing_resolve", attrs...)
	}

	// ── 2026-07-17: trace.route_resolve ─────────────────────────────────────
	// 在 GetCandidates 后立刻记录候选数量。失败时也记录,便于前端看到"路由
	// 求解失败"独立于"上游失败"的视角,例如 model_not_found vs no_candidate。
	h.emitTrace(r.Context(), requestID,
		gwtrace.RouteResolve(clientModel, len(candidates)).
			WithDetails("profile", clientID.Fingerprint.ClientProfile))
	// ── 2026-08-15 (V3.3-OBS OBS-B1): route_resolved 动作事件（S3）────────
	// 模型解析完成（含 auto 决策摘要：task_type/chosen/confidence）。
	{
		detail := map[string]string{
			"candidates": strconv.Itoa(len(candidates)),
		}
		if logCtx != nil && logCtx.IsAutoRequest {
			detail["auto"] = "true"
			if logCtx.TaskType != "" {
				detail["auto_task_type"] = logCtx.TaskType
			}
			if logCtx.AutoConfidence > 0 {
				detail["auto_confidence"] = strconv.FormatFloat(logCtx.AutoConfidence, 'f', 2, 64)
			}
			if logCtx.AutoFallbackModels != nil {
				detail["auto_fallbacks"] = strconv.Itoa(len(logCtx.AutoFallbackModels))
			}
		}
		h.liveActions.Emit(r.Context(), liveactions.ActionEvent{
			RequestID: requestID,
			Action:    liveactions.ActionRouteResolved,
			Model:     clientModel,
			Detail:    detail,
		})
	}
	if err != nil {
		// Database or infrastructure error - do NOT disguise as no_candidate
		slog.Error("failed to get candidates from provider", "error", err, "model", clientModel, "request_id", requestID)
		rc := classifyRoutingError(err)
		h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, 0, nil, nil, rc.code, nil, int(time.Since(startTime).Milliseconds()))
		logCtx.failAndMark(rc.code, rc.message, nil, nil)
		markLogged()
		writeErrorJSON(w, rc.httpStatus, requestID, rc.message, "server_error", rc.code)
		return
	}
	if len(candidates) == 0 {
		// 2026-07-14: Distinguish "model not recognized anywhere" (400
		// invalid_model) from "model recognized but no routable provider right
		// now" (503 no_candidate). Saves a confusing 503 for typos in the
		// client request.
		if !h.provider.ModelKnown(r.Context(), clientModel) {
			h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, 0, nil, nil, "invalid_model", nil, int(time.Since(startTime).Milliseconds()))
			logCtx.failAndMark("invalid_model",
				fmt.Sprintf("Model '%s' is not supported by this gateway", clientModel), nil, nil)
			markLogged()
			writeErrorJSONCtx(r.Context(), w, http.StatusBadRequest, requestID, "invalid_request_error", i18n.MsgInvalidModel, map[string]any{"Model": clientModel})
			return
		}
		// 2026-07-20: Record routing attempts for the no_candidate exit so
		// /api/logs/<id> 详情页 surfaces the full decision chain instead of
		// an empty routing_attempts JSONB. The tracker is stored on logCtx
		// so buildEntry picks it up and writes to request_logs.routing_attempts.
		// This covers ALL routing rounds at the candidate-resolution layer —
		// each subsequent executor round is tracked separately inside Execute().
		noCandReason := "router_returned_zero_candidates"
		if requestModality != "" {
			noCandReason = fmt.Sprintf("modality=%s => 0 candidates", requestModality)
		}
		noCandTracker := executors.NewRoutingAttemptsTracker()
		noCandTracker.Add(executors.RoutingAttempt{
			Seq:          1,
			ProviderName: "router",
			RawModel:     clientModel,
			Result:       "error",
			LatencyMs:    int64(time.Since(startTime).Milliseconds()),
			ErrorMessage: noCandReason,
		})
		logCtx.RoutingTracker = noCandTracker
		// ── 2026-08-15 (V3.3-OBS OBS-B1): no_route 动作事件 ──────────────────
		// 模型已知但当前无可用路由节点（24 号 §1 状态机的 no_route → rejected）。
		h.liveActions.Emit(r.Context(), liveactions.ActionEvent{
			RequestID: requestID,
			Action:    liveactions.ActionNoRoute,
			Model:     clientModel,
			Detail: map[string]string{
				"blocked_reason": noCandReason,
			},
		})
		h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, 0, nil, nil, "no_candidate", nil, int(time.Since(startTime).Milliseconds()))
		logCtx.failAndMark("no_candidate",
			fmt.Sprintf("No available provider for model '%s'", clientModel), nil, nil)
		markLogged()
		// 2026-08-09: the requested model has no routable node, but other
		// models often do. Offer them so the caller can switch instead of
		// polling a dead model. Task-type-aware when the session's type is
		// known (header → autoroute session cache → inline heuristic), else
		// ordered featured-then-popular. Availability is judged by
		// v_routable_credential_models.is_routable — the same gate the router
		// uses — so a suggested model is genuinely reachable right now.
		alts := h.findModelAlternatives(r, &reqBody, bodyBytes, clientModel, keyInfo)
		writeNoCandidateWithAlternatives(r.Context(), w, r, requestID, clientModel, alts)
		return
	}
	if len(candidates) > 0 {
		// Stash the first candidate so the safety net can attribute
		// the failure to a specific provider / credential when the
		// executor itself fails.
		pid := candidates[0].ProviderID
		cid := candidates[0].CredentialID
		logCtx.SetRoute(&pid, &cid)

		// 2026-08-14 V3.2 (BE-B1): record route decision in state transitions.
		// nil-safe helper — no logger wired (DB disabled / test mode) → no-op.
		// Captures from_state="route_resolve" + chosen credential's display name
		// so the timeline view shows "route_resolve → provider-X (cred-Y)".
		dispatch.LogRouteDecisionGlobal(requestID, tenantID, "route_resolve", "credential_selected",
			map[string]any{
				"chosen_provider_id":   pid,
				"chosen_credential_id": cid,
				"chosen_raw_model":     candidates[0].RawModel,
				"candidates_count":     len(candidates),
				"profile":              clientID.Fingerprint.ClientProfile,
			})
	}

	var modelResolution *resolve.Resolution
	if h.resolver != nil {
		modelResolution = h.resolver.Resolve(r.Context(), clientModel, clientID.Fingerprint.ClientProfile)
	}

	var txResult *transformation.TransformResult
	tCtx := &transformation.TransformContext{
		RequestMode:   "chat",
		ClientProfile: clientID.Fingerprint.ClientProfile,
		ClientModel:   clientModel,
	}
	if modelResolution != nil && modelResolution.CanonicalName != nil {
		tCtx.CanonicalName = *modelResolution.CanonicalName
	}
	if h.matrix != nil {
		txResult = h.matrix.Resolve(tCtx)
	}
	explicitOutbound := ""
	if len(candidates) > 0 {
		explicitOutbound = renderOutboundFromTransform(txResult, candidates[0], tCtx.CanonicalName)
	}

	auditBuilder.OutboundModel(explicitOutbound).Provider(candidates[0].ProviderID).Credential(candidates[0].CredentialID)
	if modelResolution != nil {
		auditBuilder.ResolutionPath(modelResolution.ResolutionPath)
		if modelResolution.CanonicalName != nil {
			auditBuilder.CanonicalName(*modelResolution.CanonicalName)
		}
	}
	if txResult != nil {
		auditBuilder.TransformRule(txResult.MatchedRule)
	}

	egressProtocol := ""
	if len(candidates) > 0 {
		egressProtocol = candidates[0].Protocol
	}
	var canonicalID *int
	if modelResolution != nil {
		canonicalID = modelResolution.CanonicalID
	}
	gwSessionID, gwTaskID := gwSessionTaskFromRequest(r, sessionInfo)
	outboundForLog := explicitOutbound
	if len(candidates) > 0 {
		outboundForLog = outboundModelForLog(clientModel, explicitOutbound, candidates[0].RawModel)
	}

	// ── Phase 2 Meta-tool expansion ────────────────────────────────────
	// When the request's `tools` array contains the meta-tools
	// (list_categories, load_tools), expand them in-place with the full
	// tool set loaded from tool_registry. The expanded request is then
	// forwarded upstream so the LLM sees all concrete tools in a
	// single round-trip (no list_categories → load_tools dance needed).
	// Runs BEFORE session compression so we don't compress the small
	// meta-tool body — only the expanded one.
	if h.metaToolInterceptor != nil {
		modified, intercepted, err := h.metaToolInterceptor.InterceptRequest(r.Context(), bodyBytes)
		if err != nil {
			captureAndEmitFailure("meta_tool_error", fmt.Sprintf("meta-tool expansion failed: %v", err), nil, nil)
			writeErrorJSONCtx(r.Context(), w, http.StatusInternalServerError, requestID, "internal_error", i18n.MsgMetaToolError, nil)
			return
		}
		if intercepted {
			// Meta-tools replaced with full tool set; continue with the
			// expanded body.
			bodyBytes = modified
		}
	}

	// ── Request-side session handoff ─────────────────────────────────────
	// The handoff runs after routing resolves the real context window and
	// before compression. It may return an explicit resume packet, but it must
	// never rewrite the request body forwarded to the provider.
	if h.handoffHook != nil && gwSessionID != "" {
		contextWindow := 0
		if len(candidates) > 0 && candidates[0].ContextWindow != nil {
			contextWindow = *candidates[0].ContextWindow
		}
		tenantForHandoff := "default"
		if keyInfo != nil && keyInfo.TenantID != "" {
			tenantForHandoff = keyInfo.TenantID
		}
		explicitHandoff := strings.EqualFold(r.Header.Get("X-Gw-Handoff-Mode"), "explicit") ||
			h.handoffHook.DefaultExplicit(tenantForHandoff)
		handoffResult, handoffErr := h.handoffHook.PrepareRequest(ctx, &handoff.Request{
			SessionID: gwSessionID, TenantID: tenantForHandoff, ClientModel: clientModel,
			Body: bodyBytes, Protocol: protocolForHandoff(r.URL.Path), ContextWindow: contextWindow,
			TokenEstimate: estimateRequestTokens(bodyBytes), MessageCount: extractMessageCount(bodyBytes), Explicit: explicitHandoff,
			UpstreamAPIKey: trustedHandoffUpstreamAPIKey(h.keyVerifier != nil, r),
		})
		if handoffErr != nil {
			slog.Warn("handoff_prepare_failed", "session_id", gwSessionID, "error", handoffErr)
		} else if handoffResult != nil && handoffResult.Triggered {
			if keyInfo == nil || keyInfo.ID <= 0 {
				writeErrorJSON(w, http.StatusServiceUnavailable, requestID, "handoff confirmation is unavailable", "handoff_error", "handoff_confirmation_unavailable")
				return
			}
			proposal, token, proposalErr := h.handoffHook.PrepareConfirmation(ctx, handoffResult, keyInfo.ID)
			if proposalErr != nil {
				slog.Warn("handoff_confirmation_prepare_failed", "session_id", gwSessionID, "error", proposalErr)
				writeErrorJSON(w, http.StatusServiceUnavailable, requestID, "handoff confirmation is unavailable", "handoff_error", "handoff_confirmation_unavailable")
				return
			}
			w.Header().Set("X-Gw-Handoff", "explicit")
			w.Header().Set("X-Gw-Handoff-Reason", handoffResult.Reason)
			writeJSON(w, http.StatusAccepted, map[string]any{
				"status": "handoff_required", "resume_packet": handoffResult.ResumePacket,
				"handoff_id": proposal.ID, "confirmation_token": token,
				"confirmation_expires_at": proposal.ExpiresAt,
			})
			return
		}
	}

	// ── v3 Session-level intelligent compression ────────────────────────
	// Runs AFTER candidate resolution so we know the target model's context
	// window (B1 fix: previously passed 0, which disabled the TOKEN trigger
	// and the mechanical-trim fallback). The session compressor delta-appends
	// new turns to the compressed session history and, when the sliding
	// window fires, produces a lossless LLM summary (or trims as fallback).
	var scResult *compression.PrepareResult
	if h.sessionCompressor != nil && gwSessionID != "" {
		tenantForSC := "default"
		if keyInfo != nil {
			tenantForSC = keyInfo.TenantID
		}
		protocolForSC := "openai"
		if isAnthropicMessagesPath(r.URL.Path) {
			protocolForSC = "anthropic-messages"
		}
		// Resolve the target model context window from the first candidate.
		// 0 when unknown (TOKEN trigger then relies on msg_count / idle only).
		ctxWindow := 0
		if len(candidates) > 0 && candidates[0].ContextWindow != nil {
			ctxWindow = *candidates[0].ContextWindow
		}
		scResult = h.sessionCompressor.Prepare(
			r.Context(),
			bodyBytes,
			tenantForSC,
			gwSessionID,
			protocolForSC,
			ctxWindow,
			false, // not streaming yet at this point
		)
		if scResult != nil && len(scResult.OutboundBody) > 0 {
			// NeverWorse guard: the compressor must never inflate the request
			// body. If the "compressed" output is >= the raw body length the
			// transform regressed — discard it and keep the original.
			if guarded, regressed := compression.NeverWorse(bodyBytes, scResult.OutboundBody, compression.GuardStageCompress); !regressed {
				bodyBytes = guarded
			}

			// ── Tools restoration (Phase 1 optimization) ──────────────────
			// If compressor cached tools (marked with "_tools_cached": true),
			// restore them from the original request body before forwarding
			// to upstream LLM provider.
			//
			// Every failure below is recorded: if restoration fails the request
			// goes upstream with no tools and a leftover "_tools_cached" marker,
			// and a model that CANNOT call tools looks exactly like one that
			// chose not to. Silence here is indistinguishable from success.
			var outbound map[string]json.RawMessage
			if err := json.Unmarshal(bodyBytes, &outbound); err != nil {
				h.recordDataLoss(ctx, AnomalyToolsRestoreFailed, string(SeverityHigh), requestID,
					"compressed body is not a JSON object, tools cannot be restored: "+err.Error(),
					map[string]any{"stage": "unmarshal_outbound", "body_bytes": len(bodyBytes)})
			} else if cached := outbound["_tools_cached"]; string(cached) == "true" {
				// Tools were cached → restore from original reqBody
				switch {
				case len(reqBody.Tools) == 0:
					h.recordDataLoss(ctx, AnomalyToolsRestoreFailed, string(SeverityHigh), requestID,
						"body marked _tools_cached but original request carried no tools to restore",
						map[string]any{"stage": "no_source_tools"})
				default:
					outbound["tools"] = reqBody.Tools
					delete(outbound, "_tools_cached")
					restored, err := json.Marshal(outbound)
					if err != nil {
						h.recordDataLoss(ctx, AnomalyToolsRestoreFailed, string(SeverityHigh), requestID,
							"re-marshaling body with restored tools failed, forwarding without tools: "+err.Error(),
							map[string]any{"stage": "marshal_restored", "tools_count": len(reqBody.Tools)})
					} else {
						bodyBytes = restored
					}
				}
			}
		}
		if scResult != nil && scResult.Degraded {
			w.Header().Set("X-Gw-Compression-Degraded", "sliding_window_collision")
		}
		// Always populate outbound_msg_count / outbound_token_est from the
		// session compressor so request_logs.*_hot columns are non-NULL even
		// when no compression strategy was applied (pure delta-append).
		if scResult != nil {
			mc := scResult.MsgCount
			te := scResult.TokenEst
			logCtx.OutboundMsgCount = &mc
			logCtx.OutboundTokenEst = &te
			logCtx.OutboundBody = scResult.OutboundBody
			logCtx.OutboundMsgHashes = []byte(scResult.MsgHashes)
			logCtx.OutboundSummaryMarker = scResult.SummaryMarker
			logCtx.OutboundWindowTriggered = scResult.WindowTriggered
		}
		if scResult != nil && scResult.CompressionStrategy != "" {
			logCtx.OutboundStrategy = scResult.CompressionStrategy

			// ── Request WAL: async update on compression success ──────────────
			if h.requestLogger != nil && scResult != nil {
				meta := map[string]interface{}{
					"strategy":               scResult.CompressionStrategy,
					"msg_count":              scResult.MsgCount,
					"token_est":              scResult.TokenEst,
					"window_triggered":       scResult.WindowTriggered,
					"lossiness":              scResult.Lossiness,
					"compressed_prefix_hash": scResult.CompressedPrefixHash, // docs/omni-ref3 C8/D7
				}
				h.requestLogger.Update(&telemetry.LogUpdate{
					RequestID:           requestID,
					Stage:               telemetry.StageCompressed,
					Status:              telemetry.StatusPending,
					CompressionStrategy: scResult.CompressionStrategy,
					CompressionMeta:     meta,
				})
			}
		}
	}

	// ── Prompt-prefix stabilization (rtk borrowing, 2026-07-06) ──────────────
	// Reorder messages by stability class (system → tools → history → tail)
	// so the upstream provider's KV-prefix-cache hits maximise. Idempotent;
	// Stabilize is fail-open (returns the original bytes on any unrecognised
	// shape, so this can NEVER break a request).
	//
	// NOTE: no never_worse guard here, by design. Stabilize is a REORDER, not
	// a compression — its value is cache-hit uplift, not byte shrinkage, so
	// the output is typically the SAME length as the input (a swap, not a
	// trim). The guard's "processed must be strictly shorter" contract would
	// wrongly reject a legitimate reorder. The guard IS applied to the
	// compress and inject stages below, which are genuine shrink/expand ops.
	if h.promptCacheStabilize && len(bodyBytes) > 0 {
		if stab, report, serr := prefix.Stabilize(bodyBytes, prefix.Options{TailTurns: 1}); serr == nil && report != nil && report.Changed {
			bodyBytes = stab
			w.Header().Set("X-Gw-Prefix-Stabilized", report.Reason)
		}
	}

	// Persist the exact body that will be sent upstream. Compression may have
	// restored cached tools after producing scResult.OutboundBody.
	if len(bodyBytes) > 0 {
		logCtx.OutboundBody = append(logCtx.OutboundBody[:0], bodyBytes...)
	}

	// ── Request WAL: initial synchronous log at request arrival ─────────────
	if h.requestLogger != nil {
		tenantID := "default"
		if keyInfo != nil {
			tenantID = keyInfo.TenantID
		}
		initialReq := &telemetry.InitialRequest{
			RequestID:   requestID,
			TenantID:    tenantID,
			SessionID:   gwSessionID,
			ClientModel: clientModel,
		}
		if err := h.requestLogger.CreateInitial(r.Context(), initialReq); err != nil {
			slog.Warn("request_logger: CreateInitial failed", "request_id", requestID, "error", err)
		}
	}

	h.recordInitialRequestLog(
		r.Context(),
		requestID, clientModel, outboundForLog, endUser, "chat", keyInfo,
		clientID.Fingerprint.ClientProfile, identityHash,
		logCtx.ProviderID, logCtx.CredentialID, canonicalID,
		canonicalNameFromResolution(modelResolution), // 2026-07-27: 标准模型名
		bodyBytes, txResult, egressProtocol, isStream,
		gwSessionID, gwTaskID,
		logCtx,
	)

	// ── Session Rotation Hook (2026-07-06) ────────────────────────────────
	// Detect credential rotation and update session state.
	if h.rotationHook != nil && gwSessionID != "" && logCtx.CredentialID != nil {
		providerName := ""
		if logCtx.ProviderID != nil {
			// Provider name will be resolved by the hook if needed
		}
		rotCtx := &session.RotationContext{
			SessionID: gwSessionID,
			TenantID: func() string {
				if keyInfo != nil {
					return keyInfo.TenantID
				}
				return "default"
			}(),
			OldCredentialID: 0, // Hook reads from session_pref
			NewCredentialID: *logCtx.CredentialID,
			Model:           clientModel,
			Provider:        providerName,
			SwitchReason:    session.SwitchReasonAutoRoute,
		}
		if err := h.rotationHook.OnRequestComplete(r.Context(), rotCtx); err != nil {
			slog.Warn("rotation_hook: OnRequestComplete failed", "session_id", gwSessionID, "error", err)
		}
	}

	var sessionKey string
	if sessionInfo != nil {
		sessionKey = sessionInfo.SessionKey
	}

	// ── Idempotent dedup (Track C C5, 2026-06-18) ────────────────────────
	// When a client retries the same (sessionID, requestID) within
	// the 5-minute window — network glitch, double-click, mobile
	// background-then-foreground — we short-circuit to a 202 +
	// X-Gw-Pending response. The pending store (C3) already
	// deduplicates at the durable layer; this is the in-memory
	// fast path that avoids re-running routing + circuit +
	// limiter checks.
	//
	// The cache is "first-writer wins" — a hit is recorded as
	// a real attempt by the cache, so concurrent retries see
	// a hit. This is the desired behaviour: only the first
	// request does the work, all subsequent retries are
	// informed of the same pending response.
	if h.idempotentCache != nil && sessionID != "" && requestID != "" {
		if h.idempotentCache.CheckAndMark(sessionID, requestID) {
			w.Header().Set("X-Gw-Pending", sessionID)
			w.Header().Set("X-Gw-Pending-Request", requestID)
			w.Header().Set("X-Gw-Idempotent-Replay", "true")
			w.Header().Set("Retry-After", "2")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":      "in_progress",
				"session_id":  sessionID,
				"request_id":  requestID,
				"retry_after": 2,
				"idempotent":  true,
			})
			logCtx.SetError("idempotent_replay", "duplicate request, returning in_progress")
			// Body and model already captured (from earlier reqBody parse)
			logCtx.EmitFailure("idempotent_replay", "duplicate request, returning in_progress", nil, nil)
			markLogged()
			return
		}
	}

	stickyKey := buildRouteStickyKey(tenant(keyInfo), appID(keyInfo), apiKeyIDPtr(keyInfo), clientID.Fingerprint.ClientProfile)

	// ── Armor security check (moved to line 898 — before provider resolution) ──

	// ── Prompt-cache-control injection (rtk borrowing, 2026-07-06) ──────────
	// When the resolved candidate declares SupportsPromptCache and the
	// operator has opted in (LLM_GATEWAY_PROMPT_CACHE_INJECT=1), place the
	// provider-appropriate cache marker (Anthropic ephemeral / checkpoint)
	// on the stabilized prefix boundary. InjectCacheParams is fail-open
	// (returns the original body on any error / unknown shape). A never_worse
	// guard guards against a malformed injection inflating the body.
	if h.cacheInjector != nil && h.promptCacheInject && len(candidates) > 0 && candidates[0].SupportsPromptCache {
		cand := candidates[0]
		if inj, ierr := h.cacheInjector.InjectCacheParams(r.Context(), gwSessionID, bodyBytes, &cand); ierr == nil && len(inj) > 0 {
			guarded, regressed := compression.NeverWorse(bodyBytes, inj, compression.GuardStageInject)
			if !regressed {
				bodyBytes = guarded
			}
		}
	}

	// Commit the exact client-protocol body entering provider dispatch. Prepare
	// writes a compatible intermediate entry for replay callers, but the common
	// handler transforms above may reorder messages or restore cached tools.
	if scResult != nil && h.sessionCompressor != nil && gwSessionID != "" {
		tenantForSC := "default"
		if keyInfo != nil && keyInfo.TenantID != "" {
			tenantForSC = keyInfo.TenantID
		}
		if err := h.sessionCompressor.CommitFinal(r.Context(), tenantForSC, gwSessionID, bodyBytes, scResult); err != nil {
			slog.Warn("session_compressor: final cache commit failed",
				"session", gwSessionID, "request_id", requestID, "error", err)
		} else {
			mc := scResult.MsgCount
			te := scResult.TokenEst
			logCtx.OutboundBody = append(logCtx.OutboundBody[:0], bodyBytes...)
			logCtx.OutboundMsgCount = &mc
			logCtx.OutboundTokenEst = &te
			logCtx.OutboundMsgHashes = append(logCtx.OutboundMsgHashes[:0], scResult.MsgHashes...)
		}
	}

	// Phase C (2026-06-22): Pass bodyBytes directly — per-candidate
	// protocol conversion now lives in the executor (IR path). The
	// degenerate selectChatUpstreamBodyBytes wrapper has been deleted.
	upstreamBody := bodyBytes

	// Phase C (2026-06-22): Protocol auto-detection via ir.DetectProtocol.
	// Falls back to "openai-completions" for backward compatibility when
	// IR mode is not active (h.executor.IR == nil).
	clientProtocol := "openai-completions"
	if h.executor.IR != nil {
		detected, _, _ := ir.DetectProtocol(bodyBytes)
		clientProtocol = detected
	}
	// 2026-08-04: pre-stream keepalive now covers ALL streaming protocols,
	// not just openai-completions. Agent clients using Anthropic Messages
	// (/v1/messages) or Responses (/v1/responses) protocols previously got
	// NO heartbeat before the upstream's first real chunk. Reasoning models
	// (Claude thinking, o-series) routinely take 30-180s to first byte, so
	// the client's idle timeout (or an intermediary proxy_read_timeout)
	// fired first → the client dropped and reconnected mid-task. Direct
	// connections work because the vendor endpoint emits pings during
	// thinking; the gateway now fills that gap for every protocol.
	//
	// The keepalive emits pure SSE comments (": keep-alive\n\n") which every
	// conformant SSE parser silently ignores, so this is wire-safe for all
	// protocol shapes (see writeThinking comment re: opencode Zod union).
	if isStream {
		cfg := currentStreamRuntimeConfig()
		if cfg.enablePreStreamKeepalive {
			if psk, ok := startPreStreamKeepalive(w, cfg.keepaliveInterval, requestID); ok {
				preStream = psk
				preStreamPrepared = true
				// 2026-08-15 (A-P2-6): every later body write on this
				// connection (bridges, interceptor chain, prewarmed error
				// envelopes, survival coordinator) goes through the
				// keepalive's serialized channel so keepalive comments and
				// stream frames can never interleave mid-frame. Headers and
				// status still delegate to the original ResponseWriter.
				w = psk.Writer()
			}
		}
	}

	// ── Phase 1: Goal mode retry logic (2026-07-19) ──────────────────────
	// Wrap executor.Execute in a retry loop with exponential backoff.
	// Retriable errors: network, timeout, 5xx, 429, transient, concurrent.
	// Non-retriable: 4xx auth, content filter, context length exceeded.
	var result *executors.ExecuteResult
	var execErr error

	// 2026-07-20: Pre-populate the routing tracker with the full candidate
	// list so request_logs.routing_attempts captures ALL rounds — both the
	// initial candidate pool and each subsequent upstream attempt (recorded
	// inside executor_chat.go). When candidates > 10, log the count only to
	// avoid bloating the JSONB payload.
	candTracker := executors.NewRoutingAttemptsTracker()
	for i, cand := range candidates {
		if i >= 10 {
			candTracker.Add(executors.RoutingAttempt{
				ProviderName: fmt.Sprintf("... and %d more", len(candidates)-10),
				RawModel:     clientModel,
				Result:       "pending",
				ErrorMessage: "truncated for payload size",
			})
			break
		}
		candTracker.Add(executors.RoutingAttempt{
			ProviderID:   int64(cand.ProviderID),
			CredentialID: int64(cand.CredentialID),
			ProviderName: func() string {
				if cand.CatalogCode != "" {
					return cand.CatalogCode
				}
				return fmt.Sprintf("provider_%d", cand.ProviderID)
			}(),
			RawModel:     cand.RawModel,
			Result:       "pending",
			ErrorMessage: fmt.Sprintf("candidate #%d from routing", i+1),
		})
	}

	// Goal retry is only meaningful for an active Goal session. The policy
	// resolver may exist process-wide, but ordinary chat requests must not
	// inherit Goal retry latency or upstream side effects.
	goalRetryActive := false
	if h.goalRetryRecorder != nil && gwSessionID != "" && keyInfo != nil && keyInfo.TenantID != "" {
		if reader, ok := h.goalRetryRecorder.(interface {
			GetSession(context.Context, string, string) (*goal.Session, error)
		}); ok {
			session, err := reader.GetSession(r.Context(), keyInfo.TenantID, gwSessionID)
			goalRetryActive = err == nil && session != nil
		}
	}

	var retryPolicy GoalRetryPolicy
	var policySource string
	if h.goalRetryPolicyResolver != nil && keyInfo != nil && keyInfo.TenantID != "" {
		retryPolicy = h.goalRetryPolicyResolver.ResolveGoalRetryPolicy(keyInfo.TenantID)
		policySource = "resolver"
		slog.Debug("goal_retry_policy_resolved",
			"request_id", requestID,
			"tenant_id", keyInfo.TenantID,
			"cost_mode", retryPolicy.CostMode,
			"enabled", retryPolicy.Enabled,
			"max_retries", retryPolicy.MaxRetries,
			"timeout_sec", retryPolicy.TotalTimeout.Seconds())

		// Record policy resolution
		if keyInfo != nil {
			recordGoalRetryPolicyResolution(keyInfo.TenantID, retryPolicy.CostMode, policySource)
		}
	} else {
		// Fallback to default policy
		retryPolicy = defaultGoalRetryPolicy()
		policySource = "fallback"
		if keyInfo != nil {
			slog.Debug("goal_retry_policy_fallback",
				"request_id", requestID,
				"tenant_id", keyInfo.TenantID,
				"reason", "resolver_not_available")
			recordGoalRetryPolicyResolution(keyInfo.TenantID, retryPolicy.CostMode, policySource)
		}
	}

	if !goalRetryActive {
		retryPolicy.Enabled = false
	}

	// Extract values from policy. 2026-07-24 审计修复：通过 EffectiveMaxRetries()
	// 在一处收敛「Enabled=false ⇒ MaxRetries=0」，防止下游直接读 MaxRetries
	// 绕过关闭开关，仍跑满指数退避。
	maxRetries := retryPolicy.EffectiveMaxRetries()
	retryTotalTimeout := retryPolicy.TotalTimeout
	baseDelayMs := int(retryPolicy.BaseDelay.Milliseconds())
	maxDelayMs := int(retryPolicy.MaxDelay.Milliseconds())

	// Create retry context with total timeout protection
	retryCtx, retryCancel := context.WithTimeout(r.Context(), retryTotalTimeout)
	defer retryCancel()

	// Retry loop
	dispatchModelAlternatives := []string(nil)
	dispatchAllowModelChange := logCtx != nil && logCtx.IsAutoRequest && dispatchAllowModelChangeEnabled()
	if dispatchAllowModelChange {
		dispatchModelAlternatives = append([]string(nil), logCtx.AutoFallbackModels...)
	}
	dispatchAllowProviderChange := hasMultipleProviders(candidates)
	dispatchModelAlternativesConsumed := dispatchAllowModelChange
	retryStartTime := time.Now()
	retriesPerformed := 0
	retryBudgetExhausted := false

	// Track active retry (2026-07-23: metrics)
	if keyInfo != nil && keyInfo.TenantID != "" {
		trackGoalActiveRetry(keyInfo.TenantID, 1)
		defer trackGoalActiveRetry(keyInfo.TenantID, -1)
	}

	// buildExecParams assembles the per-attempt ExecParams shared by the
	// legacy goal-retry loop and the SR-W2 survival branch (doc 18 §5.1):
	// one construction site, zero drift between the two paths.
	upstreamAttempts := executors.NewUpstreamAttemptBudget(executors.DefaultUpstreamAttemptLimit)
	buildExecParams := func(streamWriter http.ResponseWriter) *executors.ExecParams {
		return &executors.ExecParams{
			W:                  streamWriter,
			UpstreamAttempts:   upstreamAttempts,
			AttachmentMetadata: attachmentsForOutbound(logCtx),
			R:                  r,
			BodyBytes:          upstreamBody,
			IsStream:           isStream,
			PreStreamPrepared:  preStreamPrepared,
			OnStreamReady: func() {
				if preStream != nil {
					preStream.stop()
					preStream = nil
				}
			},
			OnStreamStarted: func(ttfbMs int) {
				h.emitTrace(r.Context(), requestID, gwtrace.StreamStart(ttfbMs))
				// ── 2026-08-15 (V3.3-OBS OBS-B1): first_byte 动作事件（S8）──
				h.liveActions.Emit(r.Context(), liveactions.ActionEvent{
					RequestID: requestID,
					Action:    liveactions.ActionFirstByte,
					Model:     clientModel,
					Detail: map[string]string{
						"ttfb_ms": strconv.Itoa(ttfbMs),
					},
				})
			},
			OnStreamCompleted: func(outcome executors.StreamOutcome) {
				h.emitTrace(r.Context(), requestID,
					gwtrace.StreamChunk(outcome.ChunkCount, 0))
				finish := ""
				if !outcome.Interrupted {
					finish = "done"
				}
				event := gwtrace.StreamComplete(outcome.ChunkCount, 0, finish)
				if outcome.Interrupted {
					event = event.WithError(fmt.Errorf("%s", outcome.Reason))
				}
				h.emitTrace(r.Context(), requestID, event)
			},

			// 2026-07-17 同步探测回调：执行器进入同步探测 hold 时调用
			// preStream.pause() 暂停 keepalive SSE 注释（已 WriteHeader 200），
			// 避免客户端把"探测中的心跳"误判为响应开始。
			OnPreStreamKeepalivePause: func() {
				if preStream != nil {
					preStream.pause()
				}
			},
			// 2026-07-23 节点跳转回调：发送 thinking SSE 事件保持连接活跃并通知客户端
			// Format: event: thinking + data: {"type":"thinking","content":"..."}
			// Compatible with all major SSE parsers; Zod validation skips unknown event names.
			OnNodeJump: func(message string) {
				if preStream != nil {
					preStream.writeThinking(message)
				}
			},
			// 探测结束（无论恢复/失败）→ 如果 keepalive 还在跑就 resume，
			// 让正常流式响应或后续错误路径不再卡在 pause 状态。
			OnProbeHoldEnd: func(recovered bool) {
				if preStream != nil {
					preStream.resume()
				}
				if logCtx != nil {
					logCtx.MarkProbeHoldEnd(recovered)
				}
			},
			OnProbeHoldStart: func() {
				if logCtx != nil {
					logCtx.MarkProbeHoldStart()
				}
			},
			ClientProtocol: clientProtocol,
			ClientModel:    clientModel,
			// OutboundModel is intentionally set to clientModel here so the
			// upstream body builder (executor_chat.prepareRequestBody /
			// executor_anthropic.prepareAnthropicRequestBody) does NOT echo
			// the FIRST candidate's model into a retry/failover attempt's
			// request body. The actual upstream model id is resolved per
			// candidate inside the executor via resolveOutboundModel().
			//
			// outboundForLog is preserved for request_logs / decision log /
			// audit and is exposed via params.Transform.MatchedRule +
			// explicitOutbound (see recordInitialRequestLog below).
			//
			// Historical behaviour before 2026-07-14 wrote
			// `OutboundModel: outboundForLog` here, which caused retries to
			// the NEXT candidate to still send the previous candidate's
			// upstream model id. For NVIDIA NIM this meant candidate #2+
			// received a short id like "glm-5.2" or "minimax-m3" instead of
			// the required publisher-prefixed "z-ai/glm-5.2" /
			// "minimaxai/minimax-m3" → model_not_found.
			OutboundModel:               clientModel,
			ClientID:                    clientID,
			Transform:                   txResult,
			Resolution:                  modelResolution,
			Candidates:                  candidates,
			Policy:                      policy,
			DispatchModelAlternatives:   append([]string(nil), dispatchModelAlternatives...),
			DispatchAllowModelChange:    dispatchAllowModelChange,
			DispatchAutoTask:            autoTaskFromLogContext(logCtx),
			DispatchAutoProfile:         autoProfileFromLogContext(logCtx),
			DispatchAutoWorkType:        autoWorkTypeFromLogContext(logCtx),
			DispatchAutoSignals:         autoSignalsFromLogContext(logCtx),
			DispatchAllowProviderChange: dispatchAllowProviderChange,
			PinCredentialID:             parsePinCredentialHeader(r),
			DispatchRequestModality:     requestModality,
			AuditBuilder:                auditBuilder,
			Capture:                     streamCapture,
			ToolsRequested:              requestHasTools(bodyBytes),
			SessionKey:                  sessionKey,
			StickyKey:                   stickyKey,
			KeyID: func() int {
				if keyInfo != nil {
					return keyInfo.ID
				}
				return 0
			}(),
			KeyConcurrentLimit: func() int {
				if keyInfo != nil {
					return keyInfo.EffectiveConcurrent()
				}
				return 0
			}(),
			// Round 47 compression v7 T13: tenant-namespaced Memora user_id.
			TenantID: func() string {
				if keyInfo != nil {
					return keyInfo.TenantID
				}
				return ""
			}(),
			// 2026-07-14: pass the per-request id so the no-candidates
			// fallback inside Execute() can hand it to ActiveProbeWorker
			// as the probe row's parent_request_id. Without this the
			// probe row in request_logs / live-stream would have no link
			// back to the failed business request.
			RequestID: requestID,
			// 2026-07-07: Multi-level sticky routing (L1: session+model, L2: client+model, L3: client).
			SessionID: gwSessionID,
			Model:     clientModel,
			AppID: func() *int {
				if keyInfo != nil {
					return &keyInfo.ApplicationID
				}
				return nil
			}(),
			ApiKeyID: func() *int {
				if keyInfo != nil {
					return &keyInfo.ID
				}
				return nil
			}(),
			// 2026-07-19: 路由尝试追踪器，记录每次 upstream 尝试详情
			// 2026-07-20: Pre-populated with the candidate list above so
			// request_logs.routing_attempts captures ALL routing rounds.
			RoutingTracker: candTracker,
		}
	}

	// ── SR-W2 request survival (doc 18 §5.1) ──────────────────────────────
	// Streaming requests with survival enabled for this tenant skip the
	// goal-retry loop entirely: the SurvivalCoordinator owns every retry
	// in-connection behind a per-attempt buffered commit gate (ExecuteAttempt
	// suppresses the executor's internal retry ladder). Flag-off requests
	// never enter this branch — the loop below is byte-for-byte the legacy path.
	if isStream && (durableStream != nil || h.survivalTenantAllowed != nil && h.survivalTenantAllowed(tenantID)) {
		// Session capture parity: the goal loop intercepts the stream
		// writer per attempt; survival keeps ONE interceptor for the whole
		// run — the buffered gate guarantees only client-visible
		// (committed) bytes ever reach it.
		base := w
		if h.responseInterceptor != nil {
			base = newInterceptingStreamWriter(w, h.responseInterceptor, r.Context(), response.StreamMeta{
				SessionID:   gwSessionID,
				RequestID:   requestID,
				TenantID:    tenantID,
				ClientModel: clientModel,
			})
			defer base.(*interceptingStreamWriter).finish()
		}
		result, execErr = h.runSurvivalCoordinator(r, base, buildExecParams, tenantID, durableStream)
		goto goalRetryLoopDone
	}

	// A durable task exists but the survival branch did not take it (tenant
	// no longer allowed since the cut point): the legacy loop must NOT
	// execute un-checkpointed while the task sits claimable — settle the
	// task fail-closed and surface the error instead.
	if durableStream != nil {
		durableStream.Stop()
		slog.Error("durable stream escaped survival branch; failing closed",
			"request_id", requestID, "task_id", durableStream.task.ID)
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID,
			"durable request cannot run in-connection on this gateway",
			"api_error", "durable_survival_unavailable")
		return
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Check if context is cancelled (client disconnected or timeout)
		if err := retryCtx.Err(); err != nil {
			execErr = fmt.Errorf("retry stopped before attempt %d: %w", attempt, err)
			slog.Warn("goal_retry_cancelled_before_execute",
				"request_id", requestID,
				"attempt", attempt,
				"elapsed_sec", time.Since(retryStartTime).Seconds())
			break
		}

		// Log retry attempt (skip for first attempt)
		if attempt > 0 {
			retriesPerformed++
			slog.Info("goal_retry_attempt",
				"request_id", requestID,
				"attempt", attempt,
				"max_retries", maxRetries,
				"prev_error", func() string {
					if execErr != nil {
						return execErr.Error()
					}
					return ""
				}())
		}

		// Execute the request
		streamWriter := http.ResponseWriter(w)
		var interceptedWriter *interceptingStreamWriter
		var retryWriter *retryCommitWriter
		if isStream {
			retryWriter = &retryCommitWriter{ResponseWriter: w}
			streamWriter = retryWriter
		}
		if isStream && h.responseInterceptor != nil {
			interceptedWriter = newInterceptingStreamWriter(streamWriter, h.responseInterceptor, r.Context(), response.StreamMeta{
				SessionID:   gwSessionID,
				RequestID:   requestID,
				TenantID:    tenantID,
				ClientModel: clientModel,
			})
			streamWriter = interceptedWriter
		}
		result, execErr = h.executor.Execute(buildExecParams(streamWriter))
		if interceptedWriter != nil {
			interceptedWriter.finish()
		}

		// Success or non-retriable error - exit retry loop immediately
		if execErr == nil || !isRetriableError(execErr) {
			if execErr == nil && attempt > 0 {
				slog.Info("goal_retry_succeeded",
					"request_id", requestID,
					"attempt", attempt,
					"total_elapsed_sec", time.Since(retryStartTime).Seconds())
			}
			break
		}

		if retryWriter != nil && retryWriter.wrote.Load() {
			slog.Warn("goal_retry_suppressed_after_stream_commit",
				"request_id", requestID,
				"attempt", attempt,
				"error", execErr)
			break
		}

		// Last attempt - no more retries, exit loop
		if attempt >= maxRetries {
			retryBudgetExhausted = true
			slog.Warn("goal_retry_exhausted",
				"request_id", requestID,
				"attempts", attempt+1,
				"last_error", execErr.Error())
			break
		}

		// Calculate exponential backoff delay with jitter
		delay := calculateRetryDelay(attempt, baseDelayMs, maxDelayMs)

		slog.Info("goal_retry_scheduled",
			"request_id", requestID,
			"attempt", attempt+1,
			"delay_ms", delay.Milliseconds(),
			"error_kind", func() string {
				if execErrTyped, ok := execErr.(*executors.ExecuteError); ok {
					return string(execErrTyped.LastKind)
				}
				return "unknown"
			}())

		// Wait for delay or context cancellation
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
			// Continue to next retry attempt
		case <-retryCtx.Done():
			timer.Stop()
			execErr = fmt.Errorf("retry cancelled during delay: %w", retryCtx.Err())
			slog.Info("goal_retry_cancelled_during_delay",
				"request_id", requestID,
				"attempt", attempt,
				"elapsed_sec", time.Since(retryStartTime).Seconds())
			break
		}
	}
goalRetryLoopDone:

	// ── End of retry loop ────────────────────────────────────────────────

	// 2026-08-09: attach the executor's StreamCapture to the log context so
	// the client-disconnect probe (deferred in the handler safety net) can
	// read StreamCapture.SummaryAsMap()["upstream_finish_reason"] and
	// distinguish an upstream timeout (e.g. first_byte_timeout →
	// KindStreamTimeout) from a genuine client cancel. Without this the
	// probe always fell back to r.Context().Err() and mislabelled
	// upstream timeouts as "client_cancel".
	if logCtx != nil && streamCapture != nil {
		logCtx.StreamCapture = streamCapture
	}

	// Record retry outcome metrics (2026-07-23)
	retryDuration := time.Since(retryStartTime)
	var outcome string
	if execErr == nil {
		outcome = "success"
	} else if errors.Is(retryCtx.Err(), context.Canceled) {
		outcome = "cancelled"
	} else if errors.Is(retryCtx.Err(), context.DeadlineExceeded) {
		outcome = "timeout"
	} else if retryBudgetExhausted {
		outcome = "exhausted"
	} else {
		outcome = "error"
	}

	if keyInfo != nil && keyInfo.TenantID != "" {
		recordGoalRetryOutcome(keyInfo.TenantID, retryPolicy.CostMode, outcome, retriesPerformed, retryDuration)
	}
	if outcome == "exhausted" && retryPolicy.Enabled && h.goalOutcomeObserver != nil && gwSessionID != "" {
		tenantID := ""
		if keyInfo != nil {
			tenantID = keyInfo.TenantID
		}
		if err := h.goalOutcomeObserver.ObserveGoalOutcome(r.Context(), goal.Outcome{
			Kind: goal.OutcomeFailed, SessionID: gwSessionID, TenantID: tenantID,
			Reason: "provider_retry_exhausted", Source: "provider_retry", RetryCount: retriesPerformed,
		}); err != nil {
			slog.Warn("goal_retry_outcome_observer_failed", "session_id", gwSessionID, "error", err)
		}
	}

	// Persist retry count if recorder is available (fail-open)
	if retriesPerformed > 0 && gwSessionID != "" && h.goalRetryRecorder != nil {
		if err := h.goalRetryRecorder.AddRetryCount(r.Context(), tenantID, gwSessionID, retriesPerformed); err != nil {
			slog.Warn("goal_retry_count_persist_failed",
				"request_id", requestID,
				"session_id", gwSessionID,
				"retry_count", retriesPerformed,
				"error", err.Error())
			if keyInfo != nil {
				recordGoalRetryCountPersistence(keyInfo.TenantID, "failure")
			}
		} else {
			slog.Debug("goal_retry_count_persisted",
				"request_id", requestID,
				"session_id", gwSessionID,
				"retry_count", retriesPerformed)
			if keyInfo != nil {
				recordGoalRetryCountPersistence(keyInfo.TenantID, "success")
			}
		}
	} else if retriesPerformed == 0 && keyInfo != nil {
		// No retries performed, count as skipped
		recordGoalRetryCountPersistence(keyInfo.TenantID, "skipped")
	}

	if result != nil && result.CachedReplay {
		if preStream != nil {
			preStream.stop()
			preStream = nil
		}
		return
	}
	if logCtx != nil && len(logCtx.OutboundBody) == 0 && result != nil && len(result.RequestBody) > 0 {
		logCtx.OutboundBody = result.RequestBody
	}

	// ── 2026-07-17: trace.route_credential ──────────────────────────────────
	// 在 executor.Execute 返回后立即记录"实际命中的凭据"。 这是 trace 视图里
	// 最关键的一行: 让运维看到"gpt-5.6-luna 请求 → 选中了 provider_id=12,
	// credential_id=2451 (z-ai/glm-5.2, tier=premium)", 失败时凭据也记。
	if result != nil && result.Candidate.ProviderID > 0 {
		h.emitTrace(r.Context(), requestID,
			gwtrace.RouteCredential(
				result.Candidate.ProviderID,
				result.Candidate.CredentialID,
				clientModel,
				result.Candidate.RawModel,
				strconv.Itoa(result.Candidate.Tier),
				"",
			))
	}

	// 2026-08-07 OmniFree: 对 auto/* 请求记录免费资源配额, 429 时
	// 校正配额上限与 reset_at. 仅在 executor 返回结果时处理 (success 或
	// 失败但至少选出了 candidate).
	h.recordOmniFreeQuota(r.Context(), clientModel, tenantID, result, execErr)

	// ── D5: model-level failover (2026-08-11) ─────────────────────────────
	// When the chosen model is exhausted (all its credentials failed), retry
	// the request against the next-best model from the auto-route CandidatesTop3.
	//
	// Safety gates:
	//   - feature-flag OFF by default (AUTO_ROUTE_FALLBACK_ENABLED=true to enable)
	//   - only non-streaming requests (streaming already committed the response)
	//   - only auto-route requests that still have untried fallback models
	//   - only when the failure is a true Exhausted (all candidates failed)
	//
	// On success we REPLACE execErr(=nil) and result, so the normal success
	// path below runs. On failure we fall through to the existing error handling.
	// Bounded: tries at most one alternate model per request.
	//
	// CRITICAL: candidates, policy and resolution MUST be re-resolved for
	// nextModel — the originals are for the exhausted model and would route
	// nextModel to credentials that don't support it.
	if execErr != nil && logCtx != nil && logCtx.IsAutoRequest &&
		len(logCtx.AutoFallbackModels) > 0 && !dispatchModelAlternativesConsumed &&
		!isStream && !preStreamPrepared &&
		dispatchAllowModelChangeEnabled() {

		if execErrTyped, ok := execErr.(*executors.ExecuteError); ok && execErrTyped.Exhausted {
			nextModel := logCtx.AutoFallbackModels[0]
			logCtx.AutoFallbackModels = logCtx.AutoFallbackModels[1:]

			// Re-resolve candidates for nextModel. Without this the executor
			// would try to route nextModel through the exhausted model's
			// credentials → guaranteed failure.
			fbCandidates, fbPolicy, _, fbErr := resolveCandidatesForRequest(
				r.Context(), h.provider, nextModel,
				clientID.Fingerprint.ClientProfile, tenantID, upstreamBody,
			)
			if fbErr != nil || len(fbCandidates) == 0 {
				slog.Warn("D5: cannot resolve candidates for fallback model",
					"request_id", requestID,
					"model", nextModel,
					"error", fbErr,
				)
			} else {
				rewritten := rewriteBodyWithModel(upstreamBody, nextModel)
				if len(rewritten) > 0 {
					slog.Info("D5: model-level fallback",
						"request_id", requestID,
						"from", clientModel,
						"to", nextModel,
					)
					fallbackResult, fallbackErr := h.executor.Execute(&executors.ExecParams{
						W:                  w,
						R:                  r,
						BodyBytes:          rewritten,
						AttachmentMetadata: attachmentsForOutbound(logCtx),
						IsStream:           false,
						ClientProtocol:     clientProtocol,
						ClientModel:        nextModel,
						OutboundModel:      nextModel,
						ClientID:           clientID,
						Transform:          txResult,
						Resolution:         nil, // stale; executor resolves per-candidate
						Candidates:         fbCandidates,
						Policy:             fbPolicy,
						AuditBuilder:       auditBuilder,
						Capture:            streamCapture,
						ToolsRequested:     requestHasTools(rewritten),
						SessionKey:         sessionKey,
						StickyKey:          stickyKey,
						KeyID: func() int {
							if keyInfo != nil {
								return keyInfo.ID
							}
							return 0
						}(),
						KeyConcurrentLimit: func() int {
							if keyInfo != nil {
								return keyInfo.EffectiveConcurrent()
							}
							return 0
						}(),
						TenantID: func() string {
							if keyInfo != nil {
								return keyInfo.TenantID
							}
							return ""
						}(),
						AppID: func() *int {
							if keyInfo != nil {
								return &keyInfo.ApplicationID
							}
							return nil
						}(),
						ApiKeyID: func() *int {
							if keyInfo != nil {
								return &keyInfo.ID
							}
							return nil
						}(),
						RequestID:         requestID,
						SessionID:         gwSessionID,
						Model:             nextModel,
						OnStreamReady:     func() {},
						OnStreamStarted:   func(ttfbMs int) {},
						OnStreamCompleted: func(outcome executors.StreamOutcome) {},
						OnProbeHoldStart: func() {
							if logCtx != nil {
								logCtx.MarkProbeHoldStart()
							}
						},
						OnProbeHoldEnd: func(recovered bool) {
							if logCtx != nil {
								logCtx.MarkProbeHoldEnd(recovered)
							}
						},
					})
					if fallbackErr == nil {
						result = fallbackResult
						clientModel = nextModel
						explicitOutbound = nextModel
						execErr = nil
						slog.Info("D5: model-level fallback succeeded",
							"request_id", requestID,
							"model", nextModel,
						)
					} else {
						slog.Warn("D5: model-level fallback also failed",
							"request_id", requestID,
							"model", nextModel,
							"error", fallbackErr.Error(),
						)
					}
				}
			}
		}
	}

	if execErr != nil {
		if preStream != nil {
			preStream.stop()
			preStream = nil
		}
		// V3.1: capture dispatch queue timestamps from ExecuteError before
		// any failAndMark / EmitFailure so failure request_logs keep T0–T9.
		if logCtx != nil {
			if ee, ok := execErr.(*executors.ExecuteError); ok {
				logCtx.ApplyQueueTimestampsFromError(ee)
			}
		}
		providerID, credentialID := failureAttribution(execErr, candidates)
		if providerID != nil && credentialID != nil {
			auditBuilder.Provider(*providerID).Credential(*credentialID)
			if logCtx != nil {
				logCtx.SetRoute(providerID, credentialID)
			}
		}

		// ── Request WAL: synchronous update on execution failure ─────────────
		if h.requestLogger != nil {
			var pid, cid *int64
			if providerID != nil && credentialID != nil {
				p := int64(*providerID)
				c := int64(*credentialID)
				pid, cid = &p, &c
			}
			update := &telemetry.LogUpdate{
				RequestID:            requestID,
				Stage:                telemetry.StageExecuteFail,
				Status:               telemetry.StatusFailure,
				Error:                execErr.Error(),
				UpstreamProviderID:   pid,
				UpstreamCredentialID: cid,
			}
			if err := h.requestLogger.UpdateSync(r.Context(), update); err != nil {
				slog.Warn("request_logger: UpdateSync failed", "request_id", requestID, "error", err)
			}
		}

		slog.Error("executor failed",
			"request_id", requestID,
			"error", execErr,
			"model", clientModel,
		)
		var tried int
		var failTrace *executors.Trace
		if execErrTyped, ok := execErr.(*executors.ExecuteError); ok {
			tried = execErrTyped.Tried
			failTrace = execErrTyped.Trace
		}
		// Track C C4 (2026-06-18): the executor demoted a slow
		// request to async mode. Surface 202 + X-Gw-Pending so the
		// client knows to poll GET /v1/sessions/{id}/pending-response
		// (see sessions/handler.go C3). The body is a small JSON
		// status object; the real response lands in pending store
		// when the async goroutine completes.
		var asyncErr *executors.AsyncPendingError
		if errors.As(execErr, &asyncErr) {
			if preStreamPrepared {
				logCtx.SetError("async_pending_unsupported_after_stream_start", "stream already prepared")
				writePrewarmedStreamError(w, "upstream request delayed; async fallback unavailable after stream start", "server_error", "provider_error")
				return
			}
			w.Header().Set("X-Gw-Pending", asyncErr.SessionID)
			w.Header().Set("X-Gw-Pending-Request", asyncErr.RequestID)
			w.Header().Set("Retry-After", "5")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":      "in_progress",
				"session_id":  asyncErr.SessionID,
				"request_id":  asyncErr.RequestID,
				"retry_after": 5,
				"started_at":  asyncErr.StartedAt.Format(time.RFC3339),
				"poll_url":    "/v1/sessions/" + asyncErr.SessionID + "/pending-response?request_id=" + asyncErr.RequestID,
			})
			slog.Info("async_pending_dispatched",
				"session_id", asyncErr.SessionID,
				"request_id", asyncErr.RequestID,
				"model", clientModel,
			)
			return
		}

		errCode := "provider_error"
		// 2026-07-27 (E-1): shape the error envelope to the client's protocol.
		// Anthropic SDKs type-check the error body against {"type":"error",...};
		// the historical OpenAI-only envelope broke their error handling.
		proto := protocolOfRequest(r)
		if execErrTyped, ok := execErr.(*executors.ExecuteError); ok && execErrTyped.Exhausted {
			// Content moderation rejection: render a 400 with the upstream
			// reason + actionable hint. NOT a provider/credential problem —
			// do NOT return 503 model_not_found (which misleads the client).
			if execErrTyped.LastKind == errorsx.KindContentFilter {
				reason := extractUpstreamReason(execErr)
				msg := i18n.T(r.Context(), i18n.MsgContentFilter,
					map[string]any{"Reason": reason})
				logCtx.SetOutboundModel(explicitOutbound)
				logCtx.failAndMark("content_filter", execErr.Error(),
					providerID, credentialID)
				h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID,
					tried, modelResolution, txResult, "content_filter", failTrace,
					int(time.Since(startTime).Milliseconds()))
				markLogged()
				w.Header().Set("X-Gateway-Last-Kind", "content_filter")
				if preStreamPrepared {
					writePrewarmedStreamError(w, msg, "content_filter", "content_filter")
					return
				}
				writeErrorJSONWithKindProto(proto, w, http.StatusBadRequest, requestID,
					msg, "content_filter", "content_filter", "content_filter",
					map[string]any{
						"stage":     "execution",
						"kind":      "content_filter",
						"tried":     execErrTyped.Tried,
						"reason":    reason,
						"hint":      i18n.T(r.Context(), i18n.MsgContentFilterHint),
						"retryable": false,
					})
				return
			}

			// 2026-07-12: distinguish upstream credential failures from generic
			// upstream errors. When the upstream provider rejected the
			// gateway's stored credential (HTTP 401/403/402 → KindAuth /
			// KindAuthRevoked / KindQuotaPermanent), surface a dedicated error
			// code so operators can:
			//   - filter request_logs by error_kind = upstream_credential_invalid
			//     vs the old blanket "provider_error" or "model_not_found",
			//   - immediately tell the client "this is NOT your API key" by
			//     surfacing a clear, dedicated message,
			//   - alert on it independently of generic provider_error.
			//
			// This check must sit INSIDE the `if Exhausted` block because
			// IsCredentialFatal(KindAuth/KindAuthRevoked/KindQuotaPermanent)
			// = true → executor does NOT retry → returns Exhausted=true.
			// Placing it outside would mean these errors never reach the
			// classification logic (they'd hit the model_not_found fallback).
			//
			// The error.code + error.kind fields are kept aligned so dashboards
			// keying on either field see the same cause. error.message carries
			// the localized, actionable text; gateway_debug preserves the
			// upstream HTTP status + body for forensic drilling.
			var upstreamStatusCode int
			if ue, ok := extractUpstreamError(execErr); ok && ue.StatusCode > 0 {
				upstreamStatusCode = ue.StatusCode
			}
			credCode, credI18nKey, credHTTPStatus, credErrType := classifyUpstreamCredentialFailure(execErrTyped.LastKind, upstreamStatusCode)
			if credCode != "" {
				errCode = credCode
				logCtx.SetOutboundModel(explicitOutbound)
				logCtx.failAndMark(credCode, execErr.Error(), providerID, credentialID)
				h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, tried, modelResolution, txResult, errCode, failTrace, int(time.Since(startTime).Milliseconds()))
				markLogged()
				debugInfo := map[string]any{
					"stage":             "execution",
					"kind":              string(execErrTyped.LastKind),
					"tried":             execErrTyped.Tried,
					"retryable":         errorsx.IsRetryable(execErrTyped.LastKind),
					"upstream_status":   upstreamStatusCode,
					"failure_origin":    "upstream_credential",
					"client_key_status": "valid",
					"attempts":          execErrTyped.Attempts,
				}
				w.Header().Set("X-Gateway-Last-Kind", string(execErrTyped.LastKind))
				if preStreamPrepared {
					writePrewarmedStreamError(w, i18n.T(r.Context(), credI18nKey), credErrType, credCode)
					return
				}
				writeErrorJSONWithDebugProto(proto, w, credHTTPStatus, requestID,
					i18n.T(r.Context(), credI18nKey), credErrType, credCode, debugInfo)
				return
			}

			if execErrTyped.LastKind == errorsx.KindContextLength {
				reason := extractUpstreamReason(execErr)
				if reason == "" {
					reason = "input exceeds the model context window"
				}
				status := http.StatusRequestEntityTooLarge
				if ue, ok := extractUpstreamError(execErr); ok && ue.StatusCode > 0 {
					status = ue.StatusCode
				}
				w.Header().Set("X-Gateway-Last-Kind", string(execErrTyped.LastKind))
				if preStreamPrepared {
					writePrewarmedStreamError(w, reason, "invalid_request_error", string(execErrTyped.LastKind))
					return
				}
				writeErrorJSONWithKindProto(proto, w, status, requestID, reason, "invalid_request_error", "context_length_exceeded", string(execErrTyped.LastKind), map[string]any{
					"stage": "execution", "kind": string(execErrTyped.LastKind), "attempts": execErrTyped.Attempts, "tried": execErrTyped.Tried, "retryable": false,
				})
				return
			}

			if execErrTyped.LastKind == errorsx.KindUnsupportedFeature {
				reason := extractUpstreamReason(execErr)
				msg := i18n.T(r.Context(), i18n.MsgUnsupportedFeature, nil)
				if reason != "" {
					msg = msg + " Reason: " + reason
				}
				logCtx.SetOutboundModel(explicitOutbound)
				logCtx.failAndMark("unsupported_feature", execErr.Error(), providerID, credentialID)
				h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, tried, modelResolution, txResult, "unsupported_feature", failTrace, int(time.Since(startTime).Milliseconds()))
				markLogged()
				w.Header().Set("X-Gateway-Last-Kind", string(execErrTyped.LastKind))
				debugInfo := map[string]any{
					"stage":     "execution",
					"kind":      string(execErrTyped.LastKind),
					"tried":     execErrTyped.Tried,
					"retryable": false,
					"reason":    reason,
				}
				if preStreamPrepared {
					writePrewarmedStreamError(w, msg, "invalid_request_error", "unsupported_feature")
					return
				}
				writeErrorJSONWithKindProto(proto, w, http.StatusBadRequest, requestID, msg, "invalid_request_error", "unsupported_feature", string(execErrTyped.LastKind), debugInfo)
				return
			}

			if execErrTyped.LastKind == errorsx.KindModelDeprecated {
				// 2026-08-05 P0: upstream has permanently end-of-lifed the
				// model (HTTP 410 Gone + "end of life" body, or 404/422
				// "has been deprecated"). Distinct from model_not_found
				// (unknown/typo'd name): deprecation is authoritative and
				// permanent, so we surface HTTP 410 Gone with the upstream's
				// own EOL message rather than the misleading
				// "unsupported_feature" (the pre-fix behaviour, caused by 410
				// being lumped into the protocol-4xx → KindUnsupportedFeature
				// switch). The executor has already cooled the per-(credential,
				// model) binding for 30 days, so subsequent requests route
				// around it.
				reason := extractUpstreamReason(execErr)
				msg := i18n.T(r.Context(), i18n.MsgModelDeprecated, nil)
				if reason != "" {
					msg = msg + " Reason: " + reason
				}
				logCtx.SetOutboundModel(explicitOutbound)
				logCtx.failAndMark("model_deprecated", execErr.Error(), providerID, credentialID)
				h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, tried, modelResolution, txResult, "model_deprecated", failTrace, int(time.Since(startTime).Milliseconds()))
				markLogged()
				w.Header().Set("X-Gateway-Last-Kind", string(execErrTyped.LastKind))
				debugInfo := map[string]any{
					"stage":     "execution",
					"kind":      string(execErrTyped.LastKind),
					"tried":     execErrTyped.Tried,
					"retryable": false,
					"reason":    reason,
				}
				if preStreamPrepared {
					writePrewarmedStreamError(w, msg, "invalid_request_error", "model_deprecated")
					return
				}
				writeErrorJSONWithKindProto(proto, w, http.StatusGone, requestID, msg, "invalid_request_error", "model_deprecated", string(execErrTyped.LastKind), debugInfo)
				return
			}

			// = "model_not_found" but surface the REAL underlying
			// kind in error.kind + X-Gateway-Last-Kind header. Many
			// in-the-wild failures labeled model_not_found are
			// actually rate_limit / concurrent / unreachable, which
			// breaks downstream alerting that keys on the surface
			// code. The kind field is the SSoT for the real cause;
			// the legacy code is preserved for clients that pattern-
			// match on it.
			errCode = "model_not_found"
			realKind := mapExecuteErrorToKind(execErrTyped)
			logCtx.SetOutboundModel(explicitOutbound)
			// 2026-06-20: write the REAL underlying kind to
			// request_logs.error_kind (e.g. "rate_limit",
			// "concurrent", "upstream_down") instead of the
			// backward-compat "model_not_found". The HTTP
			// error.code stays "model_not_found" for old clients
			// (set below in writeErrorJSONWithKind); the new
			// error_kind column + error.kind JSON field carry the
			// precise cause. Operators can now filter on
			// error_kind='rate_limit' directly without parsing the
			// X-Gateway-Last-Kind header.
			logCtx.failAndMark(errorKindOrFallback(realKind),
				fmt.Sprintf("No available provider for model '%s'. All %d candidates failed.", clientModel, execErrTyped.Tried),
				providerID, credentialID)
			h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, tried, modelResolution, txResult, errCode, failTrace, int(time.Since(startTime).Milliseconds()))
			markLogged()
			// Step 6: surface real kind in response header so log
			// scrapers and debug dashboards can see it without
			// parsing JSON.
			if realKind != "" {
				w.Header().Set("X-Gateway-Last-Kind", realKind)
			}
			// Overload exhaustion is the one all-candidates-failed cause that
			// is genuinely worth retrying on a short clock, so tell the client
			// when instead of leaving it to guess. Prefer the upstream's own
			// hint; fall back to a conservative default.
			if execErrTyped.LastKind == errorsx.KindUpstreamOverloaded {
				retryAfter := overloadRetryAfterSeconds(execErr)
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			}
			if preStreamPrepared {
				// Carry the real upstream error kind so SDKs can act on
				// overload/rate_limit instead of chasing a "model not found"
				// that the model_not_found code never actually meant. The
				// code field is preserved for backwards compatibility — see
				// writePrewarmedStreamErrorWithKind for the wire format.
				//
				// 2026-08-08: also bump the prewarmed exhaustion counter so
				// /metrics surfaces this branch. Without the counter the
				// fact that a client got an empty 200 only surfaced inside
				// journal logs, which means an alert is set up days after
				// the incident, not in time to act.
				recordPrewarmedExhaustion(
					string(execErrTyped.LastKind),
					"model_not_found",
					strconv.Itoa(candidates[0].ProviderID),
					strconv.Itoa(candidates[0].CredentialID),
					clientModel,
				)
				writePrewarmedStreamErrorWithKind(w,
					fmt.Sprintf("No available provider for model '%s'. All %d candidates failed.", clientModel, execErrTyped.Tried),
					"server_error", "model_not_found", string(execErrTyped.LastKind))
				return
			}
			writeErrorJSONWithKindProto(proto, w, http.StatusServiceUnavailable, requestID,
				fmt.Sprintf("No available provider for model '%s'. All %d candidates failed.", clientModel, execErrTyped.Tried),
				"server_error", "model_not_found", realKind, map[string]any{
					"stage":     "execution",
					"kind":      string(execErrTyped.LastKind),
					"attempts":  execErrTyped.Attempts,
					"tried":     execErrTyped.Tried,
					"retryable": errorsx.IsRetryable(execErrTyped.LastKind),
				})
			return
		}
		logCtx.SetOutboundModel(explicitOutbound)
		// 2026-06-23 P0 audit: capture upstream response body so transient
		// errors have a diagnostic message in request_logs.response_preview.
		// Without this, "error_kind=transient" rows are diagnostically
		// useless — operators can't see why upstream failed.
		enrichedErrMsg := execErr.Error()
		if ue, ok := extractUpstreamError(execErr); ok {
			// 2026-06-30: 记录上游状态码到 request_logs (migration 320)
			if ue.StatusCode > 0 {
				logCtx.SetUpstreamStatus(ue.StatusCode)
			}
			if len(ue.Body) > 0 {
				logCtx.SetResponseBody(ue.Body)
				preview := string(ue.Body)
				if len(preview) > 320 {
					preview = preview[:320] + "..."
				}
				if ue.StatusCode > 0 {
					enrichedErrMsg = fmt.Sprintf("upstream HTTP %d: %s | kind=%s", ue.StatusCode, preview, ue.Kind)
				} else {
					enrichedErrMsg = fmt.Sprintf("network error: %s | body: %s", ue.Message, preview)
				}
				slog.Warn("upstream failed with body",
					"request_id", requestID,
					"credential_id", credentialID,
					"provider_id", providerID,
					"status_code", ue.StatusCode,
					"kind", string(ue.Kind),
					"body_preview", preview,
				)
			}
		}

		logCtx.failAndMark("provider_error", enrichedErrMsg, providerID, credentialID)
		h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, tried, modelResolution, txResult, errCode, failTrace, int(time.Since(startTime).Milliseconds()))
		markLogged()
		debugInfo := map[string]any{
			"stage":     "execution",
			"tried":     tried,
			"retryable": false,
		}
		if execErrTyped, ok := execErr.(*executors.ExecuteError); ok {
			debugInfo["kind"] = string(execErrTyped.LastKind)
			debugInfo["attempts"] = execErrTyped.Attempts
			debugInfo["retryable"] = errorsx.IsRetryable(execErrTyped.LastKind)
		}
		if preStreamPrepared {
			writePrewarmedStreamError(w, "upstream request failed", "server_error", "provider_error")
			return
		}
		writeErrorJSONWithDebugProto(proto, w, http.StatusBadGateway, requestID, i18n.T(r.Context(), i18n.MsgProviderError), "server_error", "provider_error", debugInfo)
		return
	}
	logCtx.markAttachmentsSent()
	if preStream != nil {
		preStream.stop()
		preStream = nil
	}

	auditBuilder.Success(true).
		Latency(time.Duration(result.LatencyMs) * time.Millisecond).
		Provider(result.Candidate.ProviderID).
		Credential(result.Candidate.CredentialID)
	if logCtx != nil {
		providerID := result.Candidate.ProviderID
		credentialID := result.Candidate.CredentialID
		logCtx.SetRoute(&providerID, &credentialID)
	}
	// Phase D (2026-06-22): use InboundBody (original client body) for audit
	// logging, not RequestBody (which may be protocol-converted for upstream).
	//
	// 2026-07-27 (bugfix: outbound_body NULL on delta-only / fresh-session
	// requests): outbound_body was previously only set when session
	// compression fired (handler.go:2244 guards on
	// scResult.CompressionStrategy != ""), so admin UI's v3 转发体 tab
	// showed an empty body for every request without compression. Use the
	// executor's actual upstream body (result.RequestBody) as the fallback
	// so request_logs.outbound_body always reflects what was forwarded to
	// upstream, regardless of whether compression fired.
	if logCtx != nil && result != nil {
		logCtx.ApplyQueueTimestampsFromResult(result)
	}
	h.emitTelemetry(auditBuilder.Build(), result, endUser, keyInfo, streamCapture, "chat", txResult, result.InboundBody, result.ResponseBody, logCtx)

	// ── Response Interceptor (2026-06-29, auto-control feature) ─────────
	// Call interceptor after successful execution but before final metrics.
	// This enables automatic handoff when context limits are reached and
	// goal-mode continuous execution.
	if h.responseInterceptor != nil && result != nil {
		// Calculate total message count from request body
		msgCount := extractMessageCount(bodyBytes)

		interceptReq := &ResponseInterceptRequest{
			SessionID: gwSessionID,
			RequestID: requestID,
			TenantID: func() string {
				if keyInfo != nil {
					return keyInfo.TenantID
				}
				return ""
			}(),
			ClientModel:  clientModel,
			ResponseBody: result.ResponseBody,
			TokensUsed:   extractTotalTokens(result.ResponseBody, streamCapture),
			ContextWindow: func() int {
				if len(candidates) > 0 && candidates[0].ContextWindow != nil {
					return *candidates[0].ContextWindow
				}
				return 0
			}(),
			MessageCount:   msgCount,
			FinishReason:   extractFinishReason(result.ResponseBody),
			IsStreaming:    isStream,
			FollowUpAction: strings.TrimSpace(r.Header.Get("X-Gw-Follow-Up-Action")),
		}

		if isStream {
			// For streaming, call InterceptStreamEnd.
			//
			// Reassemble the streamed text + finish_reason from the stream
			// capture so stream-end interceptors (goal completion detection /
			// audit) see the same full response body the non-stream path
			// gets. When no capture is available the fields stay empty and
			// the goal hook falls back to its legacy length-based behaviour.
			interceptMeta := &ResponseStreamMeta{
				SessionID:      gwSessionID,
				RequestID:      requestID,
				TenantID:       interceptReq.TenantID,
				ClientModel:    clientModel,
				ContextWindow:  interceptReq.ContextWindow,
				MessageCount:   msgCount,
				TokensUsed:     interceptReq.TokensUsed,
				ResponseBody:   reassembleStreamBody(streamCapture),
				FinishReason:   reassembleFinishReason(streamCapture),
				FollowUpAction: interceptReq.FollowUpAction,
			}

			if endResult, err := h.responseInterceptor.InterceptStreamEnd(r.Context(), interceptMeta); err != nil {
				slog.Warn("response_interceptor_stream_end_failed", "error", err, "session_id", gwSessionID)
			} else if endResult != nil && len(endResult.InjectFollowUp) > 0 {
				// Inject follow-up request asynchronously.
				// Carry the follow-up depth from the request context so
				// recursive follow-ups are bounded by MaxFollowUpDepth.
				// Detach from r.Context() (Background) since the response
				// is already complete and r.Context() may be canceled.
				followUpCtx := withFollowUpDepth(context.Background(), FollowUpDepthFromContext(r.Context()))
				parentAuthHeader := h.buildHandoffAuthHeader(r)
				go h.injectFollowUpRequest(followUpCtx, gwSessionID, endResult.InjectFollowUp, endResult.Action, parentAuthHeader)
			}
		} else {
			// For non-streaming, call InterceptNonStream
			if interceptResult, err := h.responseInterceptor.InterceptNonStream(r.Context(), interceptReq); err != nil {
				slog.Warn("response_interceptor_failed", "error", err, "session_id", gwSessionID)
			} else if interceptResult != nil {
				if interceptResult.ShouldBlock {
					slog.Info("response_interceptor_blocked", "session_id", gwSessionID, "action", interceptResult.Action)
					// Response was blocked, don't continue
					return
				}
				if len(interceptResult.InjectFollowUp) > 0 {
					// Inject follow-up request asynchronously.
					// Carry the follow-up depth from the request context.
					followUpCtx := withFollowUpDepth(context.Background(), FollowUpDepthFromContext(r.Context()))
					parentAuthHeader := h.buildHandoffAuthHeader(r)
					go h.injectFollowUpRequest(followUpCtx, gwSessionID, interceptResult.InjectFollowUp, interceptResult.Action, parentAuthHeader)
				}
				// Apply ModifiedBody (e.g. output-compliance redaction).
				//
				// NOTE (2026-07-09): for the historical non-stream path the bytes
				// are already written to the client inside executor.Execute, so
				// this rewrite takes effect for downstream telemetry, the request
				// log, the session-cache, and any buffered/pending-store path —
				// NOT a retroactive client rewrite. Stream-end redaction is
				// applied at write-time via the transform pipeline; this metadata
				// path ensures the persisted/observed body matches what policy
				// intended (so pii_stripped tagging + session_tags stay accurate).
				if len(interceptResult.ModifiedBody) > 0 && result != nil {
					result.ResponseBody = interceptResult.ModifiedBody
					if interceptResult.Metadata != nil {
						slog.Info("response_interceptor_modified_body",
							"session_id", gwSessionID, "action", interceptResult.Action)
					}
				}
			}
		}
	}

	// ── Request WAL: async update on execution success ─────────────
	if h.requestLogger != nil && result != nil {
		var pid, cid *int64
		if result.Candidate.ProviderID > 0 {
			p := int64(result.Candidate.ProviderID)
			pid = &p
		}
		if result.Candidate.CredentialID > 0 {
			c := int64(result.Candidate.CredentialID)
			cid = &c
		}

		// Extract token counts from streamCapture if available
		var promptTokens, completionTokens int
		if streamCapture != nil {
			m := streamCapture.SummaryAsMap()
			if pt, ok := m["prompt_tokens"].(int); ok {
				promptTokens = pt
			}
			if ct, ok := m["completion_tokens"].(int); ok {
				completionTokens = ct
			}
		}

		completedAt := time.Now()
		h.requestLogger.Update(&telemetry.LogUpdate{
			RequestID:            requestID,
			Stage:                telemetry.StageCompleted,
			Status:               telemetry.StatusSuccess,
			UpstreamProviderID:   pid,
			UpstreamCredentialID: cid,
			CompletionTokens:     completionTokens,
			PromptTokens:         promptTokens,
			CompletedAt:          completedAt,
		})
	}

	markLogged()
}

func successUpstreamStatusCode(result *executors.ExecuteResult) int {
	if result != nil && result.Response != nil && result.Response.StatusCode > 0 {
		return result.Response.StatusCode
	}
	return http.StatusOK
}

func failureAttribution(execErr error, candidates []provider.Candidate) (*int, *int) {
	var typed *executors.ExecuteError
	if errors.As(execErr, &typed) {
		for i := len(typed.Attempts) - 1; i >= 0; i-- {
			attempt := typed.Attempts[i]
			if attempt.ProviderID > 0 && attempt.CredentialID > 0 {
				return intPtr(attempt.ProviderID), intPtr(attempt.CredentialID)
			}
		}
	}
	if len(candidates) > 0 && candidates[0].ProviderID > 0 && candidates[0].CredentialID > 0 {
		return intPtr(candidates[0].ProviderID), intPtr(candidates[0].CredentialID)
	}
	return nil, nil
}

func (h *ChatHandler) emitTelemetry(evt audit.Event, result *executors.ExecuteResult, endUser string, keyInfo *authentication.KeyInfo, capture *audit.StreamCapture, requestMode string, txResult *transformation.TransformResult, requestBody []byte, responseBody []byte, logCtx *RequestLogContext) {
	if h.telemetryClient == nil || !h.telemetryClient.Enabled() {
		return
	}

	if logCtx != nil {
		requestBody = preferCapturedBody(requestBody, logCtx.Body)
		responseBody = preferCapturedBody(responseBody, logCtx.ResponseBody)
	}

	var apiKeyID *int
	var tenantID = "default"
	var applicationID *int
	keyPrefix, keyOwner, appCode := "", "", ""
	if keyInfo != nil {
		apiKeyID = &keyInfo.ID
		tenantID = keyInfo.TenantID
		applicationID = appID(keyInfo)
		keyPrefix, keyOwner, appCode = keyMetaFromKeyInfo(keyInfo)
	}

	dl := &telemetry.DecisionLogEntry{
		RequestID:          evt.RequestID,
		TenantID:           tenantID,
		APIKeyID:           apiKeyID,
		Model:              canonicalOrClient(evt.CanonicalName, evt.ClientModel),
		ChosenCredentialID: intPtr(result.Candidate.CredentialID),
		ChosenProviderID:   intPtr(result.Candidate.ProviderID),
		Tier:               intPtr(result.Candidate.Tier),
		CandidatesTried:    1,
		LatencyMs:          result.LatencyMs,
		Success:            true,
		ClientModel:        strPtr(evt.ClientModel),
		OutboundModel:      strPtr(evt.OutboundModel),
		ClientProfile:      strPtr(evt.ClientProfile),
		RequestMode:        strPtr(requestMode),
		IdentityHash:       strPtr(evt.IdentityHash),
		TransformRuleID:    strPtr(evt.TransformRule),
	}
	if evt.ResolutionPath != "" {
		dl.ResolutionPath = strPtr(evt.ResolutionPath)
	}
	if evt.CanonicalName != "" {
		dl.CanonicalModel = strPtr(evt.CanonicalName)
	}
	if result.Candidate.Protocol != "" {
		dl.EgressProtocol = strPtr(result.Candidate.Protocol)
	}
	if txResult != nil && txResult.OutboundModel != "" {
		dl.OutboundModel = strPtr(txResult.OutboundModel)
	}
	if result.Trace != nil {
		traceJSON, _ := json.Marshal(result.Trace)
		dl.DecisionTrace = traceJSON
	} else if evt.DecisionTrace != nil {
		traceJSON, _ := json.Marshal(evt.DecisionTrace)
		dl.DecisionTrace = traceJSON
	}
	if result.Candidate.RawModel != "" {
		dl.ResolvedRawModel = strPtr(result.Candidate.RawModel)
		dl.ResolutionRawModels = []string{result.Candidate.RawModel}
	}

	var requestBodyText *string
	if len(requestBody) > 0 {
		v := string(redactAttachmentBodyIfEnabled(requestBody))
		requestBodyText = &v
	} else if result != nil && strings.Contains(strings.ToLower(result.Candidate.RawModel), "minimax") {
		slog.Warn("emitTelemetry: requestBody empty for known model",
			"request_id", evt.RequestID,
			"model", result.Candidate.RawModel,
			"request_mode", requestMode)
	}
	var responseBodyText *string
	if len(responseBody) > 0 {
		v := string(responseBody)
		// For streaming responses, the last SSE chunk sent to the client (and captured
		// in result.ResponseBody) often does NOT include the usage block. Merge the
		// stream-captured usage values into the response_body JSON so the persisted
		// row contains a complete `usage` block for downstream auditors/queries.
		if capture != nil {
			m := capture.SummaryAsMap()
			var pt, ct, crt, cwt int
			if val, ok := m["prompt_tokens"].(int); ok {
				pt = val
			}
			if val, ok := m["completion_tokens"].(int); ok {
				ct = val
			}
			if val, ok := m["cache_read_tokens"].(int); ok {
				crt = val
			}
			if val, ok := m["cache_write_tokens"].(int); ok {
				cwt = val
			}
			if pt > 0 || ct > 0 {
				v = string(injectUsageIntoResponseBody([]byte(v), pt, ct, crt, cwt))
			}
		} else if len(responseBody) > 0 {
			// Non-streaming: ensure we always pull whatever usage is in the body
			// (this is the primary path; capture==nil branch below is a fallback).
			ept, ect, ecrt, ecwt := extractTokensFromResponseBody(responseBody)
			if ept > 0 || ect > 0 {
				v = string(injectUsageIntoResponseBody([]byte(v), ept, ect, ecrt, ecwt))
			}
		}
		responseBodyText = &v
	} else if capture != nil {
		m := capture.SummaryAsMap()
		var textContent string
		if v, ok := m["stream_text_content"].(string); ok && v != "" {
			textContent = v
		}
		// 2026-06-25 T-NEW-1: structured tool_calls from the IR layer (see
		// audit/stream.go mergeToolCall). SummaryAsMap emits them under
		// the "tool_calls" key. Cast to []map[string]any so we can rewrite
		// each entry to drop the streaming-only "index" field.
		var toolCallsFromStream []map[string]any
		if v, ok := m["tool_calls"].([]map[string]any); ok && len(v) > 0 {
			toolCallsFromStream = v
		}
		// audit/stream.go ObserveChunk still emits a legacy "[Tool Call:
		// <name>]\n<arguments>" text rendering into stream_text_content
		// (kept for backward compatibility with consumers that read it
		// as a free-text preview). When we are also embedding structured
		// tool_calls in message.tool_calls, that legacy rendering would
		// duplicate the data inside `content` and break clients that
		// parse `content` as plain assistant text. Strip it.
		if toolCallsFromStream != nil {
			textContent = stripLegacyToolCallText(textContent)
		}
		if textContent != "" || toolCallsFromStream != nil {
			var pt, ct int
			if v, ok := m["prompt_tokens"].(int); ok {
				pt = v
			}
			if v, ok := m["completion_tokens"].(int); ok {
				ct = v
			}
			// 2026-06-25 T-NEW-1: When streaming, the IR layer (audit/stream.go
			// mergeToolCall) accumulates structured tool_calls into
			// sc.ToolCalls. Persist them into the synthetic response_body as
			// `message.tool_calls` so downstream admin UI / API consumers can
			// read tool_calls directly from response_body (instead of only
			// from the dedicated request_logs.tool_calls JSONB column).
			//
			// We strip the streaming-only `index` key from each entry: OpenAI
			// final-response tool_calls do NOT carry `index` (only streaming
			// deltas do), and including it confuses clients that strictly
			// validate the schema. We also flip finish_reason to "tool_calls"
			// when at least one tool call was emitted, matching the upstream
			// OpenAI Chat Completions contract.
			finishReason := "stop"
			var cleanedToolCalls []map[string]any
			if toolCallsFromStream != nil {
				finishReason = "tool_calls"
				cleanedToolCalls = make([]map[string]any, 0, len(toolCallsFromStream))
				for _, tc := range toolCallsFromStream {
					entry := map[string]any{}
					for k, v := range tc {
						// Skip streaming-only fields
						if k == "index" {
							continue
						}
						entry[k] = v
					}
					cleanedToolCalls = append(cleanedToolCalls, entry)
				}
			}
			message := map[string]any{"role": "assistant", "content": textContent}
			if len(cleanedToolCalls) > 0 {
				message["tool_calls"] = cleanedToolCalls
			}
			pseudoBody := map[string]any{
				"choices": []map[string]any{
					{"message": message, "finish_reason": finishReason},
				},
			}
			if pt > 0 || ct > 0 {
				pseudoBody["usage"] = map[string]any{"prompt_tokens": pt, "completion_tokens": ct, "total_tokens": pt + ct}
			}
			if b, err := json.Marshal(pseudoBody); err == nil {
				v := string(b)
				responseBodyText = &v
			}
		} else if previewStr, ok := m["response_preview"].(string); ok && previewStr != "" {
			// Fallback: textContent is empty (e.g. function-calling responses
			// that only carry `delta.tool_calls` and no `delta.content`, or
			// request_logs that are stored for audit even when no parsed text
			// was collected). Persist the raw SSE preview as the body so the
			// row is non-empty and downstream auditors/queries can still
			// inspect the wire format.
			responseBodyText = strPtr(previewStr)
		}
	}
	requestPreviewText := requestPreview(redactAttachmentBodyIfEnabled(requestBody))
	transformSummaryText := transformSummary(txResult, evt.OutboundModel)
	responsePreviewText := responsePreview(responseBody)
	var requestPreviewPtr *string
	if requestPreviewText != "" {
		requestPreviewPtr = strPtr(requestPreviewText)
	}
	var transformSummaryPtr *string
	if transformSummaryText != "" {
		transformSummaryPtr = strPtr(transformSummaryText)
	}
	var responsePreviewPtr *string
	if responsePreviewText != "" {
		responsePreviewPtr = strPtr(responsePreviewText)
	}

	// 2026-06-30 PR-5: nil-safe accessor — logCtx may be nil for
	// domains/streaming/messages.go and responses.go paths.
	var clientReqIDPtr *string
	if logCtx != nil && logCtx.ClientRequestID != "" {
		s := logCtx.ClientRequestID
		clientReqIDPtr = &s
	}

	loggedOutbound := outboundModelForLog(evt.ClientModel, evt.OutboundModel, result.Candidate.RawModel)
	eventAt := time.Now().UTC()
	if logCtx != nil {
		eventAt = logCtx.StartTime.Add(time.Duration(result.LatencyMs) * time.Millisecond)
	}

	reqLog := &telemetry.RequestLogEntry{
		RequestID:          evt.RequestID,
		EventAt:            &eventAt,
		TenantID:           tenantID,
		ApplicationID:      applicationID,
		APIKeyID:           apiKeyID,
		APIKeyPrefix:       strPtr(keyPrefix),
		APIKeyOwnerUser:    strPtr(keyOwner),
		ApplicationCode:    strPtr(appCode),
		EndUserID:          strPtr(endUser),
		ClientModel:        strPtr(evt.ClientModel),
		OutboundModel:      strPtr(loggedOutbound),
		CredentialID:       intPtr(result.Candidate.CredentialID),
		ProviderID:         intPtr(result.Candidate.ProviderID),
		UpstreamStatusCode: intPtr(successUpstreamStatusCode(result)),

		// 2026-07-27: 标准模型名 (canonical_name),见 migration 458。
		CanonicalModel: strPtr(evt.CanonicalName),
		ClientProfile:  strPtr(evt.ClientProfile),
		RequestMode:    strPtr(requestMode),
		LatencyMs:      intPtr(result.LatencyMs),
		Success:        true,
		RequestStatus:  strPtr(telemetry.RequestStatusSuccess),
		// 2026-06-20: explicitly clear ErrorKind so any stale
		// error_kind from a prior failed UPDATE attempt for the
		// same request_id is wiped. The UPSERT also handles this
		// via CASE WHEN success=TRUE, but setting it here makes
		// the intent obvious and removes a class of cross-request
		// pollution where an old failure tag leaks into a fresh
		// success row.
		ErrorKind:        strPtr(""),
		IdentityHash:     strPtr(evt.IdentityHash),
		RequestPreview:   requestPreviewPtr,
		TransformSummary: transformSummaryPtr,
		ResponsePreview:  responsePreviewPtr,
		RequestBody:      requestBodyText,
		ResponseBody:     responseBodyText,
		// 2026-07-01 P0 fix: stream_chunks_sent / stream_chunk_errors are
		// NOT NULL columns (migration 320). Initialize from logCtx (which
		// increments via IncrementStreamChunksSent in the streaming
		// callbacks) and fall back to 0 for non-streaming paths. Without
		// these defaults the INSERT failed with SQLSTATE 23502 and
		// request_logs_2026_07 stopped accepting new rows on 184.
		StreamChunksSent:  intPtr(streamChunksSentFromLogCtx(logCtx)),
		StreamChunkErrors: intPtr(streamChunkErrorsFromLogCtx(logCtx)),
		// Round 47 compression v7 T-NEW-3: write the compression event
		// captured by the executor's 4xx recovery (see
		// executors.context_summarize.handleContextLengthRecovery) into
		// request_logs.compression_*. Operators can then SQL-trace the
		// parent-child chain via parent_request_id.
		//
		// We only set these when the executor actually rewrote the body;
		// nil pointers → NULL in PG → the existing partial index on
		// parent_request_id stays cheap.
		CompressionReason:   result.CompressionReason,
		CompressionStrategy: result.CompressionStrategy,
		CompressionMeta:     result.CompressionMeta,
		// Compression-rewrite parent chain only; the header-supplied auto
		// loopback parent is applied below via applyParentCorrelationFields.
		ParentRequestID: result.ParentRequestID,
		// V3.1 dispatch queue timestamps (migration 491)
		T0ArrivedAt:       result.T0ArrivedAt,
		T1TotalEnqueuedAt: result.T1TotalEnqueuedAt,
		T2TotalDequeuedAt: result.T2TotalDequeuedAt,
		T3ModelEnqueuedAt: result.T3ModelEnqueuedAt,
		T4ModelDequeuedAt: result.T4ModelDequeuedAt,
		T5CredEnqueuedAt:  result.T5CredEnqueuedAt,
		T6CredDequeuedAt:  result.T6CredDequeuedAt,
		T7ForwardStartAt:  result.T7ForwardStartAt,
		T8ResponseStartAt: result.T8ResponseStartAt,
		T9ResponseEndAt:   result.T9ResponseEndAt,
		// 2026-06-26: persist the client-supplied X-Request-Id for debug
		// alongside the server-generated RequestID. Distinguishes
		// legitimate client retries (same client_request_id, distinct
		// request_id) from genuinely fresh requests.
		//
		// 2026-06-30 PR-5: nil-safe — logCtx may be nil from
		// domains/streaming/messages.go and responses.go paths; see
		// clientReqIDPtr setup above. Audit P0-6.
		ClientRequestID: clientReqIDPtr,
		Attachments:     attachmentsFromLogContext(logCtx),
		// 2026-07-25: 请求/响应体大小（用于 Redis 实时统计）
		RequestBytes:  requestBytesFromLogCtx(logCtx),
		ResponseBytes: intPtr(len(responseBody)),
	}
	// 2026-08-05 P0 fix: populate GwSessionID/GwTaskID on the success-path
	// reqLog. The struct literal above (line ~3840) never set these fields,
	// so reqLog.GwSessionID was always nil — which silently broke the
	// auto-title trigger (guard: GwSessionID != nil && *GwSessionID != "").
	// Recover the id from logCtx (Request header / Session / Provisional).
	if reqLog.GwSessionID == nil && logCtx != nil {
		var sess *session.Session
		if logCtx.Session != nil {
			sess = logCtx.Session
		}
		gwSID, gwTID := gwSessionTaskFromRequest(logCtx.Request, sess)
		if gwSID == "" && logCtx.ProvisionalSessionID != "" {
			gwSID = logCtx.ProvisionalSessionID
		}
		if gwSID != "" {
			reqLog.GwSessionID = strPtr(gwSID)
		}
		if gwTID != "" {
			reqLog.GwTaskID = strPtr(gwTID)
		}
	}
	// 2026-08-15 (OBS-DV2 #4)：最终 upsert 此前只带 result.ParentRequestID
	// （压缩重写父链），入口侧从 X-Gw-Parent-Request-Id 读入 logCtx 的
	// auto loopback 父子关联被丢弃，ON CONFLICT DO UPDATE 又用 NULL 覆盖了
	// recordInitialRequestLog 已写入的值 —— auto-title/auto-summary 子请求的
	// parent_request_id / origin_actor 落库恒为 NULL，live stream 的
	// child_request 帧因此从不发射。与初始写入保持同一套关联字段应用。
	applyParentCorrelationFields(reqLog, logCtx)

	// v3: if v7 compression_strategy is empty but a session compressor strategy
	// exists, prefer the session compressor value so the row is queryable.
	// (v7 and v3 strategies are mutually exclusive in a single request.)

	if capture != nil {
		m := capture.SummaryAsMap()
		// Only set pointers when the captured value is non-zero. Some providers
		// (e.g. minimax) include `"usage": null` in every SSE chunk, so the
		// stream summary may have the keys present with value 0. Setting a
		// non-nil *int to 0 would otherwise suppress the estimator fallback
		// below (because the nil-check would be false).
		if v, ok := m["prompt_tokens"].(int); ok && v > 0 {
			reqLog.PromptTokens = &v
		}
		if v, ok := m["completion_tokens"].(int); ok && v > 0 {
			reqLog.CompletionTokens = &v
		}
		if v, ok := m["cache_read_tokens"].(int); ok && v > 0 {
			reqLog.CacheReadTokens = &v
		}
		if v, ok := m["cache_write_tokens"].(int); ok && v > 0 {
			reqLog.CacheWriteTokens = &v
		}
		if v, ok := m["stream_first_chunk_ms"].(int); ok {
			reqLog.StreamFirstChunkMs = &v
		}
		if v, ok := m["stream_chunk_count"].(int); ok {
			reqLog.StreamChunkCount = &v
		}
		if v, ok := m["stream_chunks_sent"].(int); ok {
			reqLog.StreamChunksSent = &v
		}
		if v, ok := m["response_checksum"].(string); ok {
			reqLog.ResponseChecksum = &v
		}
		if v, ok := m["stream_done_received"].(bool); ok {
			reqLog.StreamDoneReceived = &v
		}
		if v, ok := m["stream_interrupted"].(bool); ok {
			reqLog.StreamInterrupted = &v
			if v {
				isErr, detailCode := classifyStreamInterruption(m)
				if isErr {
					reqLog.Success = false
					reqLog.RequestStatus = strPtr(telemetry.RequestStatusFailure)
					// 2026-07-27: Distinguish the precise kind so the
					// operator can SQL-filter by failure mode. The
					// previous "stream_error" value conflated stream
					// timeouts, concurrent-overload fallbacks, and
					// generic upstream read errors.
					reqLog.ErrorKind = strPtr(streamErrorKindForDetailCode(nil, detailCode))
					reqLog.FailureStage = strPtr("upstream")
				}
				if detailCode != "" {
					reqLog.FailureDetailCode = strPtr(detailCode)
				}
			}
		}

		// 2026-06-23: Detect empty responses
		// An empty response is one that:
		//   1. Has very few chunks (<= 3)
		//   2. Has zero completion tokens
		//   3. Has no content preview
		//   4. Has no upstream finish_reason
		// This pattern indicates the upstream returned no actual content,
		// despite sending [DONE]. Mark as failure to prevent billing
		// and alert monitoring.
		//
		// Provider 18 (NVIDIA NIM) has ~13% empty response rate across credentials.
		if reqLog.Success { // Only check if currently marked as success
			isEmpty := detectEmptyStreamResponse(m, reqLog)
			if isEmpty {
				reqLog.Success = false
				reqLog.RequestStatus = strPtr(telemetry.RequestStatusFailure)
				reqLog.ErrorKind = strPtr("empty_response")
				reqLog.FailureStage = strPtr("upstream_empty_response")
				reqLog.FailureDetailCode = strPtr("zero_tokens_few_chunks")
			}
		}

		// 2026-08-04: Detect upstream silent context loss.
		// A relay/upstream can accept a large request body (HTTP 200, clean
		// [DONE], stop_reason=end_turn) yet report prompt_tokens that are a
		// tiny fraction of what the body implies — i.e. it silently dropped
		// most of the context before invoking the model. The model then
		// answers the stripped-down prompt and returns a near-useless short
		// reply. Observed on apiclaude.cc (request e8bf0d5fc726: 919KB body
		// → prompt_tokens=337 → 21-token reply; same-session siblings
		// reported 256K–305K). From the user's perspective this is an error.
		// Runs only when still marked success (after the empty-response
		// check) so the two detectors don't clobber each other.
		if reqLog.Success {
			if detectUpstreamContextLoss(m, reqLog) {
				reqLog.Success = false
				reqLog.RequestStatus = strPtr(telemetry.RequestStatusFailure)
				reqLog.ErrorKind = strPtr(string(errorsx.KindUpstreamContextLoss))
				reqLog.FailureStage = strPtr(string(errorsx.KindUpstreamContextLoss))
				reqLog.FailureDetailCode = strPtr("prompt_tokens_body_mismatch")
				reqLog.QualityFlags = append(reqLog.QualityFlags, QualityFlagUpstreamContextLoss)
			}
		}

		// 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code.
		// audit/audit.go::SummaryAsMap now publishes the upstream finish_reason
		// under the new "upstream_finish_reason" key (for BOTH success and
		// failure rows). It only republishes the value as
		// "failure_detail_code" when the value is a known interruption code
		// (e.g. eof_without_done, stream_timeout). The block below mirrors
		// that discipline into the request_log row:
		//
		//   1. Read upstream_finish_reason → UpstreamFinishReason column.
		//   2. Fall back to m["failure_detail_code"] for UpstreamFinishReason
		//      ONLY if it wasn't set above (legacy pre-018 captures).
		//   3. Do NOT touch FailureDetailCode here — that is already set by
		//      the stream_interrupted branch above for real failures. For
		//      successful streams we leave it NULL.
		if v, ok := m["upstream_finish_reason"].(string); ok && v != "" {
			reqLog.UpstreamFinishReason = strPtr(v)
		}
		if reqLog.UpstreamFinishReason == nil {
			if v, ok := m["failure_detail_code"].(string); ok && v != "" {
				// Legacy pre-018 capture path — promotion of the old
				// "failure_detail_code == finish_reason" usage to the new
				// column. Keep the value in BOTH columns for now so the
				// admin UI does not regress before the next deploy
				// rewires the relay-side capture.
				reqLog.UpstreamFinishReason = strPtr(v)
			}
		}
		if v, ok := m["response_preview"].(string); ok && v != "" && reqLog.ResponsePreview == nil {
			reqLog.ResponsePreview = strPtr(v)
		}
		// 2026-06-19 quality fix mode: pull stream-collected quality
		// signals out of the capture summary. The stream reader
		// already pushed the running flag list into the capture;
		// the audit summary serialises it under "quality_flags".
		if v, ok := m["quality_flags"].([]string); ok && len(v) > 0 {
			reqLog.QualityFlags = v
		}
		if v, ok := m["quality_fix_actions"].(string); ok && v != "" {
			reqLog.QualityFixActions = []byte(v)
		}
		if v, ok := m["quality_score"].(float64); ok {
			reqLog.QualityScore = &v
		}
		// 2026-06-23: structured tool_calls from streaming (042_tool_calls_column.sql).
		// The audit.StreamCapture.ToolCalls array is marshaled into SummaryAsMap
		// as "tool_calls" ([]map[string]any). Convert it to json.RawMessage
		// for the RequestLogEntry.
		if v, ok := m["tool_calls"].([]map[string]any); ok && len(v) > 0 {
			if b, err := json.Marshal(v); err == nil {
				reqLog.ToolCalls = b
			}
		}
	}

	// 2026-06-19 quality fix mode (017_quality_fix_mode.sql): propagate
	// the post-processed quality signals into the request_log row.
	// The non-stream path stores the result directly on
	// ExecuteResult; the stream path already pushed them into the
	// capture above (m["quality_flags"] etc.). For non-stream we
	// simply read the fields that the executor set.
	if len(result.QualityFlags) > 0 {
		reqLog.QualityFlags = result.QualityFlags
	}
	if len(result.QualityFixActions) > 0 {
		reqLog.QualityFixActions = result.QualityFixActions
	}
	if result.QualityScore != nil {
		reqLog.QualityScore = result.QualityScore
	}

	// 2026-06-30: Extract tokens from response body for BOTH streaming and non-streaming.
	// Previously this was inside the `if capture != nil` block, which meant non-streaming
	// requests never had their completion_tokens/cache tokens extracted from the response.
	// This caused request_logs to show NULL for completion_tokens and cache_*_tokens even
	// when the upstream response contained a complete usage block.
	if len(result.ResponseBody) > 0 {
		pt, ct, crt, cwt := extractTokensFromResponseBody(result.ResponseBody)
		if pt > 0 || ct > 0 {
			// Only overwrite if not already set from streaming capture
			if reqLog.PromptTokens == nil || *reqLog.PromptTokens == 0 {
				reqLog.PromptTokens = &pt
			}
			if reqLog.CompletionTokens == nil || *reqLog.CompletionTokens == 0 {
				reqLog.CompletionTokens = &ct
			}
			if crt > 0 && (reqLog.CacheReadTokens == nil || *reqLog.CacheReadTokens == 0) {
				reqLog.CacheReadTokens = &crt
			}
			if cwt > 0 && (reqLog.CacheWriteTokens == nil || *reqLog.CacheWriteTokens == 0) {
				reqLog.CacheWriteTokens = &cwt
			}
		}
	}

	// Fallback: if upstream did not return a usage block (e.g. minimax, certain
	// volcengine pass-through responses), estimate tokens locally from the
	// request/response text and mark the row so the UI can distinguish the
	// estimated value from a real LLM-reported count.
	// Check both nil AND zero: providers like minimax emit `"usage": null` in
	// every SSE chunk, which results in stream-captured pointers to 0 that
	// would otherwise suppress this fallback.
	promptZero := reqLog.PromptTokens == nil || *reqLog.PromptTokens == 0
	completionZero := reqLog.CompletionTokens == nil || *reqLog.CompletionTokens == 0
	if promptZero && completionZero {
		estPrompt := estimatePromptTokens(result.RequestBody)
		estCompletion := estimateCompletionTokens(result.ResponseBody)
		if estPrompt > 0 || estCompletion > 0 {
			reqLog.PromptTokens = &estPrompt
			reqLog.CompletionTokens = &estCompletion
			reqLog.UsageSource = strPtr(UsageSourceEstimated)
		}

		// Record format anomaly if estimation failed or returned zero completion tokens
		// despite having response content (helps detect provider format changes)
		if h.anomalyRecorder != nil && reqLog.Success {
			providerID := result.Candidate.ProviderID
			providerCode := result.Candidate.CatalogCode
			clientModel := evt.ClientModel
			outboundModel := evt.OutboundModel

			// Detect anomaly type
			var anomalyType AnomalyType
			var severity Severity
			if estPrompt == 0 && estCompletion == 0 && len(result.ResponseBody) > 0 {
				anomalyType = AnomalyExtractionFailed
				severity = SeverityHigh
			} else if estCompletion == 0 && len(result.ResponseBody) > 100 {
				anomalyType = AnomalyZeroCompletion
				severity = SeverityMedium
			} else {
				anomalyType = AnomalyMissingUsage
				severity = SeverityLow
			}

			// Sample before recording (avoid flooding table)
			if ShouldRecordAnomaly(anomalyType, providerCode) {
				contentSize := len(result.ResponseBody)
				structure := AnalyzeResponseStructure(result.ResponseBody)
				sample := TruncateForSample(string(result.ResponseBody), 1000)
				usageSource := UsageSourceEstimated
				var providerCodePtr *string
				if providerCode != "" {
					providerCodePtr = &providerCode
				}
				var tenantCodePtr *string
				if tenantID != "" {
					tenantCodePtr = &tenantID
				}

				go func() {
					// Record asynchronously to avoid blocking request completion
					recordCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()

					_ = h.anomalyRecorder.RecordAnomaly(recordCtx, AnomalyRecord{
						RequestID:      evt.RequestID,
						ProviderID:     &providerID,
						ProviderCode:   providerCodePtr,
						ClientModel:    &clientModel,
						OutboundModel:  &outboundModel,
						AnomalyType:    anomalyType,
						Severity:       severity,
						UsageSource:    &usageSource,
						ExpectedTokens: &estCompletion,
						ActualTokens:   nil,
						ContentSize:    &contentSize,
						Structure:      structure,
						ResponseSample: &sample,
						TenantID:       tenantCodePtr,
					})
				}()
			}
		}

	} else if reqLog.UsageSource == nil {
		reqLog.UsageSource = strPtr(UsageSourceLLM)
	}

	// CO-2 (2026-08-15): 真实 usage 到达时，异步修正本请求此前可能已落库的
	// estimated 行（同 request_id 的早写路径，如重试/断连补写），并回填
	// format_anomalies 的 actual_tokens。行不是 estimated 时 UPDATE 不生效
	// （幂等，不碰 llm/corrected 行）。复制值而非指针，避免与后续 reqLog
	// 变更产生数据竞争。
	if reqLog.UsageSource != nil && *reqLog.UsageSource == UsageSourceLLM &&
		h.telemetryClient != nil &&
		(reqLog.PromptTokens != nil || reqLog.CompletionTokens != nil) {
		backfillEntry := &telemetry.RequestLogEntry{RequestID: reqLog.RequestID}
		if reqLog.PromptTokens != nil {
			v := *reqLog.PromptTokens
			backfillEntry.PromptTokens = &v
		}
		if reqLog.CompletionTokens != nil {
			v := *reqLog.CompletionTokens
			backfillEntry.CompletionTokens = &v
		}
		if reqLog.CacheReadTokens != nil {
			v := *reqLog.CacheReadTokens
			backfillEntry.CacheReadTokens = &v
		}
		if reqLog.CacheWriteTokens != nil {
			v := *reqLog.CacheWriteTokens
			backfillEntry.CacheWriteTokens = &v
		}
		backfillEstimatedUsage(h.telemetryClient, h.anomalyRecorder, backfillEntry)
	}

	if reqLog.PromptTokens != nil || reqLog.CompletionTokens != nil {
		cost := CalcCost(CostInput{
			PromptTokens:     floatPtrFromInt(reqLog.PromptTokens),
			CompletionTokens: floatPtrFromInt(reqLog.CompletionTokens),
			CacheReadTokens:  floatPtrFromInt(reqLog.CacheReadTokens),
			CacheWriteTokens: floatPtrFromInt(reqLog.CacheWriteTokens),
			PriceIn:          result.Candidate.PriceInPer1M,
			PriceOut:         result.Candidate.PriceOutPer1M,
			CacheReadPrice:   result.Candidate.CacheReadPricePer1M,
			CacheWritePrice:  result.Candidate.CacheWritePricePer1M,
		})
		reqLog.CostUSD = cost
		// For CNY-priced providers (cost_usd is intentionally nil) record the
		// native-currency value in cost_display so /request-logs can show it.
		if cost == nil && result.Candidate.Currency != "" && result.Candidate.Currency != "USD" {
			cnyCost := CalcCost(CostInput{
				PromptTokens:     floatPtrFromInt(reqLog.PromptTokens),
				CompletionTokens: floatPtrFromInt(reqLog.CompletionTokens),
				CacheReadTokens:  floatPtrFromInt(reqLog.CacheReadTokens),
				CacheWriteTokens: floatPtrFromInt(reqLog.CacheWriteTokens),
				PriceIn:          result.Candidate.PriceInPer1M,
				PriceOut:         result.Candidate.PriceOutPer1M,
				CacheReadPrice:   result.Candidate.CacheReadPricePer1M,
				CacheWritePrice:  result.Candidate.CacheWritePricePer1M,
			})
			reqLog.CostDisplay = cnyCost
			curr := result.Candidate.Currency
			reqLog.CostCurrency = &curr
		}
	}

	if h.maasSvc != nil && keyInfo != nil && keyInfo.TenantID != "" && keyInfo.TenantID != "default" {
		pt, ct, crt, cwt := 0, 0, 0, 0
		streamChunkCount := 0
		if reqLog.PromptTokens != nil {
			pt = *reqLog.PromptTokens
		}
		if reqLog.CompletionTokens != nil {
			ct = *reqLog.CompletionTokens
		}
		if reqLog.CacheReadTokens != nil {
			crt = *reqLog.CacheReadTokens
		}
		if reqLog.CacheWriteTokens != nil {
			cwt = *reqLog.CacheWriteTokens
		}
		if reqLog.StreamChunkCount != nil {
			streamChunkCount = *reqLog.StreamChunkCount
		}
		if shouldChargeUsage(reqLog.Success, reqLog.FailureStage, reqLog.ErrorKind, pt, ct, crt, cwt, streamChunkCount) {
			canonical := evt.CanonicalName
			if canonical == "" {
				canonical = evt.ClientModel
			}
			chargeCtx, chargeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			charged, err := h.maasSvc.ChargeRequest(chargeCtx, keyInfo.TenantID, evt.RequestID, canonical, pt, ct, crt, cwt)
			chargeCancel()
			if err == nil && charged > 0 {
				reqLog.CreditsCharged = &charged
			} else if err != nil {
				slog.Warn("maas charge failed", "request_id", evt.RequestID, "tenant_id", keyInfo.TenantID, "error", err)
			}
		}
	}

	dl.PromptTokens = reqLog.PromptTokens
	dl.CompletionTokens = reqLog.CompletionTokens
	dl.CostUSD = reqLog.CostUSD
	if len(requestBody) > 0 {
		rb := len(requestBody)
		dl.RequestBytes = &rb
	}
	if len(responseBody) > 0 {
		rsb := len(responseBody)
		dl.ResponseBytes = &rsb
	}
	if result.Trace != nil && len(result.Trace.PlannedCandidates) > 0 {
		dl.CandidatesTried = len(result.Trace.PlannedCandidates)
	}
	h.telemetryClient.EmitDecisionLog(dl)

	applyKeyInfoToRequestLog(reqLog, keyInfo)
	// 2026-07-27: 客户端感知字段 (agent_name/agent_type/client_protocol/virtual_client_id)
	// 通过 meta 透传到 reqLog,主表 request_logs_hot 也能 GROUP BY 统计。
	enrichRequestLogFromMeta(reqLog, keyInfo, &logCtx.meta)
	// v3: merge session compressor outbound fields into the log entry.
	applySessionCompressorFields(reqLog, logCtx)
	// 2026-08-05: propagate the X-Gw-Submit-Mode header to the v2 mirror so the
	// SubmitModeDetector's P0 (authoritative) path can emit a true "delta"
	// instead of an LCS-inferred verdict. The header never survives to the hook
	// otherwise — the hook only sees the telemetry entry, not the request.
	applySubmitModeHeader(reqLog, logCtx)

	// 2026-07-19: 填充路由尝试追踪数据到 telemetry
	// 2026-07-20: Try result.RoutingTracker first (populated by the executor),
	// then fall back to logCtx.RoutingTracker (set by no_candidate path etc.).
	tracker := trackerFromResultOrLogCtx(result, logCtx)
	if tracker != nil {
		if jsonBytes, err := tracker.ToJSONBytes(); err == nil && jsonBytes != nil {
			reqLog.RoutingAttempts = jsonBytes
		}
		if summary := tracker.Summary(); summary != "" {
			reqLog.RoutingSummary = &summary
		}
	}

	h.telemetryClient.EmitRequestLogUpdate(reqLog)
	if h.requestLogHook != nil {
		h.requestLogHook(reqLog)
	}

	// 2026-07-15: clientprofile 画像聚合（best-effort，失败仅日志）。
	if h.profileEmitter != nil && logCtx != nil {
		emitProfileFromLogCtx(h.profileEmitter, logCtx, reqLog, true)
	}

	// v2.1: emit implicit feedback signal for the auto-route tuning loop.
	// Best-effort async write via the dedicated tuning writer; never blocks
	// the request path on DB latency.
	if reqLog.IsAutoRequest != nil && *reqLog.IsAutoRequest && reqLog.TaskType != nil {
		latencyMs := 0
		if reqLog.LatencyMs != nil {
			latencyMs = *reqLog.LatencyMs
		}
		h.emitTuningSignal(reqLog, reqLog.Success, latencyMs)
	}

	// v2.2 (2026-06-22): auto-generate session title after first successful request.
	// v2.3 (2026-08-05): pass requestBody + requestPreview in-memory; also fixed
	// GwSessionID being nil on the success-path reqLog (see assignment above).
	// v2.4 (2026-08-05): exclude gateway-internal auto requests (logCtx.IsAutoRequest,
	// set from the X-Gw-Is-Auto header or model="auto") so the auto title generator
	// does not re-trigger on its own title requests (chain self-triggering).
	// v2.5 (2026-08-06): pass evt.RequestID as parentRequestID so the loopback
	// LLM call's request_logs_hot.parent_request_id is filled — operators can
	// SQL JOIN child ↔ parent to find "08aa2a8a → 3a03f7db".
	// Fire-and-forget async call; never blocks the request path.
	if h.autoTitleGenerator != nil && reqLog.Success && reqLog.GwSessionID != nil && *reqLog.GwSessionID != "" && !shouldSkipAutoTitleGeneration(logCtx) {
		preview := ""
		if reqLog.RequestPreview != nil {
			preview = *reqLog.RequestPreview
		}
		body := ""
		if reqLog.RequestBody != nil {
			body = *reqLog.RequestBody
		}
		taskID := ""
		if reqLog.GwTaskID != nil {
			taskID = *reqLog.GwTaskID
		}
		h.autoTitleGenerator.MaybeGenerateTitle(*reqLog.GwSessionID, tenantID, taskID, body, preview, evt.RequestID, reqLog.RequestID)
	}

	// 2026-08-06: auto-summary — fires after auto-title on the same
	// success path. Internally gated by an incremental-rolling N-turn
	// threshold, a per-tenant rate limit, and a worker-pool semaphore
	// (see admin/auto_summary_generator.go). Chain-prevention via
	// shouldSkipAutoSummaryGeneration keeps summary requests from
	// re-triggering themselves — the loopback sets X-Gw-Is-Auto: true
	// and the next pass sees logCtx.IsAutoRequest.
	if h.autoSummaryGenerator != nil && reqLog.Success && reqLog.GwSessionID != nil && *reqLog.GwSessionID != "" && !shouldSkipAutoSummaryGeneration(logCtx) {
		preview := ""
		if reqLog.RequestPreview != nil {
			preview = *reqLog.RequestPreview
		}
		body := ""
		if reqLog.RequestBody != nil {
			body = *reqLog.RequestBody
		}
		h.autoSummaryGenerator.MaybeGenerateSummary(*reqLog.GwSessionID, tenantID, body, preview, evt.RequestID)
	}

	// 2026-07-28: model-integrity detection (finish_refusal /
	// finish_truncation / empty_response / repeated_content). Skipped
	// when the executor already recorded (the non-stream OpenAI path
	// fires inside executeOpenAI before ResponseBody is consumed by the
	// response interceptor). The detector is nil-safe; we nil-check
	// here anyway so the call site stays explicit.
	if h.integrityDetector != nil && !result.IntegrityObserved {
		// 2026-08-02: hand over the incremental observer's finding so the
		// detector records the mid-stream repeat instead of rescanning
		// textContent. Exactly one repeated_content event per request.
		repHash, repHits, repBlockSize, repBlocks, repFound := capture.IncrementalRepeatedContent()
		h.integrityDetector.Observe(logCtx.Request.Context(), executors.IntegrityCandidate{
			RequestID:          reqLog.RequestID,
			TenantID:           tenantID,
			ApplicationID:      reqLog.ApplicationID,
			APIKeyID:           reqLog.APIKeyID,
			ProviderID:         reqLog.ProviderID,
			ProviderCode:       result.Candidate.CatalogCode,
			CredentialID:       reqLog.CredentialID,
			ClientModel:        strValueOrEmpty(reqLog.ClientModel),
			OutboundModel:      strValueOrEmpty(reqLog.OutboundModel),
			RawModel:           result.Candidate.RawModel,
			RespModel:          streamRespModelForIntegrity(capture, responseBody),
			ProviderResponseID: integrityHeader(result.Response, "X-Request-Id"),
			SystemFingerprint:  integrityHeader(result.Response, "X-System-Fingerprint"),
			UsageSource:        strValueOrEmpty(reqLog.UsageSource),
			FinishReason:       strValueOrEmpty(reqLog.UpstreamFinishReason),
			PromptTokens:       reqLog.PromptTokens,
			CompletionTokens:   reqLog.CompletionTokens,
			ChunkCount:         intValueOrZero(reqLog.StreamChunkCount),
			ChunksSent:         intValueOrZero(reqLog.StreamChunksSent),
			TextContent:        streamTextContentForIntegrity(capture),
			ResponseBody:       append([]byte(nil), responseBody...),
			IsStream:           capture != nil,

			RepeatedContentDetected:    repFound,
			RepeatedContentHash:        repHash,
			RepeatedContentHits:        repHits,
			RepeatedContentBlockSize:   repBlockSize,
			RepeatedContentBlocksTotal: repBlocks,
			StreamAborted:              capture.IntegrityBreachReason() == "integrity_repeated_content",
		})
	}
}

// shouldSkipAutoTitleGeneration reports whether auto title generation must be
// skipped for this request. 2026-08-05: gateway-internal auto requests
// (logCtx.IsAutoRequest — set from the X-Gw-Is-Auto header or model="auto")
// are excluded so the auto title generator does not re-trigger on its own
// loopback title requests (chain self-triggering). A nil logCtx is treated
// as a normal request (no skip).
func shouldSkipAutoTitleGeneration(logCtx *RequestLogContext) bool {
	return logCtx != nil && logCtx.IsAutoRequest
}

// shouldSkipAutoSummaryGeneration (2026-08-06) — symmetric companion to
// shouldSkipAutoTitleGeneration. Excludes gateway-internal auto requests
// (which include summary loopbacks and the title loopback that already
// fired) so the auto-summary generator does not re-trigger on its own
// loopback summary requests (chain self-triggering). A nil logCtx is
// treated as a normal request (no skip).
func shouldSkipAutoSummaryGeneration(logCtx *RequestLogContext) bool {
	return logCtx != nil && logCtx.IsAutoRequest
}

func hasMultipleProviders(candidates []provider.Candidate) bool {
	seen := map[int]struct{}{}
	for _, c := range candidates {
		if c.ProviderID == 0 {
			continue
		}
		seen[c.ProviderID] = struct{}{}
		if len(seen) > 1 {
			return true
		}
	}
	return false
}

// streamRespModelForIntegrity returns the upstream-returned model
// from a stream capture, falling back to the first-chunk .model in
// the response body. Empty disables the integrity mismatch check.
//
// Implementation note: we deliberately do not import
// domains/streaming/integrity here to keep the dependency graph
// one-directional (integrity → audit, not the reverse). The
// response-body fallback is a 12-line JSON parse; duplicating it in
// the streaming package avoids the cycle.
func streamRespModelForIntegrity(capture *audit.StreamCapture, responseBody []byte) string {
	if capture != nil {
		if m := capture.RespModel(); m != "" {
			return m
		}
	}
	if len(responseBody) == 0 {
		return ""
	}
	var v struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(responseBody, &v); err != nil {
		return ""
	}
	return strings.TrimSpace(v.Model)
}

// streamTextContentForIntegrity extracts the rolling text content
// from a stream capture, returning "" if absent. Mirrors
// audit.StreamCapture.SummaryAsMap's `stream_text_content` field.
func streamTextContentForIntegrity(capture *audit.StreamCapture) string {
	if capture == nil {
		return ""
	}
	// The capture keeps textContent internal; we read it via the
	// summary map to avoid a new exporter just for this. The cost is
	// one extra mu lock + map alloc per request, acceptable for a
	// signal that runs once after stream completion.
	m := capture.SummaryAsMap()
	if v, ok := m["stream_text_content"].(string); ok {
		return v
	}
	return ""
}

// integrityHeader returns an upstream response header for handler-side
// fallback observation. Executor-side observation already has this metadata;
// retaining it here prevents context loss on stream and fallback paths.
func integrityHeader(resp *http.Response, name string) string {
	if resp == nil {
		return ""
	}
	return resp.Header.Get(name)
}

// strValueOrEmpty returns "" for nil pointer and the dereferenced
// value otherwise. Used to feed the IntegrityDetector when an
// optional column is NULL.
func strValueOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// intValueOrZero returns 0 for nil pointer and the dereferenced
// value otherwise.
func intValueOrZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// recordFailedRequest writes a request_logs row for any non-success
// request exit (auth, rate-limit, budget, validation, candidate,
// executor, panic, …).  It is the safety net that guarantees
// every request that reaches any of the three handlers
// (chat completions, anthropic messages, openai responses) shows
// up in the admin request-logs UI, even when the request never
// makes it as far as the routing executor.
//
// Callers may set keyInfo to attach api_key_id / tenant_id; the
// rest of the row is filled in from the supplied error metadata.
// The caller is expected to call EmitRequestLog exactly once;
// recordFailedRequest never duplicates the entry.
// emitClientDisconnectProbe emits a dedicated probe record when the client
// disconnects (context.Canceled) or times out (context.DeadlineExceeded)
// before the request completed.
//
// 问题4 (2026-07-09): previously a client cancel/timeout only flipped a
// WAL flag (logCtx.SetClientTimeout) that no dashboard surface reads in
// real time. Operators could not see WHY a request vanished from the live
// stream. This synthesizes a first-class RequestLogEntry with a "probe-"
// prefixed request_id so it:
//  1. is persisted to request_logs_hot via EmitRequestLogInsert, and
//  2. is pushed to the live-stream swim lane via the onEmitted hook.
//
// The record carries the credential_id / provider_id selected for the
// request (when available), so the lane groups it under the right provider
// via the credential → provider reverse lookup (问题3 fix). When no
// credential was selected (routing/auth failure before candidate pick),
// credential_id is nil and the probe still records the client-side event.
//
// error_kind: "client_cancel" for context.Canceled, "probe_timeout" for
// context.DeadlineExceeded. failure_stage is always "probe".
//
// shouldEmitDisconnectProbe reports whether the client-disconnect safety net
// should synthesize a probe row: the request context must actually be canceled
// (client disconnect / deadline) AND the request must NOT have already been
// recorded via the success or failure path (IsLogged()==false). 2026-08-16:
// extracted from the ServeHTTP defer guard so this predicate is unit-testable —
// a request that already completed successfully (emitTelemetry wrote the row,
// IsLogged()==true) must never emit a probe when the client tears down at the
// very end.
func shouldEmitDisconnectProbe(rctx context.Context, logCtx *RequestLogContext) bool {
	if rctx == nil || rctx.Err() == nil {
		return false
	}
	if logCtx == nil || logCtx.IsLogged() {
		return false
	}
	return true
}

func (h *ChatHandler) emitClientDisconnectProbe(originalRequestID string, r *http.Request, logCtx *RequestLogContext) {
	if h == nil || h.telemetryClient == nil || !h.telemetryClient.Enabled() {
		return
	}
	entry, ok := buildClientDisconnectProbeEntry(originalRequestID, r, logCtx)
	if !ok {
		return
	}
	// Test sink (mirrors the initial-log path at ~handler.go:3075).
	if h.requestLogHook != nil {
		h.requestLogHook(entry)
	}
	h.telemetryClient.EmitRequestLogInsert(entry)
	// 2026-07-15: 侧表 request_context_attrs（best-effort，origin_stage="business"）。
	if logCtx != nil {
		if attrs := BuildContextAttrsEntry(logCtx, logCtx.KeyInfo, &logCtx.meta, r.Context()); attrs != nil {
			h.telemetryClient.EmitContextAttrs(attrs)
		}
	}
}

// buildClientDisconnectProbeEntry constructs the probe RequestLogEntry from
// the request context error and the in-flight log context. Returns ok=false
// when there is no context error to report (nothing to probe). Pure / side
// effect free so it can be unit tested without a telemetry client or DB.
//
// 2026-07-24 fix: 现在记录完整的请求信息（请求体、关键参数等），以便将来分析
// 客户端取消/超时的原因。之前只记录最基本的字段，导致 probe 记录是空的 JSON。
func buildClientDisconnectProbeEntry(originalRequestID string, r *http.Request, logCtx *RequestLogContext) (*telemetry.RequestLogEntry, bool) {
	if r == nil || r.Context() == nil {
		return nil, false
	}
	ctxErr := r.Context().Err()
	if ctxErr == nil {
		return nil, false
	}

	// Distinguish cancel vs timeout so the lane legend can tell them apart.
	// 2026-08-09: 修复问题 —— 上游 first_byte_timeout 会导致客户端取消，
	// 但这是"供应商超时导致的客户端取消"，应该归类为 timeout 并触发凭据降级，
	// 而非 client_cancel（客户端主动取消，不应降级供应商）。
	//
	// 检查顺序（优先级从高到低）：
	//   1. StreamCapture.finalFinish == "first_byte_timeout" | "stream_timeout" 等
	//      → probe_timeout（供应商超时，需降级）
	//   2. context.DeadlineExceeded → probe_timeout
	//   3. 其他 context.Canceled → client_cancel
	errorKind := "client_cancel"
	if logCtx != nil && logCtx.StreamCapture != nil {
		summary := logCtx.StreamCapture.SummaryAsMap()
		if reason, ok := summary["upstream_finish_reason"].(string); ok {
			// first_byte_timeout / stream_timeout / stream_chunk_timeout /
			// chunk_timeout 都是供应商端超时，应归类为 probe_timeout。
			// 这些 reason 会触发 KindStreamTimeout / KindTimeout 降级。
			switch reason {
			case "first_byte_timeout", "stream_timeout", "stream_chunk_timeout", "chunk_timeout":
				errorKind = "probe_timeout"
			}
		}
	}
	if errorKind == "client_cancel" && errors.Is(ctxErr, context.DeadlineExceeded) {
		errorKind = "probe_timeout"
	}

	// probe request_id: unique per event, "probe-" prefix so it is visually
	// distinct from real request rows in the lane. Includes the credential
	// id (when known) and a unix-nano timestamp.
	credSegment := "nocred"
	if logCtx != nil && logCtx.CredentialID != nil && *logCtx.CredentialID > 0 {
		credSegment = fmt.Sprintf("cred%d", *logCtx.CredentialID)
	}
	probeRequestID := fmt.Sprintf("probe-%s-%s-%d", errorKind, credSegment, time.Now().UnixNano())

	tenantID := ""
	clientModel := ""
	outboundModel := ""
	var providerID, credentialID *int
	var apiKeyID *int
	var endUser *string
	var requestPreview *string

	if logCtx != nil {
		clientModel = logCtx.ClientModel
		outboundModel = logCtx.OutboundModel
		providerID = logCtx.ProviderID
		credentialID = logCtx.CredentialID

		if logCtx.KeyInfo != nil {
			tenantID = logCtx.KeyInfo.TenantID
			if logCtx.KeyInfo.ID > 0 {
				apiKeyID = &logCtx.KeyInfo.ID
			}
		}

		// 记录 EndUser (如果有)
		if logCtx.EndUser != "" {
			endUser = strPtr(logCtx.EndUser)
		}

		// Client-disconnect probe rows are synthetic diagnostic events. Keep
		// compact request metadata, but do not duplicate the original body into
		// the probe row; the original row is linked through ClientRequestID.
		if len(logCtx.Body) > 0 {
			var reqBodyParsed map[string]any
			if err := json.Unmarshal(logCtx.Body, &reqBodyParsed); err == nil {
				preview := buildRequestPreview(reqBodyParsed)
				if preview != "" {
					requestPreview = &preview
				}
			}
		}
	}

	stage := "probe"
	eventAt := time.Now().UTC()
	latencyMs := int(time.Since(logCtx.StartTime).Milliseconds())

	return &telemetry.RequestLogEntry{
		RequestID:     probeRequestID,
		EventAt:       &eventAt,
		TenantID:      tenantID,
		APIKeyID:      apiKeyID,
		EndUserID:     endUser,
		ClientModel:   strPtr(clientModel),
		OutboundModel: strPtr(outboundModel),
		ProviderID:    providerID,
		CredentialID:  credentialID,
		Success:       false,
		RequestStatus: strPtr(telemetry.RequestStatusFailure),
		ErrorKind:     strPtr(errorKind),
		FailureStage:  &stage,
		LatencyMs:     &latencyMs,
		// 2026-08-15: probe rows are synthetic; keep only compact metadata.
		RequestPreview: requestPreview,
		// Link back to the original request via ClientRequestID so /request-logs
		// can correlate the probe row with the in_progress row it interrupted.
		ClientRequestID: strPtr(originalRequestID),
	}, true
}

// buildRequestPreview 从请求体中提取关键的模型参数和统计信息，用于快速预览
func buildRequestPreview(body map[string]any) string {
	preview := make(map[string]any)

	// 提取常见的模型参数
	paramKeys := []string{
		"temperature", "top_p", "top_k", "max_tokens", "max_completion_tokens",
		"presence_penalty", "frequency_penalty", "n", "stream",
		"stop", "seed", "response_format", "tool_choice",
	}

	for _, key := range paramKeys {
		if val, ok := body[key]; ok && val != nil {
			preview[key] = val
		}
	}

	// 记录消息数量和大致长度（用于分析是否因为请求太大导致超时）
	if messages, ok := body["messages"].([]any); ok {
		preview["message_count"] = len(messages)
		totalLen := 0
		for _, msg := range messages {
			if msgMap, ok := msg.(map[string]any); ok {
				if content, ok := msgMap["content"].(string); ok {
					totalLen += len(content)
				}
			}
		}
		if totalLen > 0 {
			preview["total_content_length"] = totalLen
		}
	}

	// 记录 tools 数量（如果有）
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		preview["tool_count"] = len(tools)
	}

	if len(preview) == 0 {
		return ""
	}

	previewJSON, _ := json.Marshal(preview)
	return string(previewJSON)
}

// recordFailedRequestWithKey records a failure via the unified RequestLogContext pipeline.
func (h *ChatHandler) recordFailedRequestWithKey(requestID, clientModel, outboundModel string, providerID, credentialID *int, errCode, errMessage string, latencyMs int, requestBody []byte, keyInfo *authentication.KeyInfo, r *http.Request) {
	ctx := &RequestLogContext{
		handler:       h,
		RequestID:     requestID,
		StartTime:     time.Now().Add(-time.Duration(latencyMs) * time.Millisecond),
		Request:       r,
		KeyInfo:       keyInfo,
		Body:          requestBody,
		ClientModel:   clientModel,
		OutboundModel: outboundModel,
	}
	if r != nil {
		if session := session.SessionFromContext(r.Context()); session != nil {
			ctx.SetSession(session)
		}
		// 2026-06-26: forward the client-supplied X-Request-Id (set by
		// the RequestIDMiddleware into X-Gw-Client-Request-Id) so the
		// failure row records it for debug / cross-system tracing.
		if gw := r.Header.Get("X-Gw-Client-Request-Id"); gw != "" {
			ctx.ClientRequestID = gw
		} else if gw := r.Header.Get("X-Client-Request-Id"); gw != "" {
			ctx.ClientRequestID = gw
		}
		ctx.refreshMeta()
	}
	ctx.EmitFailure(errCode, errMessage, providerID, credentialID)
}

// clientProfileFromKey returns the API key / application default client profile.
func clientProfileFromKey(keyInfo *authentication.KeyInfo) string {
	if keyInfo != nil && keyInfo.DefaultClientProfile != nil {
		return strings.TrimSpace(*keyInfo.DefaultClientProfile)
	}
	return ""
}

// failedRequestIdentity builds client_profile + identity_hash from request
// headers and key anchors without requiring a parsed request body.
func failedRequestIdentity(r *http.Request, keyInfo *authentication.KeyInfo) (clientProfile, identityHash string) {
	if r == nil {
		return "", ""
	}
	cp := clientProfileFromKey(keyInfo)
	clientID := identity.BuildIdentityFromRequest(r, tenant(keyInfo), appID(keyInfo), apiKeyIDPtr(keyInfo), cp)
	return clientID.Fingerprint.ClientProfile, clientID.ShortID()
}

func requestModeFromPath(path string) string {
	switch {
	case strings.Contains(path, "/messages"):
		return "anthropic"
	case strings.Contains(path, "/responses"):
		return "responses"
	default:
		return "chat"
	}
}

// isAnthropicMessagesPath returns true when the request targets the
// Anthropic Messages API (/v1/messages), so the session compressor
// knows which wire format to use when rebuilding the body.
func isAnthropicMessagesPath(path string) bool {
	return strings.Contains(path, "/messages")
}

// capturePartialBodyOnReadError keeps bytes already received when io.ReadAll
// fails mid-stream (timeout, client disconnect). model is usually near the
// start of JSON bodies, so partial data is enough for request_logs preview.
//
// 2026-06-20 audit fix v3: when the partial body has no "model" field
// (e.g. /v1/messages client sent messages first), set client_model to
// "<unknown>" so request_logs never shows a blank client_model alongside
// a non-empty body — same invariant as captureAttemptBody /
// ensureRequestBodyBuffered.
func capturePartialBodyOnReadError(body []byte, attemptRequestBody *[]byte, attemptClientModel *string) {
	if attemptRequestBody == nil || len(body) == 0 {
		return
	}
	*attemptRequestBody = body
	if attemptClientModel != nil && *attemptClientModel == "" {
		*attemptClientModel = extractModelFromBody(body)
		if *attemptClientModel == "" {
			*attemptClientModel = "<unknown>"
		}
	}
}

// mapGatewayErrorToDetail returns a machine-readable sub-classification for
// the given early-exit error code.  Gateway-side errors are prefixed with
// "gw_" so that request_log consumers can immediately distinguish these from
// upstream provider errors (which keep their classified kind, e.g. "rate_limit",
// "concurrent", "timeout").
//
// The mapping:
//
//	gateway RPM limit         → "gw_rpm_exceeded"
//	gateway concurrent        → "gw_concurrent_exceeded"
//	gateway TPM               → "gw_tpm_exceeded"
//	key throttled             → "gw_key_throttled"
//	budget exhausted          → "gw_budget_exhausted"
//	auto-route decider failed → "gw_auto_route_decider_failed"
//	upstream 429              → "rate_limit"        (unchanged)
//	upstream 429/503          → "concurrent"        (unchanged)
//	upstream 401/403          → "upstream_credential_invalid"
//	                            "upstream_credential_revoked"     (unchanged)
//	upstream 402/429 quota    → "upstream_quota_periodic"         (unchanged)
//	                            "upstream_quota_permanent"        (unchanged)
//	other early-exits         → errCode passthrough
func mapGatewayErrorToDetail(errCode string) string {
	switch errCode {
	case "rate_limit_exceeded":
		return "gw_rpm_exceeded"
	case "concurrent_limit_exceeded":
		return "gw_concurrent_exceeded"
	case "tpm_limit_exceeded":
		return "gw_tpm_exceeded"
	case "key_throttled":
		return "gw_key_throttled"
	case "budget_exhausted":
		return "gw_budget_exhausted"
	case "missing_key", "invalid_key":
		return "gw_" + errCode
	case "auth_unavailable":
		return "gw_auth_unavailable"
	case "method_not_allowed":
		return "gw_method_not_allowed"
	case "executor_unavailable":
		return "gw_executor_unavailable"
	case "no_candidate":
		return "gw_no_candidate"
	case "body_too_large", "body_read_error", "json_parse_error":
		return "gw_" + errCode
	case "missing_model", "missing_max_tokens":
		return "gw_" + errCode
	case "conversion_error":
		return "gw_conversion_error"
	case "session_forbidden":
		return "gw_session_forbidden"
	case "internal_panic":
		return "gw_internal_panic"
	case "chat_to_anthropic_conversion_error":
		return "gw_chat_to_anthropic_conversion_error"
	case "auto_route_decider_failed":
		return "gw_auto_route_decider_failed"
	default:
		// Upstream-originated codes (e.g. upstream_credential_invalid,
		// upstream_credential_revoked, upstream_quota_periodic,
		// upstream_quota_permanent,
		// rate_limit, concurrent, model_not_found) fall through
		// unchanged — they are NOT gateway failures.
		return errCode
	}
}

// classifyFailureStage returns where in the request lifecycle the
// failure happened so the request_log UI can group and filter.  Two
// possible values:
//
//	"gateway"  — the request never reached an upstream provider
//	             (auth/rate-limit/budget/validation/panics/...)
//	"upstream" — the request was dispatched to a provider and failed
//	             during or after the provider call
//	             (provider_error, model_not_found, stream_error, ...)
//
// Any error code that is NOT in the gateway early-exit list is
// assumed to be upstream.  This mirrors the rule used in
// mapGatewayErrorToDetail: the codes that get a "gw_" prefix are
// gateway; everything else is upstream.
//
// Note (2026-07-12): upstream_credential_invalid / upstream_credential_revoked
// / upstream_quota_periodic / upstream_quota_permanent fall into the
// "upstream" bucket because they
// originate from the upstream provider rejecting our stored credential.
// The earlier Stage distinction ("client key problem" vs "upstream
// credential problem") is carried by the error.code itself, not by the
// stage.
func classifyFailureStage(errCode string) string {
	switch errCode {
	case "rate_limit_exceeded",
		"concurrent_limit_exceeded",
		"tpm_limit_exceeded",
		"key_throttled",
		"budget_exhausted",
		"insufficient_credits",
		"missing_key",
		"invalid_key",
		"auth_unavailable",
		"method_not_allowed",
		"executor_unavailable",
		"no_candidate",
		"body_too_large",
		"body_read_error",
		"json_parse_error",
		"missing_model",
		"missing_max_tokens",
		"conversion_error",
		"session_forbidden",
		"internal_panic",
		"chat_to_anthropic_conversion_error",
		"auto_route_decider_failed":
		return "gateway"
	default:
		return "upstream"
	}
}

// canonicalNameFromResolution safely extracts the canonical model name
// from a *resolve.Resolution result. Returns "" when modelResolution is nil
// or CanonicalName is nil (passthrough case — no canonical row matched).
// 2026-07-27: written so recordInitialRequestLog can persist the standard
// name to request_logs.canonical_model (migration 458) without each caller
// having to nil-check.
func canonicalNameFromResolution(modelResolution *resolve.Resolution) string {
	if modelResolution == nil || modelResolution.CanonicalName == nil {
		return ""
	}
	return *modelResolution.CanonicalName
}

// recordInitialRequestLog writes the base request metadata as soon as routing
// is resolved and before the upstream call starts.  Streaming requests then
// appear immediately in /request-logs; completion paths update tokens, bodies,
// and final success/error state via EmitRequestLogUpdate.
func (h *ChatHandler) recordInitialRequestLog(
	ctx context.Context,
	requestID, clientModel, outboundModel, endUser, requestMode string,
	keyInfo *authentication.KeyInfo,
	clientProfile, identityHash string,
	providerID, credentialID, canonicalID *int,
	canonicalName string, // 2026-07-27: 标准模型名 (migration 458)
	requestBody []byte,
	txResult *transformation.TransformResult,
	egressProtocol string,
	isStream bool,
	gwSessionID, gwTaskID string,
	autoCtx *RequestLogContext,
) {
	if h.telemetryClient == nil || !h.telemetryClient.Enabled() {
		return
	}
	if clientModel == "" && len(requestBody) > 0 {
		clientModel = extractModelFromBody(requestBody)
	}
	if outboundModel == "" && clientModel != "" {
		outboundModel = clientModel
	}
	var requestBodyText *string
	if len(requestBody) > 0 {
		v := string(redactAttachmentBodyIfEnabled(requestBody))
		requestBodyText = &v
	}
	tenantID := "default"
	var apiKeyID *int
	var applicationID *int
	keyPrefix, keyOwner, appCode := "", "", ""
	if keyInfo != nil {
		tenantID = keyInfo.TenantID
		kid := keyInfo.ID
		apiKeyID = &kid
		applicationID = appID(keyInfo)
		keyPrefix, keyOwner, appCode = keyMetaFromKeyInfo(keyInfo)
	}
	var requestPreviewPtr *string
	if preview := requestPreview(redactAttachmentBodyIfEnabled(requestBody)); preview != "" {
		requestPreviewPtr = strPtr(preview)
	}
	var transformSummaryPtr *string
	if summary := transformSummary(txResult, outboundModel); summary != "" {
		transformSummaryPtr = strPtr(summary)
	}
	var transformRuleID *string
	if txResult != nil && txResult.MatchedRule != "" {
		transformRuleID = strPtr(txResult.MatchedRule)
	}
	streamInterrupted := false
	eventAt := time.Now().UTC()
	if autoCtx != nil {
		eventAt = autoCtx.StartTime
	}
	var clientRequestIDPtr *string
	if autoCtx != nil && autoCtx.ClientRequestID != "" {
		v := autoCtx.ClientRequestID
		clientRequestIDPtr = &v
	}
	reqLog := &telemetry.RequestLogEntry{
		RequestID:       requestID,
		EventAt:         &eventAt,
		TenantID:        tenantID,
		ApplicationID:   applicationID,
		APIKeyID:        apiKeyID,
		APIKeyPrefix:    strPtr(keyPrefix),
		APIKeyOwnerUser: strPtr(keyOwner),
		ApplicationCode: strPtr(appCode),
		EndUserID:       strPtr(endUser),
		ClientModel:     strPtr(clientModel),
		OutboundModel:   strPtr(outboundModel),
		ProviderID:      providerID,
		CredentialID:    credentialID,
		CanonicalID:     canonicalID,
		// 2026-07-27: 标准模型名 (canonical_name),见 migration 458。
		CanonicalModel:    strPtr(canonicalName),
		ClientProfile:     strPtr(clientProfile),
		IdentityHash:      strPtr(identityHash),
		RequestMode:       strPtr(requestMode),
		GwSessionID:       strPtr(gwSessionID),
		GwTaskID:          strPtr(gwTaskID),
		Success:           false,
		RequestStatus:     strPtr(telemetry.RequestStatusInProgress),
		RequestBody:       requestBodyText,
		RequestPreview:    requestPreviewPtr,
		TransformSummary:  transformSummaryPtr,
		TransformRuleID:   transformRuleID,
		EgressProtocol:    strPtr(egressProtocol),
		StreamInterrupted: &streamInterrupted,
		// 2026-06-26: preserve client-supplied X-Request-Id for debug
		// (request_id itself is server-generated; see middleware/requestid_mw.go).
		ClientRequestID: clientRequestIDPtr,
	}
	if isStream {
		zero := 0
		reqLog.StreamChunkCount = &zero
		// 2026-07-05 P0 fix: stream_chunks_sent is NOT NULL (migration 320).
		// Initialize to 0 for in-flight requests to prevent INSERT violations.
		reqLog.StreamChunksSent = &zero
	}
	applyAutoRouteFields(reqLog, autoCtx)
	// 2026-08-06: flow X-Gw-Parent-Request-Id / X-Gw-Source-Actor into the
	// persisted row. See applyParentCorrelationFields for rationale.
	applyParentCorrelationFields(reqLog, autoCtx)
	if h.requestLogHook != nil {
		h.requestLogHook(reqLog)
	}
	applyKeyInfoToRequestLog(reqLog, keyInfo)
	// 2026-07-27: 客户端感知字段透传 (streaming path)
	enrichRequestLogFromMeta(reqLog, keyInfo, &autoCtx.meta)
	// 2026-07-17: bridge OriginMiddleware context into the entry so
	// node_probe / self_check requests are marked with origin_stage.
	reqLog.ApplyOriginFromContext(ctx)
	// 2026-07-25: bridge origin_stage from main entry to RequestLogContext
	// so the side table request_context_attrs also carries origin_stage
	// and is_probe (derived from it in fillFromRequestLogContext).
	if autoCtx != nil && reqLog.OriginStage != nil && autoCtx.OriginStage == "" {
		autoCtx.SetOriginStage(*reqLog.OriginStage)
	}
	// 2026-08-02 (spec §12 GAP 3): persist routing_state_source and
	// conversion_path into request_logs metadata so the IR default-cut-
	// over readiness gate can attribute each row to its routing decision
	// and transport path. Until a dedicated routing_state_source column
	// is migrated (TODO migration), the values are merged into the
	// existing compression_meta JSONB — a generic metadata map that
	// applySessionCompressorFields already merges additively. This avoids
	// a schema change in the observability-only GAP. Best-effort: a
	// missing/evicted entry leaves the keys absent, never a row failure.
	applyRoutingMetadata(reqLog, requestID)
	h.telemetryClient.EmitRequestLogInsert(reqLog)
	// 2026-07-15: 侧表 request_context_attrs（best-effort）。
	if autoCtx != nil {
		if attrs := BuildContextAttrsEntry(autoCtx, keyInfo, &autoCtx.meta, ctx); attrs != nil {
			h.telemetryClient.EmitContextAttrs(attrs)
		}
	}
}

func extractModelFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var parsed struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		return strings.TrimSpace(parsed.Model)
	}
	return extractModelFieldLoose(body)
}

// extractModelFieldLoose reads "model":"..." from truncated or invalid JSON.
func extractModelFieldLoose(body []byte) string {
	pattern := []byte(`"model"`)
	idx := bytes.Index(body, pattern)
	if idx < 0 {
		return ""
	}
	after := body[idx+len(pattern):]
	colonIdx := bytes.IndexByte(after, ':')
	if colonIdx < 0 {
		return ""
	}
	rest := bytes.TrimLeft(after[colonIdx+1:], " \t\n\r")
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	endIdx := bytes.IndexByte(rest[1:], '"')
	if endIdx < 0 {
		return ""
	}
	return strings.TrimSpace(string(rest[1 : endIdx+1]))
}

func (h *ChatHandler) emitFailedDecisionLog(requestID, clientModel string, keyInfo *authentication.KeyInfo, clientID identity.ClientIdentity, candidatesTried int, modelResolution *resolve.Resolution, txResult *transformation.TransformResult, errCode string, failTrace *executors.Trace, latencyMs int) {
	if h.telemetryClient == nil || !h.telemetryClient.Enabled() {
		return
	}
	var apiKeyID *int
	var tenantID = "default"
	if keyInfo != nil {
		apiKeyID = &keyInfo.ID
		tenantID = keyInfo.TenantID
	}
	var canonical string
	if modelResolution != nil && modelResolution.CanonicalName != nil {
		canonical = *modelResolution.CanonicalName
	}
	dl := &telemetry.DecisionLogEntry{
		RequestID:         requestID,
		TenantID:          tenantID,
		APIKeyID:          apiKeyID,
		Model:             canonicalOrClient(canonical, clientModel),
		CandidatesTried:   candidatesTried,
		LatencyMs:         latencyMs,
		Success:           false,
		ErrorClass:        strPtr(errCode),
		FailureDetailCode: strPtr(errCode),
		ClientModel:       strPtr(clientModel),
		IdentityHash:      strPtr(clientID.IdentityHash),
	}
	if failTrace != nil {
		traceJSON, _ := json.Marshal(failTrace)
		dl.DecisionTrace = traceJSON
	}
	if modelResolution != nil {
		dl.ResolutionPath = strPtr(modelResolution.ResolutionPath)
		if modelResolution.CanonicalName != nil {
			dl.CanonicalModel = strPtr(*modelResolution.CanonicalName)
		}
		if len(modelResolution.RawModels) > 0 {
			dl.ResolutionRawModels = modelResolution.RawModels
		}
	}
	if txResult != nil {
		dl.OutboundModel = strPtr(txResult.OutboundModel)
		if txResult.MatchedRule != "" {
			dl.TransformRuleID = strPtr(txResult.MatchedRule)
		}
	}
	h.telemetryClient.EmitDecisionLog(dl)
}

func (h *ChatHandler) serveFallback(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"error": map[string]string{
			"message": "Routing executor not available. Database connection required.",
			"type":    "server_error",
			"code":    "executor_unavailable",
		},
	})
}

// ReplaceModelInRequestBody replaces the "model" field in a JSON body.
func ReplaceModelInRequestBody(body []byte, newModel string) []byte {
	quotedOld := bytes.Contains(body, []byte(`"model"`))
	if !quotedOld {
		return body
	}
	pattern := []byte(`"model"`)
	idx := bytes.Index(body, pattern)
	if idx < 0 {
		return body
	}
	after := body[idx+len(pattern):]
	colonIdx := bytes.IndexByte(after, ':')
	if colonIdx < 0 {
		return body
	}
	rest := after[colonIdx+1:]
	rest = bytes.TrimLeft(rest, " \t\n\r")
	if len(rest) == 0 || rest[0] != '"' {
		return body
	}
	endIdx := bytes.IndexByte(rest[1:], '"')
	if endIdx < 0 {
		return body
	}
	oldValue := rest[1 : endIdx+1]
	if string(oldValue) == newModel {
		return body
	}
	var buf bytes.Buffer
	prefix := body[:idx+len(pattern)+colonIdx+1]
	suffix := rest[endIdx+2:]
	buf.Write(prefix)
	buf.WriteString(" \"")
	buf.WriteString(newModel)
	buf.WriteByte('"')
	buf.Write(suffix)
	return buf.Bytes()
}

// ReplaceModelInResponseBody replaces whatever model is in the response with clientModel.
func ReplaceModelInResponseBody(body []byte, clientModel string) []byte {
	pattern := []byte(`"model"`)
	idx := bytes.Index(body, pattern)
	if idx < 0 {
		return body
	}
	after := body[idx+len(pattern):]
	colonIdx := bytes.IndexByte(after, ':')
	if colonIdx < 0 {
		return body
	}
	rest := after[colonIdx+1:]
	rest = bytes.TrimLeft(rest, " \t\n\r")
	if len(rest) < 2 || rest[0] != '"' {
		return body
	}
	endIdx := bytes.IndexByte(rest[1:], '"')
	if endIdx < 0 {
		return body
	}
	oldValue := rest[1 : endIdx+1]
	if string(oldValue) == clientModel {
		return body
	}
	var buf bytes.Buffer
	prefix := body[:idx+len(pattern)+colonIdx+1]
	suffix := rest[endIdx+2:]
	buf.Write(prefix)
	buf.WriteString(`"` + clientModel + `"`)
	buf.Write(suffix)
	return buf.Bytes()
}

func requestHasTools(body []byte) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return false
	}
	toolsRaw, ok := obj["tools"]
	if !ok || len(toolsRaw) == 0 || string(toolsRaw) == "null" {
		return false
	}
	var tools []any
	return json.Unmarshal(toolsRaw, &tools) == nil && len(tools) > 0
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(data)
}

//-----------------------------------------------------------------------------
// Health handler
//-----------------------------------------------------------------------------

// ResourceStatus represents the connection status of a resource (DB, Redis, etc).
type ResourceStatus struct {
	Connected bool   `json:"connected"`
	Latency   string `json:"latency,omitempty"`
	Error     string `json:"error,omitempty"`
}

// HealthResponse represents the health check response.
type HealthResponse struct {
	Status      string          `json:"status"`
	Version     string          `json:"version"`
	Database    *ResourceStatus `json:"database,omitempty"`
	Redis       *ResourceStatus `json:"redis,omitempty"`
	Circuit     any             `json:"circuit,omitempty"`
	Concurrency any             `json:"concurrency,omitempty"`
	Proxy       map[string]any  `json:"proxy,omitempty"`
}

// HealthHandler returns health information including circuit breaker and limiter stats.
type HealthHandler struct {
	circuit *credential.Manager
	limiter *credential.Limiter
	proxy   *upstreampkg.ProxyResolver
	db      dbConnector
	redis   redisConnector
}

// dbConnector interface for database ping check
type dbConnector interface {
	Ping(ctx context.Context) error
}

// redisConnector interface for Redis ping check
type redisConnector interface {
	Ping(ctx context.Context) error
}

// NewHealthHandler creates a new health handler.
func NewHealthHandler(cm *credential.Manager, l *credential.Limiter, proxy *upstreampkg.ProxyResolver, db dbConnector, redis redisConnector) *HealthHandler {
	return &HealthHandler{circuit: cm, limiter: l, proxy: proxy, db: db, redis: redis}
}

// SetRedis updates the Redis connection for health checks (2026-07-08).
// Called after Redis is initialized in main.go.
func (h *HealthHandler) SetRedis(redis redisConnector) {
	h.redis = redis
}

func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resp := HealthResponse{
		Status:  "ok",
		Version: resolveGatewayVersion(),
	}

	// NET-007 fix: ?full=true 必须 admin token（完整的 LLM_GATEWAY_ADMIN_API_KEY
	// 校验由外层 AdminTokenMiddleware 完成；这里只拒绝"完全无 token"的情形）。
	//
	// ?full=true 会暴露 circuit.Stats() / limiter.Stats() / proxy.Status()
	// （含 18 个内网域名、credential 熔断状态等敏感信息），不应匿名访问。
	full := r.URL.Query().Get("full") == "true"
	if full {
		const expectedHeader = "Bearer "
		auth := r.Header.Get("Authorization")
		if len(auth) <= len(expectedHeader) || auth[:len(expectedHeader)] != expectedHeader {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("WWW-Authenticate", `Bearer realm="admin"`)
			w.WriteHeader(http.StatusUnauthorized)
			//nolint:errcheck
			w.Write([]byte(`{"error":"admin token required for full healthz"}`))
			return
		}
		resp.Circuit = h.circuit.Stats()
		resp.Concurrency = h.limiter.Stats()

		// Check database connection (2026-07-08: add resource status)
		if h.db != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			start := time.Now()
			dbErr := h.db.Ping(ctx)
			latency := time.Since(start)

			resp.Database = &ResourceStatus{
				Connected: dbErr == nil,
				Latency:   latency.String(),
			}
			if dbErr != nil {
				resp.Database.Error = dbErr.Error()
			}
		}

		// Check Redis connection (2026-07-08: add resource status)
		if h.redis != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			start := time.Now()
			redisErr := h.redis.Ping(ctx)
			latency := time.Since(start)

			resp.Redis = &ResourceStatus{
				Connected: redisErr == nil,
				Latency:   latency.String(),
			}
			if redisErr != nil {
				resp.Redis.Error = redisErr.Error()
			}
		}
	}

	// NET-007 fix: proxy 字段也属于敏感信息（暴露 internal.example.com 等内网
	// 域名）。仅当 full=true 时才返回（与 circuit/concurrency 同样需要 admin token）。
	if full && h.proxy != nil {
		if status := h.proxy.Status(); status != nil {
			resp.Proxy = status
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(resp)
}

func extractBearerToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			return strings.TrimPrefix(auth, "Bearer ")
		}
		if strings.HasPrefix(auth, "bearer ") {
			return strings.TrimPrefix(auth, "bearer ")
		}
	}
	if key := r.Header.Get("x-api-key"); key != "" {
		return key
	}
	return ""
}

// resolveEndUser picks the best end-user identifier available for this
// request. Resolution order (highest priority first):
//
//  1. bodyUser — the OpenAI-style "user" field already parsed from the
//     request body. Empty when the body has no user field, when parsing
//     failed, or when the request never had a parsed body (early failure
//     paths).
//  2. X-End-User-Id header — explicit end-user id header supported by all
//     protocols (chat-completions, Anthropic Messages, OpenAI Responses).
//  3. bodyBytes sniff — extracts "user":"..." from the supplied body
//     bytes. Covers cases where the body was captured into RequestLogContext
//     but the typed handler never propagated bodyUser (Anthropic Messages,
//     OpenAI Responses, early-failure rows).
//  4. r.Body sniff — best-effort fallback if r.Body is still readable
//     (only true before captureAttemptBody runs).
//  5. "anonymous" — last-resort fallback so request_logs_hot.end_user_id
//     is always populated (operator dashboards can filter on it).
//
// 2026-08-06: previously only paths 1-2 existed. Failure-path callers
// (buildEntry in request_log_pipeline.go) didn't have bodyUser, and the
// /v1/messages handler didn't even try the body field — leading to NULL
// end_user_id on every early-failure row and every Anthropic-message
// request, including the dc767386f... incident.
func resolveEndUser(bodyUser string, r *http.Request, bodyBytes ...[]byte) string {
	// 1. Caller-supplied bodyUser (e.g. parsed reqBody.User from chat-completions)
	if v := strings.TrimSpace(bodyUser); v != "" {
		return v
	}
	// 2. X-End-User-Id header (works on all protocols)
	if r != nil {
		if v := strings.TrimSpace(r.Header.Get("X-End-User-Id")); v != "" {
			return v
		}
	}
	// 3. Captured body bytes (typically logCtx.Body). Loops over the
	// variadic so callers can pass multiple captured snapshots (e.g.
	// original + redacted) and we accept whichever first yields a user.
	for _, b := range bodyBytes {
		if v := extractEndUserFromBody(b); v != "" {
			return v
		}
	}
	// NOTE (2026-08-06 audit fix): we intentionally do NOT read r.Body
	// here. By the time buildEntry / disconnect-probe / context-attrs
	// fire, r.Body has typically already been drained by upstream body
	// capture (captureAttemptBody / attemptRequestBody). Destructive
	// reading here would silently strip the suffix of any large body
	// and break downstream consumers (decode, forward to upstream). If
	// a future caller needs body-based resolution AND has not yet
	// captured into c.Body, it must pass the bytes explicitly via the
	// variadic.
	return "anonymous"
}

// extractEndUserFromBody sniffs the request body for a JSON "user" string
// field. Tolerates truncated / invalid JSON via a loose scan. Returns ""
// when no user field is present so callers can fall through to the
// "anonymous" sentinel.
//
// The caller passes the already-captured body bytes (typically
// RequestLogContext.Body) rather than r.Body — by the time the failure-
// path buildEntry runs, the original r.Body has already been consumed
// upstream. BodyBytes may be nil/empty for early-failure paths where
// the request never got past body parsing.
//
// Loose-scan safety (2026-08-06 audit fix):
//   - Only triggers on bodies where strict-JSON unmarshal fails AND
//     the body starts with `{` (i.e. it looks like a top-level JSON
//     object — multipart/binary bodies and attachment payloads are
//     rejected by this gate to prevent false positives).
//   - Walks past JSON string escapes when finding the closing quote,
//     so {"user":"a\"b","x":1} yields "a\"b" (downstream SQL handles
//     the actual escaping).
//   - Caps total scan at the first 1 MB to bound worst-case cost on
//     huge bodies.
func extractEndUserFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	// Strict JSON first.
	var parsed struct {
		User string `json:"user"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if v := strings.TrimSpace(parsed.User); v != "" {
			return v
		}
		return ""
	}
	// Loose-scan gate: only sniff if the body STARTS with '{' so we
	// don't false-positive on multipart/binary/attachment payloads that
	// happen to contain a literal "user" substring.
	firstNonSpace := 0
	for firstNonSpace < len(body) && (body[firstNonSpace] == ' ' || body[firstNonSpace] == '\t' || body[firstNonSpace] == '\n' || body[firstNonSpace] == '\r') {
		firstNonSpace++
	}
	if firstNonSpace >= len(body) || body[firstNonSpace] != '{' {
		return ""
	}
	// Loose scan for truncated / malformed JSON like {"model":"x","user":"al…
	const maxScan = 1 << 20 // 1 MB
	scanBody := body
	if len(scanBody) > maxScan {
		scanBody = scanBody[:maxScan]
	}
	pattern := []byte(`"user"`)
	idx := bytes.Index(scanBody, pattern)
	if idx < 0 {
		return ""
	}
	// Reject nested occurrences: only accept `"user"` when it sits at
	// the top level of the JSON object — i.e. the byte preceding it is
	// `{` or `,` (not `"`/`[`/`{`/`:` which would indicate we're inside
	// another value, key, or container).
	if idx == 0 || !isTopLevelJSONContext(scanBody, idx-1) {
		// Try one more position to avoid false positives on edge cases
		// where the FIRST "user" is nested. We don't recurse indefinitely
		// to keep worst-case bounded.
		idx2 := bytes.Index(scanBody[idx+len(pattern):], pattern)
		if idx2 < 0 {
			return ""
		}
		idx = idx + len(pattern) + idx2
		if idx == 0 || !isTopLevelJSONContext(scanBody, idx-1) {
			return ""
		}
	}
	after := scanBody[idx+len(pattern):]
	colonIdx := bytes.IndexByte(after, ':')
	if colonIdx < 0 {
		return ""
	}
	after = after[colonIdx+1:]
	// Skip whitespace between colon and value.
	for len(after) > 0 && (after[0] == ' ' || after[0] == '\t' || after[0] == '\n' || after[0] == '\r') {
		after = after[1:]
	}
	if len(after) == 0 || after[0] != '"' {
		return ""
	}
	after = after[1:]
	// Walk past JSON escapes to find the true closing quote. A naive
	// bytes.IndexByte('"') would mis-cut on inputs like {"user":"a\"b"}.
	endIdx := -1
	for i := 0; i < len(after); i++ {
		if after[i] == '\\' && i+1 < len(after) {
			i++ // skip the escaped byte
			continue
		}
		if after[i] == '"' {
			endIdx = i
			break
		}
	}
	if endIdx < 0 {
		return ""
	}
	return strings.TrimSpace(string(after[:endIdx]))
}

// isTopLevelJSONContext reports whether byte at position pos is a valid
// JSON boundary immediately before a top-level object key. The byte at
// pos must be either '{' (object start) or ',' (key separator). Any
// other byte means the preceding token is a nested value, container, or
// non-JSON text — disqualifying the candidate.
func isTopLevelJSONContext(b []byte, pos int) bool {
	if pos < 0 || pos >= len(b) {
		return false
	}
	switch b[pos] {
	case '{', ',':
		return true
	default:
		return false
	}
}

func extractTokensFromResponseBody(body []byte) (promptTokens, completionTokens, cacheRead, cacheWrite int) {
	if len(body) == 0 {
		return 0, 0, 0, 0
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, 0, 0, 0
	}
	usageRaw, ok := data["usage"]
	if !ok {
		// Fallback: some providers (e.g. minimax) may return usage at top level
		usageRaw = data
	}
	usage, ok := usageRaw.(map[string]any)
	if !ok {
		return 0, 0, 0, 0
	}
	// prompt_tokens / input_tokens (Anthropic native)
	if v, ok := usage["prompt_tokens"].(float64); ok {
		promptTokens = int(v)
	} else if v, ok := usage["input_tokens"].(float64); ok {
		promptTokens = int(v)
	}
	// completion_tokens / output_tokens (Anthropic native)
	if v, ok := usage["completion_tokens"].(float64); ok {
		completionTokens = int(v)
	} else if v, ok := usage["output_tokens"].(float64); ok {
		completionTokens = int(v)
	}
	// cache_read: try 4 variants
	if v, ok := usage["cache_read_input_tokens"].(float64); ok {
		cacheRead = int(v)
	} else if v, ok := usage["cache_read_tokens"].(float64); ok {
		cacheRead = int(v)
	} else if pt := usage["prompt_tokens_details"]; pt != nil {
		if details, ok := pt.(map[string]any); ok {
			if v, ok := details["cached_tokens"].(float64); ok && cacheRead == 0 {
				cacheRead = int(v)
			}
		}
	} else if pt := usage["input_token_details"]; pt != nil {
		if details, ok := pt.(map[string]any); ok {
			if v, ok := details["cache_read"].(float64); ok && cacheRead == 0 {
				cacheRead = int(v)
			}
		}
	}
	// cache_write: try 3 variants
	if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
		cacheWrite = int(v)
	} else if v, ok := usage["cache_write_tokens"].(float64); ok {
		cacheWrite = int(v)
	} else if pt := usage["input_token_details"]; pt != nil {
		if details, ok := pt.(map[string]any); ok {
			if v, ok := details["cache_creation"].(float64); ok && cacheWrite == 0 {
				cacheWrite = int(v)
			}
		}
	}
	// total_tokens fallback: if we have total but missing prompt/completion, infer them
	if promptTokens == 0 || completionTokens == 0 {
		if total, ok := usage["total_tokens"].(float64); ok && int(total) > 0 {
			totalInt := int(total)
			if promptTokens == 0 && completionTokens > 0 && totalInt > completionTokens {
				promptTokens = totalInt - completionTokens
			} else if completionTokens == 0 && promptTokens > 0 && totalInt > promptTokens {
				completionTokens = totalInt - promptTokens
			}
		}
	}
	return
}

// injectUsageIntoResponseBody augments a response body JSON with usage data extracted
// from the stream capture. This ensures request_logs.response_body always contains a
// `usage` block even when the upstream's last SSE chunk does not include one.
func injectUsageIntoResponseBody(body []byte, pt, ct, crt, cwt int) []byte {
	if pt <= 0 && ct <= 0 {
		return body
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return body
	}
	usageRaw, ok := data["usage"]
	if !ok {
		usageRaw = map[string]any{}
	}
	usage, ok := usageRaw.(map[string]any)
	if !ok {
		usage = map[string]any{}
	}
	if pt > 0 {
		usage["prompt_tokens"] = pt
	}
	if ct > 0 {
		usage["completion_tokens"] = ct
	}
	if crt > 0 {
		usage["cache_read_tokens"] = crt
	}
	if cwt > 0 {
		usage["cache_write_tokens"] = cwt
	}
	if pt > 0 && ct > 0 {
		usage["total_tokens"] = pt + ct
	}
	data["usage"] = usage
	result, err := json.Marshal(data)
	if err != nil {
		return body
	}
	return result
}

func intPtr(v int) *int { return &v }
func floatPtrFromInt(p *int) *float64 {
	if p == nil {
		return nil
	}
	v := float64(*p)
	return &v
}
func strPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func boolPtr(v bool) *bool {
	return &v
}

// classifyStreamInterruption determines whether a stream interruption captured
// in the summary map represents a real gateway error that should mark the
// request log as failed. It returns (isError, detailCode).
//
// Benign cases that do NOT mark the request as failed:
//   - "eof_without_done" with chunk_count > 0: upstream closed without [DONE]
//     but content was already delivered (e.g. MiniMax). The gateway synthesises
//     [DONE] for the client. This mirrors executor_chat.go's isBenignEOF.
//   - "client_cancel" / "client_disconnected": the client went away; not a
//     gateway or upstream error.
func classifyStreamInterruption(m map[string]any) (isError bool, detailCode string) {
	detailCode, _ = m["failure_detail_code"].(string)
	chunkCount, _ := m["stream_chunk_count"].(int)

	isBenignEOF := detailCode == "eof_without_done" && chunkCount > 0
	isClientCancel := detailCode == "client_cancel" || detailCode == "client_disconnected"

	if isBenignEOF || isClientCancel {
		return false, detailCode
	}
	return true, detailCode
}

// streamErrorKindForDetailCode maps the StreamOutcome.FailureDetailCode
// captured at the executor boundary to a stable request_logs.error_kind
// value. Operators SQL-filter on this column to break down the "stream
// issue" bucket into actionable categories:
//
//	stream_timeout       — no data for >stream_chunk_timeout
//	concurrent_overload  — circuit breaker inferred a 429-class overload
//	empty_response       — upstream 200 with zero content (NIM pattern)
//	eof_without_done     — upstream closed without sending [DONE]; benign
//	                       when chunks > 0 (handled by executor_chat.go
//	                       isBenignEOF before this mapper is reached),
//	                       real failure when chunks == 0 (2026-07-29 split)
//	stream_read_error    — generic read failure (malformed SSE, etc.)
//	stream_panic         — recovered panic in a stream bridge
//	client_cancel        — client disconnected before stream completion
//	upstream_error       — upstream returned malformed SSE / 5xx
//	conversion_error     — protocol-conversion pipeline rejected the body
//	stream_error         — fallback when neither Kind nor detail code is set
//
// 2026-07-28 §5.6: the executor-classified Kind (StreamOutcome.Kind)
// takes precedence over the legacy detail-code mapping. When the
// executor populates Kind (Task 11) the operator dashboard gets the
// fully specified taxonomy; the detail-code switch is the fallback
// for legacy paths and tests that don't go through the executor.
func streamErrorKindForDetailCode(outcome *StreamOutcome, detailCode string) string {
	if outcome != nil && outcome.Kind != "" {
		switch outcome.Kind {
		case errorsx.KindStreamTimeout, errorsx.KindTimeout:
			return "stream_timeout"
		case errorsx.KindConcurrent, errorsx.KindRateLimit:
			return "concurrent_overload"
		case errorsx.KindEmptyResponse:
			return "empty_response"
		case errorsx.KindCanceled, errorsx.KindClientBug:
			return "client_cancel"
		case errorsx.KindUpstreamDown, errorsx.KindNetwork:
			return "upstream_error"
		case errorsx.KindUpstreamOverloaded:
			// Distinct from upstream_error on purpose: an overloaded relay
			// recovers on its own in seconds, a dead one does not. Sharing
			// one bucket made "provider is busy" indistinguishable from
			// "provider is down" on the operator dashboard.
			return "upstream_overloaded"
		case errorsx.KindConversion:
			return "conversion_error"
		}
	}
	switch detailCode {
	case "stream_panic", "stream_panic_recover":
		return "stream_panic"
	case "first_byte_timeout", "stream_chunk_timeout", "stream_timeout", "chunk_timeout":
		return "stream_timeout"
	case "json_error_in_stream":
		return "upstream_error"
	case "client_cancel", "client_disconnected":
		return "client_cancel"
	case "concurrent_overload", "concurrent":
		return "concurrent_overload"
	case "empty_stream_no_content":
		return "empty_response"
	case "eof_without_done":
		// 2026-07-29: Decomposed from the "stream_read_error" bucket so the
		// operator-facing error_kind column no longer conflates the benign
		// "upstream closed without [DONE]" pattern (observed on MiniMax,
		// ~13% of streams as of 2026-07-28) with generic read failures.
		// Mirrors executor_chat.go isBenignEOF: chunk_count > 0 is
		// classified as success and never reaches this mapping; chunks == 0
		// remains a real failure but now has its own error_kind for
		// accurate dashboard filtering.
		return "eof_without_done"
	case "anthropic_to_openai_read_error", "anthropic_to_responses_read_error",
		"read_error", "stream_read_error":
		return "stream_read_error"
	}
	return "stream_error"
}

// canonicalOrClient prefers the canonical name (standardised model key from the
// routing table). When the resolution did not yield a canonical entry (direct
// passthrough), it falls back to whatever the client supplied.
// trackerFromResultOrLogCtx returns the routing tracker from the executor
// result first, falling back to the logCtx-level tracker. This ensures the
// failure path (no_candidate, pre-executor errors) also surfaces routing
// attempts in the request_logs.routing_attempts column.
func trackerFromResultOrLogCtx(result *executors.ExecuteResult, logCtx *RequestLogContext) *executors.RoutingAttemptsTracker {
	if result != nil && result.RoutingTracker != nil {
		return result.RoutingTracker
	}
	if logCtx != nil && logCtx.RoutingTracker != nil {
		return logCtx.RoutingTracker
	}
	return nil
}

func canonicalOrClient(canonical, client string) string {
	if canonical != "" {
		return canonical
	}
	return client
}

// generateRequestID returns a stable per-request UUID used both as the
// X-Request-Id response header and as the request_logs row's request_id
// column.  Always non-empty so the safety-net logger can find a row.
func generateRequestID() string {
	return uuid.NewString()
}

func writeErrorJSON(w http.ResponseWriter, status int, requestID, msg, errType, code string) {
	writeErrorJSONWithDebug(w, status, requestID, msg, errType, code, nil)
}

// writeErrorJSONCtx emits an error response whose message is translated via
// i18n for the locale on ctx. messageKey is both the translation key and the
// machine-readable "code" field value (they are kept aligned by convention —
// see i18n messages.go), so callers pass a single token.
//
// templateData carries interpolation values for messages with placeholders
// (e.g. {"Model": "gpt-4o"} for MsgNoCandidate); pass nil when none are needed.
//
// Use this instead of writeErrorJSON for any error whose code has a translation
// entry. Sites whose code lacks a translation yet keep calling writeErrorJSON
// directly (their inline English is the de-facto fallback).
func writeErrorJSONCtx(ctx context.Context, w http.ResponseWriter, status int, requestID, errType, messageKey string, templateData map[string]any) {
	msg := i18n.T(ctx, messageKey, templateData)
	writeErrorJSON(w, status, requestID, msg, errType, messageKey)
}

func writeErrorJSONWithDebug(w http.ResponseWriter, status int, requestID, msg, errType, code string, debug map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(status)
	errObj := map[string]any{
		"message":    msg,
		"type":       errType,
		"code":       code,
		"request_id": requestID,
	}
	if debug != nil {
		errObj["gateway_debug"] = debug
	}
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(map[string]any{
		"error": errObj,
	})
}

// writeErrorJSONWithKind (Step 6, 2026-06-18) is like
// writeErrorJSONWithDebug but additionally surfaces a "kind" field in
// the error object. The kind is the SSoT for the underlying failure
// cause (rate_limit, concurrent, model_not_found, ...). It is always
// emitted even when kind == code, so clients that learn the new shape
// never have to null-check.
//
// Backward compat: the legacy "code" field is unchanged ("model_not_found"
// is still surfaced there even when the real kind is "rate_limit"). New
// clients should read "kind"; old clients keep working.
func writeErrorJSONWithKind(w http.ResponseWriter, status int, requestID, msg, errType, code, kind string, debug map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(status)
	errObj := map[string]any{
		"message":    msg,
		"type":       errType,
		"code":       code,
		"request_id": requestID,
		"kind":       kind,
	}
	if debug != nil {
		errObj["gateway_debug"] = debug
	}
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(map[string]any{
		"error": errObj,
	})
}

// writeNoCandidateWithAlternatives writes the 503 no_candidate response and,
// when the gateway can offer other routable models, attaches them so the client
// can switch instead of giving up.
//
// Shape (OpenAI envelope; Anthropic gets its own via writeErrorAnthropic):
//
//	{"error": {
//	   "message": "No available provider for model 'X'",
//	   "type": "server_error", "code": "no_candidate", "kind": "no_candidate",
//	   "request_id": "...",
//	   "alternatives": {
//	     "requested_model": "X",
//	     "task_type": "code",
//	     "alternatives": [
//	       {"model": "claude-sonnet-4-6", "display_name": "...",
//	        "family": "claude", "context_window": 200000,
//	        "featured": true, "reason": "task_match"}
//	     ]
//	   }
//	 }}
//
// "alternatives" is additive and lives inside the existing error object, so
// clients that do not know the field are unaffected. It is omitted entirely
// when the list is empty — an empty array would read as "we looked and there is
// nothing", which is true, but the absent field keeps the response identical to
// the historical one for callers that cannot use it anyway.
//
// This is the one no-candidate exit where a rich body is possible: it runs
// before startPreStreamKeepalive commits HTTP 200, so both the status code and
// the body shape are still ours to choose, for streaming and non-streaming
// requests alike.
func writeNoCandidateWithAlternatives(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	requestID, clientModel string,
	alts ModelAlternativesResult,
) {
	msg := i18n.T(ctx, i18n.MsgNoCandidate, map[string]any{"Model": clientModel})

	if len(alts.Alternatives) == 0 {
		// Nothing to offer: keep the exact legacy response.
		writeErrorJSONWithKindProto(protocolOfRequest(r), w, http.StatusServiceUnavailable,
			requestID, msg, "server_error", "no_candidate", "no_candidate", nil)
		return
	}

	if protocolOfRequest(r) == "anthropic" {
		writeErrorAnthropicWithAlternatives(w, http.StatusServiceUnavailable,
			requestID, msg, "no_candidate", alts)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message":      msg,
			"type":         "server_error",
			"code":         "no_candidate",
			"kind":         "no_candidate",
			"request_id":   requestID,
			"alternatives": alts,
		},
	})
}

// writeErrorAnthropicWithAlternatives mirrors writeErrorAnthropic and adds the
// alternatives payload. Anthropic SDKs type-check error.type against a closed
// set, so the type is mapped through anthropicErrorType; the extra key sits
// beside it and is ignored by clients that do not read it.
func writeErrorAnthropicWithAlternatives(
	w http.ResponseWriter,
	status int,
	requestID, msg, code string,
	alts ModelAlternativesResult,
) {
	w.Header().Set("Content-Type", "application/json")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(status)
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":         anthropicErrorType("server_error", code),
			"message":      msg,
			"alternatives": alts,
		},
	})
}

// protocolOfRequest returns the client-facing protocol family derived from
// the request path, so error responses can be shaped to match what the
// client SDK expects. Returns "anthropic" for /v1/messages, "openai"
// otherwise (the historical default envelope).
func protocolOfRequest(r *http.Request) string {
	if r == nil {
		return "openai"
	}
	if isAnthropicMessagesPath(r.URL.Path) {
		return "anthropic"
	}
	return "openai"
}

// anthropicErrorType maps the gateway's OpenAI-style error "type"/"code"
// values onto Anthropic's restricted set of error "type" strings
// (https://docs.anthropic.com/en/api/errors). Anthropic SDKs type-check
// this field, so an unknown value can break client error handling.
func anthropicErrorType(errType, code string) string {
	// Prefer mapping by the most specific signal available.
	switch code {
	case "rate_limit_exceeded", "rate_limit_error":
		return "rate_limit_error"
	case "context_length_exceeded":
		return "invalid_request_error"
	case "content_filter":
		return "invalid_request_error"
	case "unsupported_feature":
		return "invalid_request_error"
	case "insufficient_quota", "budget_exhausted", "insufficient_credits":
		return "invalid_request_error"
	case "authentication_error", "upstream_credential_invalid":
		return "authentication_error"
	case "permission_error", "blocked", "security_violation":
		return "permission_error"
	case "not_found", "model_not_found", "model_deprecated":
		return "not_found_error"
	case "overloaded_error", "provider_error":
		return "overloaded_error"
	}
	switch errType {
	case "authentication_error":
		return "authentication_error"
	case "permission_error", "security_violation":
		return "permission_error"
	case "rate_limit_error":
		return "rate_limit_error"
	case "invalid_request_error":
		return "invalid_request_error"
	}
	// server_error / internal_error / provider_error / fallback → overloaded.
	return "overloaded_error"
}

// writeErrorAnthropic emits the Anthropic-shaped error envelope
//
//	{"type":"error","error":{"type":<t>,"message":<msg>}}
//
// which Anthropic SDKs parse natively. The gateway historically emitted
// the OpenAI shape {"error":{message,type,code,...}} for every protocol;
// Anthropic clients received an envelope their SDK could not type-check.
//
// request_id is surfaced via the X-Request-Id response header (set by the
// caller / middleware) rather than in the body, matching Anthropic's own API.
func writeErrorAnthropic(w http.ResponseWriter, status int, requestID, msg, errType, code string, debug map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(status)
	errObj := map[string]any{
		"type":    anthropicErrorType(errType, code),
		"message": msg,
	}
	if debug != nil {
		// Preserve gateway_debug for operator diagnostics (Anthropic clients
		// ignore unknown fields in the error object).
		errObj["gateway_debug"] = debug
	}
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": errObj,
	})
}

// writeErrorJSONWithKindProto dispatches to the Anthropic envelope when the
// client speaks the Anthropic protocol, otherwise to the OpenAI envelope.
// This is the protocol-aware entry point for the execution-failure path
// (Exhausted branch) where the gateway has already committed to a request
// and the SDK error-parsing behavior matters most.
func writeErrorJSONWithKindProto(proto string, w http.ResponseWriter, status int, requestID, msg, errType, code, kind string, debug map[string]any) {
	if proto == "anthropic" {
		writeErrorAnthropic(w, status, requestID, msg, errType, code, debug)
		return
	}
	writeErrorJSONWithKind(w, status, requestID, msg, errType, code, kind, debug)
}

// writeErrorJSONWithDebugProto is the protocol-aware variant of
// writeErrorJSONWithDebug (no "kind" field). Used at Exhausted-branch sites
// that predate the kind field.
func writeErrorJSONWithDebugProto(proto string, w http.ResponseWriter, status int, requestID, msg, errType, code string, debug map[string]any) {
	if proto == "anthropic" {
		writeErrorAnthropic(w, status, requestID, msg, errType, code, debug)
		return
	}
	writeErrorJSONWithDebug(w, status, requestID, msg, errType, code, debug)
}

// mapExecuteErrorToKind (Step 6, 2026-06-18) maps an exhausted
// ExecuteError to the client-visible "kind" field. The logic prefers
// the executor's recorded LastKind when set, and falls back to a small
// lookup table for the cases where LastKind is empty (e.g. no
// candidates returned at all from the router).
//
// Returns "" when no kind can be determined (caller should omit the
// header and the field).
func mapExecuteErrorToKind(err *executors.ExecuteError) string {
	if err == nil {
		return ""
	}
	if err.LastKind != "" {
		return string(err.LastKind)
	}
	if err.Tried == 0 {
		return "no_candidates"
	}
	return "unknown"
}

// errorKindOrFallback (2026-06-20) returns the real underlying error
// kind for request_logs.error_kind. Falls back to "model_not_found"
// when the kind is empty or "unknown" so we never write a misleading
// empty/garbage value to the database. The HTTP error.code is handled
// separately (see serveWithExecutor's Exhausted branch) and stays
// "model_not_found" for backward compatibility.
func errorKindOrFallback(kind string) string {
	if kind == "" || kind == "unknown" {
		return "model_not_found"
	}
	return kind
}

// extractUpstreamError (2026-06-23 P0) walks an error chain looking for
// an *upstream.Error. Used by the relay handler to surface the vendor's
// actual response body (and HTTP status) into request_logs so that
// transient/5xx failures become diagnostically useful. Walks Unwrap
// because the routing layer wraps upstream errors with fmt.Errorf("%w",
// ...) for retry/exhaustion tracking.
//
// Returns (error, true) on the first *upstream.Error found, else
// (nil, false) — callers MUST handle the false case gracefully.
func extractUpstreamError(err error) (*upstreampkg.Error, bool) {
	if err == nil {
		return nil, false
	}
	for cur := err; cur != nil; cur = errors.Unwrap(cur) {
		if ue, ok := cur.(*upstreampkg.Error); ok {
			return ue, true
		}
	}
	return nil, false
}

// classifyUpstreamCredentialFailure (2026-07-12) maps an upstream auth /
// quota failure to the dedicated client-facing error code, i18n message
// key, HTTP status, and OpenAI-style error type. The whole purpose of
// this helper is to make "the upstream rejected OUR credential" stand
// out from "the upstream had a transient 5xx" — operations staff need to
// be able to grep request_logs.error_kind and immediately tell the two
// apart.
//
// Returns ("", "", 0, "") when the failure is NOT a credential problem
// (caller should fall back to the generic provider_error path). This
// keeps the helper non-invasive: only the three credential-related
// upstream kinds are intercepted.
//
// Mapping:
//
//	KindAuth            → upstream_credential_invalid / 502 / authentication_error
//	KindAuthRevoked     → upstream_credential_revoked / 502 / authentication_error
//	KindQuotaPeriodic   → upstream_quota_periodic     / 502 / insufficient_quota
//	KindQuotaPermanent  → upstream_quota_permanent    / 502 / insufficient_quota
//	KindQuotaBalance    → upstream_quota_balance      / 502 / insufficient_quota
//	KindQuota           → upstream_quota_generic      / 502 / insufficient_quota
//
// Every kind maps to 502: the failure is the gateway's own upstream
// credential, not the caller's key, so echoing the upstream's 401/402
// would tell the client to fix something it does not control. The
// distinction the caller acts on (key invalid vs. quota exhausted) is
// carried by error.code and the localized message, not the status.
//
// KindQuotaPeriodic is a resettable usage window rather than a spent
// account, so it gets its own code: operators must not top up or rotate
// a credential that will recover on its own at the next window reset.
//
// 2026-08-09 fix: KindQuota and KindQuotaBalance were missing. Both are
// IsCredentialFatal (errorsx/classify.go), and KindQuota is what a bare
// HTTP 402 with no recognizable body produces. Because this helper returned
// ("", "", 0, "") for them, the caller fell through to the generic
// all-candidates-failed branch and reported a genuine balance-exhaustion
// event to the client as 503 model_not_found "No available provider" — an
// operator reading that would go looking for a missing model instead of a
// spent account. Balance keeps a distinct code from Permanent because the
// remedy differs: top up the account vs. rotate the credential.
//
// The switch must stay in sync with errorsx.IsCredentialFatal; the coverage
// test in upstream_credential_error_test.go asserts that every kind that
// function accepts is mapped here.
func classifyUpstreamCredentialFailure(kind errorsx.ErrorKind, upstreamStatus int) (code, i18nKey string, httpStatus int, errType string) {
	switch kind {
	case errorsx.KindAuth:
		_ = upstreamStatus // status reserved for future use (e.g. 401 vs 403 split)
		return "upstream_credential_invalid", i18n.MsgUpstreamCredentialInvalid, http.StatusBadGateway, "authentication_error"
	case errorsx.KindAuthRevoked:
		return "upstream_credential_revoked", i18n.MsgUpstreamCredentialRevoked, http.StatusBadGateway, "authentication_error"
	case errorsx.KindQuotaPeriodic:
		return "upstream_quota_periodic", i18n.MsgUpstreamQuotaPeriodic, http.StatusBadGateway, "insufficient_quota"
	case errorsx.KindQuotaPermanent:
		return "upstream_quota_permanent", i18n.MsgUpstreamQuotaPermanent, http.StatusBadGateway, "insufficient_quota"
	case errorsx.KindQuotaBalance:
		return "upstream_quota_balance", i18n.MsgUpstreamQuotaBalance, http.StatusBadGateway, "insufficient_quota"
	case errorsx.KindQuota:
		// Bare quota signal with no periodic/permanent/balance evidence.
		return "upstream_quota_generic", i18n.MsgUpstreamQuotaGeneric, http.StatusBadGateway, "insufficient_quota"
	}
	return "", "", 0, ""
}

// defaultOverloadRetryAfterSeconds is the client-facing wait advertised when
// every candidate returned an overload-shaped 5xx and the upstream gave no
// Retry-After of its own. Five seconds is a conservative default that:
//   - matches the few seconds the apiclaude.cc relay takes to clear a load
//     spike (single sample at 2026-08-08: same credential succeeded on the
//     retry ~9.6s after the 502; n=1, not a statistical claim);
//   - is short enough that a retried caller is unlikely to hit the same
//     overload window again, but long enough to absorb the per-credential
//     backoff schedule (default 500ms * 2^attempt = 1s at attempt 1).
//
// If a tighter empirical value is needed, change this constant and rerun
// the prewarmed / non-prewarmed overload tests in PrestreamExhaustion.
const defaultOverloadRetryAfterSeconds = 5

// overloadRetryAfterSeconds returns the Retry-After value, in seconds, for an
// exhausted overload failure. It prefers the upstream's own hint and bounds it
// to the in-flight ceiling so one malformed header cannot advertise a
// multi-day wait to the client. Sub-second hints round up to 1 rather than
// down to 0, which would invite an immediate hot retry.
func overloadRetryAfterSeconds(err error) int {
	ue, ok := extractUpstreamError(err)
	if !ok || ue.RetryAfter <= 0 {
		return defaultOverloadRetryAfterSeconds
	}
	capped := upstreampkg.ClampInFlightRetryAfter(ue.RetryAfter)
	if capped <= 0 {
		return defaultOverloadRetryAfterSeconds
	}
	secs := int((capped + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return secs
}

// extractUpstreamReason returns the human-readable upstream rejection
// message from an executor error. It first tries to parse the JSON body
// of a wrapped *upstreampkg.Error (looking for error.message,
// error.error.message, or message), then falls back to the raw error
// string. The result is capped at 200 characters.
func extractUpstreamReason(err error) string {
	if ue, ok := extractUpstreamError(err); ok && len(ue.Body) > 0 {
		var parsed struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(ue.Body, &parsed) == nil {
			if parsed.Error != nil && parsed.Error.Message != "" {
				msg := parsed.Error.Message
				if len(msg) > 200 {
					msg = msg[:200]
				}
				return msg
			}
			if parsed.Message != "" {
				msg := parsed.Message
				if len(msg) > 200 {
					msg = msg[:200]
				}
				return msg
			}
		}
		// JSON parse failed — return raw body preview.
		raw := string(ue.Body)
		if len(raw) > 200 {
			raw = raw[:200]
		}
		return raw
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// captureAttemptBody reads the request body (capped at 1MB) into bodyOut
// and extracts the client_model from the JSON.  It does NOT close the
// body — the caller (serveWithExecutor) owns that responsibility via
// its own defer.
//
// 2026-06-20 audit fix v2: When the body is captured but has no
// "model" field (e.g. /v1/messages client omitted model, or body is
// `{}`), set client_model to "<unknown>" so request_logs never shows
// a blank client_model alongside a non-empty request_body. Without
// this, the operator cannot tell whether the body was empty OR the
// client simply forgot the model field — both look like an empty
// client_model. Setting "<unknown>" makes it explicit that the body
// was received but model extraction failed.
func captureAttemptBody(r *http.Request, bodyOut *[]byte, modelOut *string) {
	if bodyOut == nil || r == nil || r.Body == nil {
		return
	}
	if len(*bodyOut) > 0 {
		return
	}
	const maxBody = 1 << 20 // 1MB
	buf, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil || len(buf) == 0 {
		return
	}
	*bodyOut = buf
	if modelOut == nil {
		return
	}
	// Only attempt model extraction if modelOut is still empty
	if *modelOut != "" {
		return
	}
	var probe struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(buf, &probe)
	if probe.Model != "" {
		*modelOut = probe.Model
		return
	}
	// Body captured but no model field found — record as <unknown>
	// so request_logs.client_model is never blank when body is set.
	// This distinguishes "empty body" from "body present but no
	// model field" — both look the same otherwise.
	*modelOut = "<unknown>"
}

// emitTuningSignal computes the implicit feedback signal for an auto-route
// request and enqueues it for async batched write to tuning_signals.
//
// Only called for auto-route requests (model="auto"). All scoring is
// done in-process (no DB lookup on the hot path) to keep latency <1ms.
// The DB insert happens asynchronously in the tuning writer goroutine.

// buildHandoffAuthHeader constructs the Authorization header for the
// handoff follow-up request, preferring the caller's header and falling
// back to the system key when the request had no usable Authorization
// header (e.g. browser session that streamed without an API key, or when
// the Authorization header was rejected by the auth layer). Returns ""
// only when no auth source is available, which downstream treat as
// "no auth" and they will surface a 401 missing_key error in the log.
func (h *ChatHandler) buildHandoffAuthHeader(r *http.Request) string {
	if r == nil {
		return h.handoffFallbackAuth
	}
	if v := strings.TrimSpace(r.Header.Get("Authorization")); v != "" {
		return v
	}
	if h.handoffFallbackAuth != "" {
		return h.handoffFallbackAuth
	}
	if sk := os.Getenv("LLM_GATEWAY_ADMIN_API_KEY"); sk != "" {
		return "Bearer " + sk
	}
	return ""
}

func (h *ChatHandler) emitTuningSignal(reqLog *telemetry.RequestLogEntry, success bool, latencyMs int) {
	if h == nil || h.telemetryClient == nil {
		return
	}

	classifier := "heuristic"
	if reqLog.AutoDecision != nil {
		var d struct {
			Classifier string `json:"classifier"`
		}
		if err := json.Unmarshal([]byte(*reqLog.AutoDecision), &d); err == nil && d.Classifier != "" {
			classifier = d.Classifier
		}
	}

	taskType := ""
	if reqLog.TaskType != nil {
		taskType = *reqLog.TaskType
	}
	chosenModel := ""
	if reqLog.OutboundModel != nil {
		chosenModel = *reqLog.OutboundModel
	}
	confidence := 0.0
	if reqLog.AutoConfidence != nil {
		confidence = *reqLog.AutoConfidence
	}

	latencyScore := 0.5
	if latencyMs > 0 && latencyMs < 30000 {
		ratio := float64(latencyMs) / 30000.0
		if ratio > 1 {
			ratio = 1
		}
		latencyScore = 1.0 - ratio
	}

	costScore := 0.5
	costUSD := 0.0
	if reqLog.CostUSD != nil {
		costUSD = *reqLog.CostUSD
	}
	if costUSD > 0 {
		ratio := costUSD / 0.01
		if ratio > 1 {
			ratio = 1
		}
		costScore = 1.0 - ratio
	}

	drift := false
	quality := telemetry.ComputeTuningSignalQuality(success, latencyMs, 0, costUSD, 0, drift)

	sessionID := ""
	if reqLog.GwSessionID != nil {
		sessionID = *reqLog.GwSessionID
	}

	var payload []byte
	if reqLog.AutoDecision != nil {
		payload = []byte(*reqLog.AutoDecision)
	}

	promptTokens, completionTokens := 0, 0
	if reqLog.PromptTokens != nil {
		promptTokens = *reqLog.PromptTokens
	}
	if reqLog.CompletionTokens != nil {
		completionTokens = *reqLog.CompletionTokens
	}

	sig := telemetry.TuningSignal{
		RequestID:        reqLog.RequestID,
		SessionID:        sessionID,
		TaskType:         taskType,
		Classifier:       classifier,
		Confidence:       confidence,
		ChosenModel:      chosenModel,
		SuccessScore:     boolToFloat(success),
		LatencyScore:     latencyScore,
		CostScore:        costScore,
		DriftFlag:        drift,
		QualityScore:     quality,
		LatencyMs:        latencyMs,
		CostUSD:          costUSD,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		SignalPayload:    payload,
		Strategy:         string(autoroute.AssignStrategy(reqLog.RequestID)),
	}
	telemetry.WriteTuningSignal(sig)
}

func boolToFloat(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}

// streamChunksSentFromLogCtx returns the count of stream chunks sent for
// this request, defaulting to 0 when logCtx is nil (non-streaming path or
// pre-init failure). Added 2026-07-01 to satisfy the NOT NULL constraint
// on request_logs.stream_chunks_sent (migration 320) — without this
// helper, BuildSuccessEntry used to leave reqLog.StreamChunksSent nil,
// causing INSERT/UPDATE to fail with SQLSTATE 23502 and stopping all
// new rows from being written on 184.
func streamChunksSentFromLogCtx(c *RequestLogContext) int {
	if c == nil {
		return 0
	}
	return max(c.StreamChunksSentValue(), 0)
}

// StreamChunksSentFromLogCtxForTest is the test-only exported alias of
// streamChunksSentFromLogCtx. The unexported version is kept because the
// production call site is package-internal; tests outside the package
// would otherwise need access to RequestLogContext internals.
func StreamChunksSentFromLogCtxForTest(c *RequestLogContext) int {
	return streamChunksSentFromLogCtx(c)
}

// streamChunkErrorsFromLogCtx mirrors streamChunksSentFromLogCtx for the
// stream_chunk_errors column. Same NOT NULL rationale applies.
func streamChunkErrorsFromLogCtx(c *RequestLogContext) int {
	if c == nil {
		return 0
	}
	return max(c.StreamChunkErrorsValue(), 0)
}

// StreamChunkErrorsFromLogCtxForTest is the test-only exported alias of
// streamChunkErrorsFromLogCtx, mirroring StreamChunksSentFromLogCtxForTest.
func StreamChunkErrorsFromLogCtxForTest(c *RequestLogContext) int {
	return streamChunkErrorsFromLogCtx(c)
}

// requestBytesFromLogCtx returns the request body size from logCtx.
// Returns nil if logCtx is nil or RequestBodySize is 0 (to avoid storing
// zero values in request_logs.request_bytes for requests without bodies).
// 2026-07-25: Used for Redis real-time body size statistics.
func requestBytesFromLogCtx(c *RequestLogContext) *int {
	if c == nil || c.RequestBodySize <= 0 {
		return nil
	}
	return intPtr(c.RequestBodySize)
}

// resolveGatewayVersion reads the build version from version.json (SSOT).
// Falls back to GIT_SHA env var, then to "unknown" if neither is available.
//
// 2026-07-14: 移除对旧 VERSION 文件的依赖，统一读 version.json。
func resolveGatewayVersion() string {
	candidates := []string{
		"/opt/llm-gateway-go/version.json",
		"version.json",
	}
	for _, path := range candidates {
		if raw, err := os.ReadFile(path); err == nil {
			var v struct {
				Version string `json:"version"`
				GitSHA  string `json:"git_sha"`
			}
			if json.Unmarshal(raw, &v) == nil {
				if v.Version != "" {
					if v.GitSHA != "" {
						return v.Version + "-" + v.GitSHA
					}
					return v.Version
				}
			}
		}
	}
	if sha := strings.TrimSpace(os.Getenv("GIT_SHA")); sha != "" {
		return "1.0.0-" + sha + "-" + time.Now().UTC().Format("2006-01-02")
	}
	return "0.2.0-unknown"
}

// detectEmptyStreamResponse checks if a streaming response is effectively empty.
// Returns true if all of the following are true:
//   - Very few chunks (<= 3)
//   - Zero completion tokens
//   - No response preview content
//   - No upstream finish_reason (normal finish would have "length" or "stop")
//
// This pattern indicates the upstream returned no actual content despite sending [DONE].
// Seen with Provider 18 (NVIDIA NIM) on large inputs (160k+ tokens).
func detectEmptyStreamResponse(m map[string]any, reqLog *telemetry.RequestLogEntry) bool {
	// Check 1: Few chunks (<= 3, tightened threshold)
	chunkCount, ok := m["stream_chunk_count"].(int)
	if !ok || chunkCount > 3 {
		return false // More than 3 chunks likely has content
	}

	// Check 2: Zero completion tokens
	hasTokens := reqLog.CompletionTokens != nil && *reqLog.CompletionTokens > 0
	if hasTokens {
		return false // Has tokens means not empty
	}

	// Check 3: Structured tool calls are valid content even when the
	// assistant has no text preview. This is common for tool-only streams
	// whose first few deltas contain an id/name/arguments split across chunks.
	if toolCalls, ok := m["tool_calls"]; ok && hasStructuredToolCalls(toolCalls) {
		return false
	}

	// Check 4: No content preview (check both reqLog and capture)
	hasPreview := reqLog.ResponsePreview != nil && *reqLog.ResponsePreview != ""
	if hasPreview {
		return false // Has content means not empty
	}

	// Check stream_text_content from capture as backup
	if v, ok := m["stream_text_content"].(string); ok && strings.TrimSpace(v) != "" {
		return false // Has text content means not empty
	}

	// Check 4: No upstream finish_reason
	// Normal successful completion should have "stop" or "length"
	// NOTE: reqLog.UpstreamFinishReason is set AFTER emitTelemetry calls this,
	// so we read from the summary map m directly. This fixes the timing bug
	// where a valid stream with finish_reason (e.g. "stop", "tool_calls") and
	// few chunks was misclassified as empty (Provider 18 NVIDIA NIM).
	if v, ok := m["upstream_finish_reason"].(string); ok && v != "" {
		hasFinishReason := v == "stop" || v == "length" || v == "tool_calls"
		if hasFinishReason {
			return false // Normal finish means not empty
		}
	}

	// All empty indicators present - this is truly an empty response
	return true
}

// estimatePromptTokensFromBytes gives a rough lower-bound estimate of the
// prompt token count implied by a request body of the given byte size. The
// ratio (1 token ≈ 4 bytes) is deliberately conservative for mixed
// English/CJK + JSON overhead: it tends to *over-estimate* tokens for pure
// English (where 1 token ≈ 4-5 bytes is closer) and *under-estimate* for
// CJK-heavy bodies (where 1 token ≈ 1.5-2 bytes). For the context-loss
// detector we only need an order-of-magnitude floor; the 5% threshold in
// detectUpstreamContextLoss is wide enough to absorb this variance.
func estimatePromptTokensFromBytes(bodyBytes int) int {
	if bodyBytes <= 0 {
		return 0
	}
	return bodyBytes / 4
}

// detectUpstreamContextLoss reports whether the upstream silently dropped the
// request context: it accepted a large body (HTTP 200, clean stream close)
// but reported prompt_tokens that are a tiny fraction of the body's implied
// token count, then returned only a handful of completion tokens. This is
// the apiclaude.cc 2026-08-04 failure mode (request e8bf0d5fc726) and, to the
// user, is indistinguishable from a broken model.
//
// The detector runs after detectEmptyStreamResponse and only on rows still
// marked success, so a true empty_response (0 completion tokens) is never
// double-counted here.
//
// Threshold rationale (calibrated against the e8bf0d5fc726 incident and its
// same-session siblings):
//   - body >= 50 KB before we engage: small requests legitimately yield few
//     tokens and must not trip a context-loss alarm.
//   - prompt_tokens < estimated/20 (i.e. < 5% of the body's implied tokens):
//     the incident hit 337 vs an ~235K estimate (≈0.14%); healthy siblings
//     sat at 256K–305K (≈100%+). 5% leaves a wide safety margin.
//   - completion_tokens < 50: a model that actually read the context would
//     typically produce a substantial reply; <50 tokens after a big prompt
//     is the signature of an answered-but-wrong-context reply.
//   - upstream finish_reason is a "clean" terminator (end_turn/stop/length/
//     tool_calls): this is precisely what makes the failure deceptive — the
//     stream closes normally, so the network/timeout detectors never fire.
//     We read from the summary map m (not reqLog.UpstreamFinishReason) for
//     the same timing reason as detectEmptyStreamResponse (the field is set
//     after emitTelemetry).
func detectUpstreamContextLoss(m map[string]any, reqLog *telemetry.RequestLogEntry) bool {
	if reqLog == nil || reqLog.RequestBytes == nil {
		return false
	}
	bodyBytes := *reqLog.RequestBytes
	if bodyBytes < 50*1024 { // only engage for sizeable requests
		return false
	}

	promptTokens := 0
	if reqLog.PromptTokens != nil {
		promptTokens = *reqLog.PromptTokens
	}
	if promptTokens <= 0 {
		return false // no usage reported — can't judge a mismatch
	}

	estimated := estimatePromptTokensFromBytes(bodyBytes)
	if estimated <= 0 || promptTokens >= estimated/20 {
		return false // upstream reported a plausible share of the context
	}

	completionTokens := 0
	if reqLog.CompletionTokens != nil {
		completionTokens = *reqLog.CompletionTokens
	}
	if completionTokens >= 50 {
		return false // substantial reply — assume the context was honoured
	}

	// Require a clean terminator: this fault class closes the stream
	// normally, which is why no interruption detector catches it.
	reason := ""
	if v, ok := m["upstream_finish_reason"].(string); ok {
		reason = v
	}
	switch reason {
	case "end_turn", "stop", "length", "tool_calls":
		// clean close — the deceptive case we are looking for
	default:
		return false
	}

	return true
}

func hasStructuredToolCalls(value any) bool {
	switch calls := value.(type) {
	case []map[string]any:
		return len(calls) > 0
	case []any:
		return len(calls) > 0
	default:
		return false
	}
}

// 2026-06-28: 为 session-audit hook.CheckV1 提供 user content。
// 返回 "" 表示 body 不可解析 / 找不到 user message（hook 收到空 content
// 会降级 Pass，不阻断主流程）。
//
// The bool result reports whether the body itself failed to parse, as opposed
// to parsing cleanly with no user message. Both yield "" and both make the
// audit auto-pass, so without the distinction an audit BYPASS is
// indistinguishable from a clean pass. Callers must record the parse failure.
func extractFirstUserMessage(bodyBytes []byte) (string, bool) {
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return "", false
	}
	for _, m := range body.Messages {
		if m.Role == "user" {
			// content 可以是 string 或 []contentPart；用 extractMessageText 统一处理。
			var anyContent any
			if err := json.Unmarshal(m.Content, &anyContent); err == nil {
				return extractMessageText(anyContent), true
			}
			return string(m.Content), true
		}
	}
	return "", true
}

// 2026-07-15: clientprofile helper —— 从 RequestLogContext 构造 SessionContext 并 emit。
//
// 镜像 clientprofile.EventEmitter.EmitRequestCompleted 的入参语义：
//   - identityHash: meta.IdentityHash（identity 域）
//   - tokens: prompt + completion
//   - latencyMs: reqLog.LatencyMs 或 logCtx.LatencyMs()
//   - success: reqLog.Success
//
// 失败仅日志，不阻塞主请求流。
func emitProfileFromLogCtx(emitter interface {
	EmitRequestCompleted(ctx context.Context, sc *session.SessionContext, identityHash string, success bool, tokensUsed int, latencyMs int64) error
}, logCtx *RequestLogContext, reqLog *telemetry.RequestLogEntry, success bool) {
	if emitter == nil || logCtx == nil {
		return
	}
	tokens := 0
	if reqLog != nil {
		if reqLog.PromptTokens != nil {
			tokens += *reqLog.PromptTokens
		}
		if reqLog.CompletionTokens != nil {
			tokens += *reqLog.CompletionTokens
		}
	}
	latency := int64(logCtx.LatencyMs())
	sc := &session.SessionContext{
		TenantID:      logCtx.sessionTenantID(),
		SessionID:     logCtx.sessionID(),
		RequestID:     logCtx.RequestID,
		ClientModel:   logCtx.ClientModel,
		UpstreamModel: logCtx.OutboundModel,
	}
	identityHash := logCtx.meta.IdentityHash
	if err := emitter.EmitRequestCompleted(context.Background(), sc, identityHash, success, tokens, latency); err != nil {
		slog.Debug("profile emitter: EmitRequestCompleted failed",
			"request_id", logCtx.RequestID, "error", err)
	}
}

// sessionTenantID / sessionID 私有 helper：从 RequestLogContext 派生最小 SessionContext。
func (c *RequestLogContext) sessionTenantID() string {
	if c.KeyInfo != nil && c.KeyInfo.TenantID != "" {
		return c.KeyInfo.TenantID
	}
	return "default"
}

func (c *RequestLogContext) sessionID() string {
	sid, _ := c.SessionTask()
	return sid
}
