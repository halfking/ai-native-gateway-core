// Package testutil provides ephemeral PostgreSQL instances for integration tests.
// Each call to SetupTestDB starts a fresh testcontainer, applies the MaaS v0.1
// schema (+ pricing + quota), seeds canonical fixtures, and registers cleanup.
package testutil

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestDatabase bundles a live container with a ready-to-use connection pool.
type TestDatabase struct {
	Container *postgres.PostgresContainer
	Pool      *pgxpool.Pool
	ConnStr   string
}

// SetupTestDB is the standard entry point for MaaS integration tests.
func SetupTestDB(t *testing.T) *TestDatabase {
	t.Helper()
	ctx := context.Background()

	pgContainer, err := postgres.RunContainer(ctx,
		testcontainers.WithImage("postgres:15-alpine"),
		postgres.WithDatabase("maas_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("failed to start postgres container: %v", err)
	}

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("failed to get connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("failed to create connection pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("failed to ping database: %v", err)
	}

	t.Logf("✓ PostgreSQL container started: %s", connStr)

	if err := runMigrations(ctx, pool); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}
	t.Logf("✓ Migrations completed")

	if err := insertSeedData(ctx, pool); err != nil {
		t.Fatalf("failed to insert seed data: %v", err)
	}
	t.Logf("✓ Seed data inserted")

	t.Cleanup(func() {
		pool.Close()
		if err := pgContainer.Terminate(ctx); err != nil {
			t.Logf("failed to terminate container: %v", err)
		}
	})

	return &TestDatabase{Container: pgContainer, Pool: pool, ConnStr: connStr}
}

