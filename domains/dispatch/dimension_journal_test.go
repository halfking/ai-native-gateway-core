package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"
)

// V6-W1.6 T4 (docs/架构优化v6/09-ir-class-journal-decoupling.md §R10,
// 范围修正 2026-08-27): the dimension index carries only the request's
// MEMBERSHIP metadata (class / state / outcome / last action). The execution
// trace (AttemptJournal) is attached to the request itself and is NEVER
// replicated into this process-wide index — that scope rule is pinned by
// TestDimensionEntriesDoNotCarryJournal below.

func newJournalQR(id string) *QueuedRequest {
	qr := NewQueuedRequest(id, "t", "gpt4", context.Background(), "payload")
	qr.ResolvedModel = "gpt4"
	return qr
}

func TestDimensionIndexMaxKeysDoesNotRetainOrphanRequest(t *testing.T) {
	ix := NewDimensionIndex(DimensionIndexConfig{TTL: time.Minute, PerKeyCapacity: 8, MaxKeys: 1})
	now := time.Now()

	first := newJournalQR("dimension-first")
	ix.Track(first, now)
	if got := ix.Snapshot(DimensionModel, "gpt4", 10).Total; got != 1 {
		t.Fatalf("first ring total = %d, want 1", got)
	}

	second := NewQueuedRequest("dimension-second", "t", "other-model", context.Background(), "payload")
	second.ResolvedModel = "other-model"
	ix.Track(second, now)

	if _, ok := ix.EntriesByRequest(second.ID); ok {
		t.Fatal("request rejected by MaxKeys must not remain in byReq")
	}
	if got := ix.Snapshot(DimensionModel, "other-model", 10).Total; got != 0 {
		t.Fatalf("rejected ring total = %d, want 0", got)
	}
	_, _, byReq := ix.Stats()
	if byReq != 1 {
		t.Fatalf("byReq cardinality = %d, want 1", byReq)
	}
}

func TestDimensionEntryCarriesClass(t *testing.T) {
	ix := NewDimensionIndex(DimensionIndexConfig{TTL: time.Minute, PerKeyCapacity: 8, MaxKeys: 16})
	qr := newJournalQR("dc1")
	ix.Track(qr, time.Now())
	snap := ix.Snapshot(DimensionModel, "gpt4", 10)
	if len(snap.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(snap.Entries))
	}
	if snap.Entries[0].Class != RequestClassImmediate {
		t.Fatalf("Class = %q, want immediate", snap.Entries[0].Class)
	}

	qr2 := newJournalQR("dc2")
	qr2.DueAt = time.Now().Add(time.Hour)
	ix.Track(qr2, time.Now())
	snap = ix.Snapshot(DimensionModel, "gpt4", 10)
	if snap.Entries[0].Class != RequestClassScheduled {
		t.Fatalf("scheduled entry Class = %q, want scheduled (newest first)", snap.Entries[0].Class)
	}
}

// TestDimensionEntriesDoNotCarryJournal is the SCOPE INVARIANT (用户修正
// 2026-08-27)：轨迹附属请求，分维条目永远不携带 Journal。即使请求上有
// 大量轨迹，Track/MarkNode/UpdateWait/Complete 之后条目仍无轨迹副本。
func TestDimensionEntriesDoNotCarryJournal(t *testing.T) {
	ix := NewDimensionIndex(DimensionIndexConfig{TTL: time.Minute, PerKeyCapacity: 8, MaxKeys: 16})
	qr := newJournalQR("scope1")
	ix.Track(qr, time.Now())
	ix.MarkNode(qr, CredentialRef{CredentialID: 7, ProviderID: 3, Vendor: "kimi"}, time.Now())
	for i := 0; i < 5; i++ {
		qr.recordDecision(JournalEntry{Action: NextActionRetrySameCred, Model: "gpt4", CredentialID: 7})
	}
	now := time.Now()
	ix.UpdateWait(qr, now.Add(5*time.Second), NextActionRetrySameCred, now)
	ix.Complete(qr, ForwardOutcome{ErrorKind: "timeout", Err: errors.New("timeout")}, time.Now())

	for _, dim := range []struct {
		kind DimensionKind
		id   string
	}{{DimensionModel, "gpt4"}, {DimensionCredential, "7"}, {DimensionProvider, "3"}} {
		snap := ix.Snapshot(dim.kind, dim.id, 10)
		if len(snap.Entries) != 1 {
			t.Fatalf("%s entries = %d", dim.kind, len(snap.Entries))
		}
		e := snap.Entries[0]
		if e.State != DimensionStateCompleted || e.Outcome != "failure" {
			t.Fatalf("%s entry = %+v, want completed/failure", dim.kind, e)
		}
	}

	// The trace stays request-attached: full, terminal-tailed, and reachable
	// only via the request object.
	trace := qr.JournalSnapshot()
	if len(trace) != 6 { // 5 decisions + terminal backfill
		t.Fatalf("request journal len = %d, want 6", len(trace))
	}
	if trace[len(trace)-1].Action != NextActionFailed {
		t.Fatalf("journal tail = %+v, want failed terminal", trace[len(trace)-1])
	}
}

