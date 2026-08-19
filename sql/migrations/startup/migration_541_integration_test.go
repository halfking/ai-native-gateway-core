package startup

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const fixtureCleanup541 = `
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_insert ON public.credential_model_bindings;
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_update ON public.credential_model_bindings;
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_delete ON public.credential_model_bindings;
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision ON public.credential_model_bindings;
DROP FUNCTION IF EXISTS public.bump_candidate_binding_scope_revision_insert();
DROP FUNCTION IF EXISTS public.bump_candidate_binding_scope_revision_update();
DROP FUNCTION IF EXISTS public.bump_candidate_binding_scope_revision_delete();
DROP FUNCTION IF EXISTS public.bump_candidate_binding_scope_revision();
DROP TABLE IF EXISTS public.candidate_binding_scope_revision;
DROP TABLE IF EXISTS public.credential_model_bindings;
DROP TABLE IF EXISTS public.provider_models;
`

const fixtureDDL541 = `
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE TABLE public.provider_models (
    id bigint PRIMARY KEY,
    provider_id bigint NOT NULL,
    raw_model_name text NOT NULL,
    UNIQUE (provider_id, raw_model_name)
);
CREATE TABLE public.credential_model_bindings (
    id bigint PRIMARY KEY,
    credential_id bigint NOT NULL,
    provider_model_id bigint NOT NULL REFERENCES public.provider_models(id),
    manual_priority integer NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
`

