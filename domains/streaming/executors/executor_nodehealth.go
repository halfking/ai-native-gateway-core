package executors

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
	ursmv2api "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

func (e *Executor) nodeOutcomeAuthority() (*nodehealth.OutcomeReducer, nodehealth.Adapter) {
	if e == nil {
		return nil, nil
	}
	e.nodeHealthMu.Lock()
	defer e.nodeHealthMu.Unlock()
	if e.NodeOutcomeReducer == nil {
		e.NodeOutcomeReducer = nodehealth.NewOutcomeReducer()
	}
	adapter := e.NodeHealthAdapter
	if adapter == nil {
		adapter = executorNodeHealthAdapter{executor: e}
	}
	return e.NodeOutcomeReducer, adapter
}

func (e *Executor) reduceDispatchOutcome(ctx context.Context, observation nodehealth.Observation) (nodehealth.Decision, bool) {
	reducer, adapter := e.nodeOutcomeAuthority()
	if reducer == nil {
		return nodehealth.Decision{}, false
	}
	decision, err := reducer.ReduceAndApply(ctx, observation, adapter)
	if err != nil {
		slog.Warn("dispatch node-health outcome failed",
			"attempt_id", observation.AttemptID,
			"credential_id", observation.Node.CredentialID,
			"model", observation.Node.Model,
			"error", err)
		return decision, false
	}
	return decision, decision.Accepted
}

func (e *Executor) reduceDispatchForwardOutcome(
	ctx context.Context,
	params *ExecParams,
	cand provider.Candidate,
	attemptID string,
	out dispatch.ForwardOutcome,
	startedAt time.Time,
	healthEvidence bool,
) (nodehealth.Decision, bool) {
	// Node health and URSM state must use the upstream raw model. A standardized
	// client-facing name can map to multiple provider model bindings, so using it
	// here would merge their empty-response windows and misroute sibling models.
	model := cand.BindingRawModel()
	if model == "" {
		model = strings.TrimSpace(cand.StandardizedName)
	}
	if model == "" && params != nil {
		model = strings.TrimSpace(params.Model)
	}
	requestID := ""
	tenantID := ""
	if params != nil {
		requestID = params.RequestID
		tenantID = params.TenantID
		if requestID == "" && params.R != nil {
			requestID = params.R.Header.Get("X-Request-Id")
		}
	}

	latencyMs := int(time.Since(startedAt).Milliseconds())
	if result, ok := out.Result.(*ExecuteResult); ok && result != nil && result.LatencyMs > 0 {
		latencyMs = result.LatencyMs
	}
	outcome := requestjourney.OutcomeSuccess
	kind := nodehealth.ErrorKind("")
	if out.Err != nil {
		execKind := classifyExecError(out.Err)
		if errors.Is(out.Err, context.Canceled) || errors.Is(out.Err, context.DeadlineExceeded) || execKind == errorsx.KindCanceled {
			outcome = requestjourney.OutcomeCanceled
		} else {
			outcome = requestjourney.OutcomeFailure
			kind = nodeHealthErrorKind(execKind)
			if !healthEvidence {
				kind = nodehealth.ErrorKindRequest
			}
		}
	}
	detail := ""
	if out.Err != nil {
		detail = truncateForStore(out.Err.Error())
	}
	return e.reduceDispatchOutcome(ctx, nodehealth.Observation{
		Node: nodehealth.NodeKey{
			TenantID:     tenantID,
			ProviderID:   int64(cand.ProviderID),
			CredentialID: int64(cand.CredentialID),
			Model:        model,
		},
		AttemptID:   attemptID,
		Phase:       nodehealth.PhaseRequest,
		Outcome:     outcome,
		ErrorKind:   kind,
		HTTPStatus:  out.HTTPStatus,
		RequestID:   requestID,
		BillingMode: cand.BillingMode,
		LatencyMs:   latencyMs,
		ErrorDetail: detail,
	})
}

func dispatchOutcomeConsumesProbe(decision nodehealth.Decision, applied bool) bool {
	if !applied {
		return false
	}
	for _, effect := range decision.Effects {
		if effect.Kind == nodehealth.EffectRecordCircuitFailure || effect.Kind == nodehealth.EffectRecoverCircuit {
			return true
		}
	}
	return false
}

func dispatchHTTPStatus(err error) int {
	var upstreamErr *upstreampkg.Error
	if errors.As(err, &upstreamErr) && upstreamErr != nil {
		return upstreamErr.StatusCode
	}
	return 0
}

