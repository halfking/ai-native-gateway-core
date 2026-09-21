//go:build integration

// Isolated validation of the dbx framework against a real PostgreSQL via
// Testcontainers. The probe table exists only here - it is never part of
// production schemas or migrations. Run with:
//
//	go test -tags integration ./internal/dbx/...
package dbx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// probeEnv is a single shared container for the whole integration run.
// Spawning one container per test is slow and flaky; per-test isolation is
// provided by truncating the probe table as the privileged admin role.
type probeEnv struct {
	admin *pgxpool.Pool // superuser: DDL, grants, truncates
	pool  *pgxpool.Pool // non-privileged app role used by the framework
}

var (
	probeEnvOnce sync.Once
	probeEnvInst *probeEnv
	probeEnvErr  error
)

// sharedProbeEnv returns the shared environment, truncating the probe table
// so each test starts clean.
func sharedProbeEnv(t *testing.T) *probeEnv {
	t.Helper()
	probeEnvOnce.Do(func() { probeEnvInst, probeEnvErr = buildProbeEnv() })
	if probeEnvErr != nil {
		if strings.Contains(strings.ToLower(probeEnvErr.Error()), "docker") {
			t.Skipf("docker unavailable, skipping integration test: %v", probeEnvErr)
		}
		t.Fatalf("probe environment: %v", probeEnvErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := probeEnvInst.admin.Exec(ctx, "TRUNCATE dbx_probe_records RESTART IDENTITY"); err != nil {
		t.Fatalf("truncate probe table: %v", err)
	}
	return probeEnvInst
}

func buildProbeEnv() (*probeEnv, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("dbxtest"),
		postgres.WithUsername("dbx"),
		postgres.WithPassword("dbxpass"),
	)
	if err != nil {
		return nil, err // docker-unavailable detection happens at the caller
	}
	// Cleanup is delegated to testcontainers' reaper (Ryuk), which removes
	// the container once the test binary exits; the shared environment must
	// outlive every individual test.

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, err
	}

	// The container user is a superuser (POSTGRES_USER) and superusers
	// bypass RLS entirely. DDL runs as that user; the framework pool
	// connects as a non-privileged app role so FORCE RLS is meaningful -
	// mirroring production where the app role owns no tables.
	adminCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	adminCfg.MaxConns = 2
	admin, err := dialWithRetry(ctx, adminCfg)
	if err != nil {
		return nil, fmt.Errorf("admin pool: %w", err)
	}

	// Probe schema with RLS driven by the *injected* GUC conventions.
	sdl := fmt.Sprintf(`
		CREATE TABLE dbx_probe_records (
		    id          BIGSERIAL PRIMARY KEY,
		    tenant_id   TEXT NOT NULL,
		    name        TEXT NOT NULL,
		    note        TEXT,
		    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
		    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb,
		    version     BIGINT NOT NULL DEFAULT 1,
		    deleted_at  TIMESTAMPTZ,
		    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		ALTER TABLE dbx_probe_records ENABLE ROW LEVEL SECURITY;
		ALTER TABLE dbx_probe_records FORCE ROW LEVEL SECURITY;
		CREATE POLICY probe_tenant_isolation ON dbx_probe_records
		    USING (tenant_id = current_setting('%s', true))
		    WITH CHECK (tenant_id = current_setting('%s', true));
		CREATE POLICY probe_super_admin ON dbx_probe_records
		    USING (current_setting('%s', true) = '%s' OR current_setting('%s', true) = '%s')
		    WITH CHECK (current_setting('%s', true) = '%s' OR current_setting('%s', true) = '%s');
	`, testScope.TenantGUC, testScope.TenantGUC,
		testScope.RoleGUC, testScope.SuperAdminValue,
		testScope.BypassGUC, testScope.BypassValue,
		testScope.RoleGUC, testScope.SuperAdminValue,
		testScope.BypassGUC, testScope.BypassValue)
	if _, err := admin.Exec(ctx, sdl); err != nil {
		return nil, fmt.Errorf("create probe schema: %w", err)
	}

	// Non-privileged app role for the framework pool (see comment above).
	if _, err := admin.Exec(ctx, `
		CREATE ROLE dbx_app LOGIN PASSWORD 'dbx_apppass' NOSUPERUSER;
		GRANT USAGE ON SCHEMA public TO dbx_app;
		GRANT SELECT, INSERT, UPDATE, DELETE ON dbx_probe_records TO dbx_app;
		GRANT USAGE, SELECT ON SEQUENCE dbx_probe_records_id_seq TO dbx_app;
	`); err != nil {
		return nil, fmt.Errorf("create app role: %w", err)
	}

	appCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	appCfg.ConnConfig.User = "dbx_app"
	appCfg.ConnConfig.Password = "dbx_apppass"
	// Mirror the gateway's production setting (db/db.go Open): simple
	// protocol, no statement cache.
	appCfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := dialWithRetry(ctx, appCfg)
	if err != nil {
		return nil, fmt.Errorf("app pool: %w", err)
	}
	return &probeEnv{admin: admin, pool: pool}, nil
}

