package audit

import "time"

// DiscardEvent is one entry in the audit-side discard log that surfaces
// every place a streaming attempt throws away buffered bytes before a
// transparent retry.
//
// 2026-08-19 observability: this closes the gap where the
// "Partial assistant output was discarded before a streaming retry" failure
// class was only visible through gateway_survival_resume_safety_blocked
// counters. Operators can now SELECT discard_events FROM request_logs_hot
// WHERE request_id = ? and see exactly which attempt was dropped, how many
// bytes were thrown away, what the commit state was at decision time, and
// which action/reason the survival coordinator paired it with.
//
// DiscardEvents is JSON-serialised into the request_logs_hot.discard_events
// JSONB column by the audit upsert path (see SummaryAsMap / discard_events
// emission); there is no need to write it from per-call sites.
type DiscardEvent struct {
	// Reason is the human-readable reason token (e.g.
	// "survival_attempt_discarded",
	// "stream_recovery_discard_and_replay", "empty_stream_no_content").
	Reason string `json:"reason"`
	// BufferBytes is the size of the per-attempt metadata/semantic buffer
	// (holdback + semantic frames) at the instant the discard decision was
	// made.
	BufferBytes int `json:"buffer_bytes"`
	// HoldbackHeld is the number of semantic chunks still held inside the
	// L1 holdback window at decision time. Zero outside L1.
	HoldbackHeld int `json:"holdback_held"`
	// State is the AttemptCommitGate commit state at decision time
	// (none|metadata|content|tool_call|terminal).
	State string `json:"state"`
	// AttemptNumber is the 1-indexed attempt counter within the survival
	// loop. -1 when the discard happens outside the survival loop (e.g.
	// the empty-stream content gate).
	AttemptNumber int `json:"attempt_number"`
	// ProviderID is the upstream provider of the candidate the discard
	// applied to. -1 when not known.
	ProviderID int `json:"provider_id"`
	// RawModel is the upstream-issued model name for the discarded attempt.
	RawModel string `json:"raw_model,omitempty"`
	// DecisionAction is the TaskAction.String() the survival coordinator
	// paired with the discard ("retry_now", "wait_recovery", …).
	DecisionAction string `json:"decision_action,omitempty"`
	// DecisionReason is the low-cardinality TaskDecision.Reason.
	DecisionReason string `json:"decision_reason,omitempty"`
	// RecordedAt is the wall-clock timestamp at which the discard was
	// recorded; useful for ordering multiple discard events on one request.
	RecordedAt time.Time `json:"recorded_at"`
}

// MarkDiscarded appends one discard event to the capture. Concurrency-safe
// (uses sc.mu). Returns the index of the appended event so callers can
// refer back to it if needed.
func (sc *StreamCapture) MarkDiscarded(evt DiscardEvent) int {
	if sc == nil {
		return -1
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if evt.RecordedAt.IsZero() {
		evt.RecordedAt = time.Now()
	}
	sc.DiscardEvents = append(sc.DiscardEvents, evt)
	return len(sc.DiscardEvents) - 1
}

// DiscardEventsCopy returns a defensive copy of the capture's discard log.
// Useful for tests; production code should consume DiscardEvents through
// the audit upsert path, which already copies via SummaryAsMap.
func (sc *StreamCapture) DiscardEventsCopy() []DiscardEvent {
	if sc == nil {
		return nil
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if len(sc.DiscardEvents) == 0 {
		return nil
	}
	out := make([]DiscardEvent, len(sc.DiscardEvents))
	copy(out, sc.DiscardEvents)
	return out
}