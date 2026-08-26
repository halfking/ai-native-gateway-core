package dispatch

import (
	"context"
	"testing"
	"time"
)

// V6-W1.6 T3 (docs/架构优化v6/09-ir-class-journal-decoupling.md §R9):
// AttemptJournal is the per-request execution trace. recordDecision is the
// single write entry: Seq assignment, cumulative ActionCounts, bounded append
// and the LastFailover projection all happen there.

func newJournalTestRequest() *QueuedRequest {
	return NewQueuedRequest("req-journal", "tenant", "gpt-test", nil, nil)
}

func TestRecordDecisionAssignsSeqAndCounts(t *testing.T) {
	qr := newJournalTestRequest()
	qr.AttemptCount = 2

	e1 := qr.recordDecision(JournalEntry{
		Action: NextActionRetrySameCred, Model: "m1", CredentialID: 11, ErrorKind: "timeout",
	})
	if e1.Seq != 1 {
		t.Fatalf("first entry Seq = %d, want 1", e1.Seq)
	}
	e2 := qr.recordDecision(JournalEntry{Action: NextActionRetrySameCred})
	if e2.Seq != 2 {
		t.Fatalf("second entry Seq = %d, want 2", e2.Seq)
	}
	e3 := qr.recordDecision(JournalEntry{Action: NextActionSwitchCred})
	e4 := qr.recordDecision(JournalEntry{Action: NextActionSwitchModel})
	e5 := qr.recordDecision(JournalEntry{Action: NextActionCapacityWait})
	e6 := qr.recordDecision(JournalEntry{Action: NextActionScheduledWait})
	e7 := qr.recordDecision(JournalEntry{Action: NextActionFailed})
	_, _, _ = e4, e5, e6

	if got := qr.Counts; got != (ActionCounts{Retries: 2, NodeSwitches: 1, ModelSwitches: 1, CapacityWaits: 1, ScheduledWaits: 1}) {
		t.Fatalf("cumulative counts = %+v, want Retries=2 NodeSwitches=1 ModelSwitches=1 CapacityWaits=1 ScheduledWaits=1", got)
	}
	// Each entry carries the post-event cumulative counts snapshot.
	if e2.Counts.Retries != 2 || e3.Counts.NodeSwitches != 1 || e7.Counts.ModelSwitches != 1 {
		t.Fatalf("entry count snapshots wrong: e2=%+v e3=%+v e7=%+v", e2.Counts, e3.Counts, e7.Counts)
	}
	// Terminal entries must not fold into any sending/waits bucket.
	if qr.Counts.Retries != 2 || qr.Counts.NodeSwitches != 1 {
		t.Fatalf("terminal entry mutated counts: %+v", qr.Counts)
	}
	if len(qr.AttemptJournal) != 7 {
		t.Fatalf("journal length = %d, want 7", len(qr.AttemptJournal))
	}
}

// Invariant 1 (09 号 §3.3): Seq strictly increases with no gaps.
func TestJournalSeqStrictlyIncreasing(t *testing.T) {
	qr := newJournalTestRequest()
	prev := 0
	for i := 0; i < 10; i++ {
		e := qr.recordDecision(JournalEntry{Action: NextActionRetrySameCred})
		if e.Seq != prev+1 {
			t.Fatalf("Seq jumped: got %d after %d", e.Seq, prev)
		}
		prev = e.Seq
	}
}

// Capacity 128: the ring drops the OLDEST entries; ≥100 attempts + terminal
// never truncate in normal operation (R9 boundary).
func TestJournalCapacityBounded(t *testing.T) {
	qr := newJournalTestRequest()
	for i := 0; i < journalCapacity+30; i++ {
		qr.recordDecision(JournalEntry{Action: NextActionRetrySameCred})
	}
	if len(qr.AttemptJournal) != journalCapacity {
		t.Fatalf("journal length = %d, want %d", len(qr.AttemptJournal), journalCapacity)
	}
	first, last := qr.AttemptJournal[0], qr.AttemptJournal[len(qr.AttemptJournal)-1]
	if first.Seq != 31 {
		t.Fatalf("oldest surviving entry Seq = %d, want 31", first.Seq)
	}
	if last.Seq != journalCapacity+30 {
		t.Fatalf("newest entry Seq = %d, want %d", last.Seq, journalCapacity+30)
	}
	// The retained window stays gap-free.
	for i := 1; i < len(qr.AttemptJournal); i++ {
		if qr.AttemptJournal[i].Seq != qr.AttemptJournal[i-1].Seq+1 {
			t.Fatalf("gap inside journal at index %d", i)
		}
	}
}

// LastFailover remains a projection of the journal tail so the notice path
// and the journal can never drift apart (R9).
func TestRecordDecisionProjectsLastFailover(t *testing.T) {
	qr := newJournalTestRequest()
	qr.AttemptCount = 5
	stamp := time.Now()
	qr.recordDecision(JournalEntry{
		Action: NextActionSwitchCred, Model: "m1", CredentialID: 7, ProviderID: 3,
		Vendor: "kimi", ErrorKind: "quota", HTTPStatus: 429, Attempt: 5, At: stamp,
	})
	want := FailoverMarker{
		ErrorKind: "quota", HTTPStatus: 429, Model: "m1", CredentialID: 7,
		Vendor: "kimi", NextAction: NextActionSwitchCred, Attempt: 5, StampedAt: stamp,
	}
	if qr.LastFailover != want {
		t.Fatalf("LastFailover = %+v, want %+v", qr.LastFailover, want)
	}
}

func TestRequestClassDerivedFromDueAt(t *testing.T) {
	qr := newJournalTestRequest()
	if got := qr.requestClass(); got != RequestClassImmediate {
		t.Fatalf("requestClass() = %q, want immediate for zero DueAt", got)
	}
	qr.DueAt = time.Now().Add(time.Hour)
	if got := qr.requestClass(); got != RequestClassScheduled {
		t.Fatalf("requestClass() = %q, want scheduled for future DueAt", got)
	}
	// Explicit field wins over derivation (executor may pre-stamp it).
	qr.RequestClass = RequestClassImmediate
	if got := qr.requestClass(); got != RequestClassImmediate {
		t.Fatalf("requestClass() = %q, want explicit field value", got)
	}
}

// Terminal classification mirrors emitRequestTerminal: success → completed,
// context cancel/deadline → canceled, everything else → failed.
func TestTerminalActionClassification(t *testing.T) {
	if got := terminalActionOf(ForwardOutcome{}); got != NextActionCompleted {
		t.Fatalf("terminalActionOf(success) = %q, want completed", got)
	}
	if got := terminalActionOf(ForwardOutcome{Err: context.Canceled}); got != NextActionCanceled {
		t.Fatalf("terminalActionOf(cancel) = %q, want canceled", got)
	}
	if got := terminalActionOf(ForwardOutcome{Err: context.DeadlineExceeded}); got != NextActionCanceled {
		t.Fatalf("terminalActionOf(deadline) = %q, want canceled", got)
	}
	if got := terminalActionOf(ForwardOutcome{Err: errPaceTimeout}); got != NextActionFailed {
		t.Fatalf("terminalActionOf(failure) = %q, want failed", got)
	}
}