// TestDimensionUpdateWaitStampsMembershipOnly: UpdateWait refreshes
// membership metadata (pending state, retry_at, last action) and nothing else.
func TestDimensionUpdateWaitStampsMembershipOnly(t *testing.T) {
	ix := NewDimensionIndex(DimensionIndexConfig{TTL: time.Minute, PerKeyCapacity: 8, MaxKeys: 16})
	qr := newJournalQR("dw1")
	ix.Track(qr, time.Now())
	for i := 0; i < 20; i++ {
		qr.recordDecision(JournalEntry{Action: NextActionRetrySameCred, Model: "gpt4"})
	}
	now := time.Now()
	retryAt := now.Add(5 * time.Second)
	ix.UpdateWait(qr, retryAt, NextActionRetrySameCred, now)
	snap := ix.Snapshot(DimensionModel, "gpt4", 10)
	if len(snap.Entries) != 1 {
		t.Fatalf("entries = %d", len(snap.Entries))
	}
	e := snap.Entries[0]
	if e.State != DimensionStatePending || e.LastAction != NextActionRetrySameCred || !e.RetryAt.Equal(retryAt) {
		t.Fatalf("membership metadata wrong: %+v", e)
	}
	if qr.JournalSnapshot() == nil {
		t.Fatalf("request journal lost")
	}
}

// TestDimensionEntriesByRequest: per-request membership lookup (admin
// request-dimensions endpoint) returns the three dimension entries and 404s
// after TTL expiry. No journal is served.
func TestDimensionEntriesByRequest(t *testing.T) {
	ix := NewDimensionIndex(DimensionIndexConfig{TTL: 50 * time.Millisecond, PerKeyCapacity: 8, MaxKeys: 16})
	qr := newJournalQR("db1")
	qr.DueAt = time.Now().Add(time.Hour)
	ix.Track(qr, time.Now())
	ix.MarkNode(qr, CredentialRef{CredentialID: 9, ProviderID: 4, Vendor: "openai"}, time.Now())
	qr.recordDecision(JournalEntry{Action: NextActionScheduledWait, Model: "gpt4"})
	// UpdateWait starts the entry TTL window (parked requests age out).
	ix.UpdateWait(qr, time.Now().Add(time.Minute), NextActionScheduledWait, time.Now())

	entries, ok := ix.EntriesByRequest("db1")
	if !ok {
		t.Fatalf("EntriesByRequest not found for tracked request")
	}
	if len(entries) != 3 { // model + credential + provider rings
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if entries[0].Class != RequestClassScheduled {
		t.Fatalf("entry Class = %q, want scheduled", entries[0].Class)
	}

	// Unknown request → not found (admin maps to 404).
	if _, ok := ix.EntriesByRequest("nope"); ok {
		t.Fatalf("EntriesByRequest found unknown request")
	}

	// TTL expiry evicts the entries → not found afterwards.
	time.Sleep(80 * time.Millisecond)
	ix.Sweep(time.Now())
	if _, ok := ix.EntriesByRequest("db1"); ok {
		t.Fatalf("EntriesByRequest survived TTL expiry")
	}
}
