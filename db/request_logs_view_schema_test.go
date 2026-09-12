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
		// Migration 696 contract: system_fingerprint rides the canonical
		// lateral stage when the base wrapper's frozen intersection lacks it;
		// the shape probe keeps both wrapper generations idempotent.
		"baseHasFingerprint",
		// Migration 699 contract: raw_model_name rides the same conditional
		// lateral stage (the frozen base intersection predates 485, so the
		// drift scanner's SELECT hit 42703 on every cycle until 699), and the
		// lateral itself is trimmed to columns the hot table actually has so
		// self-heal never regresses on tables lagging 485/603.
		"baseHasRawModelName",
		"hotHasFingerprint",
		"hotHasRawModelName",
		"source.raw_model_name",
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
// matches the production column contract (intersection base + customer_id +
// request_class + due_at, with system_fingerprint/raw_model_name either
// inherited via the dynamic base intersection or appended by the 696/699
// conditional laterals on frozen chains) and that a HOT_ONLY hot column never
// breaks the UNION.
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

	// Minimal hot/parent pair: a shared base column set, the later-appended
	// columns (customer_id/request_class/due_at/system_fingerprint(487/603)/
	// raw_model_name(485/603)) on both sides, and a HOT_ONLY column that
	// exists only on hot — the exact shape that made the 341-style SELECT *
	// UNION replay fail and drop the view. The fingerprint/model-name columns
	// must exist on the tables or the 696/699 lateral shape probes have
	// nothing real to probe (this schema predated 696 and made the live
	// round-trip fail with 42703 on h.system_fingerprint).
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
			system_fingerprint text,
			raw_model_name text,
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
			system_fingerprint text,
			raw_model_name text,
			CONSTRAINT request_logs_hot_request_class_due_at_check
				CHECK ((request_class = 'immediate' AND due_at IS NULL)
					OR (request_class = 'scheduled' AND due_at IS NOT NULL))
		);
		INSERT INTO public.request_logs_hot (request_id, ts, caller_id, customer_id, request_class, due_at, system_fingerprint, raw_model_name)
		VALUES ('req-hot', now(), 'hot-only', 42, 'scheduled', now() + interval '1 hour', 'fp-hot-a', 'model-alpha');
		INSERT INTO public.request_logs (request_id, ts, customer_id, request_class, due_at, system_fingerprint, raw_model_name)
		VALUES ('req-parent', now(), 7, 'immediate', NULL, 'fp-parent-b', 'model-beta');
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
	// Dynamic-rebuild shape (b): the base intersection already carries
	// system_fingerprint/raw_model_name (both tables have them), so the
	// canonical only appends customer_id + request_class + due_at via the
	// 577/610 stages — the fingerprint/model-name columns ride v.*.
	if colCount != baseCount+3 { // base(incl. fp+raw) + customer_id + request_class + due_at
		t.Fatalf("canonical view column count = %d, want %d (base %d + customer_id + request_class + due_at)",
			colCount, baseCount+3, baseCount)
	}

	// HOT_ONLY column must be absent from the view; appended columns present.
	var hotOnly, hasClass, hasDueAt, hasCustomer, hasFingerprint, hasRawModelName bool
	if err := pool.QueryRow(ctx, `
		SELECT
			bool_or(column_name = 'caller_id'),
			bool_or(column_name = 'request_class'),
			bool_or(column_name = 'due_at'),
			bool_or(column_name = 'customer_id'),
			bool_or(column_name = 'system_fingerprint'),
			bool_or(column_name = 'raw_model_name')
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'request_logs_with_current_month'
	`).Scan(&hotOnly, &hasClass, &hasDueAt, &hasCustomer, &hasFingerprint, &hasRawModelName); err != nil {
		t.Fatalf("probe view column contract: %v", err)
	}
	if hotOnly {
		t.Error("HOT_ONLY column caller_id leaked into the canonical view")
	}
	if !hasClass || !hasDueAt || !hasCustomer || !hasFingerprint || !hasRawModelName {
		t.Errorf("appended columns missing (request_class=%v due_at=%v customer_id=%v system_fingerprint=%v raw_model_name=%v)",
			hasClass, hasDueAt, hasCustomer, hasFingerprint, hasRawModelName)
	}

	// Data round-trip through the view from both sides of the UNION.
	var hotCustomer, parentCustomer *int64
	var hotClass, hotRaw, hotFP, parentRaw string
	if err := pool.QueryRow(ctx, `
		SELECT customer_id, request_class, raw_model_name, system_fingerprint FROM request_logs_with_current_month
		WHERE request_id = 'req-hot'
	`).Scan(&hotCustomer, &hotClass, &hotRaw, &hotFP); err != nil {
		t.Fatalf("read hot row via view: %v", err)
	}
	if hotCustomer == nil || *hotCustomer != 42 || hotClass != "scheduled" || hotRaw != "model-alpha" || hotFP != "fp-hot-a" {
		t.Errorf("hot row via view = (%v, %q, %q, %q), want (42, scheduled, model-alpha, fp-hot-a)", hotCustomer, hotClass, hotRaw, hotFP)
	}
	if err := pool.QueryRow(ctx, `
		SELECT customer_id, raw_model_name FROM request_logs_with_current_month
		WHERE request_id = 'req-parent'
	`).Scan(&parentCustomer, &parentRaw); err != nil {
		t.Fatalf("read parent row via view: %v", err)
	}
	if parentCustomer == nil || *parentCustomer != 7 || parentRaw != "model-beta" {
		t.Errorf("parent row via view = (%v, %q), want (7, model-beta)", parentCustomer, parentRaw)
	}

	// Frozen-chain replay (migration 699's reason to exist): wrappers created
	// before 485/603 — the base intersection carries neither
	// system_fingerprint nor raw_model_name — plus the canonical dropped
	// out-of-band (680-incident style). The ensure must reuse the stale
	// wrappers and append fingerprint + model name on the conditional lateral
	// instead of crashing or silently omitting them. This is the exact shape
	// found on the local deploy DB and the shared 252 production PG, where
	// the drift scanner's SELECT hit 42703 on every cycle.
	if _, err := pool.Exec(ctx, `
		DROP VIEW public.request_logs_with_current_month;
		DROP VIEW public.request_logs_with_current_month_without_request_class_due_at;
		DROP VIEW public.request_logs_with_current_month_without_customer_id;
	`); err != nil {
		t.Fatalf("drop dynamic chain: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE VIEW public.request_logs_with_current_month_without_customer_id AS
		SELECT id, request_id, ts, tenant_id, prompt_tokens FROM public.request_logs_hot
		UNION ALL
		SELECT id, request_id, ts, tenant_id, prompt_tokens FROM public.request_logs
	`); err != nil {
		t.Fatalf("create stale pre-485 base wrapper: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE VIEW public.request_logs_with_current_month_without_request_class_due_at AS
		SELECT v.*, m.customer_id
		FROM public.request_logs_with_current_month_without_customer_id v
		LEFT JOIN LATERAL (
			SELECT customer_id FROM public.request_logs_hot
			WHERE request_id = v.request_id AND ts = v.ts
			UNION ALL
			SELECT customer_id FROM public.request_logs
			WHERE request_id = v.request_id AND ts = v.ts
			LIMIT 1
		) m ON true
	`); err != nil {
		t.Fatalf("create stale 577 wrapper: %v", err)
	}
	if err := db.ensureRequestLogsCurrentMonthView(ctx); err != nil {
		t.Fatalf("ensure (frozen-chain rebuild): %v", err)
	}
	// Idempotency: a second pass on the rebuilt chain must be a no-op.
	if err := db.ensureRequestLogsCurrentMonthView(ctx); err != nil {
		t.Fatalf("ensure (frozen-chain idempotent second pass): %v", err)
	}

	// 5 stale base + customer_id + request_class + due_at + system_fingerprint (696) + raw_model_name (699).
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'request_logs_with_current_month'
	`).Scan(&colCount); err != nil {
		t.Fatalf("count frozen-rebuild view columns: %v", err)
	}
	if colCount != 10 {
		t.Fatalf("frozen-rebuild canonical column count = %d, want 10 (5 stale base + customer_id + request_class + due_at + system_fingerprint + raw_model_name)", colCount)
	}
	// The scanner's exact projection must now parse and return data.
	var scanFP, scanRaw string
	if err := pool.QueryRow(ctx, `
		SELECT raw_model_name, system_fingerprint FROM request_logs_with_current_month
		WHERE request_id = 'req-hot'
	`).Scan(&scanRaw, &scanFP); err != nil {
		t.Fatalf("scanner-shape SELECT on frozen rebuild: %v", err)
	}
	if scanRaw != "model-alpha" || scanFP != "fp-hot-a" {
		t.Errorf("scanner-shape SELECT = (%q, %q), want (model-alpha, fp-hot-a)", scanRaw, scanFP)
	}
}
