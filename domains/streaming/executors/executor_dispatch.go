package executors

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/transformation"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
	"github.com/kaixuan/llm-gateway-go/internal/runctx"
	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// executor_dispatch.go implements the multi-tier dispatch pipeline
// (domains/dispatch) — per-credential queues, peak-flattening governor,
// tiered credential/model failover. Since AUDIT_24H B2b (2026-08-17) this is
// the ONLY execute path: the dispatch_v2.enabled kill-switch and the legacy
// synchronous candidate loop in Execute were retired. The dispatch path
// reuses the SAME primitives (Router, Limiter, Circuit, FpSlots,
// executeOpenAI/executeAnthropic) so streaming/retry/state semantics are
// preserved. The MM-1/MM-2 outbound attachment transforms were ported here
// from the retired loop (they had silently never run under dispatch). See
// docs/会话优化v2/57-多层队列调度架构设计方案.md.

// dispatchCtx carries the per-request context the shared pipeline's adapters
// need. Stored in QueuedRequest.Payload so the long-lived Pipeline (built once)
// can serve every request without per-request closure allocation.
type dispatchCtx struct {
	params         *ExecParams
	candidates     []provider.Candidate
	byModel        map[string][]provider.Candidate
	initialModel   string
	holder         string
	fpSlotDegraded bool
	retryPerCred   int
	tTotal         time.Time
	stickyCredID   *int // session-affinity pin (honored on first attempt; excluded once tried)
}

// SetDispatchPipeline wires the V2 dispatch pipeline. The pipeline is the
// sole execution path; a nil pipeline is a startup wiring error and causes
// Execute to fail explicitly rather than falling back to a legacy loop.
func (e *Executor) SetDispatchPipeline(p *dispatch.Pipeline) { e.dispatchPipeline = p }

// SetDispatchModelRecommender wires the autoroute decision service used after
// the current model's credentials are exhausted before the first byte.
func (e *Executor) SetDispatchModelRecommender(d DispatchModelRecommender) {
	e.dispatchModelRecommender = d
}

// NewDispatchPipeline builds the shared, long-lived dispatch.Pipeline with
// adapters that read per-request context from QueuedRequest.Payload. Call once
// at startup (cmd/gateway), then SetDispatchPipeline + pipeline.Start().
func (e *Executor) NewDispatchPipeline() *dispatch.Pipeline {
	return dispatch.NewPipeline(dispatch.Deps{
		RouteFunc:            e.dispatchRoute,
		ModelResolveFunc:     e.dispatchResolveModel,
		ModelRecommendFunc:   e.dispatchRecommendModels,
		ForwardFunc:          e.dispatchForward,
		AllowModelChangeFunc: dispatch.IsModelChangeEnabled,
	})
}

// dispatchRoute is the executor-supplied RouteFunc: it re-ranks the request's
// planned candidates via the Router, drops unavailable / already-tried ones,
// and maps them to dispatch.CredentialRef (best first).
func (e *Executor) dispatchRoute(ctx context.Context, qr *dispatch.QueuedRequest) ([]dispatch.CredentialRef, error) {
	dctx, ok := qr.Payload.(*dispatchCtx)
	if !ok || dctx == nil {
		return nil, errDispatchBadPayload
	}
	// Honor session sticky pinning on the first attempt so prompt-cache
	// affinity (Anthropic/OpenAI cache) and session continuity are preserved.
	// Once the sticky cred is tried-and-failed it is in qr.TriedCredentials
	// and naturally excluded here, so failover is unaffected.
	var sticky *int
	if len(qr.TriedCredentials) == 0 {
		sticky = dctx.stickyCredID
	}
	planned := e.Router.PlanCandidatesPinned(
		ctx, e.dispatchCandidatesForModel(ctx, dctx, qr.ResolvedModel), sticky, dctx.params.PinCredentialID, dctx.params.Policy, nil,
		dctx.params.TenantID, dctx.params.ClientModel, dctx.params.RequestID,
	)
	refs := make([]dispatch.CredentialRef, 0, len(planned))
	for _, c := range planned {
		if !dispatchCandidateAllowed(c, dctx.params.PinCredentialID) {
			continue
		}
		if qr.HasTriedCredential(c.CredentialID) {
			continue
		}
		refs = append(refs, candidateToRef(c))
	}
	// 2026-08-13: X-LLM-Pin-Credential hard filter. A trusted internal caller
	// (self-check / node-probe) pinned routing to one credential; drop every
	// other candidate so the probe verdict is attributable to that exact node.
	// If the pinned credential is unavailable/absent the request will exhaust
	// and fail with a clear no-candidate error rather than silently spilling to
	// a different node.
	if pin := dctx.params.PinCredentialID; pin != nil {
		filtered := make([]dispatch.CredentialRef, 0, 1)
		for _, r := range refs {
			if r.CredentialID == *pin {
				filtered = append(filtered, r)
			}
		}
		return filtered, nil
	}
	refs = e.dispatchRouteSoftRank(refs)
	// V3.3-OBS OBS-B1 (2026-08-15): dispatch_v2 路径的 credential_selected
	// 动作事件（S5，Router.PlanCandidates 输出，best-first 首个 ref）。
	if len(refs) > 0 {
		e.liveActions.Emit(ctx, liveactions.ActionEvent{
			RequestID:    qr.ID,
			Action:       liveactions.ActionCredentialSelected,
			Model:        qr.ResolvedModel,
			CredentialID: refs[0].CredentialID,
			Detail: map[string]string{
				"candidates": strconv.Itoa(len(refs)),
			},
		})
	}
	return refs, nil
}