// schemaSQL is the complete MaaS contract v0.1 schema: core domains + pricing + quota.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS maas_inventory_snapshots (
    id BIGSERIAL PRIMARY KEY,
    snapshot_id TEXT NOT NULL UNIQUE,
    revision BIGINT NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    stale BOOLEAN NOT NULL DEFAULT FALSE,
    metadata JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS maas_sellable_items (
    id BIGSERIAL PRIMARY KEY,
    snapshot_id TEXT NOT NULL REFERENCES maas_inventory_snapshots(snapshot_id) ON DELETE CASCADE,
    model_sku TEXT NOT NULL,
    sellable_amount TEXT NOT NULL,
    sellable_unit TEXT NOT NULL,
    sellable_window TEXT,
    capacity_available BOOLEAN,
    shared_cap_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS maas_holds (
    id BIGSERIAL PRIMARY KEY,
    hold_id TEXT NOT NULL UNIQUE,
    tenant_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','committed','expired','aborted')),
    fencing_token TEXT NOT NULL,
    inventory_revision BIGINT NOT NULL,
    quote_snapshot_id TEXT,
    resource_snapshot_id TEXT,
    expected_version BIGINT,
    expires_at TIMESTAMPTZ NOT NULL,
    committed_at TIMESTAMPTZ,
    released_at TIMESTAMPTZ,
    expired_at TIMESTAMPTZ,
    idempotency_key TEXT NOT NULL,
    trace_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS maas_hold_lines (
    id BIGSERIAL PRIMARY KEY,
    hold_id TEXT NOT NULL REFERENCES maas_holds(hold_id) ON DELETE CASCADE,
    model_sku TEXT NOT NULL,
    quantity_amount TEXT NOT NULL,
    quantity_unit TEXT NOT NULL,
    quantity_window TEXT,
    shared_cap_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS maas_quotes (
    id BIGSERIAL PRIMARY KEY,
    quote_id TEXT NOT NULL UNIQUE,
    tenant_id TEXT NOT NULL,
    quote_snapshot_id TEXT NOT NULL,
    price_version_id TEXT NOT NULL,
    resource_snapshot_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('reference')),
    money_amount_minor TEXT NOT NULL,
    money_currency TEXT NOT NULL,
    expires_at TIMESTAMPTZ,
    hold_created BOOLEAN NOT NULL DEFAULT FALSE CHECK (hold_created = FALSE),
    trace_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS maas_entitlements (
    id BIGSERIAL PRIMARY KEY,
    entitlement_id TEXT NOT NULL UNIQUE,
    tenant_id TEXT NOT NULL,
    hold_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active','suspended','revoked')),
    legal_signoff_ref TEXT,
    contract_id TEXT,
    activated_at TIMESTAMPTZ,
    idempotency_key TEXT NOT NULL,
    trace_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS maas_usage_holds (
    id BIGSERIAL PRIMARY KEY,
    usage_hold_id TEXT NOT NULL UNIQUE,
    tenant_id TEXT NOT NULL,
    entitlement_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    quantity_amount TEXT NOT NULL,
    quantity_unit TEXT NOT NULL,
    model_sku TEXT,
    status TEXT NOT NULL CHECK (status IN ('held','settled','posted')),
    expires_at TIMESTAMPTZ,
    idempotency_key TEXT NOT NULL,
    trace_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS maas_usage_settlements (
    id BIGSERIAL PRIMARY KEY,
    usage_hold_id TEXT NOT NULL REFERENCES maas_usage_holds(usage_hold_id),
    settled_amount TEXT NOT NULL,
    settled_unit TEXT NOT NULL,
    released_amount TEXT NOT NULL,
    released_unit TEXT NOT NULL,
    stream_interrupted BOOLEAN NOT NULL DEFAULT FALSE,
    entry_id TEXT,
    settled_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS maas_usage_corrections (
    id BIGSERIAL PRIMARY KEY,
    reversal_id TEXT NOT NULL UNIQUE,
    tenant_id TEXT NOT NULL,
    entry_id TEXT NOT NULL,
    reversal_amount TEXT NOT NULL,
    reversal_unit TEXT NOT NULL,
    reason TEXT,
    status TEXT NOT NULL DEFAULT 'posted',
    idempotency_key TEXT NOT NULL,
    trace_id TEXT,
    corrected_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS maas_idempotency_log (
    id BIGSERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    command_type TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_payload JSONB NOT NULL,
    response_payload JSONB NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('success','error')),
    executed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, command_type, idempotency_key)
);

CREATE TABLE IF NOT EXISTS maas_price_versions (
    id BIGSERIAL PRIMARY KEY,
    price_version_id TEXT NOT NULL UNIQUE,
    effective_from TIMESTAMPTZ NOT NULL,
    effective_until TIMESTAMPTZ,
    version_number INT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('draft','published','superseded','archived')),
    metadata JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS maas_price_rules (
    id BIGSERIAL PRIMARY KEY,
    price_version_id TEXT NOT NULL REFERENCES maas_price_versions(price_version_id) ON DELETE CASCADE,
    rule_id TEXT NOT NULL UNIQUE,
    model_sku TEXT NOT NULL,
    unit_price_minor TEXT NOT NULL,
    currency TEXT NOT NULL,
    unit TEXT NOT NULL,
    min_quantity TEXT,
    max_quantity TEXT,
    region TEXT,
    commitment_type TEXT,
    time_window_start TIME,
    time_window_end TIME,
    metadata JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS maas_price_modifiers (
    id BIGSERIAL PRIMARY KEY,
    modifier_id TEXT NOT NULL UNIQUE,
    price_version_id TEXT NOT NULL REFERENCES maas_price_versions(price_version_id) ON DELETE CASCADE,
    modifier_type TEXT NOT NULL CHECK (modifier_type IN ('discount','markup','campaign','volume')),
    target_type TEXT NOT NULL CHECK (target_type IN ('global','model','tenant','tenant_model')),
    model_sku TEXT,
    tenant_id TEXT,
    modifier_value_type TEXT NOT NULL CHECK (modifier_value_type IN ('percentage','fixed_amount')),
    modifier_value TEXT NOT NULL,
    modifier_currency TEXT,
    min_quantity TEXT,
    priority INT NOT NULL DEFAULT 0,
    valid_from TIMESTAMPTZ NOT NULL,
    valid_until TIMESTAMPTZ,
    campaign_code TEXT,
    max_uses INT,
    current_uses INT NOT NULL DEFAULT 0,
    metadata JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS maas_quote_lines (
    id BIGSERIAL PRIMARY KEY,
    quote_id TEXT NOT NULL,
    line_number INT NOT NULL,
    model_sku TEXT NOT NULL,
    quantity_amount TEXT NOT NULL,
    quantity_unit TEXT NOT NULL,
    unit_price_minor TEXT NOT NULL,
    subtotal_minor TEXT NOT NULL,
    discount_minor TEXT,
    total_minor TEXT NOT NULL,
    currency TEXT NOT NULL,
    applied_modifiers JSONB,
    price_version_id TEXT NOT NULL,
    price_rule_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS maas_entitlement_quotas (
    id BIGSERIAL PRIMARY KEY,
    entitlement_id TEXT NOT NULL REFERENCES maas_entitlements(entitlement_id) ON DELETE CASCADE,
    model_sku TEXT NOT NULL,
    allocated_amount TEXT NOT NULL,
    allocated_unit TEXT NOT NULL,
    allocated_window TEXT,
    consumed_amount TEXT NOT NULL DEFAULT '0',
    consumed_unit TEXT NOT NULL,
    held_amount TEXT NOT NULL DEFAULT '0',
    held_unit TEXT NOT NULL,
    quota_version BIGINT NOT NULL DEFAULT 1,
    last_reset_at TIMESTAMPTZ,
    next_reset_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (entitlement_id, model_sku)
);

CREATE OR REPLACE FUNCTION maas_get_active_price_version(at_time TIMESTAMPTZ DEFAULT NOW())
RETURNS TEXT AS $$
    SELECT price_version_id
    FROM maas_price_versions
    WHERE status = 'published'
      AND effective_from <= at_time
      AND (effective_until IS NULL OR effective_until > at_time)
    ORDER BY effective_from DESC
    LIMIT 1;
$$ LANGUAGE SQL STABLE;

CREATE OR REPLACE FUNCTION maas_calculate_price(
    p_price_version_id TEXT,
    p_model_sku TEXT,
    p_quantity TEXT,
    p_region TEXT DEFAULT NULL,
    p_commitment_type TEXT DEFAULT 'on_demand'
)
RETURNS TABLE (unit_price_minor TEXT, total_minor TEXT, currency TEXT, rule_id TEXT) AS $$
BEGIN
    RETURN QUERY
    SELECT
        pr.unit_price_minor,
        -- 单位归一:unit_price_minor 以规则 unit 为基准('1m_tokens'=每百万token),
        -- p_quantity 为原始量;不归一则超收 1e6 倍(2026-11-21 合并验证轮实测抓出)。
        (pr.unit_price_minor::DECIMAL * p_quantity::DECIMAL
           / CASE pr.unit
               WHEN '1m_tokens' THEN 1000000
               WHEN '1k_tokens' THEN 1000
               ELSE 1
             END)::TEXT,
        pr.currency,
        pr.rule_id
    FROM maas_price_rules pr
    WHERE pr.price_version_id = p_price_version_id
      AND pr.model_sku = p_model_sku
      AND (pr.region IS NULL OR pr.region = p_region)
      AND (pr.commitment_type IS NULL OR pr.commitment_type = p_commitment_type)
      AND (pr.min_quantity IS NULL OR p_quantity::DECIMAL >= pr.min_quantity::DECIMAL)
      AND (pr.max_quantity IS NULL OR p_quantity::DECIMAL <= pr.max_quantity::DECIMAL)
    ORDER BY
        (pr.region IS NOT NULL)::INT DESC,
        (pr.commitment_type IS NOT NULL)::INT DESC,
        pr.min_quantity::DECIMAL DESC NULLS LAST
    LIMIT 1;
END;
$$ LANGUAGE plpgsql STABLE;

CREATE OR REPLACE FUNCTION maas_check_quota_available(
    p_entitlement_id TEXT,
    p_model_sku TEXT,
    p_requested_amount TEXT
)
RETURNS BOOLEAN AS $$
DECLARE
    v_allocated DECIMAL;
    v_consumed DECIMAL;
    v_held DECIMAL;
    v_requested DECIMAL;
    v_available DECIMAL;
BEGIN
    SELECT allocated_amount::DECIMAL, consumed_amount::DECIMAL, held_amount::DECIMAL
    INTO v_allocated, v_consumed, v_held
    FROM maas_entitlement_quotas
    WHERE entitlement_id = p_entitlement_id AND model_sku = p_model_sku;

    IF NOT FOUND THEN
        RETURN FALSE;
    END IF;

    v_requested := p_requested_amount::DECIMAL;
    v_available := v_allocated - (v_consumed + v_held);
    RETURN v_requested <= v_available;
END;
$$ LANGUAGE plpgsql STABLE;

CREATE INDEX IF NOT EXISTS idx_maas_holds_tenant_status ON maas_holds(tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_maas_sellable_items_model_sku ON maas_sellable_items(model_sku);
CREATE INDEX IF NOT EXISTS idx_maas_quote_lines_quote_id ON maas_quote_lines(quote_id);
`

// seedSQL provides canonical fixtures: published price version, base GPT-4 rules,
// a volume discount, and one healthy inventory snapshot.
const seedSQL = `
INSERT INTO maas_price_versions (price_version_id, effective_from, version_number, status, published_at)
VALUES ('prv_2026_11_initial', '2026-11-01 00:00:00+00', 1, 'published', NOW())
ON CONFLICT (price_version_id) DO NOTHING;

INSERT INTO maas_price_rules (price_version_id, rule_id, model_sku, unit_price_minor, currency, unit)
VALUES
    ('prv_2026_11_initial', 'rule_gpt4_base', 'gpt-4', '3000', 'USD', '1m_tokens'),
    ('prv_2026_11_initial', 'rule_gpt4_output', 'gpt-4-output', '6000', 'USD', '1m_tokens')
ON CONFLICT (rule_id) DO NOTHING;

INSERT INTO maas_price_modifiers (
    modifier_id, price_version_id, modifier_type, target_type, model_sku,
    modifier_value_type, modifier_value, min_quantity, priority, valid_from
)
VALUES (
    'mod_volume_10m', 'prv_2026_11_initial', 'volume', 'model', 'gpt-4',
    'percentage', '10', '10000000', 100, '2026-11-01 00:00:00+00'
)
ON CONFLICT (modifier_id) DO NOTHING;

INSERT INTO maas_inventory_snapshots (snapshot_id, revision, observed_at, stale)
VALUES ('snap_test_001', 1, NOW(), false)
ON CONFLICT (snapshot_id) DO NOTHING;

INSERT INTO maas_sellable_items (
    snapshot_id, model_sku, sellable_amount, sellable_unit, sellable_window, capacity_available
)
VALUES ('snap_test_001', 'gpt-4', '1000000000', 'token', 'monthly', true);
`

func runMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

func insertSeedData(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, seedSQL); err != nil {
		return fmt.Errorf("seed data: %w", err)
	}
	return nil
}