// dialWithRetry waits out the container's postgres startup window.
func dialWithRetry(ctx context.Context, cfg *pgxpool.Config) (*pgxpool.Pool, error) {
	var pool *pgxpool.Pool
	var err error
	for attempt := 0; attempt < 30; attempt++ {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			pingCtx, pcancel := context.WithTimeout(ctx, 3*time.Second)
			err = pool.Ping(pingCtx)
			pcancel()
			if err == nil {
				return pool, nil
			}
			pool.Close()
		}
		time.Sleep(time.Second)
	}
	return nil, err
}

type probeKit struct {
	pool   *pgxpool.Pool
	runner *ScopeRunner
	crud   *CRUD
	reg    *Registry
}

func newProbeKit(t *testing.T) *probeKit {
	t.Helper()
	env := sharedProbeEnv(t)
	reg, err := NewRegistry(probeManifest())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	runner, err := NewScopeRunner(testScope, env.pool)
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	crud, err := NewCRUD(reg)
	if err != nil {
		t.Fatalf("crud: %v", err)
	}
	return &probeKit{pool: env.pool, runner: runner, crud: crud, reg: reg}
}

func mustInsert(t *testing.T, k *probeKit, tenant, name string) map[string]any {
	t.Helper()
	var row map[string]any
	err := k.runner.WithTenantTx(context.Background(), tenant, func(ctx context.Context, tx pgx.Tx) error {
		var ierr error
		row, ierr = k.crud.Insert(ctx, tx, "dbx_probe_records", tenant, map[string]any{
			"name":     name,
			"metadata": map[string]any{"origin": name},
		})
		return ierr
	})
	if err != nil {
		t.Fatalf("insert(%s): %v", name, err)
	}
	return row
}