func (e *Executor) dispatchRouteSoftRank(refs []dispatch.CredentialRef) []dispatch.CredentialRef {
	if !e.capacityAwareSortOn || e.capacityAwareSnapFn == nil {
		return refs
	}
	return dispatch.ApplySoftPenalty(refs, e.capacityAwareSnapFn)
}

// dispatchCandidateAllowed preserves a trusted probe pin that the router has
// already rescued from runtime availability filtering; ordinary traffic still
// requires the candidate's normal availability gate.
func dispatchCandidateAllowed(candidate provider.Candidate, pinCredentialID *int) bool {
	return pinCredentialID != nil || candidate.IsAvailable()
}

// dispatchResolveModel returns the requested concrete model plus request-scoped
// alternatives. Auto-route has already rewritten the request body and candidate
// list before Execute; the fallback list is explicitly carried in ExecParams so
// dispatch does not have to infer intent from the rewritten body.
func (e *Executor) dispatchResolveModel(_ context.Context, requested string, _ []string) (string, []string, error) {
	return requested, nil, nil
}

func (e *Executor) dispatchRecommendModels(ctx context.Context, qr *dispatch.QueuedRequest, tried []string) ([]string, error) {
	dctx, ok := qr.Payload.(*dispatchCtx)
	if !ok || dctx == nil || dctx.params == nil {
		return nil, errDispatchBadPayload
	}
	params := dctx.params
	if e.dispatchModelRecommender == nil || !params.DispatchAllowModelChange {
		return nil, autoroute.ErrNoCandidates
	}
	return e.dispatchModelRecommender.RecommendModelAlternatives(ctx, autoroute.ModelAlternativeRequest{
		Task:            autoroute.TaskType(params.DispatchAutoTask),
		Signals:         params.DispatchAutoSignals,
		Profile:         autoroute.Profile(params.DispatchAutoProfile),
		SessionID:       params.SessionID,
		WorkType:        params.DispatchAutoWorkType,
		InitialModel:    dctx.initialModel,
		TriedModels:     append([]string(nil), tried...),
		PreferredModels: append([]string(nil), params.DispatchModelAlternatives...),
	})
}

// dispatchForward is the executor-supplied ForwardFunc: one per-candidate
// upstream attempt.
func (e *Executor) dispatchForward(ctx context.Context, qr *dispatch.QueuedRequest, ref dispatch.CredentialRef) dispatch.ForwardOutcome {
	dctx, ok := qr.Payload.(*dispatchCtx)
	if !ok || dctx == nil {
		return dispatch.ForwardOutcome{Err: errDispatchBadPayload}
	}
	var cand provider.Candidate
	for _, c := range e.dispatchCandidatesForModel(ctx, dctx, qr.ResolvedModel) {
		if c.CredentialID == ref.CredentialID {
			cand = c
			break
		}
	}
	if cand.CredentialID == 0 {
		return dispatch.ForwardOutcome{Err: errDispatchNoCandidate}
	}
	attemptRef, ok := qr.ActiveAttemptRef()
	if !ok {
		return dispatch.ForwardOutcome{Err: errDispatchMissingAttempt}
	}
	// ActionUpstreamRequest is emitted by beginUpstreamAttempt immediately
	// before the real HTTP call. Dispatch preparation can still fail in the
	// circuit, limiter, or key rotator and must not look like provider traffic.
	return e.forwardForDispatch(dctx, cand, attemptRef.AttemptID, qr.FirstSemanticByteCallback(), ctx)
}

// candidateToRef maps a routing candidate into dispatch's decoupled view.
func candidateToRef(c provider.Candidate) dispatch.CredentialRef {
	r := dispatch.CredentialRef{
		CredentialID: c.CredentialID,
		ProviderID:   c.ProviderID,
		Vendor:       c.CatalogCode,
	}
	mode := c.ConcurrencyMode
	if mode == "" {
		mode = "concurrency"
	}
	r.ConcurrencyMode = mode
	if c.ConcurrencyLimit != nil {
		r.ConcurrencyLimit = *c.ConcurrencyLimit
	}
	if c.RPMLimit != nil {
		r.RPMLimit = *c.RPMLimit
	}
	if c.TPMLimit != nil {
		r.TPMLimit = *c.TPMLimit
	}
	if c.MaxQueueDepth != nil {
		r.MaxQueueDepth = *c.MaxQueueDepth
	}
	if c.MaxQueueWaitMS != nil {
		r.MaxQueueWaitMS = *c.MaxQueueWaitMS
	}
	return r
}

// dispatchExecutionContext keeps the dispatch wait lifecycle aligned with the
// upstream request. Ordinary streams inherit client cancellation. Only an
// explicit session owner detaches so pending/durable capture can finish after
// disconnect. SurvivalAttempt only identifies retry ownership; ordinary
// survival requests must still release dispatch and upstream resources when
// the client leaves. Non-streaming requests always keep the client context.
func dispatchExecutionContext(params *ExecParams) (context.Context, context.CancelFunc) {
	if params == nil || params.R == nil {
		return context.WithCancel(context.Background())
	}
	if params.IsStream && params.StreamSurvivesClientCancel {
		return context.WithTimeout(context.WithoutCancel(params.R.Context()), detachedStreamMaxLifetime)
	}
	return context.WithCancel(params.R.Context())
}

