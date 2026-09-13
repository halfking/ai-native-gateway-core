package startup

import (
	"os"
	"strings"
	"testing"
)

// Migration 700 appends raw_model_name to the canonical
// request_logs_with_current_month view. The 7-day fingerprint drift scanner
// (bg/integrity_fingerprint_drift.go) selects raw_model_name from the view
// since its read surface left the bare parent (83bf582dd / startup 696
// doctrine) — but the wrapper chain's frozen base intersection predates 485,
// so the column never surfaced and every scan failed with 42703 (observed
// live on the local deploy DB; the shared 252 production PG carries the same
// 112-column view shape and would reproduce it on the next scanner-bearing
// binary). 700 mirrors 696: column-presence guard + CREATE OR REPLACE of the
// canonical stage only, preserving the 696 fingerprint lateral.
// Numbering note: authored as 699 against a same-day dual-ledger check, then
// renumbered 699→700 after the parallel timezone-pinning line claimed
// 698/699 on origin first; 698-702 were re-verified free before taking 700.
func TestMigration700ViewRawModelName(t *testing.T) {
	migration, err := os.ReadFile("700_request_logs_view_raw_model_name.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	body := stripSQLComments(string(migration))

	for _, required := range []string{
		"BEGIN;",
		// Column-presence guard: dynamically rebuilt chains (680 bootstrap /
		// db.go self-heal on a post-485 database) already expose the column
		// via the base wrapper — an unconditional CREATE OR REPLACE would
		// fail with "column already exists".
		"canonical_has_raw_model_name",
		"IF canonical_has_raw_model_name THEN",
		"CREATE OR REPLACE VIEW public.request_logs_with_current_month",
		// raw_model_name rides the same hot-first lateral as 577/610/696 and
		// the 696 fingerprint column stays in the same select list — a
		// rebuild that dropped it would silently revert 696.
		"source.request_class, source.due_at, source.system_fingerprint, source.raw_model_name",
		"SELECT h.request_class, h.due_at, h.system_fingerprint, h.raw_model_name",
		"SELECT p.request_class, p.due_at, p.system_fingerprint, p.raw_model_name",
		"FROM public.request_logs_with_current_month_without_request_class_due_at v",
		"COMMIT;",
		// Ledger self-registration: the channel stamps only
		// gateway_db_revision_sequences; without this INSERT every
		// channel-upgraded database would drift in schema_migrations
		// (698/699 self-register, 696/697 needed a manual catch-up).
		"INSERT INTO public.schema_migrations (version, description)",
		"ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("migration 700 missing %q", required)
		}
	}

	// Readers must never observe a missing relation: the canonical view is
	// replaced in place, wrappers stay untouched.
	if strings.Contains(body, "DROP VIEW") {
		t.Fatalf("migration 700 must not DROP the view (readers would see 42P01)")
	}
	if strings.Contains(body, "EXCEPTION WHEN OTHERS") {
		t.Fatalf("migration 700 must not swallow errors")
	}

	// The upgrade channel must carry 700 (693-class gap: a migration that
	// never reaches apply-db-revision-sequence.sh is inert on upgrade DBs —
	// exactly how 694/695 sat unapplied on the shared production PG, and how
	// 696/697 sat unapplied on the local deploy DB).
	seq, err := os.ReadFile("../../../scripts/apply-db-revision-sequence.sh")
	if err != nil {
		t.Fatalf("read apply-db-revision-sequence.sh: %v", err)
	}
	seqBody := string(seq)
	for _, required := range []string{
		"700_request_logs_view_raw_model_name.sql",
		"701_credential_balance_floor.sql",
	} {
		if !strings.Contains(seqBody, required) {
			t.Fatalf("apply-db-revision-sequence.sh does not carry migration %s — upgrade-database deployments would never apply it", required)
		}
	}
	if strings.Index(seqBody, "700_request_logs_view_raw_model_name.sql") > strings.Index(seqBody, "701_credential_balance_floor.sql") {
		t.Fatalf("migration channel order regressed: 701 appears before 700")
	}
}
