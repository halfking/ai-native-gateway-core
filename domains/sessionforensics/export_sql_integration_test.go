//go:build integration

package sessionforensics

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestSessionForensicsExportSQL_ExecutesOnRealDatabase guards a query that has
// carried **two** fatal defects at once, for as long as it has existed:
//
//  1. `SELECT rl.role` — the view `request_logs_with_current_month` has 115
//     columns and no `role` (verified: information_schema returns 0 rows) → 42703
//  2. `COALESCE(rb.request_body, ”::jsonb)` — PostgreSQL evaluates the constant
//     at parse time and `”` is not a JSON document → 22P02
//
// Both are invisible to the existing suite: `sessionforensics_test.go` and
// friends cover the mutation/e2e logic with mocked stores. A pgxmock expectation
// matches a query *string*; it never asks PostgreSQL to parse it.
//
// This gate executes the production SQL text itself against a real database, so
// the next person to add a third one finds out here rather than in a case file.
//
// Run with a real database:
//
//	TEST_PG_URL='postgres://postgres@127.0.0.1:5432/llm_gateway' \
//	  go test -tags=integration ./domains/sessionforensics/ -run TestSessionForensicsExportSQL
func TestSessionForensicsExportSQL_ExecutesOnRealDatabase(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that the forensics export query parses")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// Both stores' message queries. They are the same statement; keeping both
	// under test is deliberate — they are separate literals, and a fix applied
	// to one is not automatically a fix to the other. That is not hypothetical:
	// a first repair pass replaced one of the two and reported the job done.
	for name, sql := range map[string]string{
		"txStore":    forensicsExportMessagesSQL,
		"storeQuery": forensicsExportMessagesSQLAlt,
	} {
		t.Run(name, func(t *testing.T) {
			// Sentinel: parse/plan/bind errors raise before any row is produced,
			// so this costs nothing and depends on no retention window.
			const missingSession = "__gate_no_such_session__"
			rows, err := pool.Query(ctx, sql, missingSession, "default")
			if err != nil {
				t.Fatalf("%s export messages SQL failed to execute: %v", name, err)
			}
			defer rows.Close()
			fields := rows.FieldDescriptions()
			if len(fields) != 12 {
				t.Fatalf("%s returned %d columns, want 12 (%v)", name, len(fields), columnNames(fields))
			}
			dest := make([]any, len(fields))
			for i := range dest {
				dest[i] = new(any)
			}
			seen := 0
			for rows.Next() {
				seen++
				if err := rows.Scan(dest...); err != nil {
					t.Fatalf("%s scan row: %v", name, err)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("%s rows: %v", name, err)
			}
			if seen != 0 {
				t.Fatalf("sentinel session unexpectedly exists with %d rows", seen)
			}
			t.Logf("%s executed cleanly; columns: %v", name, columnNames(fields))
		})
	}
}

func columnNames(fields []pgconn.FieldDescription) []string {
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		names = append(names, f.Name)
	}
	return names
}

// guard that the two literals stay in sync with each other. They are separate
// strings, and drift between them is exactly how half a fix happens.
func TestForensicsExportSQLVariantsStayInSync(t *testing.T) {
	normalize := func(s string) string {
		return strings.Join(strings.Fields(s), " ")
	}
	if normalize(forensicsExportMessagesSQL) != normalize(forensicsExportMessagesSQLAlt) {
		t.Fatalf("the two export message queries have diverged; a fix to one must "+
			"be applied to both:\n--- txStore ---\n%s\n--- storeQuery ---\n%s",
			forensicsExportMessagesSQL, forensicsExportMessagesSQLAlt)
	}
}
