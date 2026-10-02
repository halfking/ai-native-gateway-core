//go:build integration

package admin

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestSessionExportMessagesSQL_ExecutesOnRealDatabase is the gate that should
// have existed all along.
//
// `/api/admin/session-export` (registered in cmd/gateway/main.go) has been dead
// on arrival twice, for weeks each:
//
//	2026-07-08  SELECT rl.role — column does not exist → 42703
//	2026-08-28  COALESCE(rb.request_body, ''::jsonb) → 22P02, at parse time
//
// Neither was caught. `session_export_test.go` asserts on auth ordering, alias
// resolution and tenant enforcement — all of which pass while the one query the
// endpoint actually runs is unparseable. A pgxmock expectation matches a query
// *string*; it never lets PostgreSQL parse it.
//
// This gate executes the production SQL text itself — not a copy — against a
// real database. Anything the parser rejects fails here instead of in a user's
// export.
//
// Run with a real database:
//
//	TEST_PG_URL='postgres://postgres@127.0.0.1:5432/llm_gateway' \
//	  go test -tags=integration ./admin/ -run TestSessionExportMessagesSQL
func TestSessionExportMessagesSQL_ExecutesOnRealDatabase(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that the export query parses")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// A real session id is not required: parse and bind errors surface before
	// any row is produced, and using a non-existent id keeps the gate cheap and
	// independent of retention windows.
	const missingSession = "__gate_no_such_session__"

	rows, err := pool.Query(ctx, sessionExportMessagesSQL(), missingSession)
	if err != nil {
		t.Fatalf("export messages SQL failed to execute: %v", err)
	}
	defer rows.Close()

	// The statement reaching "no error" is the assertion: 22P02 and 42703 are
	// both raised at parse/plan time, so a parseable, bindable, executable
	// statement is the whole gate. Draining the result catches anything the
	// server defers to execution (RLS, per-row casts).
	fields := rows.FieldDescriptions()
	if len(fields) != 10 {
		t.Fatalf("export messages returned %d columns, want 10", len(fields))
	}
	dest := make([]any, len(fields))
	for i := range dest {
		dest[i] = new(any)
	}
	rowsSeen := 0
	for rows.Next() {
		rowsSeen++
		if err := rows.Scan(dest...); err != nil {
			t.Fatalf("scan row: %v", err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if rowsSeen != 0 {
		t.Fatalf("probe session %q unexpectedly exists with %d rows; pick a different sentinel",
			missingSession, rowsSeen)
	}
	t.Logf("export messages SQL executed cleanly; columns: %v", columnNames(fields))
}

func columnNames(fields []pgconn.FieldDescription) []string {
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		names = append(names, f.Name)
	}
	return names
}
