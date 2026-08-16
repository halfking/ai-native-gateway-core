package handoff

import "time"

// SignalKind is the normalized result of monitoring Goal and context state.
type SignalKind string

const (
	SignalNone            SignalKind = "none"
	SignalContextPressure SignalKind = "context_pressure"
	SignalGoalCompleted   SignalKind = "goal_completed"
	SignalGoalDegraded    SignalKind = "goal_degraded"
	SignalGoalFailed      SignalKind = "goal_failed"
)

// ContextSnapshot is the complete read-only input to ContextMonitor.
type ContextSnapshot struct {
	SessionID           string
	TenantID            string
	GoalState           string
	TokensUsed          int
	ContextWindow       int
	MessageCount        int
	RetryCount          int
	RetryExhausted      bool
	RepeatCount         int
	ModelSwitchCount    int
	MaxModelSwitches    int
	Outcome             SignalKind
	Reason              string
	Source              string
	ObservedAt          time.Time
	AbsoluteThreshold   int
	PercentageThreshold float64
}

// TriggerSignal is a machine-readable handoff observation.
type TriggerSignal struct {
	Kind       SignalKind `json:"kind"`
	Source     string     `json:"source"`
	Reason     string     `json:"reason"`
	Severity   int        `json:"severity"`
	ObservedAt time.Time  `json:"observed_at"`
}

// ContextMonitor converts a session snapshot into one normalized signal.
type ContextMonitor interface {
	Evaluate(snapshot ContextSnapshot) TriggerSignal
}

// MemoryContextMonitor is a stateless in-memory ContextMonitor.
type MemoryContextMonitor struct {
	absoluteThreshold   int
	percentageThreshold float64
	now                 func() time.Time
}

// NewMemoryContextMonitor creates a stateless monitor with optional default
// absolute and percentage thresholds. A per-snapshot non-zero threshold wins.
func NewMemoryContextMonitor(absoluteThreshold int, percentageThreshold float64) *MemoryContextMonitor {
	return &MemoryContextMonitor{
		absoluteThreshold:   absoluteThreshold,
		percentageThreshold: percentageThreshold,
		now:                 time.Now,
	}
}

func (m *MemoryContextMonitor) Evaluate(snapshot ContextSnapshot) TriggerSignal {
	observedAt := snapshot.ObservedAt
	if observedAt.IsZero() {
		observedAt = m.now().UTC()
	}
	signal := TriggerSignal{Kind: SignalNone, Source: snapshot.Source, ObservedAt: observedAt}
	if signal.Source == "" {
		signal.Source = "context_monitor"
	}

	if snapshot.RetryExhausted || snapshot.Outcome == SignalGoalFailed {
		return TriggerSignal{Kind: SignalGoalFailed, Source: signal.Source, Reason: nonEmpty(snapshot.Reason, "provider_retry_exhausted"), Severity: 4, ObservedAt: observedAt}
	}
	if snapshot.Outcome == SignalGoalDegraded {
		return TriggerSignal{Kind: SignalGoalDegraded, Source: signal.Source, Reason: nonEmpty(snapshot.Reason, "goal_degraded"), Severity: 3, ObservedAt: observedAt}
	}

	absolute := snapshot.AbsoluteThreshold
	if absolute == 0 {
		absolute = m.absoluteThreshold
	}
	if absolute > 0 && snapshot.TokensUsed >= absolute {
		return TriggerSignal{Kind: SignalContextPressure, Source: "context", Reason: "absolute_threshold", Severity: 2, ObservedAt: observedAt}
	}
	percentage := snapshot.PercentageThreshold
	if percentage == 0 {
		percentage = m.percentageThreshold
	}
	if percentage > 0 && snapshot.ContextWindow > 0 && float64(snapshot.TokensUsed)/float64(snapshot.ContextWindow) >= percentage {
		return TriggerSignal{Kind: SignalContextPressure, Source: "context", Reason: "percentage_threshold", Severity: 2, ObservedAt: observedAt}
	}
	if snapshot.Outcome == SignalGoalCompleted || snapshot.GoalState == "completed" {
		return TriggerSignal{Kind: SignalGoalCompleted, Source: signal.Source, Reason: nonEmpty(snapshot.Reason, "goal_completed"), Severity: 1, ObservedAt: observedAt}
	}
	return signal
}

func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
