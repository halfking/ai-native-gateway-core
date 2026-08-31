// Package main — migration_627_630_contract_test.go
//
// 2026-08-31: contract test for migration 627-630 (audit-data-closure A/B/C).
//
// These tests guard the canonical SQL files for the migration fixes that closed
// the audit-data-closure loop. They are intentionally file-level (no PostgreSQL
// required) so CI can run them without spinning up a DB container, and they
// complement the existing embeddata sync test (stats_migrations_test.go) which
// only checks byte-equality.
//
// What the contract test catches:
//   - missing DDL anchors (the unique constraint, the view, the watermark seed)
//   - accidental revert of a non-reversible change (627 view drop, 629 table
//     drop, 630 RLS drop)
//   - drift between the canonical SQL and the installer embed
//
// What it does NOT cover (would require PostgreSQL):
//   - actual RLS enforcement behavior
//   - the aggregation_id COALESCE runtime result
//   - watermark advance under real hot inserts

package main

import (
	"os"
	"strings"
	"testing"
)

// TestMigration627AggregationIDUnifiedContract locks in the three pieces that
// make 627's fix work: the parent-table column, the unified view, and the
// watermark seed for first-tick historical replay.
func TestMigration627AggregationIDUnifiedContract(t *testing.T) {
	src := canonicalMigration(t, "627_candidate_failure_logs_aggregation_id_unified.sql")
	mustContain(t, src,
		"ALTER TABLE IF EXISTS public.candidate_failure_logs",
		"ADD COLUMN IF NOT EXISTS aggregation_id bigint",
	)
	mustContain(t, src,
		"CREATE OR REPLACE VIEW public.candidate_failure_logs_unified",
		"COALESCE(aggregation_id, -id) AS aggregation_id",
		"UNION ALL",
	)
	mustContain(t, src,
		"ALTER VIEW public.candidate_failure_logs_unified SET SECURITY_INVOKER = true",
	)
	// The historical synthesize path must rely on negative values so the
	// hot-sequence positive domain never overlaps. A regression that flips the
	// sign would silently break the watermark invariant — guard the literal.
	if !strings.Contains(src, "(-c.id)") && !strings.Contains(src, "-id) AS aggregation_id") {
		t.Fatalf("migration 627 must synthesize historical aggregation_id as the negation of id (got no -id pattern)")
	}
	// Watermark seed must be bigint-min so the first post-deploy tick replays
	// every historical bucket. A regression that uses 0 here would silently
	// filter all historical rows out of provider_error_details.
	mustContain(t, src,
		"last_source_id = -9223372036854775807",
		"provider_error_aggregator_state",
	)
	// Post-conditions: column + view must be present at end of migration.
	mustContain(t, src,
		"migration 627 post-condition failed: aggregation_id missing",
		"migration 627 post-condition failed: candidate_failure_logs_unified view not created",
	)
}

// TestMigration628PromoteAtomicV3Contract guards the v3 promote fix that
// carries aggregation_id across the hot→partition promote.
func TestMigration628PromoteAtomicV3Contract(t *testing.T) {
	src := canonicalMigration(t, "628_candidate_failure_logs_promote_atomic_v3.sql")
	// Must replace the v2 promote function (not append a v4) — the migration
	// exists to override the v2 behavior so a fresh install skips the buggy
	// path entirely.
	mustContain(t, src,
		"CREATE OR REPLACE FUNCTION",
		"promote_candidate_failure_logs",
	)
	// The new INSERT must carry aggregation_id so the watermark stays correct
	// after promotion. v2 dropped this column; v3 is the fix.
	mustContain(t, src,
		"aggregation_id",
		"INSERT INTO",
	)
}

