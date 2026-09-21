package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration618CreatesTenantScopedJournalSnapshotReceipts(t *testing.T) {
	data, err := os.ReadFile("618_request_journey_snapshot_receipts.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS public.journal_snapshot_receipts",
		"snapshot_version BIGINT NOT NULL",
		"UNIQUE (tenant_id, request_id, snapshot_version)",
		"payload_hash TEXT NOT NULL",
		"status IN ('processing', 'completed')",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
		"current_setting('app.current_tenant'",
		"current_setting('app.bypass_rls'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("migration 618 missing %q", want)
		}
	}
}

func TestMigration618DownDropsOnlyReceiptObjects(t *testing.T) {
	data, err := os.ReadFile("618_request_journey_snapshot_receipts.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "DROP TABLE IF EXISTS public.journal_snapshot_receipts") {
		t.Fatal("down migration must drop receipt table")
	}
	if strings.Contains(s, "request_state_transitions") || strings.Contains(s, "request_journey_observation_outbox") {
		t.Fatal("down migration must not remove unrelated request journey data")
	}
}