func TestMigration541ScopeRevisionIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set; migration 541 integration test requires a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Skipf("TEST_PG_URL unreachable: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec failed: %v\nSQL: %s", err, sql)
		}
	}
	queryRevision := func(rawModel string) (int64, string, string) {
		t.Helper()
		var version int64
		var hash, actor string
		err := conn.QueryRow(ctx, `SELECT scope_version, scope_hash, COALESCE(last_bumped_by, '')
			FROM public.candidate_binding_scope_revision WHERE raw_model=$1`, rawModel).Scan(&version, &hash, &actor)
		if err != nil {
			t.Fatalf("load revision for %s: %v", rawModel, err)
		}
		return version, hash, actor
	}
	expectFullHash := func(rawModel string) string {
		t.Helper()
		var hash string
		err := conn.QueryRow(ctx, `
			SELECT COALESCE(encode(digest(string_agg(
				b.id::text || '|' || b.credential_id::text || '|' || b.manual_priority::text || '|' ||
				extract(epoch from b.updated_at)::text,
				'|' ORDER BY b.id
			), 'sha256'), 'hex'), '')
			FROM public.provider_models pm
			LEFT JOIN public.credential_model_bindings b ON b.provider_model_id=pm.id
			WHERE pm.raw_model_name=$1`, rawModel).Scan(&hash)
		if err != nil {
			t.Fatalf("compute full hash for %s: %v", rawModel, err)
		}
		return hash
	}

	exec(fixtureCleanup541)
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), fixtureCleanup541) })
	exec(fixtureDDL541)
	exec(`INSERT INTO provider_models (id, provider_id, raw_model_name) VALUES
		(1, 10, 'shared-model'), (2, 20, 'shared-model'), (3, 30, 'other-model')`)
	exec(`INSERT INTO credential_model_bindings (id, credential_id, provider_model_id, manual_priority, updated_at) VALUES
		(101, 1001, 1, 1, '2026-01-01T00:00:00Z'),
		(102, 1002, 2, 2, '2026-01-01T00:00:01Z'),
		(103, 1003, 1, 3, '2026-01-01T00:00:02Z'),
		(201, 2001, 3, 1, '2026-01-01T00:00:03Z')`)

	up, err := os.ReadFile("541_candidate_binding_scope_revision.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(string(up))
	exec(string(up))

	var scopes int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM public.candidate_binding_scope_revision`).Scan(&scopes); err != nil {
		t.Fatal(err)
	}
	if scopes != 2 {
		t.Fatalf("backfill scopes=%d, want 2 (one shared raw_model across providers plus other-model)", scopes)
	}
	version, hash, actor := queryRevision("shared-model")
	if version != 1 || actor != "" {
		t.Fatalf("shared backfill revision=(%d,%q), actor=%q; want version=1 and empty actor", version, hash, actor)
	}
	if want := expectFullHash("shared-model"); hash != want {
		t.Fatalf("shared backfill hash=%q, want full-scope %q", hash, want)
	}

	// One SQL statement updates three rows; statement-level trigger must bump once.
	exec(`BEGIN; SELECT set_config('app.actor', 'integration-admin', true);
		UPDATE credential_model_bindings
		SET manual_priority = manual_priority + 10, updated_at = updated_at + interval '1 second'
		WHERE id IN (101, 102, 103); COMMIT`)
	version, hash, actor = queryRevision("shared-model")
	if version != 2 {
		t.Fatalf("multi-row update version=%d, want 2", version)
	}
	if actor != "integration-admin" {
		t.Fatalf("last_bumped_by=%q, want integration-admin", actor)
	}
	if want := expectFullHash("shared-model"); hash != want {
		t.Fatalf("multi-row update hash=%q, want complete scope %q", hash, want)
	}

	// updated_at-only writes are irrelevant to ordering and must not bump.
	exec(`UPDATE credential_model_bindings SET updated_at = updated_at + interval '1 second' WHERE id = 101`)
	versionAfterNoOp, hashAfterNoOp, _ := queryRevision("shared-model")
	if versionAfterNoOp != version || hashAfterNoOp != hash {
		t.Fatalf("updated_at-only update changed revision from (%d,%q) to (%d,%q)", version, hash, versionAfterNoOp, hashAfterNoOp)
	}

	// Moving a binding between raw models invalidates both scopes exactly once.
	otherBefore, _, _ := queryRevision("other-model")
	exec(`UPDATE credential_model_bindings SET provider_model_id=3, updated_at=updated_at + interval '1 second' WHERE id=101`)
	sharedAfterMove, sharedHashAfterMove, _ := queryRevision("shared-model")
	otherAfterMove, otherHashAfterMove, _ := queryRevision("other-model")
	if sharedAfterMove != version+1 {
		t.Fatalf("old scope version after move=%d, want %d", sharedAfterMove, version+1)
	}
	if otherAfterMove != otherBefore+1 {
		t.Fatalf("new scope version after move=%d, want %d", otherAfterMove, otherBefore+1)
	}
	if want := expectFullHash("shared-model"); sharedHashAfterMove != want {
		t.Fatalf("old scope hash after move=%q, want %q", sharedHashAfterMove, want)
	}
	if want := expectFullHash("other-model"); otherHashAfterMove != want {
		t.Fatalf("new scope hash after move=%q, want %q", otherHashAfterMove, want)
	}

	down, err := os.ReadFile("541_candidate_binding_scope_revision.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(string(down))
	var revisionExists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.candidate_binding_scope_revision') IS NOT NULL`).Scan(&revisionExists); err != nil {
		t.Fatal(err)
	}
	if revisionExists {
		t.Fatal("down migration did not remove candidate_binding_scope_revision")
	}
}

