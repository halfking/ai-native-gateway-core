package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"
)

// V6-W1.6 T4 (docs/架构优化v6/09-ir-class-journal-decoupling.md §R10): the
// dimension index reuses itself as the queryable projection of the request
// class and the execution journal. Entries carry snapshots; the authority
// stays on the QueuedRequest.

func newJournalQR(id string) *QueuedRequest {
	qr := NewQueuedRequest(id, "t", "gpt4", context.Background(), "payload")
	qr.ResolvedModel = "gpt4"
	return qr
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

// UpdateWait copies at most the journal tail (16 entries) as a DETACHED
// snapshot: later appends on the request must not mutate the entry copy.
func TestDimensionUpdateWaitJournalsTail(t *testing.T) {
	ix := NewDimensionIndex(DimensionIndexConfig{TTL: time.Minute, PerKeyCapacity: 8, MaxKeys: 16})
	qr := newJournalQR("dj1")
	ix.Track(qr, time.Now())
	for i := 0; i < 20; i++ {
		qr.recordDecision(JournalEntry{Action: NextActionRetrySameCred, Model: "gpt4"})
	}
	now := time.Now()
	ix.UpdateWait(qr, now.Add(5*time.Second), NextActionRetrySameCred, now)
	snap := ix.Snapshot(DimensionModel, "gpt4", 10)
	if len(snap.Entries) != 1 {
		t.Fatalf("entries = %d", len(snap.Entries))
	}
	j := snap.Entries[0].Journal
	if len(j) != dimensionJournalTail {
		t.Fatalf("snapshot journal len = %d, want %d", len(j), dimensionJournalTail)
	}
	if j[0].Seq != 20-dimensionJournalTail+1 || j[len(j)-1].Seq != 20 {
		t.Fatalf("snapshot window wrong: first Seq %d last %d", j[0].Seq, j[len(j)-1].Seq)
	}

	// Later request-side appends must not leak into the snapshot.
	qr.recordDecision(JournalEntry{Action: NextActionSwitchCred})
	snap2 := ix.Snapshot(DimensionModel, "gpt4", 10)
	if got := snap2.Entries[0].Journal; len(got) != dimensionJournalTail || got[len(got)-1].Seq != 20 {
		t.Fatalf("snapshot mutated by later append: %+v", got[len(got)-1])
	}
}

// Complete aligns every ring entry to the terminal state carrying the FULL
// journal including the terminal entry (invariant 4).
func TestDimensionCompleteAlignsFullJournal(t *testing.T) {
	ix := NewDimensionIndex(DimensionIndexConfig{TTL: time.Minute, PerKeyCapacity: 8, MaxKeys: 16})
	qr := newJournalQR("da1")
	ix.Track(qr, time.Now())
	ix.MarkNode(qr, CredentialRef{CredentialID: 7, ProviderID: 3, Vendor: "kimi"}, time.Now())
	qr.recordDecision(JournalEntry{Action: NextActionRetrySameCred, Model: "gpt4", CredentialID: 7})
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
		tail := e.Journal[len(e.Journal)-1]
		if tail.Action != NextActionFailed {
			t.Fatalf("%s journal tail = %+v, want failed terminal", dim.kind, tail)
		}
	}
}

func TestDimensionJournalByRequest(t *testing.T) {
	ix := NewDimensionIndex(DimensionIndexConfig{TTL: 50 * time.Millisecond, PerKeyCapacity: 8, MaxKeys: 16})
	qr := newJournalQR("db1")
	qr.DueAt = time.Now().Add(time.Hour)
	ix.Track(qr, time.Now())
	ix.MarkNode(qr, CredentialRef{CredentialID: 9, ProviderID: 4, Vendor: "openai"}, time.Now())
	qr.recordDecision(JournalEntry{Action: NextActionScheduledWait, Model: "gpt4"})
	qr.recordDecision(JournalEntry{Action: NextActionSwitchCred, Model: "gpt4", CredentialID: 9})
	ix.UpdateWait(qr, time.Now().Add(time.Minute), NextActionSwitchCred, time.Now())

	view, ok := ix.JournalByRequest("db1")
	if !ok {
		t.Fatalf("JournalByRequest not found for tracked request")
	}
	if view.RequestID != "db1" {
		t.Fatalf("view RequestID = %q", view.RequestID)
	}
	if len(view.Entries) != 3 { // model + credential + provider rings
		t.Fatalf("entries = %d, want 3", len(view.Entries))
	}
	if view.Class != RequestClassScheduled {
		t.Fatalf("view Class = %q, want scheduled", view.Class)
	}
	if len(view.Journal) == 0 || view.Journal[len(view.Journal)-1].Seq != 2 {
		t.Fatalf("view journal = %+v, want latest with Seq 2", view.Journal)
	}

	// Unknown request → not found (admin maps to 404).
	if _, ok := ix.JournalByRequest("nope"); ok {
		t.Fatalf("JournalByRequest found unknown request")
	}

	// TTL expiry evicts the entries → not found afterwards.
	time.Sleep(80 * time.Millisecond)
	ix.Sweep(time.Now())
	if _, ok := ix.JournalByRequest("db1"); ok {
		t.Fatalf("JournalByRequest survived TTL expiry")
	}
}
