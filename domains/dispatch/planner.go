package dispatch

import "time"

// Planner (V6-W1.6 R11, docs/架构优化v6/09-ir-class-journal-decoupling.md):
// the PURE decision layer between the state layer (QueuedRequest markers:
// DueAt/Class/Tried*/Counts/Journal/LastFailover) and the queue-management
// pipeline. Planner functions read qr/outcome/candidates and return the next
// Decision; they never mutate state, perform I/O, take locks or spawn
// goroutines (invariant 5: same inputs → same Decision). The pipeline only
// executes Decisions — enqueue/park/complete/notify.
//
// Behavioral equivalence with the pre-planner failover ladder is pinned by
// the full dispatch test suite (hard gate: zero test modifications).

// Decision is the planner output consumed by the pipeline executor.
type Decision struct {
	// Action is the decided next step. "" means "fall through": the caller
	// continues to the next ladder stage (credential switch → model change
	// → terminal).
	Action NextActionKind
	// NextCred is the switch_cred target (Action == switch_cred).
	NextCred *CredentialRef
	// NextModel is the switch_model target (Action == switch_model).
	NextModel string
	// RetryAt is the wake-up time for wait-class actions (capacity_wait).
	RetryAt time.Time
	// Reason documents the trigger (error_kind / no_route / capacity /
	// attempt_cap …) for tests, logs and the journal vocabulary.
	Reason string
	// Terminal carries the final outcome for terminal Decisions
	// (Action == failed); the executor completes the request with it.
	Terminal *ForwardOutcome
}

// reasonAttemptCap is the R12 terminal vocabulary: the 100-attempt budget is
// the single authoritative limit (AttemptCount); a capped request ends with
// a failed(attempt_cap) journal entry.
const reasonAttemptCap = "attempt_cap"

// Capacity-wait pacing (main flow D): 5s × 12 rounds = 60s of queue-full
// polling before the ladder escalates to model-change.
const (
	capacityRetryDelay = 5 * time.Second
	maxCapacityRetries = 12
)

// AttemptBudgetLeft (R12) reports the forwards remaining under the
// maxAttempts=100 cap: first try + retries + node switches + model switches
// all count. AttemptCount is the limit authority; ActionCounts are a
// classification view (≤1 skew allowed: pace_timeout never sends).
func AttemptBudgetLeft(qr *QueuedRequest) int {
	if qr == nil {
		return maxAttempts
	}
	return maxAttempts - qr.AttemptCount
}

// retryBudgetOf resolves the same-credential retry budget: the
// request-level override (RetryPerCredential ≥ 0) wins over the pipeline
// config.
func retryBudgetOf(qr *QueuedRequest, cfg Config) int {
	if qr != nil && qr.RetryPerCredential >= 0 {
		return qr.RetryPerCredential
	}
	if cfg.RetryPerCredential >= 0 {
		return cfg.RetryPerCredential
	}
	return 0
}

// PlanAfterFailure decides the failover ladder's first step after a
// pre-firstbyte failure: same-credential retry while the per-credential
// budget lasts and the error is not credential-fatal (a fatal credential
// deterministically re-yields the same rejection, so the budget is saved for
// a healthy sibling). The attempt cap gates the continuation (R12);
// fall-through lets the executor proceed to the credential switch.
func PlanAfterFailure(qr *QueuedRequest, out ForwardOutcome, cfg Config) Decision {
	if out.FatalCredential || qr.CredRetryCount >= retryBudgetOf(qr, cfg) {
		return Decision{Reason: "cred_budget_exhausted"}
	}
	if AttemptBudgetLeft(qr) <= 0 {
		return Decision{Action: NextActionFailed, Reason: reasonAttemptCap}
	}
	return Decision{Action: NextActionRetrySameCred, Reason: firstNonEmpty(out.ErrorKind, "upstream_error")}
}

