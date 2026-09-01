package dispatch

import (
	"context"
	"errors"
	"time"
)

// AttemptJournal (执行轨迹队列, V6-W1.6 R9 / docs/架构优化v6/
// 09-ir-class-journal-decoupling.md) is the per-request execution trace:
// every model×node attempted, the decided next action, cumulative per-action
// counters and the terminal state. The trace is ATTACHED TO THE REQUEST
// (scope correction 2026-08-27): the authoritative copy lives on the
// QueuedRequest under the single-owner invariant, is delivered to the
// request's own consumers via JournalSnapshot, and is NEVER replicated into
// process-wide structures (dimension index entries carry membership metadata
// only). Post-hoc path persistence rides the request's own journey/log
// records, not a global store.

// JournalEntry is one decision record: what executed, what happened, what the
// scheduler decided next. Seq is assigned by recordDecision.
type JournalEntry struct {
	Seq          int            `json:"seq"`
	At           time.Time      `json:"at"`
	Model        string         `json:"model,omitempty"`
	CredentialID int            `json:"credential_id,omitempty"`
	ProviderID   int            `json:"provider_id,omitempty"`
	Vendor       string         `json:"vendor,omitempty"`
	Action       NextActionKind `json:"action"`
	ErrorKind    string         `json:"error_kind,omitempty"`
	HTTPStatus   int            `json:"http_status,omitempty"`
	// Attempt is the cumulative AttemptCount after the event.
	Attempt int `json:"attempt"`
	// Counts is the cumulative per-action counter snapshot after the event.
	Counts ActionCounts `json:"counts"`
	
	// FromModel / ToModel (2026-09-01 P0 fix): for NextActionSwitchModel, the
	// model being switched FROM and TO. ToModel typically matches Model; FromModel
	// must be filled by the caller when recording a switch decision so the
	// journal→journey bridge can emit valid EventModelSwitched with both endpoints.
	FromModel string `json:"from_model,omitempty"`
	ToModel   string `json:"to_model,omitempty"`
	
	// FromCredentialID / ToCredentialID (2026-09-01 P0 fix): for NextActionSwitchCred,
	// the credential being switched FROM and TO. ToCredentialID typically matches
	// CredentialID; FromCredentialID must be filled by the caller so the bridge can
	// emit valid EventNodeSwitched with both endpoints.
	FromCredentialID int `json:"from_credential_id,omitempty"`
	ToCredentialID   int `json:"to_credential_id,omitempty"`
	
	// FromProviderID / ToProviderID (2026-09-01 P0 fix, optional): for switch actions,
	// the provider being switched FROM and TO. Useful for cross-provider failover
	// forensics but not required by the journey contract.
	FromProviderID int `json:"from_provider_id,omitempty"`
	ToProviderID   int `json:"to_provider_id,omitempty"`
}

// ActionCounts aggregates the decision types across the request lifetime.
// They are a classification VIEW: the authoritative attempt limit is
// AttemptCount (R12) — sending actions (retry/switch_cred/switch_model) each
// correspond to exactly one later forward, while waits do not send and are
// not counted in AttemptCount.
type ActionCounts struct {
	Retries        int `json:"retries"`
	NodeSwitches   int `json:"node_switches"`
	ModelSwitches  int `json:"model_switches"`
	CapacityWaits  int `json:"capacity_waits"`
	ScheduledWaits int `json:"scheduled_waits"`
}

// journalCapacity bounds the trace ring: maxAttempts(100) + first admission
// + terminal leaves headroom, so a normal request is never truncated; the
// bound only defends pathological paths (R9 boundary).
const journalCapacity = 128

// Request-class string mirrors. dispatch must NOT import internal/ir (E14);
// journal_mirror_test.go pins these against ir.ClassImmediate/ClassScheduled.
const (
	RequestClassImmediate = "immediate"
	RequestClassScheduled = "scheduled"
)

// requestClass resolves the request class: an explicit RequestClass field
// (pre-stamped by the executor) wins; otherwise it derives from DueAt.
func (qr *QueuedRequest) requestClass() string {
	if qr.RequestClass != "" {
		return qr.RequestClass
	}
	if qr.DueAt.IsZero() || !qr.DueAt.After(time.Now()) {
		return RequestClassImmediate
	}
	return RequestClassScheduled
}

// recordDecision is the single journal write entry point (single-owner
// invariant: call while the current goroutine owns qr, BEFORE any ownership
// handoff such as a Tier-2 enqueue). It assigns the monotonic Seq, folds the
// action into the cumulative Counts, appends to the bounded ring (dropping
// the oldest entry on overflow) and — for CONTINUATION actions only —
// refreshes qr.LastFailover as a projection of the new tail so the notice
// path and the trace can never drift. Terminal entries (completed/failed/
// canceled) journal but leave LastFailover untouched: the marker answers
// "why is this request queued AGAIN", which stays meaningful after the
// request ended (08 号 tests pin this).
func (qr *QueuedRequest) recordDecision(entry JournalEntry) JournalEntry {
	qr.journalSeq++
	entry.Seq = qr.journalSeq
	if entry.At.IsZero() {
		entry.At = time.Now()
	}
	switch entry.Action {
	case NextActionRetrySameCred:
		qr.Counts.Retries++
	case NextActionSwitchCred:
		qr.Counts.NodeSwitches++
	case NextActionSwitchModel:
		qr.Counts.ModelSwitches++
	case NextActionCapacityWait:
		qr.Counts.CapacityWaits++
	case NextActionScheduledWait:
		qr.Counts.ScheduledWaits++
	}
	entry.Counts = qr.Counts
	qr.AttemptJournal = append(qr.AttemptJournal, entry)
	if overflow := len(qr.AttemptJournal) - journalCapacity; overflow > 0 {
		copy(qr.AttemptJournal, qr.AttemptJournal[overflow:])
		qr.AttemptJournal = qr.AttemptJournal[:journalCapacity]
	}
	switch entry.Action {
	case NextActionCompleted, NextActionFailed, NextActionCanceled:
		// Terminal: journal only; LastFailover keeps the last requeue story.
	default:
		qr.LastFailover = FailoverMarker{
			ErrorKind:    entry.ErrorKind,
			HTTPStatus:   entry.HTTPStatus,
			Model:        entry.Model,
			CredentialID: entry.CredentialID,
			Vendor:       entry.Vendor,
			NextAction:   entry.Action,
			Attempt:      entry.Attempt,
			StampedAt:    entry.At,
		}
	}
	return entry
}

// terminalActionOf classifies a terminal ForwardOutcome into the journal
// terminal vocabulary, mirroring emitRequestTerminal: success → completed,
// client cancel/deadline → canceled, everything else → failed.
func terminalActionOf(out ForwardOutcome) NextActionKind {
	if out.Err == nil {
		return NextActionCompleted
	}
	if errors.Is(out.Err, context.Canceled) || errors.Is(out.Err, context.DeadlineExceeded) {
		return NextActionCanceled
	}
	return NextActionFailed
}

// JournalSnapshot returns a detached copy of the full execution trace. This
// is THE way the trace leaves the request: callers that hold the request
// (the executor adapter after Submit returns, tests) read it here — the
// journal never flows into process-wide stores. After the terminal entry the
// ring is immutable, so a terminal-time snapshot stays valid.
func (qr *QueuedRequest) JournalSnapshot() []JournalEntry {
	if qr == nil || len(qr.AttemptJournal) == 0 {
		return nil
	}
	out := make([]JournalEntry, len(qr.AttemptJournal))
	copy(out, qr.AttemptJournal)
	return out
}
