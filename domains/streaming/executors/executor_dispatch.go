package executors

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	ursmv2api "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/transformation"
	"github.com/kaixuan/llm-gateway-go/errorsx"
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
	holder         string
	fpSlotDegraded bool
	retryPerCred   int
	tTotal         time.Time
	stickyCredID   *int // session-affinity pin (honored on first attempt; excluded once tried)
}

// SetDispatchPipeline wires the V2 dispatch pipeline. When nil OR when the
// dispatch_v2 gate is off, Execute uses the legacy synchronous loop.
func (e *Executor) SetDispatchPipeline(p *dispatch.Pipeline) { e.dispatchPipeline = p }

// NewDispatchPipeline builds the shared, long-lived dispatch.Pipeline with
// adapters that read per-request context from QueuedRequest.Payload. Call once
// at startup (cmd/gateway), then SetDispatchPipeline + pipeline.Start().
func (e *Executor) NewDispatchPipeline(allowModelChange bool) *dispatch.Pipeline {
	return dispatch.NewPipeline(dispatch.Deps{
		RouteFunc:        e.dispatchRoute,
		ModelResolveFunc: e.dispatchResolveModel,
		ForwardFunc:      e.dispatchForward,
		AllowModelChange: allowModelChange,
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
	return refs, nil
}

// dispatchResolveModel returns the requested concrete model plus request-scoped
// alternatives. Auto-route has already rewritten the request body and candidate
// list before Execute; the fallback list is explicitly carried in ExecParams so
// dispatch does not have to infer intent from the rewritten body.
func (e *Executor) dispatchResolveModel(_ context.Context, requested string, _ []string) (string, []string, error) {
	return requested, nil, nil
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
	return e.forwardForDispatch(dctx, cand)
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
	retryPerCred := 0
	if params.Policy != nil {
		retryPerCred = params.Policy.RetryPerCredential
	}
	dctx := &dispatchCtx{
		params:         params,
		candidates:     candidates,
		byModel:        mapCandidatesByModel(candidates),
		holder:         holder,
		fpSlotDegraded: fpSlotDegraded,
		retryPerCred:   retryPerCred,
		tTotal:         time.Now(),
		stickyCredID:   stickyCredID,
	}
	requestedModel := params.Model
	if requestedModel == "" {
		requestedModel = params.ClientModel
	}
	qr := dispatch.NewQueuedRequest(params.RequestID, params.TenantID, requestedModel, params.R.Context(), dctx)
	qr.EstimatedTokens = estimatePromptTokens(params)
	qr.AllowModelChange = params.DispatchAllowModelChange && len(params.DispatchModelAlternatives) > 0
	qr.AllowProviderChange = params.DispatchAllowProviderChange
	qr.RetryPerCredential = retryPerCred
	qr.ModelAlternatives = append([]string(nil), params.DispatchModelAlternatives...)

	result, err := e.dispatchPipeline.Submit(params.R.Context(), qr)
	if err != nil {
		// Wrap dispatch outcomes into *ExecuteError so the handler's
		// Exhausted branch (handler.go:3878) emits 503 + Retry-After (not
		// 502) and goal-retry (isRetriableError) recognises the kind. A raw
		// dispatch error would fall through to the generic 502 path.
		return nil, dispatchErrToExecuteError(err)
	}
	if res, ok := result.(*ExecuteResult); ok && res != nil {
		return res, nil
	}
	slog.Warn("dispatch: unexpected result type", "request_id", params.RequestID)
	return nil, &ExecuteError{LastErr: errDispatchBadResult, Exhausted: true, LastKind: errorsx.KindTransient}
}

// dispatchErrToExecuteError maps a dispatch.Pipeline error to *ExecuteError
// with an errorsx kind that drives the handler's HTTP status + goal-retry.
func dispatchErrToExecuteError(err error) *ExecuteError {
	switch {
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
	if model == "" {
		return d.candidates
	}
	if cands := d.candidatesForModel(model); len(cands) > 0 {
		return cands
	}
	if e.Provider == nil || d.params == nil || d.params.DispatchRequestModality == "" {
		return d.candidates
	}
	resolver, ok := e.Provider.(modalityProviderResolver)
	if !ok {
		return d.candidates
	}
	cands, policy, err := resolver.GetCandidatesByModality(ctx, model,
		d.params.ClientID.Fingerprint.ClientProfile, d.params.TenantID, d.params.DispatchRequestModality)
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
	if cands := d.byModel[model]; len(cands) > 0 {
		return cands
	}
	return d.candidates
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
func (e *Executor) forwardForDispatch(dctx *dispatchCtx, cand provider.Candidate) dispatch.ForwardOutcome {
	params := dctx.params

	// ── FP slot (best-effort; degraded mode tolerates failure) ──
	var fpLease *credentialfpslot.Lease
	if e.FpSlots != nil && e.FpSlots.Enabled() {
		lease, ok := e.FpSlots.Acquire(params.R.Context(), cand.CredentialID, cand.FpSlotLimit, dctx.holder, fpSlotTenantID(params))
		if !ok {
			if dctx.fpSlotDegraded {
				fpLease = nil
			} else {
				return dispatch.ForwardOutcome{Err: errDispatchFpSlotSaturated}
			}
		} else {
			fpLease = lease
		}
	}

	// ── Circuit breaker (fail-open when the module is disabled) ──
	circuitOpen := !settings.IsEnabled("circuit_degradation")
	probeConsumed := false
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
	e.recordDispatchError(params, cand, execErr, probeConsumed, len(dctx.candidates))
	return dispatch.ForwardOutcome{Err: execErr, BytesSent: bytesSent}
}

// recordDispatchSuccess applies the routing-critical success side-effects
// (sticky, state restore, health, mnf-reset, URSM record). Focused subset of
// the legacy loop's success block (executor.go:2566-2696).
func (e *Executor) recordDispatchSuccess(params *ExecParams, cand provider.Candidate, result *ExecuteResult) {
	sideEffectCtx, sideEffectCancel := runctx.DetachedTimeout(params.R.Context(), 5*time.Second)
	defer sideEffectCancel()
	e.restoreCredentialState(sideEffectCtx, cand.CredentialID, cand.StandardizedName)
	e.recordStickySuccess(params, cand.CredentialID)
	if e.Recorder != nil && e.legacyWritersEnabled() {
		e.Recorder.RecordSuccess(sideEffectCtx, cand.CredentialID, cand.RawModel)
	}
	if e.NodeProbeHealthy != nil && cand.RawModel != "" {
		_ = e.NodeProbeHealthy(sideEffectCtx, cand.CredentialID, cand.RawModel)
	}
	e.resetMnfStreak(params, cand.CredentialID)
	requestID := params.R.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = "async-" + time.Now().Format("20060102T150405.000")
	}
	if e.HealthTracker != nil {
		e.HealthTracker.OnSuccess(sideEffectCtx, cand.CredentialID, cand.StandardizedName, latencyOr(result, 0), requestID)
	}
	if e.URSMv2 != nil {
		_ = e.URSMv2.RecordRequest(params.R.Context(), ursmv2api.RequestOutcome{
			CredentialID: cand.CredentialID,
			RawModel:     cand.RawModel,
			TenantID:     params.TenantID,
			BillingMode:  cand.BillingMode,
			Success:      true,
			LatencyMs:    latencyOr(result, 0),
			RequestID:    requestID,
		})
	}
}

// recordDispatchError classifies the error and updates credential state so a
// failing credential cools. model_not_found is recorded at binding scope.
// totalCandidates is the post-filter candidate count (from the dispatch
// context), mirroring legacy's totalCandidates so the sole-candidate fail-open
// rule is honoured: never escalate the breaker when only one candidate remains.
func (e *Executor) recordDispatchError(params *ExecParams, cand provider.Candidate, err error, probeConsumed bool, totalCandidates int) {
	kind := classifyExecError(err)
	sideEffectCtx, sideEffectCancel := runctx.DetachedTimeout(params.R.Context(), 5*time.Second)
	defer sideEffectCancel()
	if mnf, ok := err.(*modelNotFoundError); ok {
		mnfKind := mnf.resolvedKind()
		e.recordModelNotFound(sideEffectCtx, mnf.credentialID, mnf.rawModel, mnf.body, mnf.status, mnfKind)
		e.writeCredentialStateOnError(sideEffectCtx, mnf.credentialID, cand.StandardizedName, mnfKind, err)
		e.recordMnfStreak(params, cand.CredentialID)
	} else if !errorsx.IsClientBug(kind) {
		e.writeCredentialStateOnError(sideEffectCtx, cand.CredentialID, cand.StandardizedName, kind, err)
	}

	propagateToBreaker := !errorsx.IsClientBug(kind) && !freeCredentialsTolerateTransient(cand.BillingMode, kind) && totalCandidates > 1
	if propagateToBreaker && e.Circuit != nil {
		e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, kind)
	} else if probeConsumed && e.Circuit != nil {
		e.Circuit.ReleaseProbe(cand.ProviderID, cand.CredentialID)
	}
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

// sentinel errors for the dispatch forward path.
var (
	errDispatchNoCandidate     = newDispatchErr("dispatch: candidate not found in planned list")
	errDispatchBadResult       = newDispatchErr("dispatch: unexpected result type")
	errDispatchBadPayload      = newDispatchErr("dispatch: payload is not *dispatchCtx")
	errDispatchFpSlotSaturated = newDispatchErr("dispatch: fp slot saturated")
	errDispatchCircuitOpen     = newDispatchErr("dispatch: circuit open")
	errDispatchKeysExhausted   = newDispatchErr("dispatch: all keys exhausted")
)

type dispatchErr struct{ msg string }

func (e *dispatchErr) Error() string { return e.msg }

func newDispatchErr(msg string) error { return &dispatchErr{msg: msg} }
