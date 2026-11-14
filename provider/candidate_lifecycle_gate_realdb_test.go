package provider

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestModelLifecyclePredicate_RealDB verifies the ACTUAL row-selection
// behaviour of the lifecycle gate on a real PostgreSQL, using the exact
// predicate text the candidate query embeds.
//
// Why a separate behaviour gate: TestCandidateQuerySQL_ModelLifecycleGate only
// proves the SQL text has the right shape. The defect it guards against (inner
// COALESCE) is invisible to a shape check in isolation — it needs a database
// with (a) the settings row PRESENT and (b) ABSENT, to prove the fail-open path.
//
// Gated on TEST_DATABASE_URL and only runs against an itgate_* database, the
// repo's convention for disposable real-db legs (see AGENTS.md). It creates its
// own temp schema objects and drops them, so it never touches real tables.
func TestModelLifecyclePredicate_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — lifecycle predicate behaviour needs a real database")
	}
	if !strings.Contains(dsn, "/itgate_") {
		t.Skip("refusing to create/drop schema objects outside a disposable itgate_* database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// Isolated schema so we cannot disturb anything else in the disposable DB.
	// Drop-then-create (not IF NOT EXISTS): a previous failed run can leave
	// tables behind, and CREATE TABLE would then abort with 42P07 instead of
	// exercising the predicate.
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS gw_lifecycle_test CASCADE`); err != nil {
		t.Fatalf("drop stale schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE SCHEMA gw_lifecycle_test`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer ccancel()
		_, _ = pool.Exec(cctx, `DROP SCHEMA IF EXISTS gw_lifecycle_test CASCADE`)
	})

	if _, err := pool.Exec(ctx, `
		CREATE TABLE gw_lifecycle_test.settings_kv (key text PRIMARY KEY, value jsonb);
		CREATE TABLE gw_lifecycle_test.m (id int, name text, released_at timestamptz, manual_priority int);
	`); err != nil {
		t.Fatalf("create tables: %v", err)
	}

	// Fixtures: one fresh, one stale, one unknown-age, one stale-but-manual.
	if _, err := pool.Exec(ctx, `
		INSERT INTO gw_lifecycle_test.m VALUES
		  (1, 'fresh',      now() - interval '1 month',   0),
		  (2, 'stale',      now() - interval '3 years',   0),
		  (3, 'unknown',    NULL,                         0),
		  (4, 'stale-pin',  now() - interval '3 years',   5);
	`); err != nil {
		t.Fatalf("insert fixtures: %v", err)
	}

	// The predicate under test, lifted verbatim from the candidate query except
	// for the schema-qualified settings table.
	predicate := `
		AND (
		    COALESCE(m.manual_priority, 0) > 0
		    OR m.released_at IS NULL
		    OR age(now(), m.released_at) <= COALESCE(
		        (SELECT (value #>> '{}')::interval
		           FROM gw_lifecycle_test.settings_kv
		          WHERE key = 'model_lifecycle.stale_threshold'),
		        '2 years'::interval
		    )
		)`

	selected := func(t *testing.T) []string {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT name FROM gw_lifecycle_test.m WHERE TRUE `+predicate+` ORDER BY id`)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				t.Fatalf("scan: %v", err)
			}
			out = append(out, n)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}
		return out
	}

	// --- Case A: settings row ABSENT → default 2 years must apply (fail-open
	//     for unknowns, exclusion for stale). This is the case the inner-COALESCE
	//     bug got wrong: it returned zero rows. ---
	if _, err := pool.Exec(ctx, `DELETE FROM gw_lifecycle_test.settings_kv`); err != nil {
		t.Fatalf("clear settings: %v", err)
	}
	got := selected(t)
	want := []string{"fresh", "unknown", "stale-pin"}
	if !equalStrings(got, want) {
		t.Fatalf("settings ABSENT: got %v, want %v — a missing settings row must fall back to "+
			"'2 years', NOT drop every dated model (inner-COALESCE regression)", got, want)
	}

	// --- Case B: settings row present, threshold widened to 5 years → the
	//     previously-stale row now survives. ---
	if _, err := pool.Exec(ctx, `INSERT INTO gw_lifecycle_test.settings_kv VALUES ('model_lifecycle.stale_threshold', '"5 years"'::jsonb)`); err != nil {
		t.Fatalf("insert setting: %v", err)
	}
	got = selected(t)
	want = []string{"fresh", "stale", "unknown", "stale-pin"}
	if !equalStrings(got, want) {
		t.Fatalf("threshold=5y: got %v, want %v — widening the threshold must re-admit the stale row", got, want)
	}

	// --- Case C: threshold narrowed to 1 day → even the "fresh" row drops;
	//     unknown age and manual priority must STILL survive. ---
	if _, err := pool.Exec(ctx, `UPDATE gw_lifecycle_test.settings_kv SET value = '"1 day"'::jsonb`); err != nil {
		t.Fatalf("narrow setting: %v", err)
	}
	got = selected(t)
	want = []string{"unknown", "stale-pin"}
	if !equalStrings(got, want) {
		t.Fatalf("threshold=1day: got %v, want %v — unknown-age must stay fail-open and "+
			"manual priority must bypass the age gate", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
