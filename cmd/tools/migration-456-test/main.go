// Command migration-456-test verifies the schema changes from migration 456
// (session V2 display + summary columns) are in place.
//
// Usage:
//
//	TEST_DATABASE_URL=postgres://user:pass@host:5432/db \
//	  go run cmd/tools/migration-456-test/main.go
//
// Exit codes:
//
//	0 — all expected columns present
//	1 — at least one missing column (or DB error)
//
// This tool is intentionally read-only: it inspects information_schema only
// and never mutates the database.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

type check struct {
	table  string
	column string
}

func main() {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		log.Fatal("TEST_DATABASE_URL required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer func() {
		if closeErr := conn.Close(context.Background()); closeErr != nil {
			log.Printf("warn: close: %v", closeErr)
		}
	}()

	checks := []check{
		{"sessions", "last_full_request"},
		{"sessions", "last_full_response"},
		{"sessions", "last_full_payload_at"},
		{"sessions", "title"},
		{"sessions", "summary"},
		{"sessions", "summary_model"},
		{"sessions", "summary_generated_at"},
		{"sessions", "summary_quality"},
		{"session_turns", "attempt_no"},
		{"session_turns", "tools"},
		{"session_turns", "title"},
		{"session_turns", "summary"},
		{"session_bodies", "request_attachments"},
		{"session_bodies", "response_attachments"},
	}

	// Indexes we expect — soft-checked, never fatal.
	//
	// Rationale: these are partial indexes (WHERE last_full_..._at IS NOT NULL).
	// CREATE INDEX CONCURRENTLY cannot run inside a transaction block, so the
	// up migration splits them out of the main BEGIN/COMMIT and uses
	// CONCURRENTLY. In some deployment contexts (fresh CI databases, debug
	// builds, dry-run applies) the CONCURRENTLY block may be skipped or
	// applied separately, leaving the index absent even though the columns
	// exist. Failing hard on a missing partial index would produce a false
	// positive and break CI for unrelated reasons. We therefore WARN only.
	//
	// Operators who want strict enforcement can grep the deployment logs for
	// "CREATE INDEX" or run `\\d gateway.sessions` in psql.
	indexes := []string{
		"idx_sessions_last_full_at",
		"idx_sessions_summary_at",
	}
	warnIndexes := 0

	failed := 0
	for _, c := range checks {
		var exists bool
		err := conn.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'gateway'
				  AND table_name = $1
				  AND column_name = $2
			)
		`, c.table, c.column).Scan(&exists)
		if err != nil {
			log.Printf("X gateway.%s.%s: query error: %v", c.table, c.column, err)
			failed++
			continue
		}
		if !exists {
			log.Printf("X MISSING column gateway.%s.%s", c.table, c.column)
			failed++
			continue
		}
		fmt.Printf("OK gateway.%s.%s\n", c.table, c.column)
	}

	for _, idx := range indexes {
		var exists bool
		err := conn.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_indexes
				WHERE schemaname = 'gateway'
				  AND tablename = 'sessions'
				  AND indexname = $1
			)
		`, idx).Scan(&exists)
		if err != nil {
			log.Printf("X index gateway.%s: query error: %v", idx, err)
			continue
		}
		if !exists {
			log.Printf("WARN partial-index gateway.%s absent (soft; non-fatal — see comment above)", idx)
			warnIndexes++
			continue
		}
		fmt.Printf("OK index gateway.%s\n", idx)
	}

	if warnIndexes > 0 {
		fmt.Printf("NOTE: %d expected partial index(es) missing — verify deployment applied CONCURRENTLY block\n", warnIndexes)
	}

	if failed > 0 {
		log.Fatalf("Migration 456 FAILED: %d missing columns", failed)
	}
	fmt.Println("Migration 456 verified successfully")
}
