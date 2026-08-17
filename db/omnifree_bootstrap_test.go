package db

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestOmniFreeBootstrap_MatchesMigrationContract verifies that
// ensureOmniFreeSchema alone (without running the full ApplyMigrations
// chain) produces a schema compatible with sql/migrations/075-omnifree-schema.sql,
// cmd/seed-free-resources, and domains/autocombo's SELECT contracts.
//
// Why this test exists (round 4 audit, 2026-08-09):
//
// ensureOmniFreeSchema used to maintain a schema shape completely different
// from sql/migrations/075-omnifree-schema.sql (different column names,
// different constraints, different function signatures). Any environment
// that booted the gateway against a fresh database (calling db.Open()
// before ever running the 075 migration) would get an incompatible
// free_resource_catalog / auto_combo_templates / keyless_providers shape,
// and cmd/seed-free-resources + domains/autocombo would fail with
// "column does not exist" at runtime. This test locks the two schema
// paths together so a future edit to either one that breaks parity fails
// CI instead of surfacing as a production 500.
//
// Run with: BOOTSTRAP_TEST_DSN=postgres://... go test ./db -run TestOmniFreeBootstrap_MatchesMigrationContract -v
func TestOmniFreeBootstrap_MatchesMigrationContract(t *testing.T) {
	dsn := os.Getenv("BOOTSTRAP_TEST_DSN")
	if dsn == "" {
		t.Skip("BOOTSTRAP_TEST_DSN not set; skipping bootstrap-vs-migration parity test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	// get_current_tenant() is normally created earlier in ApplyMigrations;
	// ensureOmniFreeSchema's RLS policies depend on it.
	if _, err := pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION public.get_current_tenant()
		RETURNS text LANGUAGE sql STABLE
		AS $$ SELECT COALESCE(NULLIF(current_setting('app.current_tenant', true), ''), 'default'); $$;
	`); err != nil {
		t.Fatalf("create get_current_tenant: %v", err)
	}

	d := &DB{pool: pool}
	if err := d.ensureOmniFreeSchema(ctx); err != nil {
		t.Fatalf("ensureOmniFreeSchema (1st run): %v", err)
	}
	// Idempotency check: running twice must not fail (all DDL is
	// CREATE IF NOT EXISTS / ADD COLUMN IF NOT EXISTS / CREATE OR REPLACE).
	if err := d.ensureOmniFreeSchema(ctx); err != nil {
		t.Fatalf("ensureOmniFreeSchema (2nd run, idempotency): %v", err)
	}

	// Verify the exact columns the seed command / resolver / factory expect.
	requiredCols := map[string][]string{
		"free_resource_catalog": {"provider_code", "model_id", "display_name", "free_type",
			"monthly_tokens", "daily_tokens", "credit_tokens", "pool_key", "tos_verdict",
			"tos_notes", "constraints_json", "discovery_method", "verified_at", "enabled",
			"tenant_id", "trains_on_prompts"},
		"auto_combo_templates": {"combo_name", "display_name", "variant", "tier_filter",
			"free_type_filter", "tos_filter", "provider_allowlist", "provider_denylist",
			"scoring_weights_json", "max_candidates", "exploration_rate", "enabled",
			"priority", "tenant_id"},
		"keyless_providers": {"provider_code", "display_name", "auth_hint", "bootstrap_method",
			"rpm_limit", "rpd_limit", "concurrent_limit", "reliability_score", "enabled",
			"allowlist_in_auto_combo", "notes", "tenant_id"},
	}
	for tbl, cols := range requiredCols {
		for _, col := range cols {
			var exists bool
			if err := pool.QueryRow(ctx, `
				SELECT EXISTS (SELECT 1 FROM information_schema.columns
					WHERE table_schema='public' AND table_name=$1 AND column_name=$2)
			`, tbl, col).Scan(&exists); err != nil {
				t.Fatalf("check column %s.%s: %v", tbl, col, err)
			}
			if !exists {
				t.Errorf("MISSING COLUMN: %s.%s (required by seed/resolver/factory contract)", tbl, col)
			}
		}
	}

	// tos_verdict CHECK constraint must accept the seed data's full enum,
	// including 'ambiguous' (configs/seed/free_resource_catalog.json).
	if _, err := pool.Exec(ctx, `
		INSERT INTO free_resource_catalog (provider_code, model_id, display_name, free_type, tos_verdict, tenant_id)
		VALUES ('bootstrap-test', 'bootstrap-model', 'Test', 'keyless', 'ambiguous', 'default')
	`); err != nil {
		t.Errorf("tos_verdict CHECK rejects 'ambiguous' (seed contract mismatch): %v", err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM free_resource_catalog WHERE provider_code = 'bootstrap-test'`)

	t.Log("bootstrap schema matches migration/seed/resolver contract")
}
