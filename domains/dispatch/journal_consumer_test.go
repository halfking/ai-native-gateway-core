package dispatch

import (
	"context"
	"testing"
)

func TestInMemoryJournalStoreStoresDetachedEntries(t *testing.T) {
	store := NewInMemoryJournalStore()
	entries := []JournalEntry{{Seq: 1, Action: NextActionCompleted}}
	store.Store(JournalSnapshot{TenantID: "tenant-a", RequestID: "request-1", Entries: entries})
	entries[0].Seq = 99

	snapshot, err := store.ConsumeSnapshot(context.Background(), "tenant-a", "request-1")
	if err != nil {
		t.Fatalf("ConsumeSnapshot: %v", err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Seq != 1 {
		t.Fatalf("stored entries were not detached: %+v", snapshot.Entries)
	}
}