func copyDispatchAttemptMetadata(ee *ExecuteError, qr *dispatch.QueuedRequest) {
	if ee == nil || qr == nil {
		return
	}
	dctx, _ := qr.Payload.(*dispatchCtx)
	if dctx != nil && dctx.params != nil && dctx.params.UpstreamAttempts != nil {
		ee.Tried = dctx.params.UpstreamAttempts.Used()
	}
}

// executeViaDispatch is the V2 entry point called from Execute. It packages
// the per-request context into a QueuedRequest and blocks on Pipeline.Submit.
func (e *Executor) executeViaDispatch(
	params *ExecParams,
	candidates []provider.Candidate,
	holder string,
	fpSlotDegraded bool,
	stickyCredID *int,
) (*ExecuteResult, error) {
	if e.dispatchPipeline == nil {
		return nil, errDispatchPipelineNotWired
	}
	requestedModel := params.Model
	if requestedModel == "" {
		requestedModel = params.ClientModel
	}
	dctx := &dispatchCtx{
		params:         params,
		candidates:     candidates,
		byModel:        mapCandidatesByModel(candidates),
		initialModel:   requestedModel,
		holder:         holder,
		fpSlotDegraded: fpSlotDegraded,
		// Dispatch mover owns same-node retry. Protocol-local loops must perform
		// exactly one HTTP call or retry ownership multiplies (mover × protocol).
		retryPerCred: 0,
		tTotal:       time.Now(),
		stickyCredID: stickyCredID,
	}
	dispatchCtx, cancelDispatch := dispatchExecutionContext(params)
	defer cancelDispatch()
	qr := dispatch.NewQueuedRequest(params.RequestID, params.TenantID, requestedModel, dispatchCtx, dctx)
	qr.GatewayInstanceID = params.JourneyGatewayInstanceID
	qr.JourneySharedSeq = params.JourneySeq
	qr.JourneyTerminal = params.JourneyTerminal
	// V3.1 waterfall: thread SessionID so WaterfallRequest can carry it for the
	// admin /sessions/{id}/timeline endpoint. Empty for one-shot traffic.
	qr.SessionID = params.SessionID
	qr.EstimatedTokens = estimatePromptTokens(params)
	qr.AllowModelChange = params.DispatchAllowModelChange
	qr.AllowProviderChange = params.DispatchAllowProviderChange
	qr.RetryPerCredential = dispatch.MaxNodeFailures - 1
	qr.ModelAlternatives = append([]string(nil), params.DispatchModelAlternatives...)
	// v6 G-Ⅱ: 定时请求 due time flows into the pipeline's due heap.
	qr.DueAt = params.DispatchDueAt
	// v6 G-Ⅲ: bridge structured dispatch notices to the handler's thinking
	// writer (params.OnNodeJump → preStream `: thinking:` SSE comment). The
	// callback must stay non-blocking — handler.go writes through the
	// serialized stream writer and detaches on failure.
	qr.OnDispatchNotice = bridgeDispatchNotice(params)

	result, err := e.dispatchPipeline.Submit(dispatchCtx, qr)
	if err != nil {
		// Wrap dispatch outcomes into *ExecuteError so the handler's
		// Exhausted branch (handler.go:3878) emits 503 + Retry-After (not
		// 502) and goal-retry (isRetriableError) recognises the kind. A raw
		// dispatch error would fall through to the generic 502 path.
		// Still attach T0–T9 so failure request_logs rows keep queue latency.
		ee := dispatchErrToExecuteError(err)
		copyDispatchAttemptMetadata(ee, qr)
		copyQueueTimestampsToError(ee, qr)
		return nil, ee
	}
	if res, ok := result.(*ExecuteResult); ok && res != nil {
		copyQueueTimestamps(res, qr)
		return res, nil
	}
	slog.Warn("dispatch: unexpected result type", "request_id", params.RequestID)
	ee := &ExecuteError{LastErr: errDispatchBadResult, Exhausted: true, LastKind: errorsx.KindTransient}
	copyQueueTimestampsToError(ee, qr)
	return nil, ee
}

// bridgeDispatchNotice builds the QueuedRequest.OnDispatchNotice transport
// bridge for one request's ExecParams (v6 G-Ⅲ). Two drop situations on this
// path were previously invisible:
//
//   - non-streaming responses have no `: thinking:` SSE channel, so the
//     notice is never surfaced: params.OnNodeJump is nil here (nothing to
//     call), or the handler-side closure discards it because preStream is
//     nil (preStream is only ever initialized for streaming requests);
//   - a streaming request whose preStream keepalive was never initialized
//     (feature disabled / startPreStreamKeepalive failed) silently swallows
//     the notice inside the handler's `if preStream != nil` guard.
//     params.PreStreamPrepared mirrors exactly that condition — all three
//     protocol entries (handler.go / messages.go / responses.go) set it
//     together with the preStream writer.
//
// Both drops are now counted in metrics.DispatchNoticeDroppedTotal. The
// recording is side-effect free: the bridge runs the exact same calls as the
// previous inline closure, so transport behaviour is bit-for-bit unchanged.
func bridgeDispatchNotice(params *ExecParams) func(dispatch.DispatchNotice) {
	return func(notice dispatch.DispatchNotice) {
		if params == nil || params.OnNodeJump == nil {
			metrics.RecordDispatchNoticeDropped(string(notice.Kind), dispatchNoticeDropReason(params))
			return
		}
		if !params.PreStreamPrepared {
			// Still invoke the callback — the handler closure itself decides
			// (and drops) when preStream is nil; we only observe the fact so
			// the drop rate is measurable.
			metrics.RecordDispatchNoticeDropped(string(notice.Kind), dispatchNoticeDropReason(params))
		}
		params.OnNodeJump(notice.Message)
	}
}

