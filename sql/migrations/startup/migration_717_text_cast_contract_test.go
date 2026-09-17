package startup

import (
	"os"
	"strings"
	"testing"
)

// TestMigration717ShapePins pins the structure of the live-DB-rewritten 717
// (origin/main, "corrected same day after live-DB audit" — adopted at the
// R37 merge):
//
//  1. NO customer_id clause — 574 already owns hot.customer_id text→bigint on
//     every existing install and the baseline is born aligned; a `customer_id
//     ~ regex` here is 42883 on bigint (the R36 Rev-1 bug, flagged by
//     e0f94a799's commit message and re-derived by both parallel sessions).
//  2. NO single whole-batch ALTER TABLE — PostgreSQL rejects ALTER COLUMN
//     TYPE on ANY column a view depends on (transitively via pg_rewrite) with
//     0A000, BEFORE comparing old/new types, so even a type-identical no-op
//     dies when one statement touches a view-projected column (680's wrapper
//     chain precedes 717 on every install). The rewrite uses per-column
//     subtransactions with feature_not_supported handlers.
//  3. Every ALTER is guarded by an information_schema data_type check, which
//     makes the file a silent no-op on aligned (fresh-install) baselines.
//  4. A post-batch report keeps residual drift visible.
func TestMigration717ShapePins(t *testing.T) {
	body, err := os.ReadFile("717_request_logs_hot_column_alignment.sql")
	if err != nil {
		t.Fatalf("read 717: %v", err)
	}
	sql := string(body)

	mustContain := []string{
		"BEGIN;",                         // atomic batch
		"feature_not_supported",          // per-column 0A000 resilience
		"information_schema.columns",     // per-column type guards
		"data_type = 'text'",             // drifted-state guard (varchar/jsonb/boolean sources)
		"data_type = 'double precision'", // content_safety_score source (live fact)
		"data_type = 'ARRAY'",            // dlp_violations text[] source (live fact)
		"columns still off-target",       // post-batch report keeps drift visible
	}
	for _, want := range mustContain {
		if !strings.Contains(sql, want) {
			t.Errorf("717 lost a live-validated invariant: %q not found", want)
		}
	}

	// The withdrawn clause must stay withdrawn: a bare text-operator on
	// customer_id is 42883 on the bigint column that 574 + the baseline
	// already guarantee; re-adding it resurrects the fresh-install blocker.
	// (SQL shapes only — the file header documents the withdrawn Rev-1 form
	// and legitimately contains the literal.)
	for _, banned := range []string{
		"WHEN customer_id ~",
		"THEN customer_id::bigint",
		"ALTER COLUMN customer_id TYPE",
	} {
		if strings.Contains(sql, banned) {
			t.Errorf("717 resurrected the withdrawn customer_id clause: %q — 574 owns the text→bigint transition; the clause is 42883 on bigint", banned)
		}
	}
	// Per-column subtransactions are load-bearing: one view-dependent 0A000
	// must not abort its siblings (the original single-statement batch died
	// on the first view-projected column).
	if got := strings.Count(sql, "EXCEPTION WHEN feature_not_supported"); got < 7 {
		t.Errorf("717 has %d per-column 0A000 handlers, want ≥7 (one per guarded column)", got)
	}
}
