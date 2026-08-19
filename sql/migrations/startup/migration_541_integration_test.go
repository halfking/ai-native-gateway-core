package startup

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