type executorNodeHealthAdapter struct {
	executor *Executor
}

// nodeFlashBlipConfirmTotal observes the Wave 3 B2② double-confirm verdicts
// (confirmed = degrade applied after both pings failed; transient = a ping
// succeeded and the node keeps its healthy state; busy = a confirm was
// already running for the node).
var nodeFlashBlipConfirmTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llmgw_node_flash_blip_confirm_total",
		Help: "Flash-blip double-confirm verdicts after the first consecutive network/timeout failure of a node.",
	},
	[]string{"result"},
)

// flashBlipEligible reports whether this decision is the FIRST consecutive
// failure of the node with a flash-blip capable error kind. Only then is the
// degrade deferred behind the double confirmation; later consecutive
// failures (the node is already suspect) degrade immediately as before.
func flashBlipEligible(decision nodehealth.Decision, confirm NodeProbeConfirmFunc) bool {
	if confirm == nil || decision.Outcome != requestjourney.OutcomeFailure {
		return false
	}
	if decision.ConsecutiveFailures != 1 {
		return false
	}
	switch decision.ErrorKind {
	case nodehealth.ErrorKindNetwork, nodehealth.ErrorKindTimeout:
		return true
	default:
		return false
	}
}

func (a executorNodeHealthAdapter) ApplyNodeHealthDecision(ctx context.Context, decision nodehealth.Decision) error {
	e := a.executor
	if e == nil {
		return nil
	}
	kind := errorKindFromNodeHealth(decision.ErrorKind)
	deferDegrade := flashBlipEligible(decision, e.NodeProbeConfirm)
	var errs []error
	var deferred []nodehealth.Effect
	for _, effect := range decision.Effects {
		switch effect.Kind {
		case nodehealth.EffectRecordCircuitFailure:
			// Circuit semantics are unchanged by the flash-blip gate: the
			// original request failure keeps recording; the confirm pings
			// themselves never touch the breaker ("不叠加熔断").
			if e.Circuit != nil {
				// 2026-09-15 (245 free-capacity plan): billing-mode-aware
				// recording lets free credentials adopt the shortened
				// freeTierPolicies cooling profile inside the breaker.
				e.Circuit.RecordFailureWithBillingMode(int(decision.Node.ProviderID), int(decision.Node.CredentialID), kind, decision.BillingMode)
			}
		case nodehealth.EffectRecoverCircuit:
			if e.Circuit != nil {
				e.Circuit.RecordSuccess(int(decision.Node.ProviderID), int(decision.Node.CredentialID))
			}
		case nodehealth.EffectRestoreBinding:
			if e.State != nil && e.State.Enabled() {
				if err := e.State.RestoreOnSuccess(ctx, int(decision.Node.CredentialID), decision.Node.Model); err != nil {
					errs = append(errs, err)
				}
			}
		case nodehealth.EffectPersistNodeStatus:
			if decision.Outcome == requestjourney.OutcomeSuccess {
				e.applyNodeHealthPersist(ctx, decision, &errs)
			} else if deferDegrade {
				deferred = append(deferred, effect)
			} else {
				e.applyNodeHealthPersist(ctx, decision, &errs)
			}
		case nodehealth.EffectSetBindingUnavailable, nodehealth.EffectSetCredentialUnavailable,
			nodehealth.EffectUpdateURSM, nodehealth.EffectInvalidateCandidateCache:
			if deferDegrade {
				deferred = append(deferred, effect)
			} else {
				e.applyNodeHealthDegradeEffect(ctx, decision, effect, &errs)
			}
		case nodehealth.EffectScheduleProbe:
			// UnifiedProbeScheduler 已于 2026-09-01 作为死代码移除；
			// 探测调度由 ProbeQueue/StateObserver 路径接管。
		case nodehealth.EffectCancelProbeBackoff:
			if e.NodeProbeHealthy != nil {
				if err := e.NodeProbeHealthy(ctx, int(decision.Node.CredentialID), decision.Node.Model); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	if len(deferred) > 0 {
		e.scheduleFlashBlipConfirm(ctx, decision, deferred)
	}
	return errors.Join(errs...)
}

// applyNodeHealthPersist applies the EffectPersistNodeStatus arm (legacy
// state observer writers).
func (e *Executor) applyNodeHealthPersist(ctx context.Context, decision nodehealth.Decision, errs *[]error) {
	if e.StateObserver == nil || !e.legacyWritersEnabled() {
		return
	}
	if decision.Outcome == requestjourney.OutcomeSuccess {
		e.StateObserver.UpdateOnSuccess(ctx, int(decision.Node.CredentialID), decision.Node.Model, decision.LatencyMs, decision.RequestID)
	} else if decision.Outcome == requestjourney.OutcomeFailure {
		kind := errorKindFromNodeHealth(decision.ErrorKind)
		e.StateObserver.UpdateOnFailure(ctx, int(decision.Node.CredentialID), decision.Node.Model, kind, decision.RequestID, decision.Node.TenantID, decision.BillingMode)
	}
}

// applyNodeHealthDegradeEffect applies one degrade-family effect (binding /
// credential unavailable, URSM failure record, candidate cache
// invalidation).
func (e *Executor) applyNodeHealthDegradeEffect(ctx context.Context, decision nodehealth.Decision, effect nodehealth.Effect, errs *[]error) {
	kind := errorKindFromNodeHealth(decision.ErrorKind)
	switch effect.Kind {
	case nodehealth.EffectSetBindingUnavailable:
		if e.State != nil && e.State.Enabled() {
			failure := credential.Failure{Kind: kind, Detail: decision.ErrorDetail}
			if err := e.State.WriteOnError(ctx, int(decision.Node.CredentialID), decision.Node.Model, failure); err != nil {
				*errs = append(*errs, err)
			}
		}
	case nodehealth.EffectSetCredentialUnavailable:
		if e.State != nil && e.State.Enabled() {
			failure := credential.Failure{Kind: kind, Detail: decision.ErrorDetail}
			if err := e.State.SetCredentialUnavailable(ctx, int(decision.Node.CredentialID), failure); err != nil {
				*errs = append(*errs, err)
			}
		}
	case nodehealth.EffectUpdateURSM:
		if e.URSMv2 != nil {
			err := e.URSMv2.RecordRequest(ctx, ursmv2api.RequestOutcome{
				CredentialID: int(decision.Node.CredentialID),
				RawModel:     decision.Node.Model,
				TenantID:     decision.Node.TenantID,
				BillingMode:  decision.BillingMode,
				Success:      decision.Outcome == requestjourney.OutcomeSuccess,
				LatencyMs:    decision.LatencyMs,
				ErrorKind:    string(kind),
				RequestID:    decision.RequestID,
				DedupKey:     decision.AttemptID,
			})
			if err != nil {
				*errs = append(*errs, err)
			}
		}
	case nodehealth.EffectInvalidateCandidateCache:
		provider.InvalidateCandidateCacheForCredential(int(decision.Node.CredentialID))
	}
}

// scheduleFlashBlipConfirm runs the deferred degrade behind the double
// confirmation (Wave 3 B2②). One confirm per node at a time; the goroutine
// outlives the request (WithoutCancel) and applies the deferred effects only
// when both pings failed. A concurrent confirm or a spawn race leaves the
// state untouched — the running confirm's verdict already converges the
// node's state either way.
func (e *Executor) scheduleFlashBlipConfirm(requestCtx context.Context, decision nodehealth.Decision, deferred []nodehealth.Effect) {
	key := fmt.Sprintf("%d|%s", decision.Node.CredentialID, decision.Node.Model)
	e.flashBlipMu.Lock()
	if e.flashBlipInFlight == nil {
		e.flashBlipInFlight = make(map[string]struct{})
	}
	if _, busy := e.flashBlipInFlight[key]; busy {
		e.flashBlipMu.Unlock()
		nodeFlashBlipConfirmTotal.WithLabelValues("busy").Inc()
		return
	}
	e.flashBlipInFlight[key] = struct{}{}
	e.flashBlipMu.Unlock()

	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("flash-blip confirm panic", "recover", rec, "credential_id", decision.Node.CredentialID)
			}
			e.flashBlipMu.Lock()
			delete(e.flashBlipInFlight, key)
			e.flashBlipMu.Unlock()
		}()
		// Wait out the blip window before the first ping; the confirm
		// itself owns its pacing and budget.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(requestCtx), 45*time.Second)
		defer cancel()
		confirmedBroken := e.NodeProbeConfirm(ctx, int(decision.Node.CredentialID), decision.Node.Model)
		if !confirmedBroken {
			nodeFlashBlipConfirmTotal.WithLabelValues("transient").Inc()
			slog.Info("node flash-blip: transient error confirmed, degrade suppressed",
				"credential_id", decision.Node.CredentialID,
				"model", decision.Node.Model,
			)
			return
		}
		nodeFlashBlipConfirmTotal.WithLabelValues("confirmed").Inc()
		slog.Warn("node flash-blip: both confirm pings failed, applying deferred degrade",
			"credential_id", decision.Node.CredentialID,
			"model", decision.Node.Model,
		)
		var errs []error
		for _, effect := range deferred {
			if effect.Kind == nodehealth.EffectPersistNodeStatus {
				e.applyNodeHealthPersist(ctx, decision, &errs)
				continue
			}
			e.applyNodeHealthDegradeEffect(ctx, decision, effect, &errs)
		}
		if err := errors.Join(errs...); err != nil {
			slog.Warn("flash-blip deferred degrade partially failed",
				"credential_id", decision.Node.CredentialID,
				"model", decision.Node.Model,
				"error", err,
			)
		}
	}()
}

