package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/compressor"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/credentialstate"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/identity"
	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/memora"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/resolve"
	"github.com/kaixuan/llm-gateway-go/sessions"
	"github.com/kaixuan/llm-gateway-go/telemetry"
	"github.com/kaixuan/llm-gateway-go/transform"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// Default minimax-m3 catalog context window (512K). DB models_canonical is SSoT for runtime;
// keep in sync via deploy/sql/20260613-minimax-m3-context-window-512k.sql.
const MinimaxM3ContextWindow = 512_000

const maxBodySize = 32 << 20

// providerResolver is the subset of *provider.Client the routing executor
// depends on. Defined as an interface so compaction tests can swap in a
// stub without booting the DB pool.
type providerResolver interface {
	Enabled() bool
	GetCandidates(ctx context.Context, model, profile string) ([]provider.Candidate, *provider.Policy, error)
}

type NormalizerFunc func(chunk []byte, isStream bool) []byte

type StreamOutcome = struct {
	Interrupted bool
	Reason      string
	Resumable   bool // Whether the stream can be resumed with a different credential
	ChunkCount  int  // Number of chunks sent before interruption
}

type StreamHandler func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, norm NormalizerFunc, capture *audit.StreamCapture, toolsRequested bool) StreamOutcome

type StreamWrapperFunc func(w http.ResponseWriter, resp *http.Response, norm NormalizerFunc, capture *audit.StreamCapture) StreamOutcome

// AnthropicPassthroughFunc is the signature for the Q4 Anthropic SSE
// AnthropicPassthroughFunc forwards Anthropic-format SSE upstream to the
// client unchanged (Q4 path: anthropic client → anthropic upstream).
// pc is an optional pending-store capturer (Track C C5, 2026-06-21)
// that records the SSE body so it can be replayed on client reconnect.
// Wired from main.go so the routing package does not import relay.
type AnthropicPassthroughFunc func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, pc any) StreamOutcome

// ChatToAnthropicFunc converts an OpenAI chat completions body to
// Anthropic Messages format. Wired from main.go so the routing
// package does not import relay.
type ChatToAnthropicFunc func(body []byte) ([]byte, error)

// AnthropicToOpenAIFunc converts an Anthropic Messages body to OpenAI
// chat completions format. Wired from main.go so the routing
// package does not import relay.
type AnthropicToOpenAIFunc func(body []byte) ([]byte, error)

// AnthropicToOpenAISSEFunc is the streaming counterpart of
// AnthropicToOpenAIFunc: reads Anthropic-format SSE upstream and
// writes OpenAI-format SSE chunks to w (Q3 path: openai client →
// anthropic upstream). pc is the optional pending-store capturer.
type AnthropicToOpenAISSEFunc func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, pc any) StreamOutcome

// AnthropicToChatResponseFunc is the non-stream counterpart that
// converts an Anthropic Messages JSON body into an OpenAI
// chat.completion JSON body. Wired from main.go.
type AnthropicToChatResponseFunc func(body []byte, clientModel string) ([]byte, error)

// SanitizeAnthropicToolsFunc strips OpenAI/custom tool type wrappers from
// an Anthropic Messages request body before forwarding to upstream.
type SanitizeAnthropicToolsFunc func(body []byte) []byte

// NormalizeOpenAIToolsFunc normalizes tools[] in a chat body to nested
// OpenAI-Chat shape.
type NormalizeOpenAIToolsFunc func(body []byte) []byte

// StripMinimaxFieldsFunc strips minimax-private top-level fields from
// a chat response body before it is returned to the client.
// Wired from main.go (relay.StripMinimaxFieldsBody).
type StripMinimaxFieldsFunc func(body []byte) []byte

// XMLCoerceNonStreamFunc transforms a non-streaming chat response body,
// rewriting any `<tool_call><function=...>` XML embedded in assistant
// `content` into structured OpenAI `tool_calls` entries.  The second
// argument is true when the original request body supplied a `tools` array.
// Implementations are expected to be no-ops when toolsRequested is false.
type XMLCoerceNonStreamFunc func(body []byte, toolsRequested bool) []byte

// 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
// QualityProcessNonStreamFunc is the per-provider tool_call quality
// post-processor. It returns the (possibly rewritten) body plus the
// four quality signals that the routing executor must propagate
// back into the ExecuteResult for emitTelemetry to persist on the
// request_log row.
//
// Implementations are expected to be no-ops when mode is "off" or
// empty (returning the original body and zero signals).
type QualityProcessNonStreamFunc func(body []byte, mode string) (outBody []byte, flags []string, fixActions []byte, score *float64)

// QualitySetModeFunc stamps the per-provider quality_fix_mode onto
// the upstream request context. relay/stream.go pulls it back out
// with qualityFixModeFromContext on every SSE line so the streaming
// path can run the same checks as the non-stream path.
type QualitySetModeFunc func(ctx context.Context, mode string) context.Context

// RequestLogEmitter (2026-06-20) is the minimum interface needed by
// runAsyncRetry to update request_logs when a backgrounded retry
// eventually succeeds. Implemented by *telemetry.Client in
// production. Using an interface (rather than a direct
// *telemetry.Client field) so tests can inject a mock without
// needing a real database.
type RequestLogEmitter interface {
	Enabled() bool
	EmitRequestLogUpdate(entry *telemetry.RequestLogEntry)
}

