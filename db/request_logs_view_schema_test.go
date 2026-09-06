package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Contract test: db.go must wire ensureRequestLogsCurrentMonthView into the
// startup migration chain, and the ensure must pin the 577+610 wrapper-chain
// column contract (base UNION + customer_id + request_class/due_at) — the
// shape whose loss broke every /api/logs request in the 2026-09-07 42P01
// incident.
func TestApplyMigrationsIncludesRequestLogsViewEnsure(t *testing.T) {
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "ensureRequestLogsCurrentMonthView(migCtx)") {
		t.Error("db.go must call ensureRequestLogsCurrentMonthView in applyMigrationsOnce")
	}
	ensureSrc, err := os.ReadFile("request_logs_view_schema.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(ensureSrc)
	for _, want := range []string{
		"request_logs_with_current_month",
		"request_logs_with_current_month_without_request_class_due_at",
		"request_logs_with_current_month_without_customer_id",
		"public.request_logs_hot",
		"public.request_logs",
		"customer_id",
		"request_class",
		"due_at",
		"NOT IN ('customer_id', 'request_class', 'due_at')",
		"LEFT JOIN LATERAL",
		// The fast path must short-circuit on a healthy view so a no-op boot
		// never takes DDL locks.
		"canonicalExists",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("request_logs_view_schema.go missing contract %q", want)
		}
	}
}