// PlanSwitchCred scans the routed candidates in priority order for the
// first one the request may switch to under the current model: not already
// tried, and within the provider scope. Candidates skipped only because of
// provider scope are reported in scoped so the executor preserves the
// legacy bookkeeping of marking them tried. A nil next means every
// candidate is consumed → the model-change ladder.
func PlanSwitchCred(qr *QueuedRequest, refs []CredentialRef) (next *CredentialRef, scoped []int) {
	for i := range refs {
		if qr.hasTriedCredential(refs[i].CredentialID) {
			continue
		}
		if !providerSwitchAllowed(qr, refs[i].ProviderID) {
			scoped = append(scoped, refs[i].CredentialID)
			continue
		}
		ref := refs[i]
		return &ref, scoped
	}
	return nil, scoped
}

// providerSwitchAllowed reports whether the request may use a credential of
// the given provider: yes when unscoped (InitialProviderID unset), same
// provider, or cross-provider switching is allowed.
func providerSwitchAllowed(qr *QueuedRequest, providerID int) bool {
	if qr == nil || qr.InitialProviderID == 0 || providerID == 0 {
		return true
	}
	if providerID == qr.InitialProviderID {
		return true
	}
	return qr.AllowProviderChange
}

// PlanNoRoute decides the no-candidate branch of the model ladder: continue
// via model-change (the executor then fetches alternatives and re-plans
// with PlanModelChange) or terminate with the original cause.
func PlanNoRoute(qr *QueuedRequest, modelChangeEnabled bool) Decision {
	if modelChangeEnabled && qr.AllowModelChange {
		return Decision{Reason: "model_change"}
	}
	return Decision{Action: NextActionFailed, Reason: "model_change_disabled"}
}

// PlanModelChange picks the first untried alternative model. A fall-through
// Decision means the combination space is exhausted — the terminal with
// exhaustion priority over the attempt budget (R2.4).
func PlanModelChange(qr *QueuedRequest, alts []string) Decision {
	for _, a := range alts {
		if _, tried := qr.TriedModels[a]; !tried {
			return Decision{Action: NextActionSwitchModel, NextModel: a, Reason: "no_node"}
		}
	}
	return Decision{Reason: "no_alternative"}
}

// PlanCapacityWait (main flow D) decides the capacity-requeue pacing: wait
// capacityRetryDelay while under maxCapacityRetries rounds; a fall-through
// Decision escalates to the model-change ladder. RetryAt derives from the
// passed now so the planner stays pure.
func PlanCapacityWait(qr *QueuedRequest, now time.Time) Decision {
	if qr.CapacityRetryCount >= maxCapacityRetries {
		return Decision{Reason: "capacity_saturated"}
	}
	return Decision{
		Action:  NextActionCapacityWait,
		RetryAt: now.Add(capacityRetryDelay),
		Reason:  "capacity_saturated",
	}
}

// attemptCapOutcome builds the terminal outcome for the attempt-cap
// termination (R12): the aggregate ExhaustedError envelope over the last
// cause, stamped with the attempt_cap reason so the journal terminal entry
// reads failed(attempt_cap). Combination exhaustion (checked before the
// budget in the ladder) keeps priority — this outcome is only reached with
// candidates remaining.
func attemptCapOutcome(qr *QueuedRequest, out ForwardOutcome) ForwardOutcome {
	terminal := out
	terminal.ErrorKind = reasonAttemptCap
	terminal.Err = exhaustedTerminal(qr, terminalErr(out.Err))
	return terminal
}

// exhaustedTerminal wraps a terminal cause with the aggregate combination
// summary (R2.4/UT-FO-05). Never-routed requests (zero attempts) keep the
// plain sentinel — ErrNoRoute stays distinguishable for the 503 mapping.
// The wrapper delegates Error()/Unwrap() to the cause so existing callers
// that pin the concrete upstream error keep working.
func exhaustedTerminal(qr *QueuedRequest, cause error) error {
	if qr == nil || qr.AttemptCount == 0 {
		return cause
	}
	return &ExhaustedError{Cause: cause, Attempts: qr.exhaustionAttempts()}
}
