package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Contract test (offline, no DB): the Go-side session projection must carry
// the same expression sequence as the migration file's `proj` block (734:
// details-joined canonical), and the Go DDL composer must mirror the
// migration's shape-conditional lateral composition plus the hasDetails
// fallback (no d.* references on 733-less databases). This is the cheap first
// line of defense; the full viewdef equivalence runs live in
// TestRequestLogsViewV2EnsureMatchesMigration.
func TestViewV2ProjectionContractSync(t *testing.T) {
	// Locate the migration file relative to the db package.
	sqlPath := filepath.Join("..", "sql", "migrations", "startup",
		"734_request_logs_view_details_join.sql")
	raw, err := os.ReadFile(sqlPath)
	if err != nil {
		t.Fatalf("read migration file: %v", err)
	}
	src := string(raw)

	projStart := strings.Index(src, "proj := $proj$")
	projEnd := strings.Index(src, "$proj$;")
	if projStart < 0 || projEnd < 0 || projEnd <= projStart {
		t.Fatal("migration file no longer carries the $proj$ block; update this contract test")
	}
	sqlProj := src[projStart+len("proj := $proj$") : projEnd]

	// Normalize both sides: strip line comments, collapse whitespace.
	normalize := func(s string) string {
		lineComment := regexp.MustCompile(`--[^\n]*`)
		spaceRun := regexp.MustCompile(`\s+`)
		return spaceRun.ReplaceAllString(lineComment.ReplaceAllString(s, " "), " ")
	}
	// The migration proj uses comma-prefix continuation; the Go const uses
	// comma-suffix. Split both into expressions on top-level commas after
	// normalization.
	splitExprs := func(s string) []string {
		var parts []string
		depth := 0
		cur := strings.Builder{}
		for _, r := range s {
			switch r {
			case '(':
				depth++
			case ')':
				depth--
			case ',':
				if depth == 0 {
					parts = append(parts, strings.TrimSpace(cur.String()))
					cur.Reset()
					continue
				}
			}
			cur.WriteRune(r)
		}
		if strings.TrimSpace(cur.String()) != "" {
			parts = append(parts, strings.TrimSpace(cur.String()))
		}
		return parts
	}

	goExprs := splitExprs(normalize(sessionFamilyProjection(true)))
	sqlExprs := splitExprs(normalize(sqlProj))
	if len(goExprs) == 0 || len(sqlExprs) == 0 {
		t.Fatalf("empty projection (go=%d sql=%d)", len(goExprs), len(sqlExprs))
	}
	if len(goExprs) != len(sqlExprs) {
		t.Fatalf("projection expression count drifted: go=%d sql=%d (frozen contract = 113)", len(goExprs), len(sqlExprs))
	}
	for i := range goExprs {
		if goExprs[i] != sqlExprs[i] {
			t.Fatalf("projection expr %d drifted:\n  go  = %s\n  sql = %s", i+1, goExprs[i], sqlExprs[i])
		}
	}
	if len(goExprs) != 113 {
		t.Fatalf("frozen contract: projection must carry 113 expressions, got %d", len(goExprs))
	}

	// 734 details overlay: the canonical projection must reference the details
	// layer on the 30 contract columns; the legacy fallback must reference
	// none (733-less databases keep the 710 NULL placeholders).
	canonical := sessionFamilyProjection(true)
	legacy := sessionFamilyProjection(false)
	if !strings.Contains(canonical, "d.client_model") ||
		!strings.Contains(canonical, "d.quality_flags") ||
		!strings.Contains(canonical, "d.request_class") {
		t.Error("canonical (734) projection must carry d.* details references")
	}
	if strings.Contains(legacy, "d.") {
		t.Error("legacy (710-fallback) projection must not reference the details alias")
	}

	// Shape-conditional lateral composition: frozen chain appends 4 lateral
	// columns, dynamic chain only 2 — both must total base+5 (frozen) /
	// base+3 (dynamic) = 113 columns on the same base count.
	if frozen := canonicalV2DDL(false, false, true); !strings.Contains(frozen, "source.system_fingerprint") ||
		!strings.Contains(frozen, "source.raw_model_name") {
		t.Error("frozen-chain v2 DDL must laterally append system_fingerprint and raw_model_name")
	}
	if dynamic := canonicalV2DDL(true, true, true); strings.Contains(dynamic, "source.system_fingerprint") ||
		strings.Contains(dynamic, "source.raw_model_name") {
		t.Error("dynamic-chain v2 DDL must not re-append columns the base wrapper already carries")
	}
	// hasDetails=false fallback: no details JOIN, no d.* references.
	if legacyDDL := canonicalV2DDL(false, false, false); strings.Contains(legacyDDL, "session_turn_details") {
		t.Error("hasDetails=false DDL must not join the details family")
	}
	if detailsDDL := canonicalV2DDL(false, false, true); !strings.Contains(detailsDDL, "LEFT JOIN public.session_turn_details_hot") ||
		!strings.Contains(detailsDDL, "LEFT JOIN public.session_turn_details ") {
		t.Error("hasDetails=true DDL must LEFT JOIN both details hot and parent")
	}

	// Name-order contract: the migration file's $names$ block must equal the
	// Go canonicalColumnOrderV2 (UNION ALL matches types positionally).
	namesStart := strings.Index(src, "names := $names$")
	namesEnd := strings.Index(src, "$names$;")
	if namesStart < 0 || namesEnd < 0 || namesEnd <= namesStart {
		t.Fatal("migration file no longer carries the $names$ block; update this contract test")
	}
	sqlNamesRaw := src[namesStart+len("names := $names$") : namesEnd]
	sqlNames := strings.Split(normalize(sqlNamesRaw), ",")
	for i := range sqlNames {
		sqlNames[i] = strings.TrimSpace(sqlNames[i])
	}
	if len(sqlNames) != len(canonicalColumnOrderV2) {
		t.Fatalf("name count drifted: go=%d sql=%d", len(canonicalColumnOrderV2), len(sqlNames))
	}
	for i := range sqlNames {
		if sqlNames[i] != canonicalColumnOrderV2[i] {
			t.Fatalf("column order drifted at position %d: go=%s sql=%s", i+1, canonicalColumnOrderV2[i], sqlNames[i])
		}
	}
}