// Live round-trip: rebuild the whole wrapper chain from scratch hot/parent
// tables inside a dedicated scratch database, then verify the canonical view
// matches the production column contract (108 base + customer_id +
// request_class + due_at = 111) and that a HOT_ONLY hot column never breaks
// the UNION.
//
// Gated on LLM_GATEWAY_TEST_PG_DSN so CI stays offline-green; run locally:
//
//	LLM_GATEWAY_TEST_PG_DSN='postgres://llm_gateway:…@127.0.0.1:5432/llm_gateway?sslmode=disable' \
//	  go test ./db/ -run TestRequestLogsCurrentMonthViewEnsureRoundTrip -count=1 -v
func TestRequestLogsCurrentMonthViewEnsureRoundTrip(t *testing.T) {
	dsn := os.Getenv("LLM_GATEWAY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("LLM_GATEWAY_TEST_PG_DSN not set — offline mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Scratch database on the same server: the ensure hardcodes the public
	// schema, so a throwaway database (not schema) is the only way to test
	// the full chain rebuild without touching the shared instance's views.
	scratchName := "llmgw_view_ensure_test"

	// NOTE: pgx.ConnConfig.ConnString() does not reflect a mutated Database
	// field (it re-serializes the original conn string), so re-parsing a
	// serialized string would silently reconnect to the original database.
	// Parse into pool configs once and override the embedded ConnConfig.
	adminPoolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse admin pool config: %v", err)
	}
	adminPoolCfg.ConnConfig.Database = "postgres"
	adminPool, err := pgxpool.NewWithConfig(ctx, adminPoolCfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	if _, err := adminPool.Exec(ctx,
		`DROP DATABASE IF EXISTS `+scratchName); err != nil {
		t.Fatalf("drop stale scratch db: %v", err)
	}
	if _, err := adminPool.Exec(ctx,
		`CREATE DATABASE `+scratchName); err != nil {
		t.Fatalf("create scratch db: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		adminPool.Exec(dropCtx, `DROP DATABASE IF EXISTS `+scratchName)
		adminPool.Close()
	})

	scratchPoolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse scratch pool config: %v", err)
	}
	scratchPoolCfg.ConnConfig.Database = scratchName
	pool, err := pgxpool.NewWithConfig(ctx, scratchPoolCfg)
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	defer pool.Close()

	// Minimal hot/parent pair: a shared base column set, the three later-
	// appended columns (customer_id/request_class/due_at) on both sides, and
	// a HOT_ONLY column that exists only on hot — the exact shape that made
	// the 341-style SELECT * UNION replay fail and drop the view.
	schema := `
		CREATE TABLE public.request_logs (
			id bigserial PRIMARY KEY,
			request_id text NOT NULL,
			ts timestamptz NOT NULL DEFAULT now(),
			tenant_id text NOT NULL DEFAULT '',
			prompt_tokens bigint NOT NULL DEFAULT 0,
			customer_id bigint,
			request_class text NOT NULL DEFAULT 'immediate',
			due_at timestamptz,
			CONSTRAINT request_logs_request_class_due_at_check
				CHECK ((request_class = 'immediate' AND due_at IS NULL)
					OR (request_class = 'scheduled' AND due_at IS NOT NULL))
		);
		CREATE TABLE public.request_logs_hot (
			id bigserial PRIMARY KEY,
			request_id text NOT NULL,
			ts timestamptz NOT NULL DEFAULT now(),
			tenant_id text NOT NULL DEFAULT '',
			prompt_tokens bigint NOT NULL DEFAULT 0,
			caller_id text, -- HOT_ONLY: must never leak into the UNION
			customer_id bigint,
			request_class text NOT NULL DEFAULT 'immediate',
			due_at timestamptz,
			CONSTRAINT request_logs_hot_request_class_due_at_check
				CHECK ((request_class = 'immediate' AND due_at IS NULL)
					OR (request_class = 'scheduled' AND due_at IS NOT NULL))
		);
		INSERT INTO public.request_logs_hot (request_id, ts, caller_id, customer_id, request_class, due_at)
		VALUES ('req-hot', now(), 'hot-only', 42, 'scheduled', now() + interval '1 hour');
		INSERT INTO public.request_logs (request_id, ts, customer_id, request_class, due_at)
		VALUES ('req-parent', now(), 7, 'immediate', NULL);
	`
	if _, err := pool.Exec(ctx, schema); err != nil {
		t.Fatalf("seed scratch schema: %v", err)
	}

	db := &DB{pool: pool}
	if err := db.ensureRequestLogsCurrentMonthView(ctx); err != nil {
		t.Fatalf("ensure (cold chain rebuild): %v", err)
	}
	// Idempotency: a second pass on a healthy chain must be a no-op.
	if err := db.ensureRequestLogsCurrentMonthView(ctx); err != nil {
		t.Fatalf("ensure (idempotent second pass): %v", err)
	}

	var colCount, baseCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'request_logs_with_current_month'
	`).Scan(&colCount); err != nil {
		t.Fatalf("count view columns: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(DISTINCT h.attname) FROM pg_attribute h
		JOIN pg_attribute p ON p.attrelid = 'public.request_logs'::regclass AND p.attname = h.attname
		WHERE h.attrelid = 'public.request_logs_hot'::regclass
		  AND h.attnum > 0 AND NOT h.attisdropped
		  AND p.attnum > 0 AND NOT p.attisdropped
		  AND h.attname NOT IN ('customer_id', 'request_class', 'due_at')
	`).Scan(&baseCount); err != nil {
		t.Fatalf("count base columns: %v", err)
	}
	if colCount != baseCount+3 { // base + customer_id + request_class + due_at
		t.Fatalf("canonical view column count = %d, want %d (base %d + customer_id + request_class + due_at)",
			colCount, baseCount+3, baseCount)
	}

	// HOT_ONLY column must be absent from the view; appended columns present.
	var hotOnly, hasClass, hasDueAt, hasCustomer bool
	if err := pool.QueryRow(ctx, `
		SELECT
			bool_or(column_name = 'caller_id'),
			bool_or(column_name = 'request_class'),
			bool_or(column_name = 'due_at'),
			bool_or(column_name = 'customer_id')
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'request_logs_with_current_month'
	`).Scan(&hotOnly, &hasClass, &hasDueAt, &hasCustomer); err != nil {
		t.Fatalf("probe view column contract: %v", err)
	}
	if hotOnly {
		t.Error("HOT_ONLY column caller_id leaked into the canonical view")
	}
	if !hasClass || !hasDueAt || !hasCustomer {
		t.Errorf("appended columns missing (request_class=%v due_at=%v customer_id=%v)",
			hasClass, hasDueAt, hasCustomer)
	}

	// Data round-trip through the view from both sides of the UNION.
	var hotCustomer, parentCustomer *int64
	var hotClass string
	if err := pool.QueryRow(ctx, `
		SELECT customer_id, request_class FROM request_logs_with_current_month
		WHERE request_id = 'req-hot'
	`).Scan(&hotCustomer, &hotClass); err != nil {
		t.Fatalf("read hot row via view: %v", err)
	}
	if hotCustomer == nil || *hotCustomer != 42 || hotClass != "scheduled" {
		t.Errorf("hot row via view = (%v, %q), want (42, scheduled)", hotCustomer, hotClass)
	}
	if err := pool.QueryRow(ctx, `
		SELECT customer_id FROM request_logs_with_current_month
		WHERE request_id = 'req-parent'
	`).Scan(&parentCustomer); err != nil {
		t.Fatalf("read parent row via view: %v", err)
	}
	if parentCustomer == nil || *parentCustomer != 7 {
		t.Errorf("parent row via view customer_id = %v, want 7", parentCustomer)
	}
}
