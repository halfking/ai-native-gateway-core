package streaming

import (
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/metrics"
)

// survival_metrics.go — SR-13 (doc 18 §15.1, doc 19 CO-1)
//
// Event producers for the survival series declared in
// metrics/survival_metrics.go. The SurvivalCoordinator decision points call
// these helpers; they are deliberately only reachable from the survival loop
// (the flag-off path never constructs a coordinator), so every series stays
// at zero unless request survival is enabled for the tenant (doc 18 §6).

// survivalOutcomeLabels are the closed outcome enum of
// gateway_survival_requests_total (doc 18 §15.1).
const (
	survivalOutcomeCompleted          = "completed"
	survivalOutcomePermanentFailed    = "permanent_failed"
	survivalOutcomeExpired            = "expired"
	survivalOutcomeCancelled          = "cancelled"
	survivalOutcomeResumeSafetyBlocked = "resume_safety_blocked"
	survivalOutcomeError              = "error"
)

// survivalMetricOutcome maps a terminal TaskDecision onto the closed outcome
// enum. Synthetic fail-closed reasons get their operational outcome: the 30min
// interactive deadline expiring (expired) and the client connection dropping
// (cancelled) are different paging signals than an internal error.
func survivalMetricOutcome(d TaskDecision) string {
	switch d.Action {
	case TaskActionSucceed:
		return survivalOutcomeCompleted
	case TaskActionFailTerminal:
		return survivalOutcomePermanentFailed
	case TaskActionResumeBlocked:
		return survivalOutcomeResumeSafetyBlocked
	case TaskActionFailClosed:
		switch d.Reason {
		case "deadline_exceeded":
			return survivalOutcomeExpired
		case "client_disconnected":
			return survivalOutcomeCancelled
		}
		return survivalOutcomeError
	default:
		return survivalOutcomeError
	}
}

// recordSurvivalRequestTerminal closes one survival task: exactly one
// gateway_survival_requests_total increment per coordinator Run, labeled by
// client protocol, durability and terminal outcome. durable is fixed to
// false until the doc 18 §12 durable phase ships its own worker path.
func recordSurvivalRequestTerminal(protocol ClientProtocol, d TaskDecision) {
	metrics.SurvivalRequestsTotal.WithLabelValues(
		protocolMetricLabel(protocol), "false", survivalMetricOutcome(d)).Inc()
}

// survivalProviderLabel renders the provider dimension of
// gateway_survival_attempts_total. Provider IDs are small bounded integers
// (GW-00 cardinality rule); synthesized outcomes with no provider read as
// "unknown".
func survivalProviderLabel(providerID int) string {
	if providerID <= 0 {
		return "unknown"
	}
	return strconv.Itoa(providerID)
}

// recordSurvivalAttempt reports one finished attempt: one
// gateway_survival_attempts_total increment per candidate outcome the
// executor walked through (kind "" on success), so per-provider failure
// attribution survives credential rotation inside a single attempt.
func recordSurvivalAttempt(a *AttemptResult) {
	if a == nil {
		return
	}
	if a.Success {
		provider := "unknown"
		if a.ExecResult != nil && a.ExecResult.Candidate.ProviderID > 0 {
			provider = survivalProviderLabel(a.ExecResult.Candidate.ProviderID)
		}
		metrics.SurvivalAttemptsTotal.WithLabelValues("", provider).Inc()
		return
	}
	if len(a.CandidateOutcomes) == 0 {
		metrics.SurvivalAttemptsTotal.WithLabelValues(string(a.LastKind()), "unknown").Inc()
		return
	}
	for _, co := range a.CandidateOutcomes {
		metrics.SurvivalAttemptsTotal.WithLabelValues(string(co.Kind), survivalProviderLabel(co.ProviderID)).Inc()
	}
}

// Survival task-state labels for gateway_survival_state_transitions_total.
// "running" is the attempt-executing state, "waiting_recovery" the doc 18 §8
// recovery window; retry dispatches go through their action pseudo-state so
// the series can replay the state machine.
const (
	survivalStateRunning  = "running"
	survivalStateWaiting  = "waiting_recovery"
	survivalStateRetryNow = "retry_now"
)

// survivalTerminalToState renders the to-state for a terminal decision: the
// synthetic fail-closed reasons get their operational terminal state
// (expired / cancelled), everything else uses the action name.
func survivalTerminalToState(d TaskDecision) string {
	switch d.Reason {
	case "deadline_exceeded":
		return "expired"
	case "client_disconnected":
		return "cancelled"
	default:
		return d.Action.String()
	}
}

// recordSurvivalTransition emits one gateway_survival_state_transitions_total
// increment. from/to are the state labels above plus terminal states; reason
// is the TaskDecision.Reason (low-cardinality by construction).
func recordSurvivalTransition(from, to, reason string) {
	metrics.SurvivalStateTransitionsTotal.WithLabelValues(from, to, reason).Inc()
}

// survivalWaitReason folds an error kind into the closed reason enum of
// gateway_survival_wait_seconds (skeleton comment, doc 18 §15.1): the bucket
// operators slice recovery windows by.
func survivalWaitReason(kind errorsx.ErrorKind) string {
	switch kind {
	case errorsx.KindRateLimit:
		return "rate_limit"
	case errorsx.KindQuota, errorsx.KindQuotaPeriodic, errorsx.KindQuotaBalance:
		return "quota_periodic"
	case errorsx.KindConcurrent:
		return "concurrent"
	case errorsx.KindUpstreamOverloaded, errorsx.KindUpstreamDown:
		return "upstream_overloaded"
	case errorsx.KindTransient, errorsx.KindTimeout, errorsx.KindAuth,
		errorsx.KindStreamTimeout, errorsx.KindEmptyResponse:
		return "transient"
	case errorsx.KindNetwork:
		return "network"
	case errorsx.KindNoAvailableChannel:
		return "no_candidates"
	default:
		return "other"
	}
}

// observeSurvivalWait records one completed waiting_recovery window in
// gateway_survival_wait_seconds{reason}.
func observeSurvivalWait(kind errorsx.ErrorKind, waited time.Duration) {
	metrics.SurvivalWaitSeconds.WithLabelValues(survivalWaitReason(kind)).Observe(waited.Seconds())
}

// observeSurvivalRecoveryLatency records outage-to-success for one task:
// from the first recoverable failure until the attempt that completed it
// (gateway_survival_recovery_latency_seconds, doc 18 §15.1).
func observeSurvivalRecoveryLatency(protocol ClientProtocol, latency time.Duration) {
	metrics.SurvivalRecoveryLatencySeconds.WithLabelValues(protocolMetricLabel(protocol)).Observe(latency.Seconds())
}
