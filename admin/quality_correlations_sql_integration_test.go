//go:build integration

package admin

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestQualityBreakdownQueries_ExecuteOnRealDatabase covers the other half of the
// same defect class found on 2026-10-01.
//
// `buildBreakdownQuery` builds one statement per `by` dimension, and two of
// them — `images` and `code_block` — carried `COALESCE(rb.request_body,
// ”::jsonb)`. PostgreSQL evaluates that constant at parse time, so those two
// dimensions returned 22P02 while `tools`, `prompt_length` and `language` all
// worked. That asymmetry is what made it survivable: a spot check of the panel
// looks fine, and only the two buckets a user happens to click are broken.
//
// The existing `quality_correlations_test.go` cannot see any of this — it tests
// `bucketIndex()`, a pure function over bucket *names*. The SQL is only ever
// handed to a live connection, so it is only ever parsed by PostgreSQL.
//
// This gate runs every dimension through a real database. A dimension that
// cannot parse fails here, not in the panel.
//
// Run with a real database:
//
//	TEST_PG_URL='postgres://postgres@127.0.0.1:5432/llm_gateway' \
//	  go test -tags=integration ./admin/ -run TestQualityBreakdownQueries
func TestQualityBreakdownQueries_ExecuteOnRealDatabase(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that every breakdown dimension parses")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// The full dimension set buildBreakdownQuery knows about. Keep this in sync
	// with its switch — a dimension added there but not here is untested, which
	// is precisely how `images` stayed broken.
	for _, by := range []string{"prompt_length", "tools", "images", "code_block", "language"} {
		t.Run(by, func(t *testing.T) {
			sql, err := buildBreakdownQuery(by)
			if err != nil {
				t.Fatalf("buildBreakdownQuery(%q): %v", by, err)
			}
			if sql == "" {
				t.Fatalf("buildBreakdownQuery(%q) returned empty SQL", by)
			}
			// 1 day window: keeps the scan bounded while still executing every
			// expression in the SELECT and GROUP BY. Parse and plan errors —
			// the failure mode this gate exists for — surface regardless.
			rows, err := pool.Query(ctx, sql, 1)
			if err != nil {
				t.Fatalf("dimension %q failed to execute: %v", by, err)
			}
			defer rows.Close()
			dest := make([]any, len(rows.FieldDescriptions()))
			for i := range dest {
				dest[i] = new(any)
			}
			n := 0
			for rows.Next() {
				n++
				if err := rows.Scan(dest...); err != nil {
					t.Fatalf("dimension %q scan row: %v", by, err)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("dimension %q rows: %v", by, err)
			}
			t.Logf("dimension %q executed cleanly, %d bucket rows", by, n)
		})
	}
}
