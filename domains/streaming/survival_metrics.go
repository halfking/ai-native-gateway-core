package streaming

import (
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
