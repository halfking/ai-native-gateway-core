//go:build integration

package startup

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestMigration759LosslessDrain runs only in a disposable PostgreSQL container.
// The transaction is also rolled back, so the fixture never changes a shared
// database. It covers parent-only enrichment, actual conflicts, JSON null,
// ordered arrays, fresh hot rows, bounded progress, and repeatability.
func TestMigration759LosslessDrain(t *testing.T) {
	t.Setenv("TEST_PG_URL", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	conn, cleanup := partitionBehaviorContainer(t, ctx)
	defer cleanup()

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	exec := func(sql string) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatalf("SQL failed: %v\n%s", err, sql)
		}
	}

	exec(`CREATE TABLE public.session_turn_details (
		id bigint NOT NULL, session_id text NOT NULL, turn_no integer NOT NULL,
		tenant_id varchar(255) NOT NULL, request_id text NOT NULL,
		ts timestamptz NOT NULL, partition_date date NOT NULL,
		quality_fix_actions jsonb, sensitive_keywords text[], request_type text,
		PRIMARY KEY (partition_date, id),
		UNIQUE (tenant_id, request_id, partition_date),
		UNIQUE (session_id, turn_no, partition_date)
	) PARTITION BY RANGE (partition_date)`)
	exec(`CREATE TABLE public.session_turn_details_hot (
		id bigint NOT NULL PRIMARY KEY, session_id text NOT NULL,
		turn_no integer NOT NULL, tenant_id varchar(255) NOT NULL,
		request_id text NOT NULL, ts timestamptz NOT NULL,
		partition_date date NOT NULL, quality_fix_actions jsonb,
		sensitive_keywords text[], request_type text,
		UNIQUE (tenant_id, request_id, partition_date),
		UNIQUE (session_id, turn_no, partition_date)
	)`)
	exec(`CREATE OR REPLACE FUNCTION public.ensure_session_turn_details_partition(p_date date)
	RETURNS void LANGUAGE plpgsql AS $$
	DECLARE v_start date := date_trunc('month', p_date)::date;
	BEGIN
		EXECUTE format(
			'CREATE TABLE IF NOT EXISTS public.session_turn_details_%s PARTITION OF public.session_turn_details FOR VALUES FROM (%L) TO (%L)',
			to_char(v_start, 'YYYY_MM'), v_start, (v_start + interval '1 month')::date);
	END; $$`)
	exec(`SELECT public.ensure_session_turn_details_partition(current_date)`)
	migration, err := os.ReadFile("801_session_turn_details_duplicate_drain.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(string(migration))

	// Parent is authoritative for fields hot never supplied; surrogate ids
	// differ because the backfill and live writer may have allocated each row.
	exec(`INSERT INTO public.session_turn_details
	(id,session_id,turn_no,tenant_id,request_id,ts,partition_date,
	 quality_fix_actions,sensitive_keywords,request_type) VALUES
	(101,'s1',1,'tenant','superset',now()-interval '1 day',current_date,'{"reviewed":true}',NULL,NULL),
	(102,'s2',1,'tenant','conflict',now()-interval '1 day',current_date,NULL,NULL,'parent'),
	(103,'s3',1,'tenant','array',now()-interval '1 day',current_date,NULL,ARRAY['a','b'],NULL),
	(104,'s4',1,'tenant','json-null',now()-interval '1 day',current_date,NULL,NULL,NULL),
	(105,'s7',1,'tenant','other-request',now()-interval '1 day',current_date,NULL,NULL,NULL),
	(9,'s9',1,'tenant','id-owner',now()-interval '1 day',current_date,NULL,NULL,NULL)`)
	exec(`INSERT INTO public.session_turn_details_hot
	(id,session_id,turn_no,tenant_id,request_id,ts,partition_date,
	 quality_fix_actions,sensitive_keywords,request_type) VALUES
	(1,'s1',1,'tenant','superset',now()-interval '1 day',current_date,NULL,NULL,NULL),
	(2,'s2',1,'tenant','conflict',now()-interval '1 day',current_date,NULL,NULL,'hot'),
	(3,'s3',1,'tenant','array',now()-interval '1 day',current_date,NULL,ARRAY['a'],NULL),
	(4,'s4',1,'tenant','json-null',now()-interval '1 day',current_date,'null'::jsonb,NULL,NULL),
	(5,'s5',1,'tenant','new',now()-interval '1 day',current_date,'{"fresh":true}',NULL,NULL),
	(6,'s6',1,'tenant','fresh',now(),current_date,NULL,NULL,NULL),
	(7,'s7',1,'tenant','insert-conflict',now()-interval '1 day',current_date,NULL,NULL,NULL),
	(8,'s8',1,'tenant','new-after-conflict',now()-interval '1 day',current_date,NULL,NULL,NULL),
	(9,'s10',1,'tenant','id-conflict',now()-interval '1 day',current_date,NULL,NULL,NULL),
	(10,'s11',1,'tenant','new-after-id-conflict',now()-interval '1 day',current_date,NULL,NULL,NULL)`)

	var drained int64
	for i := 0; i < 10; i++ {
		var batch int64
		if err := tx.QueryRow(ctx,
			`SELECT public.promote_session_turn_details_hot_to_partition('8 hours', 1)`,
		).Scan(&batch); err != nil {
			t.Fatalf("promote call %d: %v", i, err)
		}
		if batch > 1 {
			t.Fatalf("batch %d exceeded p_batch_size=1: %d", i, batch)
		}
		drained += batch
		if batch == 0 {
			break
		}
	}
	if drained != 4 {
		t.Fatalf("want redundant+three new rows drained, got %d", drained)
	}
	var n int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.session_turn_details_hot`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Fatalf("want six preserved hot rows, got %d", n)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.session_turn_details`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 9 {
		t.Fatalf("want nine parent rows (original six + three new), got %d", n)
	}
	// The scheduler's 8h cutoff leaves a current hot row untouched. Only
	// deliberately conflicting old rows remain after all eligible batches.
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.session_turn_details_hot
		WHERE ts >= now() - interval '8 hours'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("8h retention cutoff should preserve one current hot row, got %d", n)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.session_turn_details_hot
		WHERE ts < now() - interval '8 hours'
		  AND request_id IN ('superset','new','new-after-conflict','new-after-id-conflict')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("eligible nonconflicting hot rows must drain, got %d", n)
	}
	var enrichmentPreserved bool
	if err := tx.QueryRow(ctx, `SELECT quality_fix_actions = '{"reviewed":true}'::jsonb
		FROM public.session_turn_details WHERE request_id='superset'`).Scan(&enrichmentPreserved); err != nil {
		t.Fatal(err)
	}
	if !enrichmentPreserved {
		t.Fatal("parent-only enrichment overwritten")
	}
	for _, key := range []string{"conflict", "array", "json-null", "fresh", "insert-conflict", "id-conflict"} {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM public.session_turn_details_hot WHERE request_id=$1`, key,
		).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("hot row %q should remain, got %d", key, n)
		}
	}
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM public.session_turn_details WHERE request_id='new'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("new key missing from parent: %d", n)
	}
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM public.session_turn_details WHERE request_id='new-after-conflict'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("new key behind a unique conflict was starved: %d", n)
	}
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM public.session_turn_details WHERE request_id='new-after-id-conflict'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("new key behind an id conflict was starved: %d", n)
	}
	if err := tx.QueryRow(ctx,
		`SELECT public.promote_session_turn_details_hot_to_partition('8 hours', 5)`,
	).Scan(&n); err != nil || n != 0 {
		t.Fatalf("repeat promote must be idempotent, got rows=%d err=%v", n, err)
	}
}