// TestMigration541ScopeRevisionConcurrentBump exercises the advisory-lock
// ordering on the revision row: two SERIALIZABLE connections race a
// multi-row reorder against the same scope and exactly one must observe
// the post-commit version while the other receives HTTP 40001 (handled as
// "stale revision" by the handler).
//
// This must use two independent pgx connections: pgx.Conn.BeginTx aliases the
// single backend transaction, so two txns on one conn share commit state and
// never produce a SERIALIZABLE abort.
func TestMigration541ScopeRevisionConcurrentBump(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set; migration 541 concurrency test requires a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// admin conn for setup
	cfgAdmin, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfgAdmin.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	admin, err := pgx.ConnectConfig(ctx, cfgAdmin)
	if err != nil {
		t.Skipf("TEST_PG_URL unreachable: %v", err)
	}
	defer func() { _ = admin.Close(ctx) }()

	// two independent worker conns for the race
	newConn := func(t *testing.T) *pgx.Conn {
		t.Helper()
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
		c, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			t.Fatalf("worker connect: %v", err)
		}
		return c
	}

	resetAndSeed := func(t *testing.T) {
		t.Helper()
		exec := func(sql string, args ...any) {
			t.Helper()
			if _, err := admin.Exec(ctx, sql, args...); err != nil {
				t.Fatalf("exec failed: %v (sql=%s)", err, sql)
			}
		}
		exec(fixtureCleanup541)
		exec(fixtureDDL541)
		exec(`INSERT INTO provider_models VALUES (1, 10, 'race-model'), (2, 20, 'race-model')`)
		for i := 0; i < 4; i++ {
			exec(`INSERT INTO credential_model_bindings (id, credential_id, provider_model_id, manual_priority, updated_at) VALUES ($1, $1+1000, $2, $3, '2026-01-01T00:00:00Z')`, i+1, (i%2)+1, i+1)
		}
		up, err := os.ReadFile("541_candidate_binding_scope_revision.sql")
		if err != nil {
			t.Fatal(err)
		}
		exec(string(up))
	}
	resetAndSeed(t)

	c1 := newConn(t)
	defer func() { _ = c1.Close(ctx) }()
	c2 := newConn(t)
	defer func() { _ = c2.Close(ctx) }()

	tx1, err := c1.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatalf("begin tx1: %v", err)
	}
	if _, err := tx1.Exec(ctx, `SELECT set_config('app.actor', 'tx1', true)`); err != nil {
		t.Fatalf("set actor tx1: %v", err)
	}
	if _, err := tx1.Exec(ctx,
		`UPDATE credential_model_bindings
		   SET manual_priority = manual_priority + 10, updated_at = updated_at + interval '1 second'
		 WHERE id IN (1,2,3,4)`); err != nil {
		t.Fatalf("tx1 update: %v", err)
	}

	// tx2 begins a SERIALIZABLE txn and issues its UPDATE against the rows tx1
	// still holds exclusive locks on. The UPDATE blocks until tx1 commits; once
	// tx1's row writes are visible, SERIALIZABLE's first-updater-wins aborts
	// tx2's UPDATE with SQLSTATE 40001. This window is what the SERIALIZABLE
	// handler retries as "stale revision".
	tx2, err := c2.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}
	defer tx2.Rollback(ctx)
	if _, err := tx2.Exec(ctx, `SELECT set_config('app.actor', 'tx2', true)`); err != nil {
		t.Fatalf("set actor tx2: %v", err)
	}

	type updateResult struct{ err error }
	tx2UpdateDone := make(chan updateResult, 1)
	go func() {
		_, err := tx2.Exec(ctx,
			`UPDATE credential_model_bindings
			   SET manual_priority = manual_priority + 20, updated_at = updated_at + interval '1 second'
			 WHERE id IN (1,2,3,4)`)
		tx2UpdateDone <- updateResult{err}
	}()

	// Give tx2's UPDATE time to block on tx1's row locks (200ms is ample on a
	// local PG), then commit tx1 so the write-write conflict resolves in tx2.
	select {
	case <-time.After(200 * time.Millisecond):
	case r := <-tx2UpdateDone:
		t.Fatalf("tx2 update returned before tx1 committed: %v", r.err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("tx1 commit: %v", err)
	}

	res := <-tx2UpdateDone
	err = res.err
	if err == nil {
		// SSI may defer the abort to commit; commit and check.
		err = tx2.Commit(ctx)
		defer func() { _ = tx2.Rollback(ctx) }()
	}
	if err == nil {
		t.Fatalf("expected tx2 to abort under SERIALIZABLE (40001), got nil")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "40001" {
		t.Fatalf("expected SQLSTATE 40001 from tx2, got %v", err)
	}

	// Sanity: tx1's commit bumped the revision exactly once and recorded tx1.
	var version int64
	var actor string
	if err := admin.QueryRow(ctx,
		`SELECT scope_version, COALESCE(last_bumped_by,'') FROM public.candidate_binding_scope_revision WHERE raw_model='race-model'`).
		Scan(&version, &actor); err != nil {
		t.Fatalf("load race-model revision: %v", err)
	}
	if version != 2 {
		t.Fatalf("scope_version after one committed reorder = %d, want 2", version)
	}
	if actor != "tx1" {
		t.Fatalf("last_bumped_by = %q, want tx1", actor)
	}
}
