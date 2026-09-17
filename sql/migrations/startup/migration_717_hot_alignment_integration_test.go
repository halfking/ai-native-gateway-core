//go:build integration

// migration_717_hot_alignment_integration_test.go — behavioral fixture for
// the live-DB-rewritten migration 717 (adopted at the R37 merge). 717 must
// behave correctly on BOTH hot-table vintages:
//
//   - drifted (pre-717 production shape per the live-DB audit): text varchars,
//     double precision content_safety_score, text[] dlp_violations, text
//     protocol_conversion / *_extensions, customer_id ALREADY bigint
//     (migration 574 owns that transition) — the guarded ALTERs convert the
//     seven guarded columns;
//   - aligned (fresh-install baseline shape, all mother types) — every guard
//     skips and the file is a no-op that preserves typed values.
//
// The 0A000 view-dependency path (frozen wrapper chains) is exercised on the
// real 245/preprod chains; a fixture view chain would only duplicate the
// per-column subtransaction logic that TestMigration717ShapePins pins
// structurally.
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

	// ── scenario 1: drifted (pre-717 production shape per the live audit) ─
	exec(`CREATE TABLE public.request_logs_hot (
		agent_name text,
		agent_type text,
		api_key_fingerprint text,
		task_id text,
		customer_id bigint,
		content_safety_score double precision,
		dlp_violations text[],
		protocol_conversion text,
		ir_extensions text,
		sanitizer_mutations text
	)`)
	exec(`INSERT INTO public.request_logs_hot VALUES (
		'agent', 'coder', '0123456789abcdefZZ', 't1',
		42, 1.5, ARRAY['x'], 'true', '{"k":1}', '[]'
	)`)
	exec(migrationSQL)

	type col struct{ name, dataType string }
	var got []col
	rows, err := tx.Query(ctx, `
		SELECT column_name, data_type FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
		  AND column_name IN ('agent_name','agent_type','api_key_fingerprint','task_id',
		                      'customer_id','content_safety_score','dlp_violations',
		                      'protocol_conversion','ir_extensions','sanitizer_mutations')
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
		"agent_name": "character varying", "agent_type": "character varying",
		"api_key_fingerprint": "character varying", "task_id": "character varying",
		"customer_id": "bigint", "content_safety_score": "jsonb",
		"dlp_violations": "jsonb", "protocol_conversion": "boolean",
		"ir_extensions": "jsonb", "sanitizer_mutations": "jsonb",
	}
	for _, c := range got {
		if want[c.name] != c.dataType {
			t.Errorf("drifted: column %s = %s, want %s", c.name, c.dataType, want[c.name])
		}
	}

	var n int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM public.request_logs_hot
		WHERE api_key_fingerprint = '0123456789abcdef'
		  AND task_id = 't1' AND customer_id = 42
		  AND content_safety_score = '1.5'::jsonb
		  AND dlp_violations = '["x"]'::jsonb
		  AND protocol_conversion IS TRUE
		  AND ir_extensions = '{"k":1}'::jsonb
		  AND sanitizer_mutations = '[]'::jsonb`).Scan(&n); err != nil {
		t.Fatalf("drifted value check: %v", err)
	}
	if n != 1 {
		t.Errorf("drifted: expected the row aligned with values preserved, got %d matching rows", n)
	}

	// ── scenario 2: aligned (fresh-install baseline shape) — file is a no-op ─
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
	)`)
	// Every guard must skip: this must not raise, rewrite, or lose values.
	exec(migrationSQL)

	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM public.request_logs_hot
		WHERE api_key_fingerprint = 'fp' AND task_id = 'a1' AND customer_id = 42
		  AND content_safety_score = '{"score":1}'::jsonb
		  AND protocol_conversion IS TRUE AND ir_extensions = '{"k":1}'::jsonb`).Scan(&n); err != nil {
		t.Fatalf("aligned value check: %v", err)
	}
	if n != 1 {
		t.Errorf("aligned: expected typed values untouched by the no-op pass, got %d matching rows", n)
	}
}