func (e *Executor) recordProtocolCircuitSuccess(params *ExecParams, providerID, credentialID int) {
	if params != nil && params.DispatchAttempt {
		return
	}
	if e.Circuit != nil {
		e.Circuit.RecordSuccess(providerID, credentialID)
	}
}

func (e *Executor) recordProtocolCircuitFailure(params *ExecParams, providerID, credentialID int, kind errorsx.ErrorKind, billingMode string) {
	if kind == errorsx.KindEmptyResponse {
		// Empty responses are recorded on the tenant/credential/raw-model URSM
		// node for soft routing penalties. The legacy circuit is only keyed by
		// provider/credential and would incorrectly suppress sibling models.
		return
	}
	if params != nil && params.DispatchAttempt {
		return
	}
	if e.Circuit != nil {
		// R31 (audit 2026-09-16 §四#5): the legacy protocol paths (chat /
		// anthropic stream interruption) previously recorded through plain
		// RecordFailure, so a free credential failing here never adopted the
		// freeTierPolicies cooling profile and opened at the paid cadence —
		// the exact flap the 245 free-capacity plan eliminated on the V2
		// dispatch path only.
		e.Circuit.RecordFailureWithBillingMode(providerID, credentialID, kind, billingMode)
	}
}

func (e *Executor) writeProtocolCredentialStateOnError(params *ExecParams, ctx context.Context, credentialID int, model string, kind errorsx.ErrorKind, err error) {
	if params != nil && params.DispatchAttempt {
		return
	}
	e.writeCredentialStateOnError(ctx, credentialID, model, kind, err)
}

