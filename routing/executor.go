package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/credentialstate"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/disguise"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/identity"
	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/resolve"
	"github.com/kaixuan/llm-gateway-go/transform"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

const maxBodySize = 32 << 20

type NormalizerFunc func(chunk []byte, isStream bool) []byte

type StreamOutcome = struct {
	Interrupted bool
	Reason      string
	Resumable   bool // Whether the stream can be resumed with a different credential
	ChunkCount  int  // Number of chunks sent before interruption
}

type StreamHandler func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, norm NormalizerFunc, capture *audit.StreamCapture, toolsRequested bool) StreamOutcome

type StreamWrapperFunc func(w http.ResponseWriter, resp *http.Response, norm NormalizerFunc, capture *audit.StreamCapture) StreamOutcome

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
	Auditor       audit.Sink
	State         *credentialstate.Writer
	DB            *db.DB
	HeaderProfiles *HeaderProfileCache
	FpSlots          *credentialfpslot.Manager

	StreamTimeout        time.Duration
	UpstreamTimeout      time.Duration
	StreamRetryThreshold int // Max chunks sent before stream becomes non-resumable (default 5)
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

type ExecuteError struct {
	LastErr   error
	Tried     int
	Exhausted bool
	Trace     *Trace
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

		result, execErr := e.tryCandidate(params, cand, retryPerCred, tTotal, fpLease)
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
	return nil, &ExecuteError{LastErr: lastErr, Tried: tried, Exhausted: true, Trace: trace}
}

