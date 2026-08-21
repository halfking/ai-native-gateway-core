package executors

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

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

func (a executorNodeHealthAdapter) ApplyNodeHealthDecision(ctx context.Context, decision nodehealth.Decision) error {
	e := a.executor
	if e == nil {
		return nil
	}
	kind := errorKindFromNodeHealth(decision.ErrorKind)
	var errs []error
	for _, effect := range decision.Effects {
		switch effect.Kind {
		case nodehealth.EffectRecordCircuitFailure:
			if e.Circuit != nil {
				e.Circuit.RecordFailure(int(decision.Node.ProviderID), int(decision.Node.CredentialID), kind)
			}
		case nodehealth.EffectRecoverCircuit:
			if e.Circuit != nil {
				e.Circuit.RecordSuccess(int(decision.Node.ProviderID), int(decision.Node.CredentialID))
			}
		case nodehealth.EffectPersistNodeStatus:
			if e.StateObserver != nil && e.legacyWritersEnabled() {
				if decision.Outcome == requestjourney.OutcomeSuccess {
					e.StateObserver.UpdateOnSuccess(ctx, int(decision.Node.CredentialID), decision.Node.Model, decision.LatencyMs, decision.RequestID)
				} else if decision.Outcome == requestjourney.OutcomeFailure {
					e.StateObserver.UpdateOnFailure(ctx, int(decision.Node.CredentialID), decision.Node.Model, kind, decision.RequestID, decision.Node.TenantID, decision.BillingMode)
				}
			}
		case nodehealth.EffectSetBindingUnavailable:
			if e.State != nil && e.State.Enabled() {
				failure := credential.Failure{Kind: kind, Detail: decision.ErrorDetail}
				if err := e.State.WriteOnError(ctx, int(decision.Node.CredentialID), decision.Node.Model, failure); err != nil {
					errs = append(errs, err)
				}
			}
		case nodehealth.EffectSetCredentialUnavailable:
			if e.State != nil && e.State.Enabled() {
				failure := credential.Failure{Kind: kind, Detail: decision.ErrorDetail}
				if err := e.State.SetCredentialUnavailable(ctx, int(decision.Node.CredentialID), failure); err != nil {
					errs = append(errs, err)
				}
			}
		case nodehealth.EffectRestoreBinding:
			if e.State != nil && e.State.Enabled() {
				if err := e.State.RestoreOnSuccess(ctx, int(decision.Node.CredentialID), decision.Node.Model); err != nil {
					errs = append(errs, err)
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
					errs = append(errs, err)
				}
			}
		case nodehealth.EffectInvalidateCandidateCache:
			provider.InvalidateCandidateCacheForCredential(int(decision.Node.CredentialID))
		case nodehealth.EffectScheduleProbe:
			if e.UnifiedProbeScheduler != nil {
				e.UnifiedProbeScheduler.OnRealRequest(ctx, decision.Node.CredentialID, decision.Node.Model, false, decision.ErrorDetail)
			}
		case nodehealth.EffectCancelProbeBackoff:
			if e.UnifiedProbeScheduler != nil {
				e.UnifiedProbeScheduler.OnRealRequest(ctx, decision.Node.CredentialID, decision.Node.Model, true, "")
			}
			if e.NodeProbeHealthy != nil {
				if err := e.NodeProbeHealthy(ctx, int(decision.Node.CredentialID), decision.Node.Model); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return errors.Join(errs...)
}

func (e *Executor) recordProtocolCircuitSuccess(params *ExecParams, providerID, credentialID int) {
	if params != nil && params.DispatchAttempt {
		return
	}
	if e.Circuit != nil {
		e.Circuit.RecordSuccess(providerID, credentialID)
	}
}

func (e *Executor) recordProtocolCircuitFailure(params *ExecParams, providerID, credentialID int, kind errorsx.ErrorKind) {
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
		e.Circuit.RecordFailure(providerID, credentialID, kind)
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
