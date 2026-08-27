//go:build integration

package startup

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// migration608StripPsqlMeta removes psql meta-commands (`\set ...`) so the
// file body can run through pgx simple-protocol Exec (same file still uses
// ON_ERROR_STOP when applied via psql).
func migration608StripPsqlMeta(body string) string {
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "\\") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func migration608Read(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return migration608StripPsqlMeta(string(raw))
}

// TestMigration608And609RepairPkeylessTenantModelPolicies reproduces the
// production-252 shape (verified live 2026-08-27): both tables without primary
// keys, the audit table holding duplicate and NULL ids, sequences lagging
// behind, then applies 608+609 and asserts the repaired state. Re-application
// must stay clean (idempotency) and the dirty-data guard must fail closed.
func TestMigration608And609RepairPkeylessTenantModelPolicies(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	defer func() {
		terminateCtx, terminateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer terminateCancel()
		if err := container.Terminate(terminateCtx); err != nil {
			t.Errorf("terminate postgres: %v", err)
		}
	}()

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	var pool *pgxpool.Pool
	for attempt := 0; attempt < 30; attempt++ {
		pool, err = pgxpool.New(ctx, dsn)
		if err == nil {
			err = pool.Ping(ctx)
			if err == nil {
				break
			}
			pool.Close()
			pool = nil
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	defer pool.Close()

	// Dump-shaped baseline: exactly what sql/schema/01-schema.sql showed on
	// 252 before the repair — no pkeys, unique key only on the policy table.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE public.tenant_model_policies (
			id bigint NOT NULL,
			tenant_id character varying(64) NOT NULL,
			canonical_name text NOT NULL,
			reason text DEFAULT ''::text NOT NULL,
			created_by character varying(128) DEFAULT ''::character varying NOT NULL,
			deleted_at timestamp with time zone,
			deleted_by character varying(128),
			created_at timestamp with time zone DEFAULT now() NOT NULL,
			updated_at timestamp with time zone DEFAULT now() NOT NULL,
			CONSTRAINT tenant_model_policies_tenant_id_canonical_name_key UNIQUE (tenant_id, canonical_name)
		);
		CREATE SEQUENCE public.tenant_model_policies_id_seq;
		ALTER TABLE public.tenant_model_policies ALTER COLUMN id SET DEFAULT nextval('public.tenant_model_policies_id_seq'::regclass);

		CREATE TABLE public.tenant_model_policies_audit (
			id bigint,
			ts timestamp with time zone DEFAULT now() NOT NULL,
			action text NOT NULL,
			policy_id bigint,
			tenant_id text,
			canonical_name text,
			reason text,
			actor text
		);
		CREATE SEQUENCE public.tenant_model_policies_audit_id_seq;
		ALTER TABLE public.tenant_model_policies_audit ALTER COLUMN id SET DEFAULT nextval('public.tenant_model_policies_audit_id_seq'::regclass);
	`); err != nil {
		t.Fatalf("create dump-shaped baseline: %v", err)
	}

	// 252-shaped data: clean policy ids, audit ids duplicated (4 distinct ids
	// across many rows) plus a NULL id, audit sequence parked at 4.
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.tenant_model_policies (id, tenant_id, canonical_name)
		VALUES (8, 't-a', 'gpt-4o'), (9, 't-b', 'claude-3'), (10, 't-a', 'gpt-4.1');
		SELECT setval('public.tenant_model_policies_id_seq', 10, true);

		INSERT INTO public.tenant_model_policies_audit (id, ts, action, policy_id, tenant_id, canonical_name, actor) VALUES
			(1, '2026-06-22 10:00:00+08', 'insert', 8, 't-a', 'gpt-4o', 'alice'),
			(1, '2026-06-23 10:00:00+08', 'update', 8, 't-a', 'gpt-4o', 'alice'),
			(2, '2026-06-22 11:00:00+08', 'insert', 9, 't-b', 'claude-3', 'bob'),
			(2, '2026-06-24 11:00:00+08', 'delete', 9, 't-b', 'claude-3', 'bob'),
			(3, '2026-06-22 12:00:00+08', 'insert', 10, 't-a', 'gpt-4.1', 'alice'),
			(4, '2026-06-22 13:00:00+08', 'insert', 11, 't-c', 'gemini', 'carol'),
			(4, '2026-06-25 13:00:00+08', 'delete', 11, 't-c', 'gemini', 'carol'),
			(NULL, '2026-06-26 14:00:00+08', 'insert', 12, 't-d', 'qwen', 'dave');
		SELECT setval('public.tenant_model_policies_audit_id_seq', 4, true);
	`); err != nil {
		t.Fatalf("seed 252-shaped data: %v", err)
	}

	mPolicy := migration608Read(t, "608_tenant_model_policies_add_pkey.sql")
	mAudit := migration608Read(t, "609_tenant_model_policies_audit_rekey_pkey.sql")

	if _, err := pool.Exec(ctx, mPolicy); err != nil {
		t.Fatalf("apply 608 (policy): %v", err)
	}
	if _, err := pool.Exec(ctx, mAudit); err != nil {
		t.Fatalf("apply 609 (audit): %v", err)
	}

	// Both pkeys exist and are the only primary keys.
	assertPkey := func(table string) {
		t.Helper()
		var pkeys int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_index i
			JOIN pg_class c ON c.oid = i.indrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public' AND c.relname = $1 AND i.indisprimary`, table).Scan(&pkeys); err != nil {
			t.Fatalf("count pkeys on %s: %v", table, err)
		}
		if pkeys != 1 {
			t.Fatalf("table %s has %d primary keys after 608/609, want 1", table, pkeys)
		}
	}
	assertPkey("tenant_model_policies")
	assertPkey("tenant_model_policies_audit")

	// Audit trail: every row survives, ids are unique and non-NULL.
	var rows, distinctIDs, nullIDs int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(DISTINCT id), count(*) FILTER (WHERE id IS NULL)
		FROM public.tenant_model_policies_audit`).Scan(&rows, &distinctIDs, &nullIDs); err != nil {
		t.Fatal(err)
	}
	if rows != 8 || distinctIDs != 8 || nullIDs != 0 {
		t.Fatalf("audit rows=%d distinct=%d null=%d after 609, want 8/8/0", rows, distinctIDs, nullIDs)
	}

	// The earliest row of each duplicate group kept its id.
	var keptOriginal int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM public.tenant_model_policies_audit
		WHERE id IN (1, 2, 3, 4) AND ts IN ('2026-06-22 10:00:00+08', '2026-06-22 11:00:00+08', '2026-06-22 12:00:00+08', '2026-06-22 13:00:00+08')`).Scan(&keptOriginal); err != nil {
		t.Fatal(err)
	}
	if keptOriginal != 4 {
		t.Fatalf("earliest row per duplicate group must keep its id, kept=%d want 4", keptOriginal)
	}

	// Sequence is beyond max(id): the audit trigger can keep inserting.
	var nextID int
	if err := pool.QueryRow(ctx, `SELECT nextval('public.tenant_model_policies_audit_id_seq')`).Scan(&nextID); err != nil {
		t.Fatal(err)
	}
	var maxID int
	if err := pool.QueryRow(ctx, `SELECT max(id) FROM public.tenant_model_policies_audit`).Scan(&maxID); err != nil {
		t.Fatal(err)
	}
	if nextID <= maxID {
		t.Fatalf("audit sequence next=%d must exceed max(id)=%d", nextID, maxID)
	}

	// Idempotency: re-applying both migrations must be a clean no-op.
	if _, err := pool.Exec(ctx, mPolicy); err != nil {
		t.Fatalf("reapply 608 (policy): %v", err)
	}
	if _, err := pool.Exec(ctx, mAudit); err != nil {
		t.Fatalf("reapply 609 (audit): %v", err)
	}

	// Downs drop the constraints (structure only) and are re-runnable.
	dPolicyDown := migration608Read(t, "608_tenant_model_policies_add_pkey.down.sql")
	dAuditDown := migration608Read(t, "609_tenant_model_policies_audit_rekey_pkey.down.sql")
	if _, err := pool.Exec(ctx, dAuditDown); err != nil {
		t.Fatalf("apply 609 down: %v", err)
	}
	if _, err := pool.Exec(ctx, dPolicyDown); err != nil {
		t.Fatalf("apply 608 down: %v", err)
	}
	if _, err := pool.Exec(ctx, mPolicy); err != nil {
		t.Fatalf("re-apply 608 (policy) after down: %v", err)
	}
	if _, err := pool.Exec(ctx, mAudit); err != nil {
		t.Fatalf("re-apply 609 after down: %v", err)
	}

	// Dirty-data guard: duplicated policy ids must abort 608 (fail closed),
	// never silently "repair" data.
	if _, err := pool.Exec(ctx, `
		ALTER TABLE public.tenant_model_policies DROP CONSTRAINT tenant_model_policies_pkey;
		INSERT INTO public.tenant_model_policies (id, tenant_id, canonical_name) VALUES (8, 't-z', 'dupe-id');
	`); err != nil {
		t.Fatalf("stage dirty policy id: %v", err)
	}
	if _, err := pool.Exec(ctx, mPolicy); err == nil {
		t.Fatal("608 must fail closed on duplicate policy ids")
	} else if !strings.Contains(err.Error(), "migration 608 aborted") {
		t.Fatalf("608 failure must be the self-guard, got: %v", err)
	}
	// The aborted DO block leaves the session transaction open; roll back so
	// the pooled connection stays usable.
	if _, err := pool.Exec(ctx, "ROLLBACK;"); err != nil {
		t.Fatalf("rollback aborted tx: %v", err)
	}
}