// dispatchNoticeDropReason maps a dropped notice onto the closed reason enum:
// a non-streaming request can never surface notices (no SSE channel exists),
// while any other drop means a streaming request's preStream channel was not
// initialized.
func dispatchNoticeDropReason(params *ExecParams) string {
	if params == nil || !params.IsStream {
		return metrics.DispatchNoticeDropReasonNonStreaming
	}
	return metrics.DispatchNoticeDropReasonPreStreamUninit
}

// dispatchErrToExecuteError maps a dispatch.Pipeline error to *ExecuteError
// with an errorsx kind that drives the handler's HTTP status + goal-retry.
func dispatchErrToExecuteError(err error) *ExecuteError {
	switch {
	case errors.Is(err, context.Canceled):
		return &ExecuteError{LastErr: err, Exhausted: false, LastKind: errorsx.KindCanceled}
	case errors.Is(err, dispatch.ErrNoRoute), errors.Is(err, dispatch.ErrOverflow):
		// All routes/queues exhausted → 503 + Retry-After; retryable.
		return &ExecuteError{LastErr: err, Exhausted: true, LastKind: errorsx.KindConcurrent}
	case errors.Is(err, context.DeadlineExceeded):
		return &ExecuteError{LastErr: err, Exhausted: true, LastKind: errorsx.KindTimeout}
	case errors.Is(err, dispatch.ErrScheduleTooFar):
		// 定时请求的 due time 超出允许窗口：客户端参数问题，不可重试。
		return &ExecuteError{LastErr: err, Exhausted: true, LastKind: errorsx.KindClientBug}
	default:
		if ce, ok := err.(*dispatchErr); ok && ce != nil {
			// Forward-path sentinels (circuit open / fp saturated / keys
			// exhausted / no candidate): treat as transient exhaustion.
			return &ExecuteError{LastErr: err, Exhausted: true, LastKind: errorsx.KindTransient}
		}
		return &ExecuteError{LastErr: err, Exhausted: true, LastKind: errorsx.KindTransient}
	}
}

func (e *Executor) dispatchCandidatesForModel(ctx context.Context, d *dispatchCtx, model string) []provider.Candidate {
	if d == nil {
		return nil
	}
	if model == "" || model == d.initialModel {
		return d.candidates
	}
	if cands := d.candidatesForModel(model); len(cands) > 0 {
		return cands
	}
	if e.Provider == nil || d.params == nil {
		return nil
	}
	var (
		cands  []provider.Candidate
		policy *provider.Policy
		err    error
	)
	if d.params.DispatchRequestModality != "" {
		resolver, ok := e.Provider.(modalityProviderResolver)
		if !ok {
			return nil
		}
		cands, policy, err = resolver.GetCandidatesByModality(ctx, model,
			d.params.ClientID.Fingerprint.ClientProfile, d.params.TenantID, d.params.DispatchRequestModality)
	} else {
		cands, policy, err = e.Provider.GetCandidates(ctx, model,
			d.params.ClientID.Fingerprint.ClientProfile, d.params.TenantID)
	}
	if err != nil || len(cands) == 0 {
		slog.Warn("dispatch: alternate model candidate resolve failed",
			"request_id", d.params.RequestID, "model", model, "error", err)
		return nil
	}
	if policy != nil {
		d.params.Policy = policy
	}
	if d.byModel == nil {
		d.byModel = map[string][]provider.Candidate{}
	}
	d.byModel[model] = cands
	return cands
}

func (d *dispatchCtx) candidatesForModel(model string) []provider.Candidate {
	if d == nil {
		return nil
	}
	if model == "" {
		return d.candidates
	}
	return d.byModel[model]
}

func mapCandidatesByModel(candidates []provider.Candidate) map[string][]provider.Candidate {
	if len(candidates) == 0 {
		return nil
	}
	out := make(map[string][]provider.Candidate)
	for _, c := range candidates {
		if c.StandardizedName != "" {
			out[c.StandardizedName] = append(out[c.StandardizedName], c)
		}
	}
	return out
}

