//go:build integration

// migration_717_hot_alignment_integration_test.go — behavioral fixture for
// migration 717's R37 ::text-cast hardening. The R36 baseline hand-alignment
// means a FRESH INSTALL loads request_logs_hot already in the mother types
// BEFORE 717 runs, while pre-717 production installs carry the drifted text
// shape. 717 must survive both column states (the drift shape was never
// executed against an aligned baseline before — flagged by e0f94a799's
// commit message as "customer_id cast bug (~ on bigint)").
//
// Run:
//
//	go test -tags integration ./sql/migrations/startup -run TestMigration717HotColumnAlignment -count=1
//
// All DDL runs inside one transaction that is rolled back, so TEST_PG_URL
// instances are left clean.
package startup

import (
	"context"
	"testing"
	"time"
)

func TestMigration717HotColumnAlignmentBothColumnStates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn, cleanup := partitionBehaviorContainer(t, ctx)
	defer cleanup()

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	exec := func(sql string) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatalf("exec failed: %v\nSQL: %s", err, sql)
		}
	}
	migrationSQL := stripTxControl(t, "717_request_logs_hot_column_alignment.sql")

	// ── scenario 1: drifted (pre-717 production shape, all text) ────────
	exec(`CREATE TABLE public.request_logs_hot (
		agent_name text,
		agent_type text,
		api_key_fingerprint text,
		task_id text,
		customer_id text,
		content_safety_score text,
		dlp_violations text,
		protocol_conversion text,
		ir_extensions text,
		sanitizer_mutations text
	)`)
	exec(`INSERT INTO public.request_logs_hot VALUES (
		'agent', 'coder', 'fp', 't1',
		NULL, '{"score":1}', '[{"v":1}]', 'true', '{"k":1}', '[]'
	), (
		'agent', 'coder', 'fp', 't2',
		'42', 'plain', 'plain', '0', 'not-json', '   '
	), (
		'agent', 'coder', 'fp', 't3',
		'12345678901234567890', NULL, NULL, NULL, NULL, NULL
	)`)
	exec(migrationSQL)

	type col struct{ name, dataType string }
	var got []col
	rows, err := tx.Query(ctx, `
		SELECT column_name, data_type FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
		  AND column_name IN ('customer_id','protocol_conversion','ir_extensions','sanitizer_mutations','agent_name','api_key_fingerprint')
		ORDER BY column_name`)
	if err != nil {
		t.Fatalf("inspect drifted: %v", err)
	}
	for rows.Next() {
		var c col
		if err := rows.Scan(&c.name, &c.dataType); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, c)
	}
	rows.Close()
	want := map[string]string{
		"agent_name": "character varying", "api_key_fingerprint": "character varying",
		"customer_id": "bigint", "protocol_conversion": "boolean",
		"ir_extensions": "jsonb", "sanitizer_mutations": "jsonb",
	}
	for _, c := range got {
		if want[c.name] != c.dataType {
			t.Errorf("drifted: column %s = %s, want %s", c.name, c.dataType, want[c.name])
		}
	}

	queryText := func(sql string) string {
		t.Helper()
		var s string
		if err := tx.QueryRow(ctx, sql).Scan(&s); err != nil {
			t.Fatalf("query %q: %v", sql, err)
		}
		return s
	}
	// '42' → 42; NULL → NULL; 20-digit overflow debris → NULL (not an abort).
	// ORDER BY task_id makes the aggregate deterministic: t1 NULL, t2 '42',
	// t3 overflow.
	if v := queryText(`SELECT string_agg(COALESCE(customer_id::text,'<null>'), ',' ORDER BY task_id) FROM public.request_logs_hot`); v != "<null>,42,<null>" {
		t.Errorf("drifted customer_id mapping unexpected: %q", v)
	}
	if v := queryText(`SELECT protocol_conversion::text FROM public.request_logs_hot WHERE task_id='t1'`); v != "true" {
		t.Errorf("drifted protocol_conversion 'true' = %q", v)
	}
	if v := queryText(`SELECT protocol_conversion::text FROM public.request_logs_hot WHERE task_id='t2'`); v != "false" {
		t.Errorf("drifted protocol_conversion '0' = %q", v)
	}
	if v := queryText(`SELECT COALESCE(ir_extensions::text,'<null>') FROM public.request_logs_hot WHERE task_id='t2'`); v != "<null>" {
		t.Errorf("drifted non-JSON debris should map to NULL, got %q", v)
	}

	// ── scenario 2: aligned (fresh-install baseline shape, mother types) ─
	exec(`DROP TABLE public.request_logs_hot`)
	exec(`CREATE TABLE public.request_logs_hot (
		agent_name character varying(255),
		agent_type character varying(50),
		api_key_fingerprint character varying(16),
		task_id character varying(255),
		customer_id bigint,
		content_safety_score jsonb,
		dlp_violations jsonb,
		protocol_conversion boolean,
		ir_extensions jsonb,
		sanitizer_mutations jsonb
	)`)
	exec(`INSERT INTO public.request_logs_hot VALUES (
		'agent', 'coder', 'fp', 'a1',
		42, '{"score":1}', '[{"v":1}]', TRUE, '{"k":1}', '[]'
	), (
		'agent', 'coder', 'fp', 'a2',
		NULL, NULL, NULL, FALSE, NULL, NULL
	)`)
	// The whole point: this must NOT raise 42883 (bigint ~ unknown etc.).
	exec(migrationSQL)

	var n int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM public.request_logs_hot WHERE customer_id = 42 AND protocol_conversion IS TRUE AND ir_extensions = '{"k":1}'::jsonb AND sanitizer_mutations = '[]'::jsonb`).Scan(&n); err != nil {
		t.Fatalf("aligned value check: %v", err)
	}
	if n != 1 {
		t.Errorf("aligned: expected typed values preserved for 1 row, got %d", n)
	}
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM public.request_logs_hot WHERE customer_id IS NULL AND protocol_conversion IS FALSE AND ir_extensions IS NULL`).Scan(&n); err != nil {
		t.Fatalf("aligned null check: %v", err)
	}
	if n != 1 {
		t.Errorf("aligned: expected NULL row preserved, got %d", n)
	}
}