func nodeHealthErrorKind(kind errorsx.ErrorKind) nodehealth.ErrorKind {
	if errorsx.IsClientBug(kind) || errorsx.IsContentFilter(kind) || kind == errorsx.KindContextLength || kind == errorsx.KindCanceled {
		return nodehealth.ErrorKindRequest
	}
	switch kind {
	case errorsx.KindNetwork:
		return nodehealth.ErrorKindNetwork
	case errorsx.KindTimeout, errorsx.KindStreamTimeout:
		return nodehealth.ErrorKindTimeout
	case errorsx.KindRateLimit, errorsx.KindConcurrent:
		return nodehealth.ErrorKindRateLimit
	case errorsx.KindAuth, errorsx.KindAuthRevoked:
		return nodehealth.ErrorKindAuth
	case errorsx.KindQuota, errorsx.KindQuotaBalance, errorsx.KindQuotaPeriodic, errorsx.KindQuotaPermanent:
		return nodehealth.ErrorKindQuota
	case errorsx.KindModelNotFound, errorsx.KindModelDeprecated:
		return nodehealth.ErrorKindModelBinding
	case errorsx.KindEmptyResponse:
		return nodehealth.ErrorKindEmptyResponse
	default:
		return nodehealth.ErrorKindUpstream
	}
}

func errorKindFromNodeHealth(kind nodehealth.ErrorKind) errorsx.ErrorKind {
	switch kind {
	case nodehealth.ErrorKindNetwork:
		return errorsx.KindNetwork
	case nodehealth.ErrorKindTimeout:
		return errorsx.KindTimeout
	case nodehealth.ErrorKindRateLimit:
		return errorsx.KindRateLimit
	case nodehealth.ErrorKindAuth:
		return errorsx.KindAuth
	case nodehealth.ErrorKindQuota:
		return errorsx.KindQuotaPermanent
	case nodehealth.ErrorKindModelBinding:
		return errorsx.KindModelNotFound
	case nodehealth.ErrorKindEmptyResponse:
		return errorsx.KindEmptyResponse
	default:
		return errorsx.KindTransient
	}
}