func TestProbeInsertSelectRoundtrip(t *testing.T) {
	k := newProbeKit(t)
	row := mustInsert(t, k, "tenant-a", "alpha")
	id, _ := row["id"].(int64)
	if id == 0 {
		t.Fatalf("insert did not return id: %#v", row)
	}

	var got map[string]any
	err := k.runner.WithTenantReadOnlyTx(context.Background(), "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		var ierr error
		got, ierr = k.crud.SelectByPK(ctx, tx, "dbx_probe_records", "tenant-a", id)
		return ierr
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got["name"] != "alpha" || got["tenant_id"] != "tenant-a" {
		t.Errorf("roundtrip row = %#v", got)
	}
	if v, _ := got["version"].(int64); v != 1 {
		t.Errorf("version = %v, want 1", got["version"])
	}
}

func TestProbeCrossTenantIsolation(t *testing.T) {
	k := newProbeKit(t)
	rowA := mustInsert(t, k, "tenant-a", "a-row")
	idA, _ := rowA["id"].(int64)
	mustInsert(t, k, "tenant-b", "b-row")

	// Framework predicate: tenant B cannot read tenant A's row.
	err := k.runner.WithTenantReadOnlyTx(context.Background(), "tenant-b", func(ctx context.Context, tx pgx.Tx) error {
		_, ierr := k.crud.SelectByPK(ctx, tx, "dbx_probe_records", "tenant-a", idA)
		return ierr
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant select = %v, want ErrNotFound", err)
	}

	// RLS alone (raw SQL, no tenant predicate): each tenant sees only its
	// own rows, and unsccoped connections see nothing.
	var countA, countB, countNone int
	if err := k.runner.WithTenantTx(context.Background(), "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM dbx_probe_records").Scan(&countA)
	}); err != nil {
		t.Fatalf("count tenant-a: %v", err)
	}
	if err := k.runner.WithTenantTx(context.Background(), "tenant-b", func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM dbx_probe_records").Scan(&countB)
	}); err != nil {
		t.Fatalf("count tenant-b: %v", err)
	}
	if err := k.pool.QueryRow(context.Background(), "SELECT count(*) FROM dbx_probe_records").Scan(&countNone); err != nil {
		t.Fatalf("count unsccoped: %v", err)
	}
	if countA != 1 || countB != 1 || countNone != 0 {
		t.Fatalf("RLS counts a=%d b=%d none=%d, want 1/1/0", countA, countB, countNone)
	}
}

func TestProbeSuperAdminSeesAllTenants(t *testing.T) {
	k := newProbeKit(t)
	mustInsert(t, k, "tenant-a", "a")
	mustInsert(t, k, "tenant-b", "b")

	var total int
	err := k.runner.WithSuperAdminTx(context.Background(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM dbx_probe_records").Scan(&total)
	})
	if err != nil {
		t.Fatalf("super-admin count: %v", err)
	}
	if total != 2 {
		t.Fatalf("super-admin sees %d rows, want 2", total)
	}
}

func TestProbeMissingScopeFailsClosed(t *testing.T) {
	k := newProbeKit(t)
	err := k.runner.WithTenantTx(context.Background(), "", func(context.Context, pgx.Tx) error {
		t.Fatal("fn must not run")
		return nil
	})
	if !errors.Is(err, ErrMissingScope) {
		t.Fatalf("err = %v, want ErrMissingScope", err)
	}
}

func TestProbePatchSemanticsOnRealRows(t *testing.T) {
	k := newProbeKit(t)
	row := mustInsert(t, k, "tenant-a", "alpha")
	id, _ := row["id"].(int64)

	// Explicit NULL on nullable column + zero-value bool write.
	// The probe starts with note=NULL; write a value then null it back.
	var updated map[string]any
	err := k.runner.WithTenantTx(context.Background(), "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		var ierr error
		updated, ierr = k.crud.UpdateByPK(ctx, tx, "dbx_probe_records", "tenant-a", id, nil, nil)
		return ierr
	})
	if !errors.Is(err, ErrEmptyPatch) {
		t.Fatalf("nil-ish update = %v, want ErrEmptyPatch", err)
	}

	err = k.runner.WithTenantTx(context.Background(), "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		m, _ := k.reg.Lookup("dbx_probe_records")
		patch, perr := PatchFromMap(m, map[string]any{"note": "seen", "enabled": false})
		if perr != nil {
			return perr
		}
		var ierr error
		updated, ierr = k.crud.UpdateByPK(ctx, tx, "dbx_probe_records", "tenant-a", id, patch, nil)
		return ierr
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated["note"] != "seen" || updated["enabled"] != false {
		t.Errorf("updated row = %#v", updated)
	}
	if v, _ := updated["version"].(int64); v != 2 {
		t.Errorf("version = %v, want 2 (auto-incremented)", updated["version"])
	}

	// Explicit NULL restore.
	err = k.runner.WithTenantTx(context.Background(), "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		m, _ := k.reg.Lookup("dbx_probe_records")
		patch, perr := PatchFromMap(m, map[string]any{"note": nil})
		if perr != nil {
			return perr
		}
		var ierr error
		updated, ierr = k.crud.UpdateByPK(ctx, tx, "dbx_probe_records", "tenant-a", id, patch, nil)
		return ierr
	})
	if err != nil {
		t.Fatalf("null update: %v", err)
	}
	if updated["note"] != nil {
		t.Errorf("note after null = %#v, want nil", updated["note"])
	}
}

