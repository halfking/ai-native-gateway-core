//go:build integration

// Shadow-read pilot for tenant_model_policies. The table is created here
// in a throwaway container with a faithful replica of migration 024's DDL
// (the FK to tenants(code) is dropped because the replica has no tenants
// table; the audit trigger is omitted because the pilot does not exercise
// it). Two shapes are exercised:
//
//   - migration shape (id BIGSERIAL PRIMARY KEY): the metadata gate must
//     stay green and dbx CRUD results must match the legacy handwritten
//     statements from admin/model_policies.go;
//   - production-dump shape (no PK, only UNIQUE(tenant_id,canonical_name)):
//     the gate must fail closed with DriftPKMismatch - this was the real
//     drift production 252 carried until migrations 608/609 repaired it
//     (2026-08-27); the shape stays here as a permanent gate regression.
//
// Run with: go test -tags integration ./db/dbxmanifest/...
package dbxmanifest

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
	"github.com/kaixuan/llm-gateway-go/internal/dbx"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

type pilotEnv struct {
	admin *pgxpool.Pool
	app   *pgxpool.Pool
}

var (
	pilotOnce sync.Once
	pilotInst *pilotEnv
	pilotErr  error
)

func sharedPilotEnv(t *testing.T) *pilotEnv {
	t.Helper()
	pilotOnce.Do(func() { pilotInst, pilotErr = buildPilotEnv() })
	if pilotErr != nil {
		if strings.Contains(strings.ToLower(pilotErr.Error()), "docker") {
			t.Skipf("docker unavailable, skipping pilot integration test: %v", pilotErr)
		}
		t.Fatalf("pilot environment: %v", pilotErr)
	}
	return pilotInst
}

func buildPilotEnv() (*pilotEnv, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("dbxpilot"),
		postgres.WithUsername("pilot"),
		postgres.WithPassword("pilotpass"),
	)
	if err != nil {
		return nil, err
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, err
	}

	adminCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	adminCfg.MaxConns = 2
	admin, err := dialPilot(ctx, adminCfg)
	if err != nil {
		return nil, fmt.Errorf("admin pool: %w", err)
	}

	// Migration-024 replica (FK and audit trigger omitted, see file docs).
	sdl := `
		CREATE TABLE tenant_model_policies (
		    id              BIGSERIAL PRIMARY KEY,
		    tenant_id       VARCHAR(64) NOT NULL,
		    canonical_name  TEXT NOT NULL,
		    reason          TEXT NOT NULL DEFAULT '',
		    created_by      VARCHAR(128) NOT NULL DEFAULT '',
		    deleted_at      TIMESTAMPTZ,
		    deleted_by      VARCHAR(128),
		    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
		    UNIQUE (tenant_id, canonical_name),
		    CHECK (canonical_name <> '')
		);
		CREATE FUNCTION public.get_current_tenant() RETURNS text
		LANGUAGE sql STABLE AS $fn$
		    SELECT COALESCE(NULLIF(current_setting('app.current_tenant', true), ''), 'default')
		$fn$;
		ALTER TABLE tenant_model_policies ENABLE ROW LEVEL SECURITY;
		ALTER TABLE tenant_model_policies FORCE ROW LEVEL SECURITY;
		CREATE POLICY tenant_isolation_tmp ON public.tenant_model_policies
		    USING ((tenant_id)::text = (public.get_current_tenant())::text);
		CREATE POLICY tmp_super_admin ON public.tenant_model_policies
		    USING (current_setting('app.current_role', true) = 'super_admin'
		        OR current_setting('app.bypass_rls', true) = 'true');

		-- Production-dump shape: same columns, NO primary key, only the
		-- two-column unique (mirrors sql/schema/01-schema.sql).
		CREATE TABLE tmp_dumpshape (
		    id              BIGSERIAL NOT NULL,
		    tenant_id       VARCHAR(64) NOT NULL,
		    canonical_name  TEXT NOT NULL,
		    reason          TEXT NOT NULL DEFAULT '',
		    created_by      VARCHAR(128) NOT NULL DEFAULT '',
		    deleted_at      TIMESTAMPTZ,
		    deleted_by      VARCHAR(128),
		    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
		    UNIQUE (tenant_id, canonical_name)
		);
		ALTER TABLE tmp_dumpshape ENABLE ROW LEVEL SECURITY;
		ALTER TABLE tmp_dumpshape FORCE ROW LEVEL SECURITY;
		CREATE POLICY tenant_isolation_dumps ON public.tmp_dumpshape
		    USING ((tenant_id)::text = (public.get_current_tenant())::text);

		CREATE ROLE dbx_pilot_app LOGIN PASSWORD 'pilot_apppass' NOSUPERUSER;
		GRANT USAGE ON SCHEMA public TO dbx_pilot_app;
		GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_model_policies, tmp_dumpshape TO dbx_pilot_app;
		GRANT USAGE, SELECT ON SEQUENCE tenant_model_policies_id_seq, tmp_dumpshape_id_seq TO dbx_pilot_app;`
	if _, err := admin.Exec(ctx, sdl); err != nil {
		return nil, fmt.Errorf("create pilot schema: %w", err)
	}

	appCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	appCfg.ConnConfig.User = "dbx_pilot_app"
	appCfg.ConnConfig.Password = "pilot_apppass"
	// Mirror the gateway's production setting (db/db.go Open).
	appCfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	app, err := dialPilot(ctx, appCfg)
	if err != nil {
		return nil, fmt.Errorf("app pool: %w", err)
	}
	return &pilotEnv{admin: admin, app: app}, nil
}