type Executor struct {
	Router        *Router
	Circuit       *circuit.Manager
	Limiter       *limiter.Limiter
	Pools         *pool.PoolManager
	Upstream      *upstreampkg.Client
	Normalize     NormalizerFunc
	StreamChat    StreamHandler
	// XMLCoerceNonStream post-processes a non-stream chat response body to
	// turn XML-style tool calls into structured tool_calls. Wired from
	// main.go (relay.coerceXMLToolCallsInChatResponse) so the routing
	// package does not need to import relay.
	XMLCoerceNonStream XMLCoerceNonStreamFunc
	// 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
	// QualityProcessNonStream is the per-provider tool_call quality
	// post-processor for non-stream responses. Wired from main.go
	// (relay.WrapQualityProcessNonStream) so the routing package
	// does not need to import relay.
	QualityProcessNonStream QualityProcessNonStreamFunc
	// QualitySetMode stamps the per-provider mode onto the upstream
	// request context. relay/stream.go reads it via
	// qualityFixModeFromContext. Wired from main.go
	// (relay.SetQualityFixModeOnContext wrapped as a func).
	QualitySetMode QualitySetModeFunc
	// AnthropicPassthroughStream is the Q4 Anthropic SSE forwarder with
	// side-channel audit capture. Wired from main.go (relay.StreamAnthropicPassthrough).
	AnthropicPassthroughStream AnthropicPassthroughFunc
	// ChatToAnthropic converts OpenAI chat body to Anthropic Messages
	// format. Used by executeAnthropic when ClientProtocol != "anthropic-messages".
	ChatToAnthropic ChatToAnthropicFunc
	// AnthropicToOpenAI converts Anthropic Messages body to OpenAI chat
	// format. Used by executeOpenAI when ClientProtocol == "anthropic-messages".
	AnthropicToOpenAI AnthropicToOpenAIFunc
	// AnthropicToOpenAIStream is the Q3 streaming counterpart of
	// AnthropicToOpenAI: reads Anthropic-format SSE upstream and writes
	// OpenAI-format SSE chunks to the client. Used by executeAnthropic
	// when ClientProtocol != "anthropic-messages" (openai client ->
	// anthropic upstream). Defaults to nil; when nil the Q3 stream
	// path falls back to PassthroughStream which the OpenAI client
	// can't parse.
	AnthropicToOpenAIStream AnthropicToOpenAISSEFunc
	// AnthropicToChatResponse is the Q3 non-stream counterpart:
	// converts an Anthropic Messages JSON body into an OpenAI
	// chat.completion JSON body. Used by executeAnthropic when
	// ClientProtocol != "anthropic-messages".
	AnthropicToChatResponse AnthropicToChatResponseFunc
	// SanitizeAnthropicTools strips invalid tool type fields from Anthropic
	// Messages bodies (Q3/Q4) before forwarding to minimax/anthropic upstream.
	SanitizeAnthropicTools SanitizeAnthropicToolsFunc
	// NormalizeOpenAITools coerces flat/anthropic tool defs to OpenAI-Chat shape.
	NormalizeOpenAITools NormalizeOpenAIToolsFunc
	// StripMinimaxFields strips minimax-private top-level fields
	// (nvext, base_resp, input_sensitive*, output_sensitive*) from
	// the chat response body before it is returned to the client.
	// Wired from main.go (relay.StripMinimaxFieldsBody).
	StripMinimaxFields StripMinimaxFieldsFunc
	Auditor       audit.Sink
	State         *credentialstate.Writer
	// Provider is the credential/candidate resolver. Typed as an interface
	// (defined in routing) so the compaction fallback tests can inject a
	// stub without standing up a real pgx pool. The concrete
	// *provider.Client satisfies the interface, so production wiring
	// is unchanged.
	Provider      providerResolver
	DB            *db.DB
	HeaderProfiles *HeaderProfileCache
	FpSlots          *credentialfpslot.Manager
	// PeakCollector records per-credential-model concurrency for the
	// auto-tune background worker. Nil disables the feature.
	PeakCollector interface {
		Acquire(credID int64, model string)
		Release(credID int64, model string)
	}
	// DisguisePool injects User-Agent / Accept-Language headers.
	// HeadersForSlot returns deterministic headers keyed by slot index
	// (same slot → same UA, every call). Headers is the random fallback
	// for stateless (no-slot) requests. Nil disables the feature.
	DisguisePool interface {
		Headers() map[string]string
		HeadersForSlot(slot int) map[string]string
		MaybeRotate()
	}

	StreamTimeout        time.Duration
	UpstreamTimeout      time.Duration
	StreamRetryThreshold int // Max chunks sent before stream becomes non-resumable (default 5)

	// MnfStreak tracks consecutive model_not_found occurrences per
	// (stickyKey, credentialID). When the count reaches
	// MnfStickyBreakThreshold, the sticky binding is deleted so the
	// next request re-picks. This is the client hot-path complement
	// to the background 3-strike consensus in bg/model_probe.go: the
	// background probe is authoritative for credential health, but
	// the streak breaker protects the user from being pinned to a
	// broken credential for the full 30-minute sticky TTL when the
	// upstream has clearly gone away. Nil disables the feature
	// (production default: enabled; tests may omit).
	MnfStreak               *MnfStreak
	MnfStickyBreakThreshold int  // default 3
	MnfStreakEnabled        bool // feature flag (env-gated by main.go)

	// BUG-4 fix (2026-06-18): mnf_cooling temporarily disables a
	// credential_model_binding when it accumulates too many
	// model_not_found errors in a short window. This prevents a
	// 0%-success credential from being repeatedly selected when
	// it's the only routable candidate.
	MnfCoolThreshold int // default 5, env LLM_GATEWAY_MNF_COOL_THRESHOLD
	MnfCoolMinutes   int // default 2, env LLM_GATEWAY_MNF_COOL_MINUTES

	// Round 47 compression v7 T16: the unified compression dispatcher
	// (mode=0/1/2). Built at startup by main.go from
	// LLM_GATEWAY_COMPRESSION_MODE + LLM_GATEWAY_COMPRESSION_WINDOW_FRACTION
	// env. Nil is allowed (treated as ModeOff) for tests and unconfigured
	// single-tenant installs.
	Compressor *compressor.Compressor

	// Memora is the optional context-compression oracle. When non-nil
	// and enabled, the executor (a) enqueues per-request writes to
	// Memora for later retrieval, and (b) on context-length overflow,
	// queries Memora for L1 session facts and rebuilds the body before
	// retrying. Nil means the entire Memora path is a no-op.
	Memora *memora.Client
	// MemoraSink is the async write buffer for Memora persistence.
	// When nil (or when Memora is disabled), enqueue calls are no-ops.
	// Wired from main.go alongside Memora; the sink owns its own worker
	// goroutines and graceful-shutdown lifecycle.
	MemoraSink *memora.Sink

	// PendingStore (Track C, 2026-06-18) is the durable cache for
	// client reconnect and vendor async retry. When set, the
	// executor can transparently demote a slow request to async
	// mode: if the synchronous candidate walk exceeds
	// AsyncShortTimeout (default 15s) without success, the
	// executor spawns a goroutine that continues trying the
	// remaining candidates with an independent context
	// (AsyncLongTimeout, default 300s), writes the eventual
	// outcome to PendingStore, and the handler returns 202 +
	// X-Gw-Pending so the client can poll
	// GET /v1/sessions/{id}/pending-response.
	//
	// Nil disables both async retry and the async branch — the
	// executor falls back to the existing synchronous exhaustion
	// path. Wired from main.go when Redis is available.
	PendingStore            *pending.Store
	AsyncShortTimeout       time.Duration
	AsyncLongTimeout        time.Duration
	AsyncMaxFallbackCreds   int  // cap on credential fallbacks in async goroutine

	// RequestLogEmitter (2026-06-20): optional hook that runAsyncRetry
	// calls when a backgrounded retry succeeds, so the original
	// request_logs row (stuck at "in_progress" because the sync phase
	// did not call emitTelemetry) gets corrected to "success".
	//
	// Without this hook, async-retry success leaves request_logs in
	// its post-sync state, and operators see "model_not_found" /
	// "in_progress" for requests that actually completed via the
		// async path. Wired from cmd/gateway/main.go after telemetryClient
		// is created. Nil is safe (preserves the pre-fix behavior).
		RequestLogEmitter RequestLogEmitter

		// SyncRetryTimeout (2026-06-21): when ALL candidates fail for a
		// non-streaming request, the executor keeps retrying candidates
		// synchronously for this duration before returning an error to
		// the client. During this period the HTTP connection is held
		// open, the client's context is respected (disconnect aborts the
		// loop via ctx.Done()), and the async fallback path is NOT
		// started. This prevents wasting tokens on retries that the
		// client can no longer consume.
		//
		// Default 120s (set in cmd/gateway/main.go). <= 0 disables sync
		// retry and preserves the old behavior (immediate async fallback
		// or synchronous exhaustion).
		SyncRetryTimeout time.Duration

		// asyncDepth is the recursion guard (Track C C4). The async
	// goroutine (runAsyncRetry) calls Execute again; we bump this
	// so shouldAsyncFallback returns false on the inner call.
	// Uses atomic.Int32 because the Executor is a singleton
	// shared across all request goroutines — a plain int would
	// race under concurrent requests.
	asyncDepth atomic.Int32

	// ProviderSettings (Phase 3.2, 2026-06-21): resolver for provider-level
	// setting overrides. When non-nil, the executor checks provider-specific
	// compression.mode, cache.enabled, and format_conversion.enabled before
	// applying global defaults. Wired from main.go.
	ProviderSettings interface {
		GetString(ctx context.Context, providerID int, key string) (string, bool)
		GetBool(ctx context.Context, providerID int, key string) (bool, bool)
		GetInt64(ctx context.Context, providerID int, key string) (int64, bool)
	}
}

func NewExecutor(
	router *Router,
	cm *circuit.Manager,
	lim *limiter.Limiter,
	pools *pool.PoolManager,
	upstream *upstreampkg.Client,
	normalize NormalizerFunc,
	streamChat StreamHandler,
	auditor audit.Sink,
) *Executor {
	if auditor == nil {
		auditor = &audit.LogSink{}
	}
	if normalize == nil {
		normalize = func(chunk []byte, isStream bool) []byte { return chunk }
	}
	return &Executor{
		Router:          router,
		Circuit:         cm,
		Limiter:         lim,
		Pools:           pools,
		Upstream:        upstream,
		Normalize:       normalize,
		StreamChat:      streamChat,
		Auditor:         auditor,
		StreamTimeout:   900 * time.Second,
		UpstreamTimeout: 120 * time.Second,
		StreamRetryThreshold: 5, // Default: allow stream failover if < 5 chunks sent
	}
}

type ExecParams struct {
	W             http.ResponseWriter
	R             *http.Request
	BodyBytes     []byte
	IsStream      bool
	SuppressSuccessWrite bool
	ClientModel   string
	OutboundModel string
	ClientID      identity.ClientIdentity
	Transform     *transform.TransformResult
	Resolution    *resolve.Resolution
	Candidates    []provider.Candidate
	Policy        *provider.Policy
	AuditBuilder  *audit.EventBuilder
	Capture       *audit.StreamCapture
	StreamWrapper StreamWrapperFunc
	// ToolsRequested indicates the upstream request body carried a non-empty
	// `tools` array. Some providers (Xiaomi MiMo, MiniMax M2.7) cannot emit
	// structured `tool_calls` and instead fall back to embedding
	// `<tool_call><function=...>...</tool_call>` XML inside the assistant
	// `content`. With this flag set, the stream and non-stream response
	// post-processors will coerce the XML into real `tool_calls` entries so
	// downstream agents (which only inspect the structured field) recognise
	// the call and dispatch the tool.
	ToolsRequested bool
	// ClientProtocol is the wire format the client used: "openai-completions"
	// for /v1/chat/completions, "anthropic-messages" for /v1/messages.
	// Empty defaults to "openai-completions". Used by executeAnthropic to
	// decide whether the body needs Q3 conversion (openai->anthropic).
	ClientProtocol string
	SessionKey     string
	StickyKey      string
	// KeyID is the API key ID from keyInfo.ID. Used for per-key concurrent limiting.
	KeyID int
	// KeyConcurrentLimit is the per-key concurrent limit from keyInfo.EffectiveConcurrent().
	// If 0, the per-key concurrent check is skipped.
	KeyConcurrentLimit int
	// TenantID is the tenant owning this request (from keyInfo.TenantID).
	// Round 47 (2026-06-18) compression v7 T13: used by enqueueMemoraWrite /
	// tryMemoraCompression to namespace Memora user_ids by tenant (per docs/
	// multi-tenant-standards.md §3.2 Pattern A). Empty means single-tenant
	// mode (legacy "default" tenant); the Memora user_id falls back to the
	// pre-v7 "k:<api_key_id>:<task_id>" format so existing tests stay green.
	TenantID string
}

type ExecuteResult struct {
	Response  *http.Response
	Candidate provider.Candidate
	LatencyMs int
	RequestBody []byte
	ResponseBody []byte
	Trace     *Trace
	// Round 47 compression v7 T-NEW-2: optional compression event captured
	// by handleContextLengthRecovery. Populated when a 4xx recovery rewrote
	// the body (mechanical trim / memora L1 / LLM summary). nil otherwise.
	//
	// relay/handler.go emitTelemetry reads these fields and writes them
	// into request_logs.compression_reason / compression_strategy /
	// compression_meta so operators can SQL-trace the parent-child chain
	// per v7 §6.
	CompressionReason   *string
	CompressionStrategy *string
	CompressionMeta     []byte // JSON-encoded v7 §3.2 schema
	// ParentRequestID is the pre-compression request_id. Populated when
	// the retry leg (after 4xx recovery) is treated as a child of the
	// original attempt by the executor. Currently we emit a single
	// request_id per logical call (the retry reuses the same id), so this
	// stays nil — kept here so the v7 §6 "single-level chain" invariant
	// can be enforced in a future change without refactoring the struct.
	ParentRequestID *string

	// 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
	// QualityFlags is the per-request list of detected tool_call issues
	// (empty_tool_name, duplicate_tool_call_id, …). QualityFixActions
	// is the JSON-encoded per-flag {detected, renamed, dropped} tally.
	// QualityScore is 0..1 from computeScore. All three are populated
	// only when the chosen provider's quality_fix_mode is non-'off'.
	QualityFlags      []string
	QualityFixActions []byte // JSONB: {"empty_tool_name":{"detected":2,"renamed":1},...}
	QualityScore      *float64
}