func TestProbeVersionCASConflict(t *testing.T) {
	k := newProbeKit(t)
	row := mustInsert(t, k, "tenant-a", "alpha")
	id, _ := row["id"].(int64)
	stale := int64(99) // never the live version

	err := k.runner.WithTenantTx(context.Background(), "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		m, _ := k.reg.Lookup("dbx_probe_records")
		patch, perr := PatchFromMap(m, map[string]any{"name": "clobber"})
		if perr != nil {
			return perr
		}
		_, ierr := k.crud.UpdateByPK(ctx, tx, "dbx_probe_records", "tenant-a", id, patch, &UpdateOptions{ExpectedVersion: &stale})
		return ierr
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS = %v, want ErrConflict", err)
	}

	// Fresh CAS succeeds and returns the new version.
	var updated map[string]any
	err = k.runner.WithTenantTx(context.Background(), "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		m, _ := k.reg.Lookup("dbx_probe_records")
		patch, _ := PatchFromMap(m, map[string]any{"name": "committed"})
		fresh := int64(1)
		var ierr error
		updated, ierr = k.crud.UpdateByPK(ctx, tx, "dbx_probe_records", "tenant-a", id, patch, &UpdateOptions{ExpectedVersion: &fresh})
		return ierr
	})
	if err != nil {
		t.Fatalf("fresh CAS: %v", err)
	}
	if v, _ := updated["version"].(int64); v != 2 {
		t.Errorf("version = %v, want 2", updated["version"])
	}
}

func TestProbeSoftDeleteAndNotFound(t *testing.T) {
	k := newProbeKit(t)
	row := mustInsert(t, k, "tenant-a", "doomed")
	id, _ := row["id"].(int64)

	err := k.runner.WithTenantTx(context.Background(), "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		return k.crud.DeleteByPK(ctx, tx, "dbx_probe_records", "tenant-a", id)
	})
	if err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	err = k.runner.WithTenantReadOnlyTx(context.Background(), "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		_, ierr := k.crud.SelectByPK(ctx, tx, "dbx_probe_records", "tenant-a", id)
		return ierr
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("select after delete = %v, want ErrNotFound (row still exists physically)", err)
	}

	// Double delete: already soft-deleted → not found.
	err = k.runner.WithTenantTx(context.Background(), "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		return k.crud.DeleteByPK(ctx, tx, "dbx_probe_records", "tenant-a", id)
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete = %v, want ErrNotFound", err)
	}
}

func TestProbeTransactionRollback(t *testing.T) {
	k := newProbeKit(t)
	boom := errors.New("intentional")

	err := k.runner.WithTenantTx(context.Background(), "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		if _, ierr := k.crud.Insert(ctx, tx, "dbx_probe_records", "tenant-a", map[string]any{"name": "rolled"}); ierr != nil {
			return ierr
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}

	// The app-role pool has no GUC set, so RLS hides everything; count via
	// the explicit privileged path instead.
	var count int
	err = k.runner.WithSuperAdminTx(context.Background(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM dbx_probe_records WHERE name = 'rolled'").Scan(&count)
	})
	if err != nil {
		t.Fatalf("count rolled: %v", err)
	}
	if count != 0 {
		t.Fatalf("rolled-back row survived: %d", count)
	}
}