// TestMigration629AuditAttachmentsCleanupContract locks in the table shape
// that makes the cleanup audit append-only.
func TestMigration629AuditAttachmentsCleanupContract(t *testing.T) {
	src := canonicalMigration(t, "629_audit_attachments_cleanup.sql")
	mustContain(t, src,
		"CREATE TABLE IF NOT EXISTS public.audit_attachments_cleanup",
		"cleanup_run_id    uuid        NOT NULL",
		"triggered_by_user text        NOT NULL",
		"CONSTRAINT audit_attachments_cleanup_unique",
		"UNIQUE (request_id, attachment_hash, cleanup_run_id)",
	)
	// Cleanup is intentionally RLS-free at the table level (super-admin only);
	// a regression that adds ENABLE ROW LEVEL SECURITY here would silently
	// lock out the reaper.
	if strings.Contains(src, "ENABLE ROW LEVEL SECURITY") {
		t.Fatalf("migration 629 must NOT enable RLS on audit_attachments_cleanup (cleanup is super-admin-only)")
	}
}

// TestMigration630SessionAggregateOutboxContract locks in the durable retry
// queue shape: status state machine, RLS bypass for the reaper, and the
// FOR UPDATE SKIP LOCKED-compatible index shape.
func TestMigration630SessionAggregateOutboxContract(t *testing.T) {
	src := canonicalMigration(t, "630_session_aggregate_outbox.sql")
	mustContain(t, src,
		"CREATE TABLE IF NOT EXISTS public.session_aggregate_outbox",
		"status          text        NOT NULL DEFAULT 'pending'",
		"CHECK (status IN ('pending', 'claimed', 'done', 'dead'))",
		"CONSTRAINT session_aggregate_outbox_unique_request",
		"UNIQUE (tenant_id, session_id, partition_date, request_id)",
		"ALTER TABLE public.session_aggregate_outbox ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE public.session_aggregate_outbox FORCE ROW LEVEL SECURITY",
		"CREATE POLICY tenant_isolation_session_aggregate_outbox",
		"app.current_role', true) = 'super_admin'",
		"app.bypass_rls', true) = 'true'",
	)
	// The reaper relies on the partial index for the pending queue scan; a
	// regression that drops the WHERE clause turns it into a full scan.
	mustContain(t, src,
		"CREATE INDEX IF NOT EXISTS idx_session_aggregate_outbox_pending",
		"WHERE status = 'pending'",
	)
}

// TestMigrations627To630EmbeddedBytesMatchCanonicalSources verifies the
// installer embed is byte-equivalent to the canonical SQL — a regression here
// means a CI-built installer would ship SQL that does not match the source
// of truth the contract tests above guard.
func TestMigrations627To630EmbeddedBytesMatchCanonicalSources(t *testing.T) {
	cases := []struct {
		key  string
		embed []byte
	}{
		{"startup/627_candidate_failure_logs_aggregation_id_unified.sql", candidateFailureLogsAggregationIdUnifiedMigration627},
		{"startup/628_candidate_failure_logs_promote_atomic_v3.sql", candidateFailureLogsPromoteAtomicV3Migration628},
		{"startup/629_audit_attachments_cleanup.sql", auditAttachmentsCleanupMigration629},
		{"startup/630_session_aggregate_outbox.sql", sessionAggregateOutboxMigration630},
	}
	for _, c := range cases {
		if len(c.embed) == 0 {
			t.Errorf("embed for %s is empty", c.key)
			continue
		}
		canonical := canonicalMigration(t, strings.TrimPrefix(c.key, "startup/"))
		if string(c.embed) != canonical {
			t.Errorf("embed drift for %s: installer embed differs from canonical SQL", c.key)
		}
	}
}

func canonicalMigration(t *testing.T, name string) string {
	t.Helper()
	canonicalDir := "../../../sql/migrations/startup"
	data, err := os.ReadFile(canonicalDir + "/" + name)
	if err != nil {
		t.Fatalf("read canonical migration %s: %v", name, err)
	}
	return string(data)
}

func mustContain(t *testing.T, src string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(src, n) {
			t.Errorf("expected migration SQL to contain %q", n)
		}
	}
}