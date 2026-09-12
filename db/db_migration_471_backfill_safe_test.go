package db

import (
	"os"
	"strings"
	"testing"
)

// TestEnsureSessionSummariesArchivalLockSafe is the RUNBOOK-zstd-deploy §7.7
// contract: the 471 repair block must never replay the whole-table backfill or
// blocking CREATE INDEX against the live hot table — the shared PG's 30s
// statement_timeout (SQLSTATE 57014) killed both under active traffic and
// latched "postgres disabled" → readyz 503 on every canary boot (2026-09-11).
func TestEnsureSessionSummariesArchivalLockSafe(t *testing.T) {
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	for _, want := range []string{
		"func (d *DB) ensureSessionSummariesArchivalSchema",
		// columns: short lock_timeout so the DDL wait cannot stack traffic
		"SET LOCAL lock_timeout = '2s'",
		"ADD COLUMN IF NOT EXISTS last_accessed_at TIMESTAMPTZ",
		"ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ",
		// backfill: PK-watermark chunk with DB-collation boundary + bounded slice
		"sessionSummariesBackfillBatch",
		"sessionSummariesBackfillSlice",
		"max(session_key) OVER () AS chunk_max",
		"AND last_request_at IS NOT NULL",
		// indexes: concurrent builds + INVALID-corpse repair
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_summaries_archival",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_summaries_archived",
		"DROP INDEX CONCURRENTLY IF EXISTS public.",
		"i.indisvalid",
		// ledger rows preserved
		"'471', 'session_summaries_archival'",
		"'690', 'session_summaries archived ttl index'",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("db.go missing 471 lock-safe contract %q", want)
		}
	}

	// The pre-fix shape: one whole-table UPDATE inside the same batch as the
	// index DDL. The chunked statement aliases the table (ss.last_request_at),
	// so this exact unaliased form must be gone.
	if banned := "UPDATE public.session_summaries SET last_accessed_at = last_request_at"; strings.Contains(text, banned) {
		t.Errorf("db.go still contains whole-table 471 backfill %q", banned)
	}
}