func TestProbeSavepointPartialRollback(t *testing.T) {
	k := newProbeKit(t)
	inner := errors.New("inner failure")

	err := k.runner.WithTenantTx(context.Background(), "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		if _, ierr := k.crud.Insert(ctx, tx, "dbx_probe_records", "tenant-a", map[string]any{"name": "kept"}); ierr != nil {
			return ierr
		}
		spErr := WithSavepoint(ctx, tx, "sp_probe", func(ctx context.Context, tx pgx.Tx) error {
			if _, ierr := k.crud.Insert(ctx, tx, "dbx_probe_records", "tenant-a", map[string]any{"name": "discarded"}); ierr != nil {
				return ierr
			}
			return inner
		})
		if spErr != nil && !errors.Is(spErr, inner) {
			return spErr
		}
		return nil
	})
	if err != nil {
		t.Fatalf("savepoint tx: %v", err)
	}

	var kept, discarded int
	if err := k.runner.WithSuperAdminTx(context.Background(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM dbx_probe_records WHERE name = 'kept'").Scan(&kept)
	}); err != nil {
		t.Fatalf("count kept: %v", err)
	}
	if err := k.runner.WithSuperAdminTx(context.Background(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM dbx_probe_records WHERE name = 'discarded'").Scan(&discarded)
	}); err != nil {
		t.Fatalf("count discarded: %v", err)
	}
	if kept != 1 || discarded != 0 {
		t.Fatalf("savepoint outcome kept=%d discarded=%d, want 1/0", kept, discarded)
	}
}

func TestProbeJSONBGuardsOnRealDB(t *testing.T) {
	k := newProbeKit(t)

	// Oversized payload rejected before reaching the database.
	big := strings.Repeat("x", DefaultJSONBMaxBytes+1)
	err := k.runner.WithTenantTx(context.Background(), "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		_, ierr := k.crud.Insert(ctx, tx, "dbx_probe_records", "tenant-a", map[string]any{
			"name":     "big",
			"metadata": map[string]any{"blob": big},
		})
		return ierr
	})
	if !errors.Is(err, ErrInvalidJSONB) {
		t.Fatalf("oversized jsonb = %v, want ErrInvalidJSONB", err)
	}

	// Valid JSONB lands and round-trips.
	var row map[string]any
	err = k.runner.WithTenantTx(context.Background(), "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		var ierr error
		row, ierr = k.crud.Insert(ctx, tx, "dbx_probe_records", "tenant-a", map[string]any{
			"name":     "json",
			"metadata": map[string]any{"nested": map[string]any{"ok": true}},
		})
		return ierr
	})
	if err != nil {
		t.Fatalf("jsonb insert: %v", err)
	}
	raw, ok := row["metadata"]
	if !ok {
		t.Fatalf("metadata missing from RETURNING row: %#v", row)
	}
	// pgx decodes jsonb depending on protocol path; accept both shapes.
	switch mv := raw.(type) {
	case map[string]any:
		nested, _ := mv["nested"].(map[string]any)
		if nested["ok"] != true {
			t.Errorf("metadata nested = %#v", mv)
		}
	case []byte:
		if !strings.Contains(string(mv), `"ok"`) {
			t.Errorf("metadata raw = %s", mv)
		}
	default:
		t.Errorf("metadata round-trip type %T", raw)
	}
}

func TestProbeContextCancelKeepsConnectionAlive(t *testing.T) {
	k := newProbeKit(t)

	// Cancel the request context mid-transaction. The rollback must run on
	// a detached context, so the pooled connection survives and stays
	// usable for later work.
	ctx, cancel := context.WithCancel(context.Background())
	err := WithTx(ctx, k.pool, func(ctx context.Context, tx pgx.Tx) error {
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()
	if err := k.pool.Ping(pingCtx); err != nil {
		t.Fatalf("connection pool unhealthy after cancelled tx: %v", err)
	}

	// The pool must still execute real scoped work afterwards.
	row := mustInsert(t, k, "tenant-a", "after-cancel")
	if row["name"] != "after-cancel" {
		t.Errorf("post-cancel insert = %#v", row)
	}
}