func (e *Executor) tryCandidate(
	params *ExecParams,
	cand provider.Candidate,
	maxRetries int,
	tTotal time.Time,
	fpLease *credentialfpslot.Lease,
) (*ExecuteResult, error) {
	outboundModel := params.OutboundModel
	if outboundModel == "" {
		outboundModel = cand.RawModel
	}

	bodyBytes := params.BodyBytes
	if outboundModel != params.ClientModel {
		bodyBytes = replaceModelInRequestBody(bodyBytes, outboundModel)
	}
	if params.IsStream {
		bodyBytes = injectStreamOptions(bodyBytes)
	}
	if params.Transform != nil {
		bodyBytes = transform.ApplyRequestWhitelist(
			bodyBytes,
			params.Transform.PassthroughFields,
			params.Transform.StripRequestFields,
		)
	}
	if !transform.IsToolUseCapable(cand.CatalogCode, cand.Protocol) && transform.NeedsToolCollapse(bodyBytes) {
		bodyBytes = transform.CollapseToolHistory(bodyBytes)
	}
	bodyBytes = transform.ApplyCapabilitySanitizer(bodyBytes, cand.CatalogCode)
	bodyBytes = transform.MergeConsecutiveMessages(bodyBytes)

	if disguise.IsEnabled() && disguise.ShouldApply(bodyBytes) {
		profileName := ""
		if params.Transform != nil && params.Transform.DisguiseProfileID != "" {
			profileName = params.Transform.DisguiseProfileID
		} else if params.ClientID.Fingerprint.ClientProfile != "" {
			profileName = params.ClientID.Fingerprint.ClientProfile
		}
		if profileName != "" {
			bodyBytes, _ = disguise.Apply(bodyBytes, nil, nil, profileName, 0)
			slog.Debug("disguise layer applied", "profile", profileName)
		}
	}

	if params.SessionKey != "" && cand.SupportsPromptCache {
		bodyBytes, _ = injectCacheParams(bodyBytes, cand.CacheMode, params.SessionKey)
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(500*(1<<(attempt-1))) * time.Millisecond
			select {
			case <-params.R.Context().Done():
				return nil, params.R.Context().Err()
			case <-time.After(delay):
			}
		}

		result, tryErr := func() (*ExecuteResult, error) {
			var reqPool *pool.Pool
			base := strings.TrimRight(cand.BaseURL, "/")
			upstreamURL := base + "/chat/completions"
			if cand.Protocol == "anthropic-messages" {
				upstreamURL = base + "/messages"
			}

			req, err := http.NewRequestWithContext(
				params.R.Context(),
				http.MethodPost,
				upstreamURL,
				bytes.NewReader(bodyBytes),
			)
			if err != nil {
				return nil, err
			}

			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+cand.APIKey)
			if params.IsStream {
				req.Header.Set("Accept", "text/event-stream")
			}
			req.Header.Set("X-Request-Id", params.R.Header.Get("X-Request-Id"))
			if fpLease != nil && fpLease.Egress != nil {
				credentialfpslot.ApplyEgressHeaders(req.Header, fpLease.Egress)
			} else {
				req.Header.Set("X-Virtual-Client-Id", params.ClientID.VirtualClientID)
				req.Header.Set("X-Virtual-IP", params.ClientID.VirtualIP)
				req.Header.Set("X-Virtual-MAC", params.ClientID.VirtualMAC)
			}

			if params.Transform != nil {
				for _, h := range params.Transform.StripHeaders {
					req.Header.Del(h)
				}
				for k, v := range params.Transform.InjectHeaders {
					req.Header.Set(k, v)
				}
			}

			if e.HeaderProfiles != nil {
				prof := e.HeaderProfiles.load(params.R.Context(), cand.CatalogCode, cand.Protocol)
				if prof != nil {
					for k, v := range prof.Headers {
						req.Header.Set(k, v)
					}
				}
			}

			var httpClient *http.Client
			if e.Pools != nil {
				poolKey := pool.PoolKey{
					IdentityHash: params.ClientID.IdentityHash,
					ProviderID:   cand.ProviderID,
					CredentialID: cand.CredentialID,
				}
				if p := e.Pools.GetOrCreate(poolKey, ""); p != nil && p.State() == pool.PoolActive {
					reqPool = p
					httpClient = p.Client()
				}
			}
			if httpClient == nil {
				httpClient = http.DefaultClient
			} else if err := reqPool.Acquire(params.R.Context()); err != nil {
				return nil, err
			} else {
				// C-2: only defer Release if we actually Acquired
				defer reqPool.Release()
			}

			timeout := e.UpstreamTimeout
			if params.IsStream {
				timeout = e.StreamTimeout
			}
			ctx, cancel := context.WithTimeout(params.R.Context(), timeout)
			defer cancel()
			req = req.WithContext(ctx)

			if e.Upstream != nil {
				req.GetBody = func() (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(bodyBytes)), nil
				}
			}

			var resp *http.Response
			var uErr *upstreampkg.Error
			if e.Upstream != nil {
				resp, uErr = e.Upstream.Do(req)
			} else {
				var doErr error
				resp, doErr = httpClient.Do(req)
				if doErr != nil {
					uErr = &upstreampkg.Error{Kind: errorsx.ClassifyError(doErr, nil), Message: doErr.Error(), Err: doErr}
				}
			}

			if uErr != nil && (resp == nil || resp.StatusCode >= 500) {
				errKind := uErr.Kind
				if errKind == errorsx.KindRateLimit {
					e.Limiter.Shrink(cand.ProviderID, cand.CredentialID)
				}
				if !errorsx.IsRetryable(errKind) || attempt >= maxRetries {
					return nil, uErr
				}
				return nil, &retryableError{err: uErr}
			}

		if resp != nil && resp.StatusCode >= 400 {
			defer resp.Body.Close()
			// Read up to 4 KiB for error classification, then drain the remainder
			// so that the underlying TCP connection can be reused by the HTTP
			// transport (Go will discard the connection if the body is not fully
			// consumed before Close).
			body := make([]byte, 4096)
			n, _ := resp.Body.Read(body)
			_, _ = io.Copy(io.Discard, resp.Body) // drain remainder
			// Pass the (status, body) pair through the body-aware classifier so
			// overload-shaped payloads (e.g. 429 with "concurrent limit
			// exceeded" body) are upgraded to KindConcurrent — which gets a
			// 5-minute cooling and immediate failover to the next candidate
			// rather than the short quota-style cooling for plain 429s.
			errKind := errorsx.ClassifyErrorWithBody(resp.StatusCode, body[:n])

			if bodyKind := errorsx.ClassifyResponseBody(body[:n]); bodyKind == errorsx.KindModelNotFound {
				slog.Info("model_not_found skip offer",
					"credential_id", cand.CredentialID,
					"model", cand.RawModel,
					"status", resp.StatusCode,
					"body_preview", string(body[:min(n, 120)]),
				)
				return nil, &modelNotFoundError{
					credentialID: cand.CredentialID,
					rawModel:     cand.RawModel,
					body:         string(body[:n]),
				}
			}

			if resp.StatusCode >= 400 && resp.StatusCode < 500 &&
				resp.StatusCode != 429 && resp.StatusCode != 401 &&
				resp.StatusCode != 403 && resp.StatusCode != 402 &&
				errKind != errorsx.KindConcurrent {
				e.Circuit.RecordSuccess(cand.ProviderID, cand.CredentialID)
			} else if errKind == errorsx.KindRateLimit {
				e.Limiter.Shrink(cand.ProviderID, cand.CredentialID)
			} else if errKind == errorsx.KindConcurrent {
				// Concurrent-overload: mark the circuit open and write the
				// credential state immediately (5-minute cooling). The
				// executor's outer loop will then route to the next candidate
				// instead of retrying this overloaded credential. We bypass
				// shouldWriteCredentialStateOnConfirmedFailure's threshold
				// check because concurrent overload is a definitive signal
				// — a single occurrence is enough to take the credential
				// out of rotation.
				e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, errorsx.KindConcurrent)
				e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, errorsx.KindConcurrent,
					fmt.Errorf("upstream %d concurrent overload: %s", resp.StatusCode, string(body[:min(n, 200)])))
				slog.Warn("credential concurrent-overload, failing over to next candidate",
					"credential_id", cand.CredentialID,
					"provider_id", cand.ProviderID,
					"status", resp.StatusCode,
					"body_preview", string(body[:min(n, 120)]),
				)
			}
			if !errorsx.IsRetryable(errKind) || attempt >= maxRetries {
				for k, vs := range resp.Header {
					for _, v := range vs {
						params.W.Header().Add(k, v)
					}
				}
				params.W.WriteHeader(resp.StatusCode)
				if n > 0 {
					params.W.Write(body[:n])
				}
				return nil, fmt.Errorf("upstream %d", resp.StatusCode)
			}
			return nil, &retryableError{err: fmt.Errorf("upstream %d", resp.StatusCode)}
		}

			e.Circuit.RecordSuccess(cand.ProviderID, cand.CredentialID)
			latencyMs := int(time.Since(tTotal).Milliseconds())

			if params.IsStream {
				var streamOutcome StreamOutcome
				if params.StreamWrapper != nil {
					streamOutcome = params.StreamWrapper(params.W, resp, e.Normalize, params.Capture)
				} else if e.StreamChat != nil {
					streamOutcome = e.StreamChat(params.W, resp, params.ClientModel, outboundModel, e.Normalize, params.Capture, params.ToolsRequested)
				}
			if streamOutcome.Interrupted && streamOutcome.Reason != "client_cancel" {
				// Check if stream is resumable (chunk count below threshold)
				isResumable := streamOutcome.Resumable && streamOutcome.ChunkCount < e.StreamRetryThreshold

				// Classify the interruption. eof_without_done is the
				// dominant pattern seen for MiniMax / similar providers —
				// they close the SSE connection without sending [DONE].
				// This is NOT necessarily concurrent overload; many providers
				// simply omit the sentinel on successful streams.
				// We only escalate to KindConcurrent when the upstream
				// explicitly returns an overload error body (not just
				// eof_without_done). For eof_without_done with chunks
				// already sent, we use KindStreamTimeout with a light
				// cooling that goes through the normal threshold gate.
				streamKind := errorsx.KindStreamTimeout
				if errorsx.IsConcurrentOverload(streamOutcome.Reason) {
					streamKind = errorsx.KindConcurrent
				}

				// eof_without_done with chunks already received: the stream
				// likely completed its work, just without the [DONE] sentinel.
				// Do NOT trigger aggressive cooling for this case.
				isBenignEOF := streamOutcome.Reason == "eof_without_done" && streamOutcome.ChunkCount > 0

				slog.Warn("executor: stream interrupted",
					"credential_id", cand.CredentialID,
					"provider_id", cand.ProviderID,
					"reason", streamOutcome.Reason,
					"chunk_count", streamOutcome.ChunkCount,
					"resumable", isResumable,
					"classified_as", streamKind,
					"benign_eof", isBenignEOF,
				)

				if isBenignEOF {
					// Stream produced output but ended without [DONE].
					// This is a benign provider quirk (common for MiniMax).
					// Don't penalize the credential — record success so the
					// circuit breaker stays healthy. The bytes have already
					// been written to the client (relay/stream.go sends the
					// [DONE] terminator after EOF), so we must NOT return
					// an error here — the response has already gone out.
					e.Circuit.RecordSuccess(cand.ProviderID, cand.CredentialID)
					return &ExecuteResult{
						Response:    resp,
						Candidate:   cand,
						LatencyMs:   latencyMs,
						RequestBody: append([]byte(nil), bodyBytes...),
					}, nil
				} else if isResumable {
					e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, streamKind)
					if streamKind == errorsx.KindConcurrent {
						e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, streamKind,
							fmt.Errorf("stream %s (concurrent-overload inferred)", streamOutcome.Reason))
					} else if e.shouldWriteCredentialStateOnConfirmedFailure(cand.ProviderID, cand.CredentialID, streamKind) {
						e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, streamKind, fmt.Errorf("stream %s", streamOutcome.Reason))
					}
				} else if streamKind == errorsx.KindConcurrent {
					// Non-resumable stream interruption from confirmed overload:
					// record circuit failure and write credential state immediately.
					e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, streamKind)
					e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, streamKind,
						fmt.Errorf("stream %s (concurrent-overload inferred, non-resumable)", streamOutcome.Reason))
					slog.Warn("non-resumable stream interrupted by concurrent-overload, credential now in 5-min cooling",
						"credential_id", cand.CredentialID,
						"provider_id", cand.ProviderID,
						"reason", streamOutcome.Reason,
						"chunk_count", streamOutcome.ChunkCount,
					)
				} else {
					// Non-resumable stream interrupted by non-concurrent cause
					// (e.g. KindStreamTimeout): still record failure so the
					// circuit counter advances and credential state is written
					// when the threshold is met.
					e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, streamKind)
					if e.shouldWriteCredentialStateOnConfirmedFailure(cand.ProviderID, cand.CredentialID, streamKind) {
						e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, streamKind,
							fmt.Errorf("stream %s (non-resumable)", streamOutcome.Reason))
					}
					slog.Warn("non-resumable stream interrupted",
						"credential_id", cand.CredentialID,
						"provider_id", cand.ProviderID,
						"reason", streamOutcome.Reason,
						"kind", streamKind,
						"chunk_count", streamOutcome.ChunkCount,
					)
				}

					return &ExecuteResult{
						Response:    resp,
						Candidate:   cand,
						LatencyMs:   latencyMs,
						RequestBody: append([]byte(nil), bodyBytes...),
					}, &streamInterruptedError{reason: streamOutcome.Reason, credentialID: cand.CredentialID, resumable: isResumable, kind: streamKind}
				}
				return &ExecuteResult{
					Response:    resp,
					Candidate:   cand,
					LatencyMs:   latencyMs,
					RequestBody: append([]byte(nil), bodyBytes...),
				}, nil
			}
			defer resp.Body.Close()
			respBody, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBodySize)+1))
			if err != nil {
				return nil, err
			}
			if len(respBody) > maxBodySize {
				slog.Warn("upstream response truncated", "size", len(respBody))
				respBody = respBody[:maxBodySize]
			}
			if params.ClientModel != "" {
				respBody = replaceModelInResponseBody(respBody, params.ClientModel)
			}
			// Coerce XML-style `<tool_call><function=...>` tool calls embedded
			// in assistant `content` (emitted by Xiaomi MiMo / MiniMax M2.7
			// when their native tool_use is unavailable) into structured
			// tool_calls so downstream agents dispatch the tool instead of
			// treating the XML as a plain-text todo list.
			if e.XMLCoerceNonStream != nil {
				respBody = e.XMLCoerceNonStream(respBody, params.ToolsRequested)
			}
			if e.Normalize != nil {
				respBody = e.Normalize(respBody, false)
			}
			if !params.SuppressSuccessWrite {
				for k, vs := range resp.Header {
					for _, v := range vs {
						params.W.Header().Add(k, v)
					}
				}
				params.W.WriteHeader(resp.StatusCode)
				params.W.Write(respBody)
			}
			return &ExecuteResult{
				Response:     resp,
				Candidate:    cand,
				LatencyMs:    latencyMs,
				RequestBody:  append([]byte(nil), bodyBytes...),
				ResponseBody: append([]byte(nil), respBody...),
			}, nil
		}()

		if tryErr == nil {
			return result, nil
		}
		if _, ok := tryErr.(*retryableError); !ok {
			return nil, tryErr
		}
	}
	return nil, fmt.Errorf("exhausted %d retries for credential %d", maxRetries, cand.CredentialID)
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
		 WHERE credential_id = $1 AND raw_model_name = $2 AND available = TRUE`,
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
		   AND availability_state NOT IN ('suspended', 'auth_failed')`,
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
	stickyTTL := time.Duration(params.Policy.StickyTTLMilliseconds) * time.Millisecond // M-6: was * time.Second — field is in milliseconds
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
	if kind == errorsx.KindCanceled {
		return
	}
	e.Router.Sticky.RecordFailure(params.StickyKey, 3)
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