// Live round-trip (LLM_GATEWAY_TEST_PG_DSN): on a scratch database holding
// the full 113-column dynamic-contract shape, the Go ensure and the 710
// migration file must produce byte-identical canonical view definitions, the
// anti-join must dedup dual-written request_ids, and the D4 'sys:%' NULL
// gw_session_id semantics must hold.
//
//	LLM_GATEWAY_TEST_PG_DSN='postgres://llm_gateway:…@127.0.0.1:5432/llm_gateway?sslmode=disable' \
//	  go test ./db/ -run TestRequestLogsViewV2EnsureMatchesMigration -count=1 -v
func TestRequestLogsViewV2EnsureMatchesMigration(t *testing.T) {
	dsn := os.Getenv("LLM_GATEWAY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("LLM_GATEWAY_TEST_PG_DSN not set — offline mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	scratchName := "llmgw_view_v2_contract_test"
	adminPoolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse admin pool config: %v", err)
	}
	adminPoolCfg.ConnConfig.Database = "postgres"
	adminPool, err := pgxpool.NewWithConfig(ctx, adminPoolCfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	if _, err := adminPool.Exec(ctx, `DROP DATABASE IF EXISTS `+scratchName); err != nil {
		t.Fatalf("drop stale scratch db: %v", err)
	}
	if _, err := adminPool.Exec(ctx, `CREATE DATABASE `+scratchName); err != nil {
		t.Fatalf("create scratch db: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		adminPool.Exec(dropCtx, `DROP DATABASE IF EXISTS `+scratchName) //nolint:errcheck
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

	// Clone the REAL frozen contract: the live canonical view's column list
	// minus the five appended names IS the frozen 108-column base contract;
	// both request_logs tables keep exactly those shared columns (plus the
	// appended five and hot-only extras), so the scratch is a faithful
	// frozen-chain shape — the shape production (local + 252) runs today.
	frozen, err := frozenContractColumnList(ctx, pool, dsn)
	if err != nil {
		t.Fatalf("derive frozen contract columns: %v", err)
	}
	ddl, err := cloneTablesFrozenDDL(ctx, dsn, frozen)
	if err != nil {
		t.Fatalf("derive clone DDL from live catalogs: %v", err)
	}
	if _, err := pool.Exec(ctx, ddl+`
		CREATE TABLE public.schema_migrations (version text PRIMARY KEY, description text, applied_at timestamptz DEFAULT now());

		-- Pre-create the frozen-shape wrapper chain (no fp/raw in the base —
		-- the ensure must reuse it and compose the 4-lateral v1 branch).
		CREATE VIEW public.request_logs_with_current_month_without_customer_id AS
		SELECT `+strings.Join(frozen, ", ")+` FROM public.request_logs_hot
		UNION ALL
		SELECT `+strings.Join(frozen, ", ")+` FROM public.request_logs;
		CREATE VIEW public.request_logs_with_current_month_without_request_class_due_at AS
		SELECT v.*, m.customer_id
		FROM public.request_logs_with_current_month_without_customer_id v
		LEFT JOIN LATERAL (
			SELECT customer_id FROM public.request_logs_hot h
			WHERE h.request_id = v.request_id AND h.ts = v.ts
			UNION ALL
			SELECT customer_id FROM public.request_logs p
			WHERE p.request_id = v.request_id AND p.ts = v.ts
			LIMIT 1
		) m ON true;
	`); err != nil {
		t.Fatalf("seed scratch schema: %v", err)
	}

	db := &DB{pool: pool}
	var dbName string
	_ = pool.QueryRow(ctx, `SELECT current_database()`).Scan(&dbName)
	t.Logf("scratch db = %q", dbName)

	// 733 must precede the ensure so the details family exists and the
	// ensure composes the 734 (details-joined) shape.
	migration733, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup",
		"733_session_turn_details.sql"))
	if err != nil {
		t.Fatalf("read migration 733: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration733)); err != nil {
		t.Fatalf("apply 733 on scratch: %v", err)
	}

	if err := db.ensureRequestLogsCurrentMonthView(ctx); err != nil {
		t.Fatalf("ensure (v2 cold build): %v", err)
	}
	var views int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM pg_views WHERE schemaname='public' AND viewname LIKE 'request_logs%'`).Scan(&views)
	t.Logf("request_logs%% views after ensure = %d", views)
	ensureViewdef := viewDefinition(t, ctx, pool)
	if !strings.Contains(ensureViewdef, "session_turns") {
		t.Fatal("ensure produced a non-v2 body on a session-family database")
	}
	if !strings.Contains(ensureViewdef, "session_turn_details") {
		t.Fatal("ensure must produce the 734 details-joined body when 733 tables exist")
	}
	// Idempotency: second pass must not change the definition.
	if err := db.ensureRequestLogsCurrentMonthView(ctx); err != nil {
		t.Fatalf("ensure (idempotent second pass): %v", err)
	}
	if again := viewDefinition(t, ctx, pool); again != ensureViewdef {
		t.Fatal("second ensure pass changed the view definition")
	}

	// Migration equivalence: drop the canonical, replay the 710 file then the
	// 734 file (production migration order), and require the exact same view
	// definition as the ensure.
	if _, err := pool.Exec(ctx, `DROP VIEW public.request_logs_with_current_month`); err != nil {
		t.Fatalf("drop canonical before migration replay: %v", err)
	}
	migrationSQL, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup",
		"710_request_logs_view_session_family_v2.sql"))
	if err != nil {
		t.Fatalf("read migration file: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migrationSQL)); err != nil {
		t.Fatalf("apply 710 migration on scratch: %v", err)
	}
	migration734, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup",
		"734_request_logs_view_details_join.sql"))
	if err != nil {
		t.Fatalf("read migration 734: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration734)); err != nil {
		t.Fatalf("apply 734 migration on scratch: %v", err)
	}
	migrationViewdef := viewDefinition(t, ctx, pool)
	if migrationViewdef != ensureViewdef {
		t.Fatalf("ensure and migrations 710+734 produce different view definitions:\n--- ensure ---\n%s\n--- migration ---\n%s",
			ensureViewdef, migrationViewdef)
	}

	// Data semantics: dedup + D4 NULL passthrough + details overlay.
	seed := `
		INSERT INTO public.request_logs_hot (request_id, gw_session_id, prompt_tokens, completion_tokens, customer_id, request_class, raw_model_name, client_model, quality_flags)
		VALUES ('req-v1-only', 'sess-legacy', 10, 5, 1, 'immediate', 'm-alpha', 'cli-alpha', '{}')
		     , ('req-dual', 'sess-dual', 20, 8, 2, 'immediate', 'm-beta', 'cli-beta', '{}');
		INSERT INTO public.request_logs (request_id, gw_session_id, prompt_tokens, completion_tokens, customer_id, request_class, raw_model_name)
		VALUES ('req-parent-only', NULL, 1, 2, 3, 'immediate', 'm-gamma');
		INSERT INTO public.session_turns_hot (session_id, tenant_id, request_id, ts, turn_no, model, success, status_code, credits_charged, partition_date)
		VALUES ('sess-dual', 'default', 'req-dual', now(), 1, 'm-beta-live', true, 200, 7, CURRENT_DATE)
		     , ('sys:probe:cred9:20260914', 'default', 'req-synthetic', now(), 1, 'm-probe', true, 200, 0, CURRENT_DATE);
		INSERT INTO public.session_turns (session_id, tenant_id, request_id, ts, turn_no, model, success, status_code, credits_charged, partition_date)
		VALUES ('sess-live', 'default', 'req-turn-parent', now(), 1, 'm-live', true, 200, 3, CURRENT_DATE);
		-- 733 特征层：req-dual 的 hot 行带特征；req-turn-parent 走 parent 分支
		INSERT INTO public.session_turn_details_hot (session_id, tenant_id, request_id, turn_no, ts, partition_date, client_model, quality_flags, request_class)
		VALUES ('sess-dual', 'default', 'req-dual', 1, now(), CURRENT_DATE, 'cli-beta-client', '{empty_tool_name}', 'immediate');
		INSERT INTO public.session_turn_details (session_id, tenant_id, request_id, turn_no, ts, partition_date, client_model, request_class)
		VALUES ('sess-live', 'default', 'req-turn-parent', 1, now(), CURRENT_DATE, 'cli-live-client', 'scheduled');
	`
	if _, err := pool.Exec(ctx, seed); err != nil {
		t.Fatalf("seed scratch rows: %v", err)
	}

	var dualCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM public.request_logs_with_current_month WHERE request_id = 'req-dual'
	`).Scan(&dualCount); err != nil {
		t.Fatalf("dedup probe failed: %v", err)
	}
	if dualCount != 1 {
		t.Fatalf("anti-join dedup: request_id=req-dual appears %d times, want exactly 1", dualCount)
	}

	var syntheticGW *string
	if err := pool.QueryRow(ctx, `
		SELECT gw_session_id FROM public.request_logs_with_current_month WHERE request_id = 'req-synthetic'
	`).Scan(&syntheticGW); err != nil {
		t.Fatalf("synthetic row probe failed: %v", err)
	}
	if syntheticGW != nil {
		t.Fatalf("D4 synthetic session must surface as NULL gw_session_id on the compat view, got %q", *syntheticGW)
	}

	// Details overlay: hot-branch join surfaces client_model/quality_flags;
	// parent-branch join surfaces request_class; a turn without a details row
	// still yields NULL (LEFT semantics, 710-compatible).
	var dualClientModel string
	var dualQualityFlags []string
	if err := pool.QueryRow(ctx, `
		SELECT client_model::text, quality_flags FROM public.request_logs_with_current_month
		WHERE request_id = 'req-dual'
	`).Scan(&dualClientModel, &dualQualityFlags); err != nil {
		t.Fatalf("details hot overlay probe failed: %v", err)
	}
	if dualClientModel != "cli-beta-client" {
		t.Fatalf("details hot overlay: client_model = %q, want %q", dualClientModel, "cli-beta-client")
	}
	if len(dualQualityFlags) != 1 || dualQualityFlags[0] != "empty_tool_name" {
		t.Fatalf("details hot overlay: quality_flags = %v, want [empty_tool_name]", dualQualityFlags)
	}
	var liveClass string
	if err := pool.QueryRow(ctx, `
		SELECT request_class FROM public.request_logs_with_current_month WHERE request_id = 'req-turn-parent'
	`).Scan(&liveClass); err != nil {
		t.Fatalf("details parent overlay probe failed: %v", err)
	}
	if liveClass != "scheduled" {
		t.Fatalf("details parent overlay: request_class = %q, want %q", liveClass, "scheduled")
	}
	var syntheticClientModel *string
	if err := pool.QueryRow(ctx, `
		SELECT client_model FROM public.request_logs_with_current_month WHERE request_id = 'req-synthetic'
	`).Scan(&syntheticClientModel); err != nil {
		t.Fatalf("missing-details probe failed: %v", err)
	}
	if syntheticClientModel != nil {
		t.Fatalf("turn without details row must surface NULL client_model, got %q", *syntheticClientModel)
	}

	var rows int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM public.request_logs_with_current_month
		WHERE request_id IN ('req-v1-only','req-parent-only','req-turn-parent','req-dual','req-synthetic')
	`).Scan(&rows); err != nil {
		t.Fatalf("coverage probe failed: %v", err)
	}
	if rows != 5 {
		t.Fatalf("view coverage = %d rows, want 5 (both branches visible)", rows)
	}

	// Down chain in reverse numeric order (733 header contract: 734 down
	// runs first, then 733 down). 734 down must rebuild the 710 body — its
	// "already v2" probe must NOT mistake the 734 details-joined body for the
	// 710 shape, or the family drop below hits 2BP01 (view dependency).
	down734, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup",
		"734_request_logs_view_details_join.down.sql"))
	if err != nil {
		t.Fatalf("read 734 down: %v", err)
	}
	if _, err := pool.Exec(ctx, string(down734)); err != nil {
		t.Fatalf("apply 734 down: %v", err)
	}
	postDown732 := viewDefinition(t, ctx, pool)
	if !strings.Contains(postDown732, "session_turns") || strings.Contains(postDown732, "session_turn_details") {
		t.Fatal("734 down must restore the 710 body (v2, no details join)")
	}
	// 733 down then drops the details family cleanly (no view dependency left).
	down733, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup",
		"733_session_turn_details.down.sql"))
	if err != nil {
		t.Fatalf("read 733 down: %v", err)
	}
	if _, err := pool.Exec(ctx, string(down733)); err != nil {
		t.Fatalf("apply 733 down: %v", err)
	}
	// 710 down then restores a v1 (request_logs-only) body.
	downSQL, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup",
		"710_request_logs_view_session_family_v2.down.sql"))
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(downSQL)); err != nil {
		t.Fatalf("apply 710 down: %v", err)
	}
	if v1Viewdef := viewDefinition(t, ctx, pool); strings.Contains(v1Viewdef, "session_turns") {
		t.Fatal("710 down must restore a v1 (non-session) view body")
	}
}

func viewDefinition(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var def string
	if err := pool.QueryRow(ctx,
		`SELECT pg_get_viewdef('public.request_logs_with_current_month'::regclass, true)`,
	).Scan(&def); err != nil {
		t.Fatalf("read view definition: %v", err)
	}
	return def
}

// frozenContractColumnList returns the frozen base contract: the live
// canonical view's 113 columns minus the five appended names
// (customer_id/request_class/due_at/system_fingerprint/raw_model_name) = the
// 108 columns the pre-485 base wrapper carries, in canonical order.
func frozenContractColumnList(ctx context.Context, scratch *pgxpool.Pool, liveDSN string) ([]string, error) {
	liveCfg, err := pgxpool.ParseConfig(liveDSN)
	if err != nil {
		return nil, fmt.Errorf("parse live DSN: %w", err)
	}
	live, err := pgxpool.NewWithConfig(ctx, liveCfg)
	if err != nil {
		return nil, fmt.Errorf("connect live catalogs: %w", err)
	}
	defer live.Close()

	appended := map[string]bool{
		"customer_id": true, "request_class": true, "due_at": true,
		"system_fingerprint": true, "raw_model_name": true,
	}
	rows, err := live.Query(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'request_logs_with_current_month'
		ORDER BY ordinal_position`)
	if err != nil {
		return nil, fmt.Errorf("canonical column query: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if !appended[name] {
			names = append(names, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(names) != 108 {
		return nil, fmt.Errorf("frozen base contract = %d columns, want 108", len(names))
	}
	return names, nil
}

// cloneTablesFrozenDDL derives CREATE TABLE statements for the request_logs
// pair restricted to the frozen contract (the shared post-485 columns —
// among them five hot/parent type-conflicting ones — are dropped from BOTH
// sides), plus the session_turns pair cloned in full. The result is a
// type-consistent frozen-chain shape matching production.
func cloneTablesFrozenDDL(ctx context.Context, liveDSN string, frozen []string) (string, error) {
	liveCfg, err := pgxpool.ParseConfig(liveDSN)
	if err != nil {
		return "", fmt.Errorf("parse live DSN: %w", err)
	}
	live, err := pgxpool.NewWithConfig(ctx, liveCfg)
	if err != nil {
		return "", fmt.Errorf("connect live catalogs: %w", err)
	}
	defer live.Close()

	keep := map[string]bool{}
	for _, name := range frozen {
		keep[name] = true
	}
	for _, name := range []string{"customer_id", "request_class", "due_at", "system_fingerprint", "raw_model_name"} {
		keep[name] = true
	}

	var b strings.Builder
	for _, table := range []string{"request_logs", "request_logs_hot"} {
		cols, err := liveColumns(ctx, live, table)
		if err != nil {
			return "", err
		}
		var defs []string
		for _, col := range cols {
			if keep[col.name] {
				defs = append(defs, col.name+" "+col.typ)
			}
		}
		fmt.Fprintf(&b, "CREATE TABLE public.%s (\n%s\n);\n", table, strings.Join(defs, ",\n"))
	}
	for _, table := range []string{"session_turns", "session_turns_hot"} {
		cols, err := liveColumns(ctx, live, table)
		if err != nil {
			return "", err
		}
		defs := make([]string, 0, len(cols))
		for _, col := range cols {
			defs = append(defs, col.name+" "+col.typ)
		}
		fmt.Fprintf(&b, "CREATE TABLE public.%s (\n%s\n);\n", table, strings.Join(defs, ",\n"))
	}
	return b.String(), nil
}

type liveColumn struct {
	name string
	typ  string
}

func liveColumns(ctx context.Context, live *pgxpool.Pool, table string) ([]liveColumn, error) {
	rows, err := live.Query(ctx, `
		SELECT a.attname, format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relname = $1
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, table)
	if err != nil {
		return nil, fmt.Errorf("catalog query for %s: %w", table, err)
	}
	defer rows.Close()
	var cols []liveColumn
	for rows.Next() {
		var col liveColumn
		if err := rows.Scan(&col.name, &col.typ); err != nil {
			return nil, err
		}
		cols = append(cols, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("table public.%s not found in live catalogs", table)
	}
	return cols, nil
}
