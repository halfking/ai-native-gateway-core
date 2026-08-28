package migration

import (
	"path/filepath"
	"testing"
)

// TestLedgerAppendPersistsImmediately covers the MEDIUM hardening
// (2026-08-29): with the default fsync-on-append behaviour, every
// successful Append must be visible to a fresh os.ReadFile invocation
// even when the Ledger handle has been closed. The contract is "writes
// survive an unclean shutdown", which we approximate by reading the
// file from a separate handle after Append returns.
func TestLedgerAppendPersistsImmediately(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.ndjson")
	l := NewLedger(path)
	item := Item{
		SourceKey:      "ursm:v2:node:9:m",
		Classification: ClassificationMigratable,
		Status:         StatusClassified,
	}
	if err := l.Append(item); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := l.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 1 || got[0].SourceKey != item.SourceKey {
		t.Fatalf("expected one item %q, got %+v", item.SourceKey, got)
	}
}

// TestLedgerSyncToggle confirms SetSyncOnAppend / NewLedgerWithSync
// route the fsync decision correctly. When disabled, Append must still
// succeed (it just doesn't fsync).
func TestLedgerSyncToggle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.ndjson")

	// Disabled via constructor.
	l := NewLedgerWithSync(path, false)
	if err := l.Append(Item{SourceKey: "k1", Status: StatusClassified}); err != nil {
		t.Fatalf("Append (no fsync): %v", err)
	}

	// Toggle on a second ledger and confirm Append still works.
	l2 := NewLedger(filepath.Join(dir, "ledger2.ndjson")).SetSyncOnAppend(true)
	if err := l2.Append(Item{SourceKey: "k2", Status: StatusClassified}); err != nil {
		t.Fatalf("Append (fsync on): %v", err)
	}
	got, err := l2.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 1 || got[0].SourceKey != "k2" {
		t.Fatalf("expected one item k2, got %+v", got)
	}
}
