package startup

import (
	"os"
	"strings"
	"testing"
)

// Migration 696 appends system_fingerprint to the canonical
// request_logs_with_current_month view so the 7-day fingerprint drift
// scanner (bg/integrity_fingerprint_drift.go) can leave the bare parent per
// the recent-window read-surface doctrine. The view chain's base wrapper was
// created before 603 added the column to request_logs_hot, so its frozen
// hot∩parent intersection never carried the column and no later migration
// rebuilt the chain — 696 closes that gap with a CREATE OR REPLACE of the
// canonical stage only (same lateral shape as 577/610, no DROP).
func TestMigration696ViewSystemFingerprint(t *testing.T) {
	migration, err := os.ReadFile("696_request_logs_view_system_fingerprint.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	body := stripSQLComments(string(migration))

	for _, required := range []string{
		"BEGIN;",
		// Column-presence guard: dynamically rebuilt chains (680 bootstrap /
		// db.go self-heal on a post-603 database) already expose the column
		// via the base wrapper — an unconditional CREATE OR REPLACE would
		// fail with "column already exists".
		"canonical_has_fingerprint",
		"IF canonical_has_fingerprint THEN",
		"CREATE OR REPLACE VIEW public.request_logs_with_current_month",
		// The 696 column rides the same hot-first lateral as 577/610.
		"source.request_class, source.due_at, source.system_fingerprint",
		"SELECT h.request_class, h.due_at, h.system_fingerprint",
		"SELECT p.request_class, p.due_at, p.system_fingerprint",
		"FROM public.request_logs_with_current_month_without_request_class_due_at v",
		"COMMIT;",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("migration 696 missing %q", required)
		}
	}

	// Readers must never observe a missing relation: the canonical view is
	// replaced in place, wrappers stay untouched.
	if strings.Contains(body, "DROP VIEW") {
		t.Fatalf("migration 696 must not DROP the view (readers would see 42P01)")
	}
	if strings.Contains(body, "EXCEPTION WHEN OTHERS") {
		t.Fatalf("migration 696 must not swallow errors")
	}

	// The upgrade channel must carry 696 (693-class gap: a migration that
	// never reaches apply-db-revision-sequence.sh is inert on upgrade DBs —
	// exactly how 695/694 sat unapplied on the shared production PG).
	seq, err := os.ReadFile("../../../scripts/apply-db-revision-sequence.sh")
	if err != nil {
		t.Fatalf("read apply-db-revision-sequence.sh: %v", err)
	}
	if !strings.Contains(string(seq), "696_request_logs_view_system_fingerprint.sql") {
		t.Fatalf("apply-db-revision-sequence.sh does not carry migration 696 — " +
			"upgrade-database deployments would never apply it")
	}
}
