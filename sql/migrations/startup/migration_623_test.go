package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration623AddsStableJournalSnapshotProjectionBase(t *testing.T) {
	data, err := os.ReadFile("623_journal_snapshot_receipts_projection_base.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ALTER TABLE public.journal_snapshot_receipts",
		"ADD COLUMN IF NOT EXISTS projection_base_seq BIGINT NOT NULL DEFAULT 0",
		"projection_base_seq >= 0",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("migration 623 missing %q", want)
		}
	}
}

func TestMigration623DownDropsOnlyStableProjectionBase(t *testing.T) {
	data, err := os.ReadFile("623_journal_snapshot_receipts_projection_base.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "DROP COLUMN IF EXISTS projection_base_seq") {
		t.Fatal("migration 623 down must drop projection_base_seq")
	}
	if strings.Contains(s, "DROP TABLE") || strings.Contains(s, "request_state_transitions") {
		t.Fatal("migration 623 down must not remove unrelated data")
	}
}