// forwardForDispatch performs ONE upstream forward attempt for a candidate and
// returns the dispatch outcome. Mirrors the per-candidate core of the legacy
// loop (fp slot → circuit → Limiter.AcquireAllNoCredLayer → key rotator →
// executeOpenAI/executeAnthropic → success/error side effects) but returns
// control to the dispatch mover on pre-firstbyte failure.
func (e *Executor) forwardForDispatch(dctx *dispatchCtx, cand provider.Candidate, attemptID string, firstSemanticByte func(), dispatchContexts ...context.Context) (out dispatch.ForwardOutcome) {
	paramsCopy := *dctx.params
	if len(dispatchContexts) > 0 && dispatchContexts[0] != nil {
		paramsCopy.R = paramsCopy.R.WithContext(dispatchContexts[0])
	}
	paramsCopy.DispatchAttempt = true
	paramsCopy.DispatchAttemptID = attemptID
	visibility := &atomic.Bool{}
	paramsCopy.ClientSemanticBytesVisible = visibility
	paramsCopy.FirstSemanticByteCallback = func() {
		visibility.Store(true)
		if firstSemanticByte != nil {
			firstSemanticByte()
		}
	}
	params := &paramsCopy
	startedAt := time.Now()
	probeConsumed := false
	healthEvidence := false
	failureLogged := false
	defer func() {
		if recovered := recover(); recovered != nil {
			out = dispatch.ForwardOutcome{Err: fmt.Errorf("dispatch forward panic: %v", recovered)}
		}
		if out.Err != nil {
			kind := classifyExecError(out.Err)
			out.ErrorKind = string(kind)
			out.HTTPStatus = dispatchHTTPStatus(out.Err)
			if e.FailureLogger != nil && !failureLogged {
				extra := buildEnhancedErrorContext(params, kind, out.Err, len(dctx.candidates), params.AttemptNo)
				if extra == nil {
					extra = map[string]any{}
				}
				extra["failure_stage"], extra["preflight_reason"] = dispatchFailureStage(out.Err)
				e.FailureLogger.LogFailureWithKind(
					params.R.Header.Get("X-Request-Id"), tenantFromCtx(params.R), params.SessionID,
					cand.CredentialID, cand.ProviderID, cand.RawModel, params.AttemptNo,
					out.Err, kind, nil, nil, extra,
				)
				failureLogged = true
			}
		} else if result, ok := out.Result.(*ExecuteResult); ok && result != nil && result.Response != nil {
			out.HTTPStatus = result.Response.StatusCode
		}
		sideEffectCtx, cancel := runctx.DetachedTimeout(params.R.Context(), 5*time.Second)
		defer cancel()
		decision, applied := e.reduceDispatchForwardOutcome(sideEffectCtx, params, cand, attemptID, out, startedAt, healthEvidence)
		if probeConsumed && e.Circuit != nil && !dispatchOutcomeConsumesProbe(decision, applied) {
			e.Circuit.ReleaseProbe(cand.ProviderID, cand.CredentialID)
		}
	}()

	// ── FP slot (best-effort). A slot can become saturated after the
	// prefilter but before this queued request reaches Forward. Degrade at the
	// actual Acquire point as well; fingerprint isolation must not turn a
	// healthy provider into a request failure under concurrent dispatch. ──
	var fpLease *credentialfpslot.Lease
	if e.FpSlots != nil && e.FpSlots.Enabled() {
		lease, ok := e.FpSlots.Acquire(params.R.Context(), cand.CredentialID, cand.FpSlotLimit, dctx.holder, fpSlotTenantID(params))
		if !ok {
			dctx.fpSlotDegraded = true
			fpSlotDegradedTotal.WithLabelValues(params.ClientModel, "acquire_saturated").Inc()
			slog.Warn("dispatch fp slot saturated, running without slot",
				"request_id", params.RequestID,
				"credential_id", cand.CredentialID,
				"provider_id", cand.ProviderID,
			)
			// 2026-08-30 P1: 记录 fpSlot 饱和降级到 candidate_failure_logs_hot
			// 审计发现：fpSlot 饱和未被记录，运维无法看到哪些 credential 频繁触发限流降级
			// 2026-08-31 复审改用 KindFpSlotSaturated（而非 KindRateLimit）：
			// 此路径是降级继续（请求仍会执行并大概率成功），且是网关侧按指纹的
			// 准入信号，与上游 429 的供应商质量问题不同桶，避免污染
			// provider_error_details 的 rate_limit 统计与凭据质量评估。
			logDispatchPreflightRejection(e.FailureLogger, params, cand, dctx, startedAt,
				errDispatchFpSlotSaturated, errorsx.KindFpSlotSaturated,
				map[string]any{
					"degraded_continue": true,
					"rejection_type":    "fp_slot_saturated",
					"candidates_left":   len(dctx.candidates),
				},
			)
		} else {
			fpLease = lease
		}
	}

	// ── Circuit breaker (fail-open when the module is disabled) ──
	circuitOpen := !settings.IsEnabled("circuit_degradation")
	if !circuitOpen {
		probeConsumed = e.Circuit.Allow(cand.ProviderID, cand.CredentialID)
		circuitOpen = !probeConsumed
	}
	if circuitOpen && settings.IsEnabled("circuit_degradation") {
		releaseFpLease(e.FpSlots, fpLease)

		// 2026-08-29 P1: 记录熔断拒绝到 candidate_failure_logs_hot
		// 审计发现：circuit-open 错误未记录，运维无法看到哪些 credential 处于熔断状态
		extra := map[string]any{
			"circuit_open":   true,
			"rejection_type": "circuit_breaker",
		}
		// 获取熔断器状态用于诊断
		if e.Circuit != nil {
			if breaker := e.Circuit.Get(cand.ProviderID, cand.CredentialID); breaker != nil {
				extra["circuit_state"] = breaker.State().String()
				extra["circuit_consecutive_failures"] = breaker.ConsecutiveFailures()
			}
		}
		logDispatchPreflightRejection(e.FailureLogger, params, cand, dctx, startedAt,
			errDispatchCircuitOpen, errorsx.KindCircuitOpen, extra)

		return dispatch.ForwardOutcome{Err: errDispatchCircuitOpen}
	}

	// ── Outer concurrency layers (global/pool/identity/key). The credential
	// layer + RPM are owned by the dispatch governor → use the no-cred-layer
	// variant to avoid double-counting. ──
	release, acquireErr := e.Limiter.AcquireAllNoCredLayer(
		params.R.Context(),
		cand.ProviderID,
		cand.CredentialID,
		params.ClientID.IdentityHash,
		params.KeyID,
		params.KeyConcurrentLimit,
	)
	if acquireErr != nil {
		releaseFpLease(e.FpSlots, fpLease)
		if probeConsumed && e.Circuit != nil {
			e.Circuit.ReleaseProbe(cand.ProviderID, cand.CredentialID)
			probeConsumed = false
		}

		// 2026-08-29 P1: 记录并发限流拒绝到 candidate_failure_logs_hot
		// 审计发现：并发限流拒绝未记录，无法统计因并发限流导致的失败
		logDispatchPreflightRejection(e.FailureLogger, params, cand, dctx, startedAt,
			acquireErr, errorsx.KindRateLimit,
			map[string]any{
				"rate_limit_rejection": true,
				"rejection_type":       "concurrency_limiter",
				"candidates_left":      len(dctx.candidates),
			})

		return dispatch.ForwardOutcome{Err: acquireErr}
	}

	var result *ExecuteResult
	var execErr error
	func() {
		releasePeak := e.PeakCollector != nil
		if releasePeak {
			e.PeakCollector.Acquire(int64(cand.CredentialID), cand.RawModel)
		}
		defer func() {
			if releasePeak {
				e.PeakCollector.Release(int64(cand.CredentialID), cand.RawModel)
			}
			release()
			releaseFpLease(e.FpSlots, fpLease)
		}()

		// multi-key rotation (same as legacy loop).
		if cand.KeyRotator != nil {
			idx := cand.KeyRotator.ResolveKey(cand.CredentialID, -1)
			if idx < 0 {
				execErr = errDispatchKeysExhausted
				// 2026-08-30 P1: 记录 key 轮询耗尽到 candidate_failure_logs_hot
				// 审计发现：所有 api key 都标记为不可用时，credential 仍会被选入
				// 候选队列并最终在第一个请求处失败。运营需要看到这类"凭据已耗尽"
				// 事件来诊断配额/欠费/封号场景。failureLogged=true 抑制 post-block
				// 的通用 LogFailure（后者会用 KindTransient 替换分类）。
				logDispatchPreflightRejection(e.FailureLogger, params, cand, dctx, startedAt,
					errDispatchKeysExhausted, errorsx.KindRateLimit,
					map[string]any{
						"rejection_type": "key_rotation_exhausted",
					})
				failureLogged = true
				return
			} else if idx >= 1 && idx-1 < len(cand.APIKeys) {
				cand.APIKey = cand.APIKeys[idx-1]
			}
		}

		// ── MM-1/MM-2 outbound attachment transforms ─────────────────
		// Native Responses candidates must transform their own preserved
		// Responses envelope; legacy candidates continue using the Chat body.
		attachmentBody := params.BodyBytes
		nativeBody := cand.Protocol == "openai-responses" && (cand.SupportsNativeResponses || cand.SupportsNativeResponsesStream) && len(params.ResponsesBodyBytes) > 0
		if nativeBody {
			attachmentBody = params.ResponsesBodyBytes
		}
		// Ported from the retired legacy sync candidate loop (AUDIT_24H

		// B2b, 2026-08-17). Per-candidate: derive the attempt body from the
		// ORIGINAL body so a failover from a URL-mode provider to a
		// data-URI-only provider never inherits rewritten URLs. Until this
		// port the dispatch path silently skipped both hooks — the loop was
		// their only consumer, so the feature had been inert in production
		// since dispatch_v2 became the default path.
		execParams := params
		if e.AttachmentURLRewriter != nil && len(params.AttachmentMetadata) > 0 {
			var newBody []byte
			var n int
			if params.ClientProtocol == "anthropic-messages" {
				// E-P2-3 (doc 20): Anthropic-protocol clients bridged to a
				// URL-mode OpenAI provider — rewrite the Anthropic base64
				// source blocks to url sources before the bridge conversion
				// maps them to image_url.
				newBody, n = e.AttachmentURLRewriter.RewriteAnthropicBody(
					attachmentBody, params.AttachmentMetadata, cand.CatalogCode)
			} else {
				newBody, n = e.AttachmentURLRewriter.RewriteOpenAIBody(
					attachmentBody, params.AttachmentMetadata, cand.CatalogCode)
			}
			if n > 0 {
				cp := *params
				if nativeBody {
					cp.ResponsesBodyBytes = newBody
				} else {
					cp.BodyBytes = newBody
				}
				execParams = &cp
			}

		}
		// MM-2 (doc 19): URL 拉取回退——目标 provider 矩阵判定不支持 url
		// source 而出站 body 以网关 URL 引用附件时，取回内容重新内联
		// base64。flag-off（nil）零开销直通。
		if e.AttachmentURLFetchFallback != nil && !nativeBody {
			if newBody, n := e.AttachmentURLFetchFallback.InlineOpenAIBody(
				execParams.BodyBytes, cand.CatalogCode); n > 0 {
				cp := *execParams
				cp.BodyBytes = newBody
				execParams = &cp
			}
		}

		healthEvidence = true
		// Audit-2026-08-29: native Responses stream capability is verified
		// independently of the non-stream capability. A credential that opts
		// in to native_responses_stream only must still be usable here; the
		// executeOpenAI gate (executor_chat.go:374-382) accepts both forms,
		// so the dispatch gate has to mirror that contract or stream-only
		// candidates are silently dropped before the gate even fires.
		if cand.Protocol == "openai-responses" &&
			!cand.SupportsNativeResponses &&
			!cand.SupportsNativeResponsesStream {
			execErr = fmt.Errorf("native Responses upstream capability is not enabled")
			return
		}
		switch cand.Protocol {
		case "anthropic-messages":
			result, execErr = e.executeAnthropic(execParams, cand, dctx.retryPerCred, dctx.tTotal, fpLease)
		default:
			result, execErr = e.executeOpenAI(execParams, cand, dctx.retryPerCred, dctx.tTotal, fpLease)
		}
	}()

	if execErr == nil {
		e.recordDispatchSuccess(params, cand, result)
		return dispatch.ForwardOutcome{Result: result}
	}

	bytesSent := params.ClientSemanticBytesVisible != nil && params.ClientSemanticBytesVisible.Load()
	if !bytesSent && params.IsStream && params.Capture != nil {
		if sent, _ := params.Capture.ChunkCountersSnapshot(); sent > 0 {
			bytesSent = true
		}
	}
	kind := e.recordDispatchError(params, cand, execErr)

	// candidate_failure_logs (migration 300 + V358 session_id): one row per
	// failed dispatch attempt. Ported from the retired legacy sync loop —
	// both of ee2565046's call sites (generic per-candidate failure +
	// mid-stream interruption) lived in the loop, so without this port the
	// table (and V358's session-scoped aggregation) would have no writer.
	// streamInterruptedError carries no *upstream.Error; pass the classified
	// kind explicitly there — the message-based fallback would flatten e.g.
	// KindNetwork to transient.
	if e.FailureLogger != nil && !failureLogged {
		perAttemptMs := int(time.Since(startedAt).Milliseconds())
		extra := buildEnhancedErrorContext(params, kind, execErr, len(dctx.candidates), params.AttemptNo)
		if extra == nil {
			extra = map[string]any{}
		}
		var sie *streamInterruptedError
		if errors.As(execErr, &sie) && sie != nil {
			extra["stream_reason"] = sie.reason
			extra["stream_resumable"] = sie.resumable
			extra["upstream_status_code"] = sie.statusCode
			extra["upstream_raw_error"] = sie.rawError
			e.FailureLogger.LogFailureWithKind(
				params.R.Header.Get("X-Request-Id"),
				tenantFromCtx(params.R),
				params.SessionID,
				cand.CredentialID,
				cand.ProviderID,
				cand.RawModel,
				params.AttemptNo,
				execErr,
				kind,
				nil,
				&perAttemptMs,
				extra,
			)
			failureLogged = true
		} else {

			e.FailureLogger.LogFailure(
				params.R.Header.Get("X-Request-Id"),
				tenantFromCtx(params.R),
				params.SessionID,
				cand.CredentialID,
				cand.ProviderID,
				cand.RawModel,
				params.AttemptNo,
				execErr,
				nil, // latency_ms: end-to-end candidate latency, not tracked per dispatch forward
				&perAttemptMs,
				extra,
			)
			failureLogged = true
		}

	}

	return dispatch.ForwardOutcome{
		Err:             execErr,
		BytesSent:       bytesSent,
		FatalCredential: errorsx.IsCredentialFatal(kind),
		ErrorKind:       string(kind),
		HTTPStatus:      dispatchHTTPStatus(execErr),
	}
}

