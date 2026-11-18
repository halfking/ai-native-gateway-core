-- MaaS Contract v0.1 Database Schema
-- Phase 2: M1 Domain Implementation
-- Strategy: Same database, different tables (PostgreSQL schema isolation)

-- ============================================================================
-- INVENTORY DOMAIN
-- ============================================================================

-- Inventory snapshots track point-in-time sellable availability
CREATE TABLE IF NOT EXISTS maas_inventory_snapshots (
    id BIGSERIAL PRIMARY KEY,
    snapshot_id TEXT NOT NULL UNIQUE,  -- snap_*
    revision BIGINT NOT NULL,          -- Optimistic concurrency control version
    observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    stale BOOLEAN NOT NULL DEFAULT FALSE,
    metadata JSONB,                    -- Additional snapshot metadata
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_maas_inventory_snapshots_observed ON maas_inventory_snapshots(observed_at DESC);
CREATE INDEX idx_maas_inventory_snapshots_revision ON maas_inventory_snapshots(revision);

-- Sellable items represent inventory availability in a snapshot
CREATE TABLE IF NOT EXISTS maas_sellable_items (
    id BIGSERIAL PRIMARY KEY,
    snapshot_id TEXT NOT NULL REFERENCES maas_inventory_snapshots(snapshot_id) ON DELETE CASCADE,
    model_sku TEXT NOT NULL,
    sellable_amount TEXT NOT NULL,     -- Decimal string
    sellable_unit TEXT NOT NULL,
    sellable_window TEXT,              -- e.g., "monthly", "daily"
    capacity_available BOOLEAN,
    shared_cap_id TEXT,                -- Shared supplier capacity ID
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_maas_sellable_items_snapshot ON maas_sellable_items(snapshot_id);
CREATE INDEX idx_maas_sellable_items_model_sku ON maas_sellable_items(model_sku);

-- Holds represent inventory reservations (soft and hard)
-- State machine: pending → committed | expired | aborted
CREATE TABLE IF NOT EXISTS maas_holds (
    id BIGSERIAL PRIMARY KEY,
    hold_id TEXT NOT NULL UNIQUE,      -- hld_*
    tenant_id TEXT NOT NULL,           -- FK to tenants table
    status TEXT NOT NULL CHECK (status IN ('pending', 'committed', 'expired', 'aborted')),
    fencing_token TEXT NOT NULL,       -- Idempotency fence for confirm/release
    inventory_revision BIGINT NOT NULL,
    quote_snapshot_id TEXT,
    resource_snapshot_id TEXT,
    expected_version BIGINT,
    expires_at TIMESTAMPTZ NOT NULL,
    committed_at TIMESTAMPTZ,
    released_at TIMESTAMPTZ,
    expired_at TIMESTAMPTZ,
    idempotency_key TEXT NOT NULL,     -- Command idempotency
    trace_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_maas_holds_hold_id ON maas_holds(hold_id);
CREATE INDEX idx_maas_holds_tenant_status ON maas_holds(tenant_id, status);
CREATE INDEX idx_maas_holds_expires_at ON maas_holds(expires_at) WHERE status = 'pending';
CREATE INDEX idx_maas_holds_idempotency ON maas_holds(tenant_id, idempotency_key);

-- Hold lines represent individual items in a hold
CREATE TABLE IF NOT EXISTS maas_hold_lines (
    id BIGSERIAL PRIMARY KEY,
    hold_id TEXT NOT NULL REFERENCES maas_holds(hold_id) ON DELETE CASCADE,
    model_sku TEXT NOT NULL,
    quantity_amount TEXT NOT NULL,     -- Decimal string
    quantity_unit TEXT NOT NULL,
    quantity_window TEXT,
    shared_cap_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_maas_hold_lines_hold_id ON maas_hold_lines(hold_id);

-- ============================================================================
-- COMMERCIAL (QUOTING) DOMAIN
-- ============================================================================

-- Quotes represent non-binding price estimates
CREATE TABLE IF NOT EXISTS maas_quotes (
    id BIGSERIAL PRIMARY KEY,
    quote_id TEXT NOT NULL UNIQUE,     -- quo_*
    tenant_id TEXT NOT NULL,
    quote_snapshot_id TEXT NOT NULL,
    price_version_id TEXT NOT NULL,
    resource_snapshot_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('reference')), -- v0.1 only has reference quotes
    money_amount_minor TEXT NOT NULL,  -- Decimal string (minor units)
    money_currency TEXT NOT NULL,
    expires_at TIMESTAMPTZ,
    hold_created BOOLEAN NOT NULL DEFAULT FALSE CHECK (hold_created = FALSE), -- MUST be false
    trace_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_maas_quotes_quote_id ON maas_quotes(quote_id);
CREATE INDEX idx_maas_quotes_tenant ON maas_quotes(tenant_id);
CREATE INDEX idx_maas_quotes_created_at ON maas_quotes(created_at DESC);

-- ============================================================================
-- ENTITLEMENT (TRADING) DOMAIN
-- ============================================================================

-- Entitlements represent customer rights to consume resources
CREATE TABLE IF NOT EXISTS maas_entitlements (
    id BIGSERIAL PRIMARY KEY,
    entitlement_id TEXT NOT NULL UNIQUE, -- ent_*
    tenant_id TEXT NOT NULL,
    hold_id TEXT NOT NULL,               -- Reference to committed hold
    status TEXT NOT NULL CHECK (status IN ('active', 'suspended', 'revoked')),
    legal_signoff_ref TEXT,
    contract_id TEXT,
    activated_at TIMESTAMPTZ,
    idempotency_key TEXT NOT NULL,
    trace_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_maas_entitlements_entitlement_id ON maas_entitlements(entitlement_id);
CREATE INDEX idx_maas_entitlements_tenant_status ON maas_entitlements(tenant_id, status);
CREATE INDEX idx_maas_entitlements_hold_id ON maas_entitlements(hold_id);
CREATE INDEX idx_maas_entitlements_idempotency ON maas_entitlements(tenant_id, idempotency_key);

-- ============================================================================
-- LEDGER DOMAIN
-- ============================================================================

-- Usage holds represent request-level usage reservations
CREATE TABLE IF NOT EXISTS maas_usage_holds (
    id BIGSERIAL PRIMARY KEY,
    usage_hold_id TEXT NOT NULL UNIQUE, -- usg_*
    tenant_id TEXT NOT NULL,
    entitlement_id TEXT NOT NULL,       -- FK to maas_entitlements
    request_id TEXT NOT NULL,           -- External request ID for idempotency
    quantity_amount TEXT NOT NULL,      -- Decimal string
    quantity_unit TEXT NOT NULL,
    model_sku TEXT,
    status TEXT NOT NULL CHECK (status IN ('held', 'settled', 'posted')),
    expires_at TIMESTAMPTZ,
    idempotency_key TEXT NOT NULL,
    trace_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_maas_usage_holds_usage_hold_id ON maas_usage_holds(usage_hold_id);
CREATE INDEX idx_maas_usage_holds_tenant ON maas_usage_holds(tenant_id);
CREATE INDEX idx_maas_usage_holds_entitlement ON maas_usage_holds(entitlement_id);
CREATE INDEX idx_maas_usage_holds_request_id ON maas_usage_holds(request_id);
CREATE INDEX idx_maas_usage_holds_idempotency ON maas_usage_holds(tenant_id, idempotency_key);

-- Usage settlements record actual consumption
CREATE TABLE IF NOT EXISTS maas_usage_settlements (
    id BIGSERIAL PRIMARY KEY,
    usage_hold_id TEXT NOT NULL REFERENCES maas_usage_holds(usage_hold_id),
    settled_amount TEXT NOT NULL,       -- Actual consumed amount (decimal string)
    settled_unit TEXT NOT NULL,
    released_amount TEXT NOT NULL,      -- Released unused amount (decimal string)
    released_unit TEXT NOT NULL,
    stream_interrupted BOOLEAN NOT NULL DEFAULT FALSE,
    entry_id TEXT,                      -- Ledger entry reference
    settled_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_maas_usage_settlements_usage_hold_id ON maas_usage_settlements(usage_hold_id);
CREATE INDEX idx_maas_usage_settlements_settled_at ON maas_usage_settlements(settled_at DESC);

-- Usage corrections represent append-only ledger adjustments
CREATE TABLE IF NOT EXISTS maas_usage_corrections (
    id BIGSERIAL PRIMARY KEY,
    reversal_id TEXT NOT NULL UNIQUE,   -- Unique correction ID
    tenant_id TEXT NOT NULL,
    entry_id TEXT NOT NULL,             -- Original ledger entry
    reversal_amount TEXT NOT NULL,      -- Reversal quantity (decimal string)
    reversal_unit TEXT NOT NULL,
    reason TEXT,
    status TEXT NOT NULL DEFAULT 'posted',
    idempotency_key TEXT NOT NULL,
    trace_id TEXT,
    corrected_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_maas_usage_corrections_reversal_id ON maas_usage_corrections(reversal_id);
CREATE INDEX idx_maas_usage_corrections_entry_id ON maas_usage_corrections(entry_id);
CREATE INDEX idx_maas_usage_corrections_tenant ON maas_usage_corrections(tenant_id);
CREATE INDEX idx_maas_usage_corrections_idempotency ON maas_usage_corrections(tenant_id, idempotency_key);

-- ============================================================================
-- IDEMPOTENCY TRACKING (Cross-domain)
-- ============================================================================

-- Idempotency log tracks command execution for replay detection
CREATE TABLE IF NOT EXISTS maas_idempotency_log (
    id BIGSERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    command_type TEXT NOT NULL,        -- e.g., "HoldCreate", "UsageSettle"
    idempotency_key TEXT NOT NULL,
    request_payload JSONB NOT NULL,
    response_payload JSONB NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('success', 'error')),
    executed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL    -- TTL for cleanup (e.g., 7 days)
);

CREATE UNIQUE INDEX idx_maas_idempotency_log_key ON maas_idempotency_log(tenant_id, command_type, idempotency_key);
CREATE INDEX idx_maas_idempotency_log_expires_at ON maas_idempotency_log(expires_at);

-- ============================================================================
-- COMMENTS & DESIGN NOTES
-- ============================================================================

COMMENT ON TABLE maas_holds IS 'Inventory holds (soft and hard reservations) per contract v0.1';
COMMENT ON COLUMN maas_holds.fencing_token IS 'Used for idempotent confirm/release operations (prevents concurrent modifications)';
COMMENT ON COLUMN maas_holds.inventory_revision IS 'OCC version from inventory snapshot at hold creation time';

COMMENT ON TABLE maas_quotes IS 'Non-binding price estimates. hold_created MUST always be false per contract v0.1';
COMMENT ON COLUMN maas_quotes.hold_created IS 'MUST be false - EstimateQuote never creates holds';

COMMENT ON TABLE maas_entitlements IS 'Customer rights to consume resources, activated after committed hold';
COMMENT ON COLUMN maas_entitlements.hold_id IS 'Must reference a hold in committed status';

COMMENT ON TABLE maas_usage_holds IS 'Request-level usage reservations against activated entitlements';
COMMENT ON COLUMN maas_usage_holds.request_id IS 'External request ID for correlation';

COMMENT ON TABLE maas_usage_corrections IS 'Append-only ledger corrections (never modify posted entries)';

COMMENT ON TABLE maas_idempotency_log IS 'Cross-domain idempotency tracking for command replay detection';
