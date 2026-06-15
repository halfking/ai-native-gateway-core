package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/credentialstate"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/identity"
	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/memora"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/resolve"
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
// passthrough hook (relay/anthropic_passthrough_stream.go). Wired from
// main.go so the routing package does not import relay.
type AnthropicPassthroughFunc func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture) StreamOutcome

// ChatToAnthropicFunc converts an OpenAI chat completions body to
// Anthropic Messages format. Wired from main.go so the routing
// package does not import relay.
type ChatToAnthropicFunc func(body []byte) ([]byte, error)

// AnthropicToOpenAIFunc converts an Anthropic Messages body to OpenAI
// chat completions format. Wired from main.go so the routing package
// does not import relay.
type AnthropicToOpenAIFunc func(body []byte) ([]byte, error)

// AnthropicToOpenAISSEFunc is the streaming counterpart of
// AnthropicToOpenAIFunc: reads Anthropic-format SSE upstream and
// writes OpenAI-format SSE chunks to w. Wired from main.go.
type AnthropicToOpenAISSEFunc func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture) StreamOutcome

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

// XMLCoerceNonStreamFunc transforms a non-streaming chat response body,
// rewriting any `<tool_call><function=...>` XML embedded in assistant
// `content` into structured OpenAI `tool_calls` entries.  The second
// argument is true when the original request body supplied a `tools` array.
// Implementations are expected to be no-ops when toolsRequested is false.
type XMLCoerceNonStreamFunc func(body []byte, toolsRequested bool) []byte

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
	// DisguisePool injects rotating User-Agent / Accept-Language headers.
	// Nil disables the feature.
	DisguisePool interface {
		Headers() map[string]string
		MaybeRotate()
	}

	StreamTimeout        time.Duration
	UpstreamTimeout      time.Duration
	StreamRetryThreshold int // Max chunks sent before stream becomes non-resumable (default 5)

	// Memora is the optional context-compression oracle. When non-nil
	// and enabled, the executor (a) enqueues per-request writes to
	// Memora for later retrieval, and (b) on context-length overflow,
	// queries Memora for L1 session facts and rebuilds the body before
	// retrying. Nil means the entire Memora path is a no-op.
	Memora *memora.Client
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
	SessionKey    string
	StickyKey     string
}

type ExecuteResult struct {
	Response  *http.Response
	Candidate provider.Candidate
	LatencyMs int
	RequestBody []byte
	ResponseBody []byte
	Trace     *Trace
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
			e.disableModelOffer(params.R.Context(), mnf.credentialID, mnf.rawModel, errorsx.KindModelNotFound, mnf.body)
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
				} else if e.shouldWriteCredentialStateOnConfirmedFailure(cand.ProviderID, cand.CredentialID, kind) {
					e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, kind, execErr)
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
		}
	}

	trace.FailureReason = "all_candidates_failed"
	if params.AuditBuilder != nil {
		params.AuditBuilder.DecisionTrace(trace)
	}
	return nil, &ExecuteError{LastErr: lastErr, Tried: tried, Exhausted: true, Trace: trace, Attempts: attempts, LastKind: lastKind}
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