// recordDispatchSuccess applies non-authoritative routing side effects
// (sticky, route recorder, health tracker, and mnf reset). Focused subset of
// the legacy loop's success block (executor.go:2566-2696).
func (e *Executor) recordDispatchSuccess(params *ExecParams, cand provider.Candidate, result *ExecuteResult) {
	sideEffectCtx, sideEffectCancel := runctx.DetachedTimeout(params.R.Context(), 5*time.Second)
	defer sideEffectCancel()
	e.recordStickySuccess(params, cand.CredentialID)
	if e.Recorder != nil && e.legacyWritersEnabled() {
		e.Recorder.RecordSuccess(sideEffectCtx, cand.CredentialID, cand.RawModel)
	}
	e.resetMnfStreak(params, cand.CredentialID)
	requestID := params.R.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = "async-" + time.Now().Format("20060102T150405.000")
	}
	if e.HealthTracker != nil {
		e.HealthTracker.OnSuccess(sideEffectCtx, cand.CredentialID, cand.StandardizedName, latencyOr(result, 0), requestID)
	}
}

// recordDispatchError classifies the error and retains model-not-found audit
// and streak tracking. Node-health state and circuit writes are reducer-owned.
func (e *Executor) recordDispatchError(params *ExecParams, cand provider.Candidate, err error) errorsx.ErrorKind {
	kind := classifyExecError(err)
	sideEffectCtx, sideEffectCancel := runctx.DetachedTimeout(params.R.Context(), 5*time.Second)
	defer sideEffectCancel()
	if mnf, ok := err.(*modelNotFoundError); ok {
		mnfKind := mnf.resolvedKind()
		e.recordModelNotFound(sideEffectCtx, mnf.credentialID, mnf.rawModel, mnf.body, mnf.status, mnfKind)
		e.recordMnfStreak(params, cand.CredentialID)
	}
	return kind
}