func dialPilot(ctx context.Context, cfg *pgxpool.Config) (*pgxpool.Pool, error) {
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

func newPilotKit(t *testing.T) (*dbx.ScopeRunner, *dbx.CRUD, *dbx.Registry) {
	t.Helper()
	env := sharedPilotEnv(t)
	reg, err := DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry: %v", err)
	}
	runner, err := dbx.NewScopeRunner(ScopeConfig(), env.app)
	if err != nil {
		t.Fatalf("NewScopeRunner: %v", err)
	}
	crud, err := dbx.NewCRUD(reg)
	if err != nil {
		t.Fatalf("NewCRUD: %v", err)
	}
	return runner, crud, reg
}

// legacyPolicy is the row shape returned by admin/model_policies.go.
type legacyPolicy struct {
	ID            int64
	TenantID      string
	CanonicalName string
	Reason        string
	CreatedBy     string
	DeletedAt     *time.Time
}

// legacyList copies the active-list statement from admin/model_policies.go.
func legacyList(ctx context.Context, runner *dbx.ScopeRunner, tenant string, includeDeleted bool) ([]legacyPolicy, error) {
	sqlText := `
		SELECT id, tenant_id, canonical_name, reason, created_by, deleted_at
		FROM tenant_model_policies
		WHERE tenant_id = $1`
	if !includeDeleted {
		sqlText += ` AND deleted_at IS NULL`
	}
	sqlText += ` ORDER BY id DESC`
	var out []legacyPolicy
	err := runner.WithTenantReadOnlyTx(ctx, tenant, func(ctx context.Context, tx pgx.Tx) error {
		rows, ierr := tx.Query(ctx, sqlText, tenant)
		if ierr != nil {
			return ierr
		}
		defer rows.Close()
		for rows.Next() {
			var p legacyPolicy
			if serr := rows.Scan(&p.ID, &p.TenantID, &p.CanonicalName, &p.Reason, &p.CreatedBy, &p.DeletedAt); serr != nil {
				return serr
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

func TestPilotGateGreenOnMigrationShape(t *testing.T) {
	env := sharedPilotEnv(t)
	reg, err := DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry: %v", err)
	}
	reader, err := dbx.NewMetadataReader("public")
	if err != nil {
		t.Fatalf("NewMetadataReader: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	snapshot, err := reader.ReadRegistry(ctx, env.admin, reg)
	if err != nil {
		t.Fatalf("ReadRegistry: %v", err)
	}
	drifts := dbx.CompareSnapshot(reg, snapshot)
	if dbx.HasBlockingDrift(drifts) {
		t.Fatalf("migration shape must pass the gate, got: %+v", drifts)
	}
	meta := snapshot.Tables["tenant_model_policies"]
	if len(meta.SingleColumnUniqueKeys) != 1 || meta.SingleColumnUniqueKeys[0] != "id" {
		t.Errorf("unique keys = %v, want [id]", meta.SingleColumnUniqueKeys)
	}
	if !meta.RLS || !meta.ForceRLS {
		t.Errorf("RLS flags = %+v", meta)
	}
}

func TestPilotGateBlocksDumpShapeWithoutPK(t *testing.T) {
	env := sharedPilotEnv(t)
	m := TenantModelPolicies()
	m.Table = "tmp_dumpshape"
	reader, _ := dbx.NewMetadataReader("public")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	meta, err := reader.ReadTable(ctx, env.admin, "tmp_dumpshape")
	if err != nil {
		t.Fatalf("ReadTable: %v", err)
	}
	if len(meta.SingleColumnUniqueKeys) != 0 {
		t.Errorf("dump shape unique keys = %v, want none", meta.SingleColumnUniqueKeys)
	}
	drifts := dbx.CompareManifest(&m, meta)
	if !dbx.HasBlockingDrift(drifts) {
		t.Fatalf("dump shape (no PK) must be blocking, got: %+v", drifts)
	}
	found := false
	for _, d := range drifts {
		if d.Kind == dbx.DriftPKMismatch {
			found = true
		}
	}
	if !found {
		t.Errorf("DriftPKMismatch not reported: %+v", drifts)
	}
}

func TestPilotShadowReadAgainstLegacyStatements(t *testing.T) {
	runner, crud, reg := newPilotKit(t)
	ctx := context.Background()

	// dbx insert (payload mirrors the legacy handler's INSERT set).
	var inserted map[string]any
	err := runner.WithTenantTx(ctx, "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		var ierr error
		inserted, ierr = crud.Insert(ctx, tx, "tenant_model_policies", "tenant-a", map[string]any{
			"canonical_name": "claude-opus-4-6",
			"reason":         "not procured",
			"created_by":     "admin@example.com",
		})
		return ierr
	})
	if err != nil {
		t.Fatalf("dbx insert: %v", err)
	}
	id, _ := inserted["id"].(int64)
	if id == 0 {
		t.Fatalf("inserted row = %#v", inserted)
	}

	// Legacy statement must see exactly what dbx wrote.
	legacy, err := legacyList(ctx, runner, "tenant-a", false)
	if err != nil {
		t.Fatalf("legacy list: %v", err)
	}
	if len(legacy) != 1 || legacy[0].ID != id ||
		legacy[0].CanonicalName != "claude-opus-4-6" || legacy[0].Reason != "not procured" ||
		legacy[0].CreatedBy != "admin@example.com" || legacy[0].DeletedAt != nil {
		t.Fatalf("legacy read mismatch: %+v vs dbx %#v", legacy, inserted)
	}

	// dbx select-by-PK must equal the legacy row.
	var selected map[string]any
	err = runner.WithTenantReadOnlyTx(ctx, "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		var ierr error
		selected, ierr = crud.SelectByPK(ctx, tx, "tenant_model_policies", "tenant-a", id)
		return ierr
	})
	if err != nil {
		t.Fatalf("dbx select: %v", err)
	}
	if selected["canonical_name"] != legacy[0].CanonicalName || selected["reason"] != legacy[0].Reason {
		t.Errorf("dbx row %#v != legacy %+v", selected, legacy[0])
	}

	// dbx patch (reason only) vs the legacy UPDATE ... SET reason shape.
	m, _ := reg.Lookup("tenant_model_policies")
	err = runner.WithTenantTx(ctx, "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		patch, perr := dbx.PatchFromMap(m, map[string]any{"reason": "re-reviewed"})
		if perr != nil {
			return perr
		}
		_, ierr := crud.UpdateByPK(ctx, tx, "tenant_model_policies", "tenant-a", id, patch, nil)
		return ierr
	})
	if err != nil {
		t.Fatalf("dbx update: %v", err)
	}
	legacy, err = legacyList(ctx, runner, "tenant-a", false)
	if err != nil || len(legacy) != 1 || legacy[0].Reason != "re-reviewed" {
		t.Fatalf("legacy read after patch: %+v err=%v", legacy, err)
	}

	// dbx soft delete hides the row from the active view but keeps it
	// visible to the legacy include_deleted read (deleted_at IS NOT NULL).
	err = runner.WithTenantTx(ctx, "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		return crud.DeleteByPK(ctx, tx, "tenant_model_policies", "tenant-a", id)
	})
	if err != nil {
		t.Fatalf("dbx soft delete: %v", err)
	}
	if legacy, err = legacyList(ctx, runner, "tenant-a", false); err != nil || len(legacy) != 0 {
		t.Fatalf("active read after soft delete: %+v err=%v", legacy, err)
	}
	if legacy, err = legacyList(ctx, runner, "tenant-a", true); err != nil || len(legacy) != 1 || legacy[0].DeletedAt == nil {
		t.Fatalf("include-deleted read after soft delete: %+v err=%v", legacy, err)
	}
	err = runner.WithTenantReadOnlyTx(ctx, "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		_, ierr := crud.SelectByPK(ctx, tx, "tenant_model_policies", "tenant-a", id)
		return ierr
	})
	if !errors.Is(err, dbx.ErrNotFound) {
		t.Errorf("dbx read after soft delete = %v, want ErrNotFound", err)
	}
}

func TestPilotCrossTenantIsolationAndUnique(t *testing.T) {
	runner, crud, _ := newPilotKit(t)
	ctx := context.Background()

	var idA int64
	err := runner.WithTenantTx(ctx, "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		row, ierr := crud.Insert(ctx, tx, "tenant_model_policies", "tenant-a", map[string]any{
			"canonical_name": "gpt-5-turbo", "reason": "x", "created_by": "a",
		})
		if ierr != nil {
			return ierr
		}
		idA, _ = row["id"].(int64)
		return nil
	})
	if err != nil {
		t.Fatalf("insert tenant-a: %v", err)
	}

	// Cross-tenant read fails closed through both the framework predicate
	// and RLS.
	err = runner.WithTenantReadOnlyTx(ctx, "tenant-b", func(ctx context.Context, tx pgx.Tx) error {
		_, ierr := crud.SelectByPK(ctx, tx, "tenant_model_policies", "tenant-a", idA)
		return ierr
	})
	if !errors.Is(err, dbx.ErrNotFound) {
		t.Fatalf("cross-tenant select = %v, want ErrNotFound", err)
	}

	// The two-column unique (tenant_id, canonical_name) surfaces as the
	// typed unique-violation error.
	err = runner.WithTenantTx(ctx, "tenant-a", func(ctx context.Context, tx pgx.Tx) error {
		_, ierr := crud.Insert(ctx, tx, "tenant_model_policies", "tenant-a", map[string]any{
			"canonical_name": "gpt-5-turbo", "reason": "dup", "created_by": "a",
		})
		return ierr
	})
	if !errors.Is(err, dbx.ErrUniqueViolation) {
		t.Fatalf("duplicate insert = %v, want ErrUniqueViolation", err)
	}

	// The same canonical_name under another tenant is legal (per-tenant
	// uniqueness).
	err = runner.WithTenantTx(ctx, "tenant-b", func(ctx context.Context, tx pgx.Tx) error {
		_, ierr := crud.Insert(ctx, tx, "tenant_model_policies", "tenant-b", map[string]any{
			"canonical_name": "gpt-5-turbo", "reason": "b", "created_by": "b",
		})
		return ierr
	})
	if err != nil {
		t.Fatalf("same name other tenant: %v", err)
	}
}
