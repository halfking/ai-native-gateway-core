// Package routeincident owns the route-incident lifecycle for the
// dashboard's live swim-lane diagnosis feature.
//
// See docs/superpowers/specs/2026-07-13-route-incident-diagnosis-design.md.
//
// Phase 1 is read-only:
//   - State transitions happen automatically (active → recovering → recovered).
//   - There is no DiagnosticRun / mutating action / evidence export here.
//   - All evidence is sanitized before it leaves this package.
package routeincident

// State is the lifecycle state of a route incident.
//
//	healthy    — internal, not stored; the observer computes this by absence.
//	active     — 3+ consecutive terminal failures, no recovery streak yet.
//	recovering — at least one terminal success after active; the entry stays visible.
//	recovered  — 5 consecutive terminal successes; the lane entry disappears.
type State string

const (
	StateActive     State = "active"
	StateRecovering State = "recovering"
	StateRecovered  State = "recovered"
)

// IsVisible reports whether the state should appear on the dashboard
// swim lane. Recovered incidents are kept in the database for
// timeline/audit queries but must not be visible on the live lane.
func (s State) IsVisible() bool {
	switch s {
	case StateActive, StateRecovering:
		return true
	default:
		return false
	}
}

// EventType discriminates rows in route_incident_events. See the
// sql/migrations/startup/389_route_incidents.sql check constraint for
// the canonical list.
type EventType string

const (
	EventOpened           EventType = "opened"
	EventFailureObserved  EventType = "failure_observed"
	EventRecoveryProgress EventType = "recovery_progress"
	EventRecovered        EventType = "recovered"
	EventDiagnosticRun    EventType = "diagnostic_run"  // Phase 2 only.
	EventOperatorAction   EventType = "operator_action" // Phase 2 only.
)

// Thresholds holds the trigger/recovery thresholds. Phase-1 defaults
// match the spec: 3 consecutive failures to become active, 5
// consecutive successes to recover. They are service configuration so
// the operator can tune them without redeploying the frontend.
type Thresholds struct {
	FailureToActive    int // default 3
	SuccessToRecovered int // default 5
}

// DefaultThresholds returns the spec defaults.
func DefaultThresholds() Thresholds {
	return Thresholds{FailureToActive: 3, SuccessToRecovered: 5}
}

// DecideState applies the state machine to a single terminal request
// against the current aggregate (passed in as `cur`). The function is
// pure: it does not read or write the database. The caller is
// responsible for holding the row lock and persisting the result.
//
// Inputs:
//   - cur  — the current incident row, or nil if no active/recovering
//     incident exists for this route yet.
//   - terminalStatus — the request's terminal classification
//     ("success" or "failure"). Any other value is treated as a
//     non-terminal event and returns cur unchanged with applied=false.
//   - th   — thresholds; pass DefaultThresholds() to use spec defaults.
//
// Returns the new State and the new {failure_streak, recovery_streak}.
// applied=true only when the state actually changed AND the caller
// should insert a corresponding row into route_incident_events.
//
// Invariants preserved:
//   - A success after active moves to recovering(1/5); only a fifth
//     consecutive success moves to recovered.
//   - A failure during recovering resets the recovery streak to 0
//     and returns the incident to active.
//   - A failure when there is no incident opens one (active, 1/3).
//   - A success when there is no incident returns applied=false.
func DecideState(cur *Incident, terminalStatus string, th Thresholds) (newState State, failureStreak, recoveryStreak int, applied bool) {
	if th.FailureToActive <= 0 {
		th.FailureToActive = DefaultThresholds().FailureToActive
	}
	if th.SuccessToRecovered <= 0 {
		th.SuccessToRecovered = DefaultThresholds().SuccessToRecovered
	}

	switch terminalStatus {
	case TerminalSuccess:
		if cur == nil {
			return "", 0, 0, false
		}
		switch cur.State {
		case StateActive:
			return StateRecovering, 0, 1, true
		case StateRecovering:
			next := cur.RecoveryStreak + 1
			if next >= th.SuccessToRecovered {
				return StateRecovered, 0, next, true
			}
			return StateRecovering, 0, next, true
		default:
			// recovered or unknown — nothing to do.
			return cur.State, 0, cur.RecoveryStreak, false
		}
	case TerminalFailure:
		if cur == nil {
			// Open a new incident. failure_streak starts at 1 because
			// the caller has just observed the first qualifying
			// failure; the store will write the "opened" event for
			// streak=1 and the (threshold-1) subsequent failures as
			// failure_observed.
			return StateActive, 1, 0, true
		}
		switch cur.State {
		case StateActive:
			return StateActive, cur.FailureStreak + 1, 0, true
		case StateRecovering:
			// Failure during recovery resets to active with streak=1.
			return StateActive, 1, 0, true
		case StateRecovered:
			// A new failure on a previously-recovered route reopens.
			return StateActive, 1, 0, true
		default:
			return StateActive, 1, 0, true
		}
	default:
		// in_progress / non-terminal / unknown — no transition.
		if cur == nil {
			return "", 0, 0, false
		}
		return cur.State, cur.FailureStreak, cur.RecoveryStreak, false
	}
}

// IsVisibleState is a small convenience for callers that need to test
// a state without importing the constants.
func IsVisibleState(s State) bool { return s.IsVisible() }