func dispatchFailureIsCredentialHealthy(kind errorsx.ErrorKind, modelNotFound bool) bool {
	return modelNotFound || errorsx.IsClientBug(kind) ||
		errorsx.IsContentFilter(kind) || kind == errorsx.KindContextLength
}

// estimatePromptTokens returns a rough pre-send token estimate for tpm pacing.
// It reuses the same chars/3.5 heuristic the auto_route path applies, so the
// tpm governor charges an approximation of the real prompt size instead of a
// flat fixed cost. A 0 (empty/unknown body) return is intentional: the tpm
// governor then falls back to its conservative defaultTokenEstimate.
func estimatePromptTokens(params *ExecParams) int {
	if params == nil || len(params.BodyBytes) == 0 {
		return 0
	}
	return transformation.EstimateTokens(params.BodyBytes)
}

func latencyOr(r *ExecuteResult, def int) int {
	if r != nil && r.LatencyMs > 0 {
		return r.LatencyMs
	}
	return def
}

// copyQueueTimestamps copies V3.1 T0–T9 timestamps from the finished
// QueuedRequest onto ExecuteResult for telemetry persistence.
func copyQueueTimestamps(res *ExecuteResult, qr *dispatch.QueuedRequest) {
	if res == nil || qr == nil {
		return
	}
	t0, t1, t2, t3, t4, t5, t6, t7, t8, t9 := extractQueueTimestamps(qr)
	res.T0ArrivedAt = t0
	res.T1TotalEnqueuedAt = t1
	res.T2TotalDequeuedAt = t2
	res.T3ModelEnqueuedAt = t3
	res.T4ModelDequeuedAt = t4
	res.T5CredEnqueuedAt = t5
	res.T6CredDequeuedAt = t6
	res.T7ForwardStartAt = t7
	res.T8ResponseStartAt = t8
	res.T9ResponseEndAt = t9
}

