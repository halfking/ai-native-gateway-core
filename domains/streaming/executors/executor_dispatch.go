package executors

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/transformation"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
	"github.com/kaixuan/llm-gateway-go/internal/runctx"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// executor_dispatch.go wires the multi-tier dispatch pipeline (domains/dispatch)
// into the executor as a SEPARATE, feature-flagged code path:
//   - dispatch_v2.enabled ON  → executeViaDispatch (per-credential queue +
//     peak-flattening governor + tiered credential/model failover)
//   - dispatch_v2.enabled OFF → the legacy synchronous candidate loop in
//     Execute (unchanged, the kill-switch fallback)
//
// The dispatch path reuses the SAME primitives (Router, Limiter, Circuit,
// FpSlots, executeOpenAI/executeAnthropic) so streaming/retry/state semantics
// are preserved. It does not replicate every nuanced branch of the legacy loop
// (predictive TTFB, session blacklist, content_filter sibling-skip, async
// retry); those remain in the legacy OFF path. See
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
	stickyCredID *int // session-affinity pin (honored on first attempt; excluded once tried)
}

// SetDispatchPipeline wires the V2 dispatch pipeline. When nil OR when the
// dispatch_v2 gate is off, Execute uses the legacy synchronous loop.
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
	if !qr.HasTriedCredential(0) && len(qr.TriedCredentials) == 0 {
		sticky = dctx.stickyCredID
	}
	planned := e.Router.PlanCandidatesWithContext(
		ctx, e.dispatchCandidatesForModel(ctx, dctx, qr.ResolvedModel), sticky, dctx.params.Policy, nil,
		dctx.params.TenantID, dctx.params.ClientModel, dctx.params.RequestID,
	)
	refs := make([]dispatch.CredentialRef, 0, len(planned))
	for _, c := range planned {
		if !c.IsAvailable() {
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
// explicit session or survival owner detaches so pending/durable capture can
// finish after disconnect. Non-streaming requests keep the client context even
// when they carry a session id: there is no stream capture to drain.
func dispatchExecutionContext(params *ExecParams) (context.Context, context.CancelFunc) {
	if params == nil || params.R == nil {
		return context.WithCancel(context.Background())
	}
	if params.IsStream && (params.StreamSurvivesClientCancel || params.SurvivalAttempt) {
		return context.WithCancel(context.WithoutCancel(params.R.Context()))
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
		return nil, nil
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
	paramsCopy.FirstSemanticByteCallback = firstSemanticByte
	params := &paramsCopy
	startedAt := time.Now()
	probeConsumed := false
	healthEvidence := false
	defer func() {
		if recovered := recover(); recovered != nil {
			out = dispatch.ForwardOutcome{Err: fmt.Errorf("dispatch forward panic: %v", recovered)}
		}
		if out.Err != nil {
			kind := classifyExecError(out.Err)
			out.ErrorKind = string(kind)
			out.HTTPStatus = dispatchHTTPStatus(out.Err)
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
				return
			} else if idx >= 1 && idx-1 < len(cand.APIKeys) {
				cand.APIKey = cand.APIKeys[idx-1]
			}
		}

		healthEvidence = true
		switch cand.Protocol {
		case "anthropic-messages":
			result, execErr = e.executeAnthropic(params, cand, dctx.retryPerCred, dctx.tTotal, fpLease)
		default:
			result, execErr = e.executeOpenAI(params, cand, dctx.retryPerCred, dctx.tTotal, fpLease)
		}
	}()

	if execErr == nil {
		e.recordDispatchSuccess(params, cand, result)
		return dispatch.ForwardOutcome{Result: result}
	}

	bytesSent := false
	if params.IsStream && params.Capture != nil {
		if sent, _ := params.Capture.ChunkCountersSnapshot(); sent > 0 {
			bytesSent = true
		}
	}
	kind := e.recordDispatchError(params, cand, execErr)
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
	if !qr.T0_ArrivedAt.IsZero() {
		t := qr.T0_ArrivedAt
		t0 = &t
	}
	t1 = qr.T1_TotalEnqueuedAt
	t2 = qr.T2_TotalDequeuedAt
	t3 = qr.T3_ModelEnqueuedAt
	t4 = qr.T4_ModelDequeuedAt
	t5 = qr.T5_CredEnqueuedAt
	t6 = qr.T6_CredDequeuedAt
	t7 = qr.T7_ForwardStartAt
	t8 = qr.T8_ResponseStartAt
	t9 = qr.T9_ResponseEndAt
	return
}

// sentinel errors for the dispatch forward path.
var (
	errDispatchNoCandidate     = newDispatchErr("dispatch: candidate not found in planned list")
	errDispatchMissingAttempt  = newDispatchErr("dispatch: missing active attempt")
	errDispatchBadResult       = newDispatchErr("dispatch: unexpected result type")
	errDispatchBadPayload      = newDispatchErr("dispatch: payload is not *dispatchCtx")
	errDispatchFpSlotSaturated = newDispatchErr("dispatch: fp slot saturated")
	errDispatchCircuitOpen     = newDispatchErr("dispatch: circuit open")
	errDispatchKeysExhausted   = newDispatchErr("dispatch: all keys exhausted")
)

type dispatchErr struct{ msg string }

func (e *dispatchErr) Error() string { return e.msg }

func newDispatchErr(msg string) error { return &dispatchErr{msg: msg} }
