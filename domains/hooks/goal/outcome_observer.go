package goal

import (
	"context"
	"log/slog"
	"time"
)

// OutcomeKind is an observation emitted after existing Goal decisions complete.
type OutcomeKind string

const (
	OutcomeCompleted OutcomeKind = "completed"
	OutcomeDegraded  OutcomeKind = "degraded"
	OutcomeFailed    OutcomeKind = "failed"
)

// Outcome contains only state already decided by Goal or provider retry logic.
type Outcome struct {
	Kind                OutcomeKind
	SessionID           string
	TenantID            string
	Reason              string
	Source              string
	RetryCount          int
	RepeatCount         int
	ModelSwitchCount    int
	MaxModelSwitchCount int
	ObservedAt          time.Time
}

// OutcomeObserver receives Goal lifecycle outcomes without changing decisions.
type OutcomeObserver interface {
	ObserveGoalOutcome(ctx context.Context, outcome Outcome) error
}

func (h *ModeHook) SetOutcomeObserver(observer OutcomeObserver) {
	if h != nil {
		h.outcomeObserver = observer
	}
}

func (h *ModeHook) observeOutcome(ctx context.Context, outcome Outcome) {
	if h == nil || h.outcomeObserver == nil || outcome.SessionID == "" {
		return
	}
	if outcome.ObservedAt.IsZero() {
		outcome.ObservedAt = time.Now().UTC()
	}
	if err := h.outcomeObserver.ObserveGoalOutcome(ctx, outcome); err != nil {
		slog.Warn("goal_outcome_observer_failed", "session_id", outcome.SessionID, "reason", outcome.Reason, "error", err)
	}
}