// copyQueueTimestampsToError attaches the same T0–T9 set onto ExecuteError
// so failure rows can still persist queue latency.
func copyQueueTimestampsToError(ee *ExecuteError, qr *dispatch.QueuedRequest) {
	if ee == nil || qr == nil {
		return
	}
	t0, t1, t2, t3, t4, t5, t6, t7, t8, t9 := extractQueueTimestamps(qr)
	ee.T0ArrivedAt = t0
	ee.T1TotalEnqueuedAt = t1
	ee.T2TotalDequeuedAt = t2
	ee.T3ModelEnqueuedAt = t3
	ee.T4ModelDequeuedAt = t4
	ee.T5CredEnqueuedAt = t5
	ee.T6CredDequeuedAt = t6
	ee.T7ForwardStartAt = t7
	ee.T8ResponseStartAt = t8
	ee.T9ResponseEndAt = t9
}

func extractQueueTimestamps(qr *dispatch.QueuedRequest) (
	t0, t1, t2, t3, t4, t5, t6, t7, t8, t9 *time.Time,
) {
	if qr == nil {
		return
	}
	return qr.StageTimestamps()
}

// sentinel errors for the dispatch forward path.
var (
	errDispatchPipelineNotWired = newDispatchErr("dispatch: pipeline not wired")
	errDispatchNoCandidate      = newDispatchErr("dispatch: candidate not found in planned list")
	errDispatchMissingAttempt   = newDispatchErr("dispatch: missing active attempt")
	errDispatchBadResult        = newDispatchErr("dispatch: unexpected result type")
	errDispatchBadPayload       = newDispatchErr("dispatch: payload is not *dispatchCtx")
	errDispatchFpSlotSaturated  = newDispatchErr("dispatch: fp slot saturated")
	errDispatchCircuitOpen      = newDispatchErr("dispatch: circuit open")
	errDispatchKeysExhausted    = newDispatchErr("dispatch: all keys exhausted")
)

// logDispatchPreflightRejection is the shared writer for pre-upstream
// admission rejections (fp-slot saturation, circuit-open, limiter rejection,
// key-rotation exhaustion). Consolidates the boilerplate that was previously
// inlined four times in forwardForDispatch and gives every preflight rejection
// the same shape on candidate_failure_logs_hot:
//
//   - explicit kind so the dashboard can filter "circuit_open" /
//     "rate_limit" / "fp_slot_saturated" independently of the upstream-error
//     classifiers (which can't tell why an attempt was rejected before any
//     HTTP call)
//   - rejection_type + candidates_left in the JSON context so the operator
//     can see how many siblings are still eligible
//   - perAttemptLatencyMs only (no end-to-end latency), matching the legacy
//     candidate-loop convention
//
// nil-safe: writer == nil is a no-op (mirrors LogFailure's own contract).
func logDispatchPreflightRejection(
	writer *CandidateFailureWriter,
	params *ExecParams,
	cand provider.Candidate,
	dctx *dispatchCtx,
	startedAt time.Time,
	rejErr error,
	kind errorsx.ErrorKind,
	extra map[string]any,
) {
	if writer == nil {
		return
	}
	if params == nil || dctx == nil || rejErr == nil {
		return
	}
	if extra == nil {
		extra = map[string]any{}
	}
	if _, ok := extra["candidates_left"]; !ok {
		extra["candidates_left"] = len(dctx.candidates)
	}
	perAttemptMs := int(time.Since(startedAt).Milliseconds())
	writer.LogFailureWithKind(
		params.R.Header.Get("X-Request-Id"),
		tenantFromCtx(params.R),
		params.SessionID,
		cand.CredentialID,
		cand.ProviderID,
		cand.RawModel,
		params.AttemptNo,
		rejErr,
		kind,
		nil,
		&perAttemptMs,
		extra,
	)
}

type dispatchErr struct{ msg string }

func (e *dispatchErr) Error() string { return e.msg }

func newDispatchErr(msg string) error { return &dispatchErr{msg: msg} }