type AttemptRecord struct {
	ProviderID   int              `json:"provider_id"`
	CredentialID int              `json:"credential_id"`
	RawModel     string           `json:"raw_model"`
	Kind         errorsx.ErrorKind `json:"kind"`
	Reason       string           `json:"reason,omitempty"`
}

type ExecuteError struct {
	LastErr   error
	Tried     int
	Exhausted bool
	Trace     *Trace
	Attempts  []AttemptRecord
	LastKind  errorsx.ErrorKind
}

// Trace records the per-candidate decision points during routing/execution.
// It is rendered as the `decision_trace` jsonb column in routing_decision_log
// so the admin UI can show planned candidates, blocked candidates, and the
// final chosen credential.
type Trace struct {
	PlannedCandidates []TraceCandidate `json:"planned_candidates,omitempty"`
	BlockedCandidates []TraceCandidate `json:"blocked_candidates,omitempty"`
	Chosen            *TraceCandidate  `json:"chosen,omitempty"`
	FailureReason     string           `json:"failure_reason,omitempty"`
}

type TraceCandidate struct {
	ProviderID   int    `json:"provider_id"`
	CredentialID int    `json:"credential_id"`
	ProviderName string `json:"provider_name,omitempty"`
	RawModel     string `json:"raw_model,omitempty"`
	Tier         int    `json:"tier,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

func (e *ExecuteError) Error() string {
	if e.LastErr != nil {
		return fmt.Sprintf("all %d candidates failed: %v", e.Tried, e.LastErr)
	}
	return fmt.Sprintf("all %d candidates failed", e.Tried)
}

func (e *Executor) Execute(params *ExecParams) (*ExecuteResult, error) {
	candidates := e.Router.PlanCandidates(
		params.Candidates,
		e.stickyCredentialID(params.StickyKey),
		params.Policy,
		egressPref(params.Transform),
	)
	trace := &Trace{
		PlannedCandidates: make([]TraceCandidate, 0, len(params.Candidates)),
		BlockedCandidates: []TraceCandidate{},
	}
	for _, c := range params.Candidates {
		trace.PlannedCandidates = append(trace.PlannedCandidates, TraceCandidate{
			ProviderID:   c.ProviderID,
			CredentialID: c.CredentialID,
			RawModel:     c.RawModel,
			Tier:         c.Tier,
		})
	}
	if len(candidates) == 0 {
		trace.FailureReason = "no_candidates_from_router"
		// Aggregate why every input candidate was filtered out so the next
		// "every provider failed simultaneously" outage is diagnosable from
		// this single log line.
		reasonCounts := make(map[string]int, 8)
		for _, c := range params.Candidates {
			reason := c.UnavailableReason()
			if reason == "" {
				reason = "unknown"
			}
			reasonCounts[reason]++
		}
		slog.Warn("executor: no candidates after router",
			"input_candidates", len(params.Candidates),
			"client_model", params.ClientModel,
			"reasons", reasonCounts,
		)
		if params.AuditBuilder != nil {
			params.AuditBuilder.DecisionTrace(trace)
		}
		return nil, &ExecuteError{Tried: 0, Exhausted: true, Trace: trace}
	}

	holder := params.StickyKey
	if holder == "" {
		holder = params.R.Header.Get("X-Request-Id")
	}
	if e.FpSlots != nil && e.FpSlots.Enabled() {
		filtered := make([]provider.Candidate, 0, len(candidates))
		for _, cand := range candidates {
			if e.FpSlots.RoutingEligible(params.R.Context(), cand.CredentialID, cand.ConcurrencyLimit, holder) {
				filtered = append(filtered, cand)
			} else {
				slog.Info("cred_fp_slot prefilter skip",
					"credential_id", cand.CredentialID,
					"provider_id", cand.ProviderID,
				)
			}
		}
		candidates = filtered
		if len(candidates) == 0 {
			return nil, &ExecuteError{LastErr: fmt.Errorf("cred_fp_slot: all saturated"), Tried: 0, Exhausted: true}
		}
	}

	tTotal := time.Now()
	retryPerCred := params.Policy.RetryPerCredential
	var lastErr error
	var lastKind errorsx.ErrorKind
	var attempts []AttemptRecord
	tried := 0

	for _, cand := range candidates {
		tried++

		// Reset the stream capture for this candidate so textContent, chunk
		// count, checksum, and the done/interrupted flags from a prior
		// failed attempt do not leak into this attempt's metrics. Without
		// this reset, a credential failover mid-stream would produce an
		// audit row with merged data from both credentials and logically
		// inconsistent flags (interrupted=true && done=true).
		if params.IsStream && params.Capture != nil && tried > 1 {
			params.Capture.Reset()
		}

		var fpLease *credentialfpslot.Lease
		if e.FpSlots != nil && e.FpSlots.Enabled() {
			lease, ok := e.FpSlots.Acquire(params.R.Context(), cand.CredentialID, cand.ConcurrencyLimit, holder, "default")
			if !ok {
				slog.Info("cred_fp_slot saturated",
					"credential_id", cand.CredentialID,
					"provider_id", cand.ProviderID,
				)
				lastErr = fmt.Errorf("cred_fp_slot saturated for credential %d", cand.CredentialID)
				continue
			}
			fpLease = lease
		}

		if !e.Circuit.Allow(cand.ProviderID, cand.CredentialID) {
			slog.Debug("executor: circuit open, skipping candidate",
				"credential_id", cand.CredentialID,
				"provider_id", cand.ProviderID,
			)
			lastErr = fmt.Errorf("circuit open for credential %d", cand.CredentialID)
			if fpLease != nil {
				e.FpSlots.Release(params.R.Context(), fpLease)
			}
			continue
		}

		release, acquireErr := e.Limiter.AcquireAll(
			params.R.Context(),
			cand.ProviderID,
			cand.CredentialID,
			params.ClientID.IdentityHash,
			params.KeyID,
			params.KeyConcurrentLimit,
		)
		if acquireErr != nil {
			slog.Debug("executor: concurrency limit, skipping candidate",
				"credential_id", cand.CredentialID,
			)
			lastErr = acquireErr
			if fpLease != nil {
				e.FpSlots.Release(params.R.Context(), fpLease)
			}
			continue
		}

		// Track live peak concurrency for auto-tune.
		if e.PeakCollector != nil {
			e.PeakCollector.Acquire(int64(cand.CredentialID), cand.RawModel)
		}

		var execErr error
		var result *ExecuteResult
		switch cand.Protocol {
		case "anthropic-messages":
			// Q3 / Q4 path: handled by AnthropicExecutor in Phase 2.
			// Phase 1 returns a sentinel "not yet implemented" error so
			// the dispatcher is wired without behavior change for Q1/Q2.
			result, execErr = e.executeAnthropic(params, cand, retryPerCred, tTotal, fpLease)
		default:
			// Q1 / Q2 path: openai-completions, openai-responses, "".
			// All existing chat-style traffic goes through this branch;
			// no behavior change from the pre-split executor.
			result, execErr = e.executeOpenAI(params, cand, retryPerCred, tTotal, fpLease)
		}
		// Release peak tracking before the concurrency limiter so the
		// next sample run sees the post-release state.
		if e.PeakCollector != nil {
			e.PeakCollector.Release(int64(cand.CredentialID), cand.RawModel)
		}
		release()
		if fpLease != nil {
			e.FpSlots.Release(params.R.Context(), fpLease)
		}

		if execErr == nil {
			e.restoreCredentialState(params.R.Context(), cand.CredentialID)
			e.recordStickySuccess(params, cand.CredentialID)
			// Step 6 (2026-06-18): a successful response on this
			// credential clears its model_not_found streak. The next
			// request from this sticky session will not be tripped by
			// a stale counter from a prior intermittent failure.
			e.resetMnfStreak(params, cand.CredentialID)
			trace.Chosen = &TraceCandidate{
				ProviderID:   cand.ProviderID,
				CredentialID: cand.CredentialID,
				RawModel:     cand.RawModel,
				Tier:         cand.Tier,
				Reason:       "succeeded",
			}
			result.Trace = trace
			if params.AuditBuilder != nil {
				params.AuditBuilder.DecisionTrace(trace)
			}
			return result, nil
		}

		if mnf, ok := execErr.(*modelNotFoundError); ok {
			// Step 5 (2026-06-18): removed the e.disableModelOffer(...) call.
			//
			// Why: KindModelNotFound is in the IsClientBug set (errorsx.IsClientBug),
			// so disableModelOffer was guaranteed to early-return after logging a
			// warn. It was dead code in the only path that called it, AND it
			// masked the actual intent ("this credential's offer is gone — keep
			// moving, do NOT punish the credential") behind a misleading log.
			//
			// What we do instead:
			//   1. record a row in model_probe_runs so the
			//      /api/routing/recent-model-failures badge can surface it
			//      to the operator.
			//   2. continue to the next candidate — the credential stays
			//      available, the circuit is not opened, the cooling state
			//      is not written. The classifier + targeted probe will
			//      catch a real "this model is gone" pattern within the
			//      next 30s-2m-5m-15m backoff window.
			e.recordModelNotFound(params.R.Context(), mnf.credentialID, mnf.rawModel, mnf.body)
			// Step 6 (2026-06-18): MnfStreak — client hot-path break
			// for persistent (not intermittent) model_not_found. The
			// background probe consensus (bg/model_probe.go) owns
			// authoritative credential health; the streak owns the
			// user experience when the upstream has clearly gone
			// away.
			e.recordMnfStreak(params, cand.CredentialID)

			// BUG-4 fix (2026-06-18): If this credential+model has
			// accumulated too many recent model_not_found errors (from
			// both routing_404 and scheduler probes), temporarily cool
			// the binding so the router skips it for the next N minutes.
			// This prevents a 0%-success credential from being
			// repeatedly selected when it's the only routable candidate.
			e.coolBindingOnMnfStreak(params.R.Context(), cand.CredentialID, mnf.rawModel)

			lastErr = execErr
			lastKind = errorsx.KindModelNotFound
			attempts = append(attempts, AttemptRecord{
				ProviderID:   cand.ProviderID,
				CredentialID: cand.CredentialID,
				RawModel:     cand.RawModel,
				Kind:         errorsx.KindModelNotFound,
				Reason:       mnf.body,
			})
			continue
		}

		// 2026-06-15: IsClientBug kinds (tool_call_id_mismatch,
		// unsupported_feature, canceled) are upstream-side format
		// rejections. The credential is healthy, the candidate list is
		// correct, but THIS particular upstream doesn't support the
		// request shape (e.g. minimax-anthropic rejects the openai-
		// style tool wrapper the chat->anthropic Q3 converter emits).
		// Without this branch, the executor would record a sticky
		// failure, mark the credential cooling, and bubble 502 to
		// the client — even when a downstream openai-completions
		// candidate could have served the request via Q2 reverse
		// conversion. Skip all side effects and continue to the
		// next candidate so the next credential gets a turn.
		{
			var kind errorsx.ErrorKind
			if ue, ok := execErr.(*upstreampkg.Error); ok && ue.Kind != "" {
				kind = ue.Kind
			} else {
				kind = errorsx.ClassifyError(execErr, nil)
			}
			if errorsx.IsClientBug(kind) {
				slog.Warn("executor: client-bug kind, trying next candidate",
					"kind", kind,
					"credential_id", cand.CredentialID,
					"provider_id", cand.ProviderID,
					"raw_model", cand.RawModel,
					"err", execErr.Error(),
				)
				lastErr = execErr
				lastKind = kind
				attempts = append(attempts, AttemptRecord{
					ProviderID:   cand.ProviderID,
					CredentialID: cand.CredentialID,
					RawModel:     cand.RawModel,
					Kind:         kind,
					Reason:       execErr.Error(),
				})
				continue
			}
		}

		// contextLengthExhaustedError: handleContextLengthRecovery
		// gave up after mechanical trim + the multi-model LLM-summary
		// chain. This is a property of the model the client asked for
		// (its context window is too small for the body), not of the
		// credential serving it. Skip the circuit / sticky / disable-
		// model-offer side effects so the credential stays routable,
		// and let the next candidate (different credential for the
		// same model) have a turn. If all candidates fail with this
		// same kind, the final 4xx bubbles up to the client with the
		// last credential's body as the reason.
		if cle, ok := execErr.(*contextLengthExhaustedError); ok {
			var ctxWindow int
			if cand.ContextWindow != nil {
				ctxWindow = *cand.ContextWindow
			}
			slog.Warn("candidate exhausted context window, trying next credential",
				"credential_id", cand.CredentialID,
				"provider_id", cand.ProviderID,
				"raw_model", cand.RawModel,
				"context_window", ctxWindow,
				"status", cle.status,
				"body_preview", cle.body,
			)
			lastErr = execErr
			lastKind = errorsx.KindContextLength
			attempts = append(attempts, AttemptRecord{
				ProviderID:   cand.ProviderID,
				CredentialID: cand.CredentialID,
				RawModel:     cand.RawModel,
				Kind:         errorsx.KindContextLength,
				Reason:       cle.body,
			})
			continue
		}

		if sie, ok := execErr.(*streamInterruptedError); ok {
			kind := sie.kind
			if kind == "" {
				kind = errorsx.KindStreamTimeout
			}
			e.recordStickyFailure(params, cand.CredentialID, kind)

			if sie.resumable {
				// Stream is resumable (few chunks sent) - try next candidate.
				// The inner tryCandidate already wrote the credential state
				// with the correct kind; here we just record the failure on
				// the circuit to keep the counter consistent. For
				// KindConcurrent we also re-affirm by writing state again
				// because the executor's outer loop now drives the failover
				// to the next candidate, and we want the DB state to be
				// authoritative before that next lookup.
				e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, kind)
				if kind == errorsx.KindConcurrent {
					e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, kind, execErr)
					e.forceUnpinOnFatalKind(params.R.Context(), holder, cand.CredentialID, kind)
				} else if e.shouldWriteCredentialStateOnConfirmedFailure(cand.ProviderID, cand.CredentialID, kind) {
					e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, kind, execErr)
					e.forceUnpinOnFatalKind(params.R.Context(), holder, cand.CredentialID, kind)
				}
				lastErr = execErr
				lastKind = kind
				attempts = append(attempts, AttemptRecord{
					ProviderID:   cand.ProviderID,
					CredentialID: cand.CredentialID,
					RawModel:     cand.RawModel,
					Kind:         kind,
					Reason:       sie.reason,
				})
				slog.Warn("candidate stream interrupted (resumable), trying next",
					"credential_id", cand.CredentialID,
					"provider_id", cand.ProviderID,
					"reason", sie.reason,
					"kind", kind,
				)
				continue
			} else {
				// Stream is not resumable (too many chunks sent) - return error.
				// The inner tryCandidate already wrote the credential state
				// with the correct kind; this branch keeps the circuit counter
				// consistent and ensures the kind is recorded.
				e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, kind)
				if e.shouldWriteCredentialStateOnConfirmedFailure(cand.ProviderID, cand.CredentialID, kind) {
					e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, kind, execErr)
					e.forceUnpinOnFatalKind(params.R.Context(), holder, cand.CredentialID, kind)
				}
				slog.Warn("candidate stream interrupted (non-resumable), returning error",
					"credential_id", cand.CredentialID,
					"provider_id", cand.ProviderID,
					"reason", sie.reason,
					"kind", kind,
				)
				return nil, execErr
			}
		}

		lastErr = execErr
		// Prefer the typed Kind from *upstreampkg.Error if available, to
		// avoid re-classifying from the error text (which embeds the
		// Kind in [brackets] and can trigger false-positive matches on
		// the concurrentOverload regex).
		var kind errorsx.ErrorKind
		if ue, ok := execErr.(*upstreampkg.Error); ok && ue.Kind != "" {
			kind = ue.Kind
		} else {
			kind = errorsx.ClassifyError(execErr, nil)
		}
		lastKind = kind
		attempts = append(attempts, AttemptRecord{
			ProviderID:   cand.ProviderID,
			CredentialID: cand.CredentialID,
			RawModel:     cand.RawModel,
			Kind:         kind,
			Reason:       execErr.Error(),
		})
		e.recordStickyFailure(params, cand.CredentialID, kind)
		e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, kind)
		trace.BlockedCandidates = append(trace.BlockedCandidates, TraceCandidate{
			ProviderID:   cand.ProviderID,
			CredentialID: cand.CredentialID,
			RawModel:     cand.RawModel,
			Tier:         cand.Tier,
			Reason:       fmt.Sprintf("request_failed:%s", kind),
		})
		if e.shouldWriteCredentialStateOnConfirmedFailure(cand.ProviderID, cand.CredentialID, kind) {
			e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, kind, execErr)
			e.forceUnpinOnFatalKind(params.R.Context(), holder, cand.CredentialID, kind)
		}
	}

	trace.FailureReason = "all_candidates_failed"
	if params.AuditBuilder != nil {
		params.AuditBuilder.DecisionTrace(trace)
	}

	// ── 同步重试：非流式请求，全候选失败后保持连接继续重试 ──────────
	// 2026-06-21: 客户端在等待，不返回错误、不启动异步 goroutine，
	// 而是保持 HTTP 连接，继续同步重试候选。客户端断开时自动停止。
	if !params.IsStream && e.SyncRetryTimeout > 0 && tried > 0 {
		retried := 0
		e.asyncDepth.Add(1)
		defer e.asyncDepth.Add(-1)

		deadline := time.Now().Add(e.SyncRetryTimeout)

	syncRetryLoop:
		for time.Now().Before(deadline) {
			retried++

			// 检查客户端是否已断开
			if err := params.R.Context().Err(); err != nil {
				slog.Info("sync_retry_stopped",
					"model", params.ClientModel,
					"reason", "client_disconnect",
					"elapsed_ms", time.Since(tTotal).Milliseconds(),
					"tried", tried,
					"retried", retried,
				)
				break syncRetryLoop
			}

			// 间隔等待（可被 ctx 中断），每轮 5s
			select {
			case <-params.R.Context().Done():
				slog.Info("sync_retry_stopped",
					"model", params.ClientModel,
					"reason", "client_disconnect",
					"elapsed_ms", time.Since(tTotal).Milliseconds(),
				)
				break syncRetryLoop
			case <-time.After(5 * time.Second):
			}

			if err := params.R.Context().Err(); err != nil {
				break syncRetryLoop
			}

			// 重新推导候选（去粘性），让路由层基于最新状态做选择
			subCandidates := e.Router.PlanCandidates(
				params.Candidates,
				nil, // 无粘性偏好
				params.Policy,
				egressPref(params.Transform),
			)
			if len(subCandidates) == 0 {
				continue // 路由器也没候选，下一轮再试
			}

			// 递归执行 Execute()
			// asyncDepth>0 阻止内层触发 async 回退或嵌套 sync 重试
			subParams := *params
			subParams.Candidates = subCandidates
			result, retryErr := e.Execute(&subParams)
			if retryErr == nil {
				slog.Info("sync_retry_succeeded",
					"model", params.ClientModel,
					"elapsed_ms", time.Since(tTotal).Milliseconds(),
					"retried", retried,
				)
				return result, nil
			}
			// 更新 lastErr/LastKind（递归返回的是 ExecuteError 类型）
			if execErrTyped, ok := retryErr.(*ExecuteError); ok {
				lastErr = execErrTyped.LastErr
				lastKind = execErrTyped.LastKind
				attempts = append(attempts, execErrTyped.Attempts...)
				tried += execErrTyped.Tried
				if execErrTyped.Trace != nil {
					trace.BlockedCandidates = append(trace.BlockedCandidates, execErrTyped.Trace.BlockedCandidates...)
				}
			}
		}

		if params.R.Context().Err() == nil {
			slog.Warn("sync_retry_exhausted",
				"model", params.ClientModel,
				"elapsed_ms", time.Since(tTotal).Milliseconds(),
				"tried", tried,
				"retried", retried,
				"last_kind", lastKind,
			)
		}
		// 同步重试耗尽了所有时间 → 返回 ExecuteError
		// 不走到 async 回退路径
		trace.FailureReason = "sync_retry_exhausted"
		return nil, &ExecuteError{
			LastErr:   fmt.Errorf("sync retry exhausted after %v: %w", time.Since(tTotal), lastErr),
			Tried:     tried,
			Exhausted: true,
			Trace:     trace,
			Attempts:  attempts,
			LastKind:  lastKind,
		}
	}

	// Track C C4 (2026-06-18): async fallback. If the synchronous
	// walk took longer than AsyncShortTimeout (default 15s) and
	// the request is session-bearing, hand off to a background
	// goroutine that continues trying with an independent context
	// (AsyncLongTimeout, default 300s). The handler will see the
	// returned AsyncPendingError, write 202 + X-Gw-Pending, and
	// the client can poll GET /v1/sessions/{id}/pending-response.
	//
	// Gating: PendingStore must be configured; the request must
	// have a session id; the long timeout must be > short timeout
	// (sanity); and at least one credential must have been tried
	// (otherwise there's nothing to demote — the failure was a
	// no_candidates condition, not a slow path).
	if e.shouldAsyncFallback(params, tTotal, tried) {
		asyncErr := e.startAsyncRetry(params, trace, attempts, lastKind, lastErr)
		if asyncErr != nil {
			return nil, asyncErr
		}
	}

	return nil, &ExecuteError{LastErr: lastErr, Tried: tried, Exhausted: true, Trace: trace, Attempts: attempts, LastKind: lastKind}
}


// recordModelNotFound logs a single upstream model_not_found 404 to the
// model_probe_runs table so that the /api/routing/recent-model-failures
// admin endpoint and the probe history badge can surface it. The probe
// background worker will pick the binding up and run targeted probes
// (consensus + backoff) to decide whether to mark it broken_confirmed.
//
// We deliberately do NOT touch model_offers.available or
// credentials.availability_state here — KindModelNotFound is in the
// IsClientBug set (errorsx.IsClientBug), and a transient 404 from one
// upstream should not cool the credential or strip the offer. The 3-strike
// consensus logic in bg/model_probe.go is the only thing that may eventually
// mark a binding unavailable, and it only does so after 3 *consecutive*
// targeted probes agree the model is gone.
func (e *Executor) recordModelNotFound(ctx context.Context, credentialID int, rawModel, body string) {
	if e.DB == nil || !e.DB.Enabled() {
		return
	}
	preview := body
	if len(preview) > 500 {
		preview = preview[:500]
	}
	httpStatus := 404
	_, err := e.DB.Pool().Exec(ctx, `
		INSERT INTO model_probe_runs
		    (tenant_id, credential_id, raw_model_name, status,
		     http_status, error_code, error_message, latency_ms,
		     state_change, state_applied, triggered_by)
		VALUES ($1, $2, $3, 'http_4xx', $4, 'model_not_found', NULLIF($5, ''), 0,
		        'unchanged', FALSE, 'routing_404')
	`, "default", credentialID, rawModel, httpStatus, preview)
	if err != nil {
		slog.Warn("record_model_not_found: insert failed",
			"credential_id", credentialID,
			"raw_model", rawModel,
			"error", err)
	}
}

// recordMnfStreak (Step 6, 2026-06-18) increments the per-credential
// model_not_found counter for the current sticky session. When the
// counter reaches MnfStickyBreakThreshold, the sticky binding is
// deleted so the next request re-picks a credential instead of being
// pinned to this broken one for the full 30-min sticky TTL.
//
// All guards are nil/disabled-aware:
//   - MnfStreakEnabled is false → no-op (feature flag)
//   - MnfStreak is nil → no-op (tests / older wiring)
//   - params.StickyKey == "" → no-op (stateless request, no sticky)
//   - threshold <= 0 → defaults to 3
func (e *Executor) recordMnfStreak(params *ExecParams, credentialID int) {
	if !e.MnfStreakEnabled || e.MnfStreak == nil {
		return
	}
	if params == nil || params.StickyKey == "" {
		return
	}
	threshold := e.MnfStickyBreakThreshold
	if threshold <= 0 {
		threshold = 3
	}
	key := BuildMnfStreakKey(params.StickyKey, credentialID)
	count := e.MnfStreak.Increment(key)
	if count >= threshold {
		if e.Router != nil && e.Router.Sticky != nil {
			e.Router.Sticky.Delete(params.StickyKey)
		}
		e.MnfStreak.Reset(key)
		slog.Warn("mnf_streak_sticky_broken",
			"sticky_key", params.StickyKey,
			"credential_id", credentialID,
			"streak", count,
			"threshold", threshold,
		)
	}
}

// resetMnfStreak clears the per-credential model_not_found counter when
// a request succeeds on that credential. Counterpart to
// recordMnfStreak; the two are paired so a single success undoes
// accumulated intermittent failures.
func (e *Executor) resetMnfStreak(params *ExecParams, credentialID int) {
	if e.MnfStreak == nil || params == nil || params.StickyKey == "" {
		return
	}
	key := BuildMnfStreakKey(params.StickyKey, credentialID)
	e.MnfStreak.Reset(key)
}

// coolBindingOnMnfStreak (BUG-4 fix, 2026-06-18) checks the recent
// model_not_found count for a credential+model pair. If the count
// exceeds MnfCoolThreshold (default 5) within the last MnfCoolWindow
// (default 10 minutes), it temporarily marks the
// credential_model_binding unavailable with a short cooling period
// (default 2 minutes) so the router skips it and picks a different
// candidate. This prevents a 0%-success credential from being
// repeatedly selected when it's the only routable candidate — the
// background probe consensus (bg/model_probe.go) may take 30s-15m to
// confirm broken_confirmed, during which every user request fails.
//
// The cooling is short (2 min) so the binding auto-recovers if the
// upstream comes back. It does NOT set circuit_open or cooling_until
// on the credentials table — it only flips the cmb.available flag,
// which the router's filterAvailable() checks.
func (e *Executor) coolBindingOnMnfStreak(ctx context.Context, credentialID int, rawModel string) {
	if e.DB == nil || !e.DB.Enabled() {
		return
	}
	threshold := e.MnfCoolThreshold
	if threshold <= 0 {
		threshold = 5
	}
	coolMins := e.MnfCoolMinutes
	if coolMins <= 0 {
		coolMins = 2
	}

	var recentCount int
	err := e.DB.Pool().QueryRow(ctx, `
		SELECT count(*) FROM model_probe_runs
		WHERE credential_id = $1
		  AND raw_model_name = $2
		  AND status = 'http_4xx'
		  AND error_code = 'model_not_found'
		  AND created_at > now() - interval '1 minute' * $3
	`, credentialID, rawModel, coolMins).Scan(&recentCount)
	if err != nil {
		slog.Debug("cool_binding_mnf: count query failed",
			"credential_id", credentialID,
			"raw_model", rawModel,
			"error", err)
		return
	}
	if recentCount < threshold {
		return
	}

	_, err = e.DB.Pool().Exec(ctx, `
		UPDATE credential_model_bindings cmb
		SET available = FALSE,
		    unavailable_reason = 'mnf_cooling',
		    unavailable_at = now()
		FROM model_offers mo
		WHERE mo.id = cmb.provider_model_id
		  AND cmb.credential_id = $1
		  AND COALESCE(mo.outbound_model_name, mo.raw_model_name) = $2
		  AND cmb.available = TRUE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
	`, credentialID, rawModel)
	if err != nil {
		slog.Warn("cool_binding_mnf: update failed",
			"credential_id", credentialID,
			"raw_model", rawModel,
			"error", err)
		return
	}
	slog.Warn("cool_binding_mnf: temporarily disabled binding",
		"credential_id", credentialID,
		"raw_model", rawModel,
		"recent_mnf_count", recentCount,
		"threshold", threshold,
		"cool_minutes", coolMins,
	)
}


func (e *Executor) restoreCredentialState(ctx context.Context, credentialID int) {
	if e.State == nil || !e.State.Enabled() {
		return
	}
	if err := e.State.RestoreOnSuccess(ctx, credentialID); err != nil {
		slog.Debug("credential state restore failed", "credential_id", credentialID, "error", err)
	}
}

func (e *Executor) disableModelOffer(ctx context.Context, credentialID int, rawModel string, kind errorsx.ErrorKind, detail string) {
	// 2026-06-13: IsClientBug kinds (model_not_found, tool_call_id_mismatch,
	// canceled, unsupported_feature) are NOT the credential's fault. Without
	// this guard, a single upstream 404 (e.g. Zhipu/Aliyun intermittent
	// 'InvalidEndpointOrModel.NotFound' for glm-5.1) would silently cool the
	// user's sticky credential for 60s. Skip the DB write entirely; the
	// credential stays available.
	if errorsx.IsClientBug(kind) {
		slog.Warn("disable_model_offer: skipping (client-bug kind, not credential's fault)",
			"credential_id", credentialID,
			"model", rawModel,
			"kind", kind,
		)
		return
	}
	if e.DB == nil || !e.DB.Enabled() {
		slog.Warn("disable_model_offer: no db pool available")
		return
	}
	pool := e.DB.Pool()
	tx, err := pool.Begin(ctx)
	if err != nil {
		slog.Warn("disable_model_offer: begin tx failed", "error", err)
		return
	}
	defer tx.Rollback(ctx)

	reason := "auto_" + string(kind)
	if len(reason) > 100 {
		reason = reason[:100]
	}

	tag, err := tx.Exec(ctx,
		`UPDATE model_offers SET available = FALSE, unavailable_reason = $3, unavailable_at = now()
		 WHERE credential_id = $1 AND raw_model_name = $2 AND available = TRUE
		   AND COALESCE(admin_protected, FALSE) = FALSE`,
		credentialID, rawModel, reason,
	)
	if err != nil {
		slog.Warn("disable_model_offer: model_offers update failed", "error", err)
		return
	}

	coolingSeconds := 60
	detailStr := detail
	if len(detailStr) > 500 {
		detailStr = detailStr[:500]
	}

	_, err = tx.Exec(ctx,
		`UPDATE credentials SET availability_state = 'cooling',
			availability_recover_at = now() + ($2 || ' seconds')::interval,
			state_reason_code = $3, state_reason_detail = $4, state_updated_at = now()
		 WHERE id = $1 AND lifecycle_status = 'active'
		   AND availability_state NOT IN ('suspended', 'auth_failed')
		   AND NOT EXISTS (
		       SELECT 1 FROM credential_model_bindings cmb
		       WHERE cmb.credential_id = $1
		         AND cmb.admin_protected = TRUE
		   )`,
		credentialID, coolingSeconds, string(kind), detailStr,
	)
	if err != nil {
		slog.Warn("disable_model_offer: credentials update failed", "error", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Warn("disable_model_offer: commit failed", "error", err)
		return
	}

	if tag.RowsAffected() > 0 {
		slog.Info("model_offer_disabled",
			"credential_id", credentialID,
			"model", rawModel,
			"reason", reason,
		)
		// 2026-06-13: Invalidate the in-memory candidate cache so the next
		// request reflects the new state immediately rather than waiting
		// for the 30s cache TTL. Without this, the just-cooled credential
		// can still be picked from the cache.
		provider.InvalidateAllCandidateCache()
	}
}

func (e *Executor) writeCredentialStateOnError(ctx context.Context, credentialID int, kind errorsx.ErrorKind, err error) {
	if e.State == nil || !e.State.Enabled() {
		return
	}
	if !shouldWriteCredentialState(kind) {
		return
	}
	failure := credentialstate.Failure{Kind: kind}
	if err != nil {
		failure.Detail = err.Error()
	}
	if err := e.State.WriteOnError(ctx, credentialID, failure); err != nil {
		slog.Debug("credential state error write failed", "credential_id", credentialID, "kind", kind, "error", err)
		return
	}
	// Invalidate candidate cache to ensure routing picks up the new credential state
	// without waiting for the 30-second cache expiry. This is critical for quota exhaustion
	// scenarios where we need to immediately exclude the exhausted credential.
	provider.InvalidateAllCandidateCache()
}

// forceUnpinOnFatalKind clears the session pin for a credential whose kind
// indicates the credential is permanently dead (auth revoked, quota
// exhausted, etc.). Transient blip kinds (network/timeout/upstream-down),
// client-bug kinds (model_not_found, tool_call_id_mismatch), and
// provider-side congestion (KindConcurrent/KindStreamTimeout) are NOT fatal
// to the credential, so we keep the pin for them. Concurrent calls are safe
// because pin is keyed by (holder, credentialID).
func (e *Executor) forceUnpinOnFatalKind(ctx context.Context, holder string, credentialID int, kind errorsx.ErrorKind) {
	if e.FpSlots == nil || !e.FpSlots.Enabled() {
		return
	}
	if !errorsx.IsCredentialFatal(kind) {
		return
	}
	e.FpSlots.ForceUnpin(ctx, holder, credentialID)
}

func (e *Executor) stickyCredentialID(stickyKey string) *int {
	if e.Router == nil || e.Router.Sticky == nil || stickyKey == "" {
		return nil
	}
	credentialID, _, ok := e.Router.Sticky.GetEntry(stickyKey)
	if !ok {
		return nil
	}
	return &credentialID
}

func (e *Executor) recordStickySuccess(params *ExecParams, credentialID int) {
	if e.Router == nil || e.Router.Sticky == nil || params == nil || params.StickyKey == "" || params.Policy == nil {
		return
	}
	// Policy.StickyTTLSeconds is in seconds (DB column `sticky_ttl_seconds`).
	// 2026-06-13: the previous code multiplied this value by Millisecond,
	// which collapsed the intended 1800s TTL to ~1.8s and the
	// minute-floor in this function masked the bug for years.
	stickyTTL := time.Duration(params.Policy.StickyTTLSeconds) * time.Second
	if stickyTTL < time.Minute {
		stickyTTL = time.Minute
	}
	e.Router.Sticky.RecordSuccess(params.StickyKey, credentialID, stickyTTL)
}

func (e *Executor) recordStickyFailure(params *ExecParams, credentialID int, kind errorsx.ErrorKind) {
	if e.Router == nil || e.Router.Sticky == nil || params == nil || params.StickyKey == "" {
		return
	}
	boundID, _, ok := e.Router.Sticky.GetEntry(params.StickyKey)
	if !ok || boundID != credentialID {
		return
	}
	if errorsx.IsCredentialFatal(kind) {
		e.Router.Sticky.Delete(params.StickyKey)
		return
	}
	// 2026-06-13: network / upstream-down / client-bug kinds are NOT the
	// credential's fault. Previously any of these counted toward the
	// sticky-failure threshold (3), so 3 transient TCP resets in an
	// hour would silently unbind the sticky session and force a
	// credential re-pick. Only "real" credential-level failures should
	// count: rate-limit, concurrent-overload, stream-timeout, quota.
	// KindContextLength is also included here because the request's
	// context overflow is the caller's fault, not the credential's.
	if kind == errorsx.KindCanceled ||
		kind == errorsx.KindNetwork ||
		kind == errorsx.KindTimeout ||
		kind == errorsx.KindUpstreamDown ||
		kind == errorsx.KindContextLength ||
		errorsx.IsClientBug(kind) {
		return
	}
	e.Router.Sticky.RecordFailure(params.StickyKey, 5)
}

// AsyncPendingError (Track C C4, 2026-06-18) is returned by Execute
// when the synchronous candidate walk has exceeded AsyncShortTimeout
// and the request is eligible for the async fallback. The handler
// (relay/handler.go) recognises this via errors.As and returns
//
//   HTTP 202 Accepted
//   X-Gw-Pending: {sessionID}
//   X-Gw-Pending-Request: {requestID}
//   body: {"status":"in_progress", ...}
//
// The client then polls GET /v1/sessions/{id}/pending-response
// (see sessions/handler.go C3) until the goroutine finishes and
// either writes a completed body or marks the entry failed.
//
// This is a *graceful degradation*, not an error. We use a
// distinct error type (rather than a sentinel error value) so
// callers can inspect the request key without re-parsing the
// message string.
type AsyncPendingError struct {
	SessionID string
	RequestID string
	// StartedAt is for observability; the handler does not need
	// it. Stored for the "how long has the async goroutine been
	// running" admin metric.
	StartedAt time.Time
}

func (e *AsyncPendingError) Error() string {
	return fmt.Sprintf("async_pending: session=%s request=%s started_at=%s",
		e.SessionID, e.RequestID, e.StartedAt.Format(time.RFC3339))
}

// shouldAsyncFallback (Track C C4, 2026-06-18) gates the async
// fallback path. Returns false (synchronous exhaustion, as before)
// if any precondition is missing. Each check is a one-liner so a
// regression in the gate is easy to spot.
func (e *Executor) shouldAsyncFallback(params *ExecParams, tTotal time.Time, tried int) bool {
	// Recursion guard: the async goroutine calls Execute again.
	// The inner call must take the synchronous exhaustion path
	// regardless of the time elapsed.
	if e.asyncDepth.Load() > 0 {
		return false
	}
	if e.PendingStore == nil {
		return false
	}
	// No session → no GET endpoint, no way for the client to
	// retrieve the cached body. Async is useless here.
	if params == nil || params.R == nil {
		return false
	}
	hasSession := false
	if v := params.R.Header.Get("X-Gw-Session-Id"); v != "" {
		hasSession = true
	}
	if !hasSession {
		if v := params.R.Header.Get("X-Session-Id"); v != "" {
			hasSession = true
		}
	}
	if !hasSession {
		return false
	}
	short := e.AsyncShortTimeout
	if short <= 0 {
		short = 15 * time.Second
	}
	if time.Since(tTotal) < short {
		return false
	}
	// Nothing was tried at all — that's a no_candidates condition,
	// not a slow path. Async would not help.
	if tried <= 0 {
		return false
	}
	// Sanity: long timeout must exceed short. If mis-configured
	// the goroutine would just bail immediately; easier to skip
	// the async detour and return the synchronous error.
	long := e.AsyncLongTimeout
	if long <= 0 {
		long = 300 * time.Second
	}
	if long <= short {
		return false
	}
	return true
}

// startAsyncRetry (Track C C4, 2026-06-18) spawns a goroutine that
// continues retrying the remaining credentials with an independent
// context. The goroutine writes its outcome to PendingStore and
// the caller (handler) returns AsyncPendingError so the client
// polls. The goroutine uses context.Background() so a client
// disconnect on the original request does NOT cancel the
// vendor retry — the work to date is preserved.
//
// Max fallback cap (default 2): the goroutine tries the same
// set of candidates the synchronous loop tried, plus at most
// AsyncMaxFallbackCreds new candidates (i.e. any that were
// filtered out by circuit/limiter in the synchronous phase and
// might have recovered). This matches the design-doc target
// "primary + up to 2 fallback credentials = 3 total".
//
// Returns AsyncPendingError on successful hand-off, or nil if
// the goroutine could not be started (the caller falls back to
// the synchronous exhaustion path in that case).
func (e *Executor) startAsyncRetry(
	params *ExecParams,
	trace *Trace,
	attempts []AttemptRecord,
	lastKind errorsx.ErrorKind,
	lastErr error,
) *AsyncPendingError {
	sessionID := params.R.Header.Get("X-Gw-Session-Id")
	if sessionID == "" {
		sessionID = params.R.Header.Get("X-Session-Id")
	}
	requestID := params.R.Header.Get("X-Request-Id")
	if requestID == "" {
		// Async retry needs a stable key for the GET endpoint.
		// Synthesise one so the goroutine can still write the
		// entry; the client can poll by sessionID + GET latest.
		requestID = "async-" + time.Now().Format("20060102T150405.000")
	}
	startedAt := time.Now()

	// Mark in_progress BEFORE the goroutine starts so a concurrent
	// GET immediately knows the work is in flight. Save is a no-op
	// if Redis is unavailable; we still proceed to spawn the
	// goroutine (it would just not write back). Better than
	// silently dropping the request.
	// Audit fix 2.5: use context.Background() with a short timeout
	// for MarkInProgress. The client's request context may already
	// be canceled (the client disconnected during the >15s sync
	// walk), which would cause MarkInProgress to fail silently and
	// the client's first poll would get 404 instead of 202.
	mipCtx, mipCancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = e.PendingStore.MarkInProgress(mipCtx, &pending.Response{
		SessionID:   sessionID,
		TenantID:    tenantFromCtx(params.R),
		RequestID:   requestID,
		Status:      pending.StatusInProgress,
		Body:        "",
		ContentType: "",
		IsStream:    params.IsStream,
		CreatedAt:   startedAt.Unix(),
	})
	mipCancel()

	maxFallbacks := e.AsyncMaxFallbackCreds
	if maxFallbacks <= 0 {
		maxFallbacks = 2
	}
	longTimeout := e.AsyncLongTimeout
	if longTimeout <= 0 {
		longTimeout = 300 * time.Second
	}

	// Capture the params we need for the goroutine. We build a
	// synthetic *http.Request with context.Background() so the
	// async walk is NOT tied to the client connection. The
	// headers (X-Gw-Session-Id, X-Request-Id, etc.) are copied
	// from the original request so downstream routing logic
	// (Router.PlanCandidates, FpSlots, etc.) can still read them.
	//
	// Audit fix 2.4: the synthetic request's context is set to
	// the longTimeout ctx (created below in runAsyncRetry) so
	// the Execute() call is bounded by the long timeout, not
	// unbounded. We pass the ctx via a closure to runAsyncRetry
	// which sets it on the request before calling Execute.
	bgParams := *params
	if params.R != nil {
		syntheticReq := httptest.NewRequest("POST", params.R.URL.Path, nil)
		syntheticReq.Header = params.R.Header.Clone()
		bgParams.R = syntheticReq // context will be set in runAsyncRetry
	} else {
		bgParams.R = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	}
	bgTrace := trace
	bgAttempts := append([]AttemptRecord(nil), attempts...)
	bgLastKind := lastKind
	bgLastErr := lastErr

	go e.runAsyncRetry(&bgParams, bgTrace, bgAttempts, bgLastKind, bgLastErr, sessionID, requestID, startedAt, longTimeout, maxFallbacks)

	return &AsyncPendingError{
		SessionID: sessionID,
		RequestID: requestID,
		StartedAt: startedAt,
	}
}

// runAsyncRetry is the body of the async retry goroutine. It is
// separated from startAsyncRetry so the latter stays small and
// obviously correct (it does only "kick off" work; this method
// does the work).
//
// Lifecycle:
//  1. Build a background context with AsyncLongTimeout deadline.
//  2. Re-derive candidates from scratch (the synchronous loop's
//     circuit / limiter state has moved on; the goroutine gets a
//     fresh chance with any credential that may have recovered).
//  3. Walk the candidates with the same per-credential retry policy.
//  4. On success → PendingStore.Save(completed, body).
//  5. On exhaustion → PendingStore.Save(failed, error_message).
//
// All resource cleanup (fpSlots, limiter) is handled inside
// executeOpenAI/executeAnthropic (defer release patterns), so
// this method does not need to manage them explicitly.
//
// Recursion guard: this method calls e.Execute(), which would
// itself try to demote to async if the walk is still slow. We
// pass suppressAsync=true via a package-internal flag so the
// inner call takes the synchronous exhaustion path. (See
// shouldAsyncFallback — when suppressAsync is set, the gate
// returns false.)
func (e *Executor) runAsyncRetry(
	params *ExecParams,
	trace *Trace,
	priorAttempts []AttemptRecord,
	lastKind errorsx.ErrorKind,
	lastErr error,
	sessionID, requestID string,
	startedAt time.Time,
	longTimeout time.Duration,
	maxFallbacks int,
) {
	defer func() {
		// Defensive: an async goroutine panic must NOT take the
		// process down. The request is already in_progress in
		// Redis, so the client will get a stale-pending error
		// on poll (the sweeper will mark it failed eventually).
		if r := recover(); r != nil {
			slog.Error("async_retry_panic",
				"session_id", sessionID,
				"request_id", requestID,
				"panic", r,
			)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = e.PendingStore.Save(ctx, &pending.Response{
				SessionID:    sessionID,
				RequestID:    requestID,
				Status:       pending.StatusFailed,
				ErrorMessage: "async_retry_panic",
				CompletedAt:  time.Now().Unix(),
			})
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), longTimeout)
	defer cancel()

	// Re-derive candidates. We DO NOT reuse params.Candidates
	// directly because the synchronous loop's planCandidates may
	// have re-ordered them; we want the async goroutine to get a
	// fresh look at the current state.
	asyncParams := *params
	// Audit fix 2.4: bind the longTimeout ctx to the synthetic
	// request so Execute() and its upstream HTTP calls are bounded.
	if asyncParams.R != nil {
		asyncParams.R = asyncParams.R.WithContext(ctx)
	}
	candidates := e.Router.PlanCandidates(
		params.Candidates,
		nil, // no sticky: the async walk is its own attempt
		params.Policy,
		egressPref(params.Transform),
	)
	if len(candidates) == 0 {
		// No candidates available — fail and write the reason.
		_ = e.PendingStore.Save(ctx, &pending.Response{
			SessionID:    sessionID,
			RequestID:    requestID,
			Status:       pending.StatusFailed,
			ErrorMessage: "async_no_candidates",
			CompletedAt:  time.Now().Unix(),
		})
		return
	}
	// Cap to primary + maxFallbacks per the design doc.
	if len(candidates) > maxFallbacks+1 {
		candidates = candidates[:maxFallbacks+1]
	}
	// Audit fix 2.1: assign the re-derived candidates to asyncParams
	// so Execute() actually uses them. Without this, Execute() would
	// retry the original (already-failed) candidates.
	asyncParams.Candidates = candidates

	// Recursion guard: bump asyncDepth for the inner Execute call
	// so shouldAsyncFallback returns false there. Restore on
	// return. Audit fix 2.3: use Add/Sub instead of save/restore
	// to avoid lost updates under concurrent requests.
	e.asyncDepth.Add(1)
	defer func() { e.asyncDepth.Add(-1) }()

	result, execErr := e.Execute(&asyncParams) //nolint:gocritic // intentionally recursive: async walk uses same executor
	if execErr == nil {
		// Audit fix 2.2: for streaming responses, Execute() does
		// NOT set ResponseBody (the stream is consumed by the
		// StreamChat closure which writes to the capturer
		// directly). We must NOT overwrite the capturer's entry
		// with an empty body. For non-streaming, ResponseBody is
		// set and we write it to the pending store.
		if !params.IsStream && len(result.ResponseBody) > 0 {
			_ = e.PendingStore.Save(ctx, &pending.Response{
				SessionID:   sessionID,
				RequestID:   requestID,
				Status:      pending.StatusCompleted,
				Body:        string(result.ResponseBody),
				ContentType: contentTypeFor(params.IsStream),
				IsStream:    params.IsStream,
				CompletedAt: time.Now().Unix(),
			})
		} else if params.IsStream {
			// For streaming, the capturer in main.go's StreamChat
			// closure already wrote the body to the pending store.
			// We only need to ensure the entry is marked completed
			// if the capturer missed it (e.g. panic). Best-effort:
			// check if an entry exists and is still in_progress.
			if entry, found, _ := e.PendingStore.Get(ctx, sessionID, requestID); found && entry.Status == pending.StatusInProgress {
				_ = e.PendingStore.Save(ctx, &pending.Response{
					SessionID:   sessionID,
					RequestID:   requestID,
					Status:      pending.StatusCompleted,
					Body:        entry.Body,
					ContentType: contentTypeFor(true),
					IsStream:    true,
					CompletedAt: time.Now().Unix(),
				})
			}
		}

		// 2026-06-20: correct the request_logs row that the
		// synchronous phase left at "in_progress" (because the
		// handler returned 202 + AsyncPendingError without
		// calling emitTelemetry). Best-effort: nil-safe, no
		// failure path on writeback error.
		if e.RequestLogEmitter != nil && e.RequestLogEmitter.Enabled() {
			e.RequestLogEmitter.EmitRequestLogUpdate(e.buildAsyncSuccessEntry(
				requestID, sessionID, startedAt, result, params,
			))
		}
		return
	}

	// Failure path. Audit fix 2.6: use the async Execute()'s
	// error (execErr), not the synchronous walk's lastErr/lastKind.
	// The synchronous walk's error is misleading — it describes
	// why the FIRST attempt failed, not why the ASYNC retry failed.
	asyncErr := "async_exhausted"
	if execErr != nil {
		asyncErr = "async: " + truncateForStore(execErr.Error())
	}
	_ = e.PendingStore.Save(ctx, &pending.Response{
		SessionID:    sessionID,
		RequestID:    requestID,
		Status:       pending.StatusFailed,
		ErrorMessage: truncateForStore(asyncErr),
		CompletedAt:  time.Now().Unix(),
	})

	_ = trace
	_ = priorAttempts
}

// buildAsyncSuccessEntry (2026-06-20) constructs a minimal
// RequestLogEntry for async-retry success writeback. Only the
// fields available in the executor's scope are populated:
//   - RequestID, sessionID, startedAt (from runAsyncRetry params)
//   - ClientModel, OutboundModel (from ExecParams)
//   - CredentialID, ProviderID, EgressProtocol (from result.Candidate)
//   - ResponsePreview (truncated, for admin UI inspection)
//   - IdentityHash (from params.ClientID)
//
// TenantID, APIKeyID, and tokens/cost are intentionally NOT
// populated — the sync-phase handler has these but the executor
// does not. A minimal success row is still far better than the
// pre-fix behavior of leaving the row at "in_progress".
//
// ErrorKind is explicitly set to "" (NOT nil) so the SQL CASE in
// the UPSERT / UPDATE writes NULL to the column. Without this,
// COALESCE would preserve any stale error_kind from a prior
// failure update.
//
// Returned entry has Op="" — the caller is expected to use
// EmitRequestLogUpdate which sets Op=RequestLogUpdate.
func (e *Executor) buildAsyncSuccessEntry(
	requestID, sessionID string,
	startedAt time.Time,
	result *ExecuteResult,
	params *ExecParams,
) *telemetry.RequestLogEntry {
	// Defensive nil check. In practice runAsyncRetry always passes
	// a non-nil params (it received it from the executor caller),
	// but staticcheck rightly flags the field accesses below.
	if params == nil {
		latencyMs := int(time.Since(startedAt).Milliseconds())
		status := telemetry.RequestStatusSuccess
		emptyKind := ""
		return &telemetry.RequestLogEntry{
			RequestID:     requestID,
			Success:       true,
			RequestStatus: &status,
			LatencyMs:     &latencyMs,
			ErrorKind:     &emptyKind,
			GwSessionID:   strPtr(sessionID),
		}
	}
	latencyMs := int(time.Since(startedAt).Milliseconds())
	success := true
	status := telemetry.RequestStatusSuccess
	emptyKind := ""

	entry := &telemetry.RequestLogEntry{
		RequestID:     requestID,
		TenantID:      tenantFromCtx(params.R),
		ClientModel:   strPtr(params.ClientModel),
		OutboundModel: strPtr(params.OutboundModel),
		Success:       success,
		RequestStatus: &status,
		LatencyMs:     &latencyMs,
		ErrorKind:     &emptyKind, // empty string → COALESCE writes NULL
		GwSessionID:   strPtr(sessionID),
	}
	if result != nil {
		if result.Candidate.CredentialID != 0 {
			id := result.Candidate.CredentialID
			entry.CredentialID = &id
		}
		if result.Candidate.ProviderID != 0 {
			id := result.Candidate.ProviderID
			entry.ProviderID = &id
		}
		if result.Candidate.Protocol != "" {
			entry.EgressProtocol = strPtr(result.Candidate.Protocol)
		}
		if len(result.ResponseBody) > 0 {
			preview := string(result.ResponseBody)
			if len(preview) > 200 {
				preview = preview[:200] + "..."
			}
			entry.ResponsePreview = &preview
		}
		// 2026-06-20 audit enhancement: include the request body
		// preview (truncated) so operators can correlate async-retry
		// success with the original intent without joining against
		// usage_ledger. Bounded at 200 chars to match the
		// ResponsePreview policy.
		if len(result.RequestBody) > 0 {
			preview := string(result.RequestBody)
			if len(preview) > 200 {
				preview = preview[:200] + "..."
			}
			entry.RequestPreview = &preview
		}
		// 2026-06-20 audit enhancement: forward Round 47 compression
		// metadata so the parent-child chain remains queryable for
		// async-retry successes. Without this, the chain breaks at
		// any async-retry that triggered a 4xx recovery.
		if result.CompressionReason != nil {
			entry.CompressionReason = result.CompressionReason
		}
		if result.CompressionStrategy != nil {
			entry.CompressionStrategy = result.CompressionStrategy
		}
		if len(result.CompressionMeta) > 0 {
			entry.CompressionMeta = result.CompressionMeta
		}
		// Note: result.Trace (planned candidates) and
		// result.Candidate.CanonicalID are NOT forwarded here.
		// RequestLogEntry doesn't have a trace field (it lives
		// on DecisionLogEntry, written separately by the sync
		// phase), and provider.Candidate has no CanonicalID.
		// The sync-phase emitTelemetry already populates these
		// via its dedicated decision log emit; we don't need to
		// duplicate here.
	}
	if params.ClientID.IdentityHash != "" {
		ih := params.ClientID.IdentityHash
		entry.IdentityHash = &ih
	}
	return entry
}

// strPtr is a small local helper for building *string fields in
// RequestLogEntry. Returns nil for empty input so the COALESCE
// pattern in the SQL UPDATE leaves the column unchanged.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// tenantFromCtx pulls the tenant id from request context, falling
// back to the literal "default" if unset. Uses the exported
// sessions.GetTenantIDFromContext (Track C C4 audit fix #5).
func tenantFromCtx(r *http.Request) string {
	if r == nil {
		return "default"
	}
	return sessions.GetTenantIDFromContext(r.Context())
}

// contentTypeFor picks the canonical content type for a replay.
// Streaming responses are SSE; everything else is JSON.
func contentTypeFor(isStream bool) string {
	if isStream {
		return "text/event-stream"
	}
	return "application/json"
}

// truncateForStore clamps error strings to a Redis-friendly size
// so a 10KB vendor error body doesn't blow up the Hash.
func truncateForStore(s string) string {
	const max = 1024
	if len(s) > max {
		return s[:max]
	}
	return s
}

type modelNotFoundError struct {
	credentialID int
	rawModel     string
	body         string
}

func (e *modelNotFoundError) Error() string {
	return "model_not_found: " + e.rawModel
}

type streamInterruptedError struct {
	reason       string
	credentialID int
	resumable    bool // Whether the stream can be resumed with a different credential
	kind         errorsx.ErrorKind // Errorsx kind to record on the circuit (defaults to KindStreamTimeout)
}

func (e *streamInterruptedError) Error() string {
	return "stream_interrupted: " + e.reason
}

type retryableError struct {
	err error
}

func (e *retryableError) Error() string { return e.err.Error() }

// contextLengthHTTPError signals the upstream rejected the request because
// the prompt exceeded the model context window. executeAnthropic uses this
// to attempt one client-side trim + retry before bubbling the 4xx up.
type contextLengthHTTPError struct {
	status  int
	body    []byte
	headers http.Header
}

func (e *contextLengthHTTPError) Error() string {
	return fmt.Sprintf("upstream %d context_length_exceeded", e.status)
}

// contextLengthExhaustedError signals that handleContextLengthRecovery
// gave up after both phases (mechanical trim + LLM summary fallback
// chain). Carries the credential that was the last to fail so the outer
// Execute loop can decide which kind of failover to attempt next
// (e.g. try a different credential, or a bigger-context model), and
// crucially so the credential is NOT recorded as a circuit failure —
// hitting a context limit is a property of the model the client asked
// for, not of the credential serving it.
type contextLengthExhaustedError struct {
	credentialID int
	rawModel     string
	status       int
	body         string // first 200 bytes of upstream 4xx body
}

func (e *contextLengthExhaustedError) Error() string {
	return fmt.Sprintf("context_length_exhausted: %s (cred=%d, status=%d)", e.rawModel, e.credentialID, e.status)
}

func shouldWriteCredentialState(kind errorsx.ErrorKind) bool {
	switch kind {
	case errorsx.KindAuth, errorsx.KindAuthRevoked,
		errorsx.KindQuota, errorsx.KindQuotaPeriodic, errorsx.KindQuotaBalance, errorsx.KindQuotaPermanent,
		errorsx.KindConcurrent, errorsx.KindRateLimit,
		errorsx.KindStreamTimeout:
		return true
	default:
		return false
	}
}

func (e *Executor) shouldWriteCredentialStateOnConfirmedFailure(providerID, credentialID int, kind errorsx.ErrorKind) bool {
	if !shouldWriteCredentialState(kind) {
		return false
	}
	// Quota errors (402 Payment Required) should write state immediately
	// without waiting for circuit open, since 402 is definitive evidence
	// that the credential is exhausted
	if kind == errorsx.KindQuota || kind == errorsx.KindQuotaBalance ||
		kind == errorsx.KindQuotaPeriodic || kind == errorsx.KindQuotaPermanent {
		return true
	}
	if e.Circuit == nil {
		return true
	}
	b := e.Circuit.GetOrCreate(providerID, credentialID)
	state := b.State()
	if state == circuit.StateOpen || state == circuit.StateQuarantined {
		return true
	}
	slog.Warn("credential state write pending failure confirmation",
		"credential_id", credentialID,
		"provider_id", providerID,
		"kind", kind,
		"consecutive", b.ConsecutiveFailures(),
	)
	return false
}

func egressPref(tx *transform.TransformResult) []string {
	if tx == nil || len(tx.EgressPreference) == 0 {
		return nil
	}
	return tx.EgressPreference
}

func replaceModelInRequestBody(body []byte, newModel string) []byte {
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

func replaceModelInResponseBody(body []byte, clientModel string) []byte {
	// Use raw byte matching to replace "model":"<old>" with the client model,
	// preserving original JSON key ordering.
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

func injectStreamOptions(body []byte) []byte {
	body = bytes.TrimSpace(body)
	if len(body) < 2 || body[len(body)-1] != '}' {
		return body
	}
	if bytes.Contains(body, []byte(`"stream_options"`)) {
		return body
	}
	end := len(body) - 1
	for end > 0 && body[end-1] <= ' ' {
		end--
	}

	streamOpts := `"include_usage":true`
	insert := `,"stream_options":{` + streamOpts + `}`

	var buf bytes.Buffer
	buf.Write(body[:end])
	buf.WriteString(insert)
	buf.Write(body[end:])
	return buf.Bytes()
}

func executorMustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func injectCacheParams(body []byte, cacheMode, sessionKey string) ([]byte, error) {
	if cacheMode == "" || sessionKey == "" {
		return body, nil
	}

	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return body, err
	}

	switch cacheMode {
	case "checkpoint":
		obj["cache_checkpoint"] = sessionKey
	case "tokens":
		meta, ok := obj["metadata"].(map[string]any)
		if !ok {
			meta = make(map[string]any)
		}
		if cc, ok := meta["cache_control"].(map[string]any); ok {
			cc["type"] = "ephemeral"
			meta["cache_control"] = cc
		} else {
			meta["cache_control"] = map[string]any{"type": "ephemeral"}
		}
		obj["metadata"] = meta
	}

	return json.Marshal(obj)
}
