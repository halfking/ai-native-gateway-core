package errorsx

// RecoveryProjection is the low-coupling, log-oriented view of recovery
// semantics for an error kind. GenericRetryable deliberately mirrors
// IsRetryable; CandidateFailover describes the separate candidate-loop path.
// The projection does not perform retries, failover, or state changes.
type RecoveryProjection struct {
	GenericRetryable  bool   `json:"generic_retryable"`
	CandidateFailover bool   `json:"candidate_failover"`
	TransparentResume bool   `json:"transparent_resume"`
	EffectiveAction   string `json:"effective_action"`
	Reason            string `json:"reason"`
}

const (
	RecoveryActionCandidateFailover = "candidate_failover"
	RecoveryActionGenericRetry      = "generic_retry"
	RecoveryActionTerminal          = "terminal"
)

// ProjectRecovery returns the recovery semantics that callers should attach to
// logs and observations. In particular, empty_response and
// no_available_channel are intentionally candidate-loop failovers, not members
// of the generic IsRetryable set. This keeps IsRetryable's existing contract
// unchanged while making the effective behavior observable.
func ProjectRecovery(kind ErrorKind) RecoveryProjection {
	projection := RecoveryProjection{
		GenericRetryable: IsRetryable(kind),
		EffectiveAction:  RecoveryActionTerminal,
		Reason:           "not_recoverable",
	}

	if kind == KindEmptyResponse || kind == KindNoAvailableChannel {
		projection.CandidateFailover = true
		projection.TransparentResume = true
		projection.EffectiveAction = RecoveryActionCandidateFailover
		if kind == KindEmptyResponse {
			projection.Reason = "empty_response_candidate_failover"
		} else {
			projection.Reason = "no_available_channel_candidate_failover"
		}
		return projection
	}

	// 2026-09-01 (P1-1 24h-audit round2): circuit_open / fp_slot_saturated are
	// gateway-side admission signals logged before any upstream call. They are
	// not upstream quality problems: retrying the upstream from the generic
	// retry loop would bypass the breaker / slot that just rejected the
	// attempt. Project as terminal with an explicit reason so
	// candidate_failure_logs context says "gateway admission" instead of the
	// misleading default "not_recoverable" (which implies an upstream verdict).
	if kind == KindCircuitOpen || kind == KindFpSlotSaturated {
		projection.EffectiveAction = RecoveryActionTerminal
		projection.Reason = "gateway_admission_rejection"
		return projection
	}

	if projection.GenericRetryable {
		projection.TransparentResume = true
		projection.EffectiveAction = RecoveryActionGenericRetry
		projection.Reason = "generic_retryable_upstream_failure"
	}
	return projection
}
