package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Migration 743 (252 SQL 日志审计第六轮, 2026-09-24) adds the partial indexes
// that close two 30s-kill loops observed on pg-252-pg17 (45min snapshot,
// docs/audit/2026-09-24-252-sql-log-audit.md):
//
//	A session_aggregate_outbox done-row TTL trim (reaper trimDoneRows):
//	255 full scans of a 645MB table per 45min (3 replicas × 30s tick),
//	med 4.2s, 3 kills — no existing index covers (status='done',
//	completed_at).
//	B session_turns digest backfill empty-scan: `digest IS NULL` has no
//	index anywhere and the with_current_month view is really hot arm +
//	FULL partitioned parent, so proving emptiness scans 68 万 rows
//	(6 kills per 45min). The (ts, id) WHERE digest IS NULL partial index
//	matches the query's ORDER BY so top-N also uses it when non-empty.
//
// The contract pins the invariants that make 743 safe and effective:
//
//	C1  A targets the reaper's exact trim predicate (bound by parsing the
//	    reaper source, not copy-paste).
//	C2  B builds (ts, id) WHERE digest IS NULL on all three layers —
//	    per-partition (\gexec), the independent hot table, and the parent
//	    ONLY shell + ATTACH loop — and stays bound to the backfill
//	    SELECT's filter + ORDER BY (session_digest_backfill.go source).
//	C3  Every full index build is CONCURRENTLY, the file is NOT wrapped in
//	    BEGIN/COMMIT (same reason as 727/728/729 — installer runs files
//	    with psql --single-transaction; 743 ships through the Go ensure
//	    db.ensureSqlAuditPartialIndexes + the revision-sequence channel),
//	    and the only non-CONCURRENTLY build is the ON ONLY metadata shell.
//	C4  down drops exactly the three named indexes (+ the per-partition
//	    leftover loop for children not yet ATTACHed).
func TestMigration743PartialIndexesContract(t *testing.T) {
	upBytes, err := os.ReadFile("743_sql_audit_partial_indexes.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	upCompact := normalizeSQL(up)

	// ── C1: outbox index ↔ reaper trim predicate ───────────────────────
	outboxIdx := "CREATE INDEX CONCURRENTLY IF NOT EXISTS IDX_SESSION_AGGREGATE_OUTBOX_DONE_COMPLETED_AT ON PUBLIC.SESSION_AGGREGATE_OUTBOX (COMPLETED_AT) WHERE STATUS = 'DONE'"
	if !strings.Contains(upCompact, outboxIdx) {
		t.Errorf("743 must create idx_session_aggregate_outbox_done_completed_at CONCURRENTLY with the status='done' predicate")
	}
	reaperSrc, err := os.ReadFile(filepath.Join("..", "..", "..", "domains", "session", "v2", "session_aggregate_outbox_reaper.go"))
	if err != nil {
		t.Fatal(err)
	}
	reaperCompact := normalizeGoSQL(string(reaperSrc))
	for _, frag := range []string{"WHERE STATUS = 'DONE'", "COMPLETED_AT < NOW() - INTERVAL '7 DAYS'", "ORDER BY COMPLETED_AT"} {
		if !strings.Contains(reaperCompact, frag) {
			t.Errorf("reaper trimDoneRows no longer matches its 743 F-A predicate fragment %q — re-audit migration 743 A against the new trim SQL", frag)
		}
	}

	// ── C2: digest indexes on all three layers, bound to the backfill ──
	const pred = "(TS, ID) WHERE DIGEST IS NULL"
	for _, frag := range []string{
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON PUBLIC.%I " + pred,          // per-partition \gexec
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS IDX_SESSION_TURNS_HOT_DIGEST_NULL ON PUBLIC.SESSION_TURNS_HOT " + pred, // hot arm
		"CREATE INDEX IF NOT EXISTS IDX_SESSION_TURNS_DIGEST_NULL ON ONLY PUBLIC.SESSION_TURNS " + pred, // parent ONLY shell
		"ALTER INDEX PUBLIC.IDX_SESSION_TURNS_DIGEST_NULL ATTACH PARTITION",        // ATTACH loop
		"PG_INHERITS WHERE INHPARENT = 'PUBLIC.SESSION_TURNS'::REGCLASS",           // live partition enumeration
	} {
		if !strings.Contains(upCompact, frag) {
			t.Errorf("743 digest section lost fragment %q", frag)
		}
	}
	backfillSrc, err := os.ReadFile(filepath.Join("..", "..", "..", "domains", "session", "v2", "session_digest_backfill.go"))
	if err != nil {
		t.Fatal(err)
	}
	backfillCompact := normalizeGoSQL(string(backfillSrc))
	if !strings.Contains(backfillCompact, "WHERE T.DIGEST IS NULL") ||
		!strings.Contains(backfillCompact, "ORDER BY T.TS, T.ID") {
		t.Error("digest backfill SELECT no longer filters digest IS NULL / orders by (ts, id) — re-audit migration 743 B against the new scan shape")
	}

	// ── C3: non-transactional + CONCURRENTLY everywhere except the shell ─
	if strings.Contains(upCompact, "BEGIN;") || strings.Contains(upCompact, "COMMIT;") {
		t.Error("743 must NOT wrap its body in BEGIN/COMMIT — CREATE INDEX CONCURRENTLY cannot run inside a transaction block")
	}
	if got := strings.Count(upCompact, "CREATE INDEX CONCURRENTLY IF NOT EXISTS"); got != 3 {
		t.Errorf("743 must issue exactly 3 CONCURRENTLY builds (outbox + hot + per-partition template), found %d", got)
	}
	if got := strings.Count(upCompact, "CREATE INDEX IF NOT EXISTS"); got != 1 {
		t.Errorf("743 must issue exactly 1 non-concurrent build (the ON ONLY metadata shell), found %d", got)
	}

	// ── C4: down drops the named indexes + partition leftovers ─────────
	downBytes, err := os.ReadFile("743_sql_audit_partial_indexes.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downCompact := normalizeSQL(string(downBytes))
	for _, idx := range []string{
		"DROP INDEX CONCURRENTLY IF EXISTS PUBLIC.IDX_SESSION_AGGREGATE_OUTBOX_DONE_COMPLETED_AT",
		"DROP INDEX IF EXISTS PUBLIC.IDX_SESSION_TURNS_DIGEST_NULL",
		"DROP INDEX CONCURRENTLY IF EXISTS PUBLIC.IDX_SESSION_TURNS_HOT_DIGEST_NULL",
	} {
		if !strings.Contains(downCompact, idx) {
			t.Errorf("743 down missing %q", idx)
		}
	}
	if !strings.Contains(downCompact, "I.INHPARENT = 'PUBLIC.SESSION_TURNS'::REGCLASS") {
		t.Error("743 down must sweep unattached per-partition leftover indexes")
	}
}
