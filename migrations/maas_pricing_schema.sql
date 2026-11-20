-- MaaS Pricing Schema - Extension to maas_v01_schema.sql
-- Phase 2: Commercial Domain (Quoting)

-- ============================================================================
-- PRICING DOMAIN
-- ============================================================================

-- Price versions track pricing snapshots over time
-- Each version is immutable once published (append-only history)
CREATE TABLE IF NOT EXISTS maas_price_versions (
    id BIGSERIAL PRIMARY KEY,
    price_version_id TEXT NOT NULL UNIQUE, -- prv_*
    effective_from TIMESTAMPTZ NOT NULL,
    effective_until TIMESTAMPTZ,           -- NULL means currently active
    version_number INT NOT NULL,           -- Sequential version counter
    status TEXT NOT NULL CHECK (status IN ('draft', 'published', 'superseded', 'archived')),
    metadata JSONB,                        -- Additional version metadata (e.g., approval info)
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_at TIMESTAMPTZ,
    
    -- Ensure no overlapping published versions
    EXCLUDE USING gist (
        tstzrange(effective_from, effective_until, '[)') WITH &&
    ) WHERE (status = 'published')
);

CREATE INDEX idx_maas_price_versions_effective ON maas_price_versions(effective_from, effective_until) 
    WHERE status = 'published';
CREATE INDEX idx_maas_price_versions_status ON maas_price_versions(status);

-- Price rules define unit pricing for models
-- Multiple rules per version allow tiered/volume pricing
CREATE TABLE IF NOT EXISTS maas_price_rules (
    id BIGSERIAL PRIMARY KEY,
    price_version_id TEXT NOT NULL REFERENCES maas_price_versions(price_version_id) ON DELETE CASCADE,
    rule_id TEXT NOT NULL UNIQUE,          -- rule_*
    model_sku TEXT NOT NULL,
    
    -- Pricing structure
    unit_price_minor TEXT NOT NULL,        -- Price per unit in minor currency units (decimal string)
    currency TEXT NOT NULL,                -- ISO 4217 code
    unit TEXT NOT NULL,                    -- Pricing unit (e.g., "token", "1k_tokens", "request")
    
    -- Optional tiering/volume discounts
    min_quantity TEXT,                     -- Minimum quantity for this rule to apply (decimal string)
    max_quantity TEXT,                     -- Maximum quantity (NULL = unlimited)
    
    -- Regional/dimensional pricing
    region TEXT,                           -- Optional regional pricing
    commitment_type TEXT,                  -- e.g., "on_demand", "committed", "spot"
    
    -- Time-based pricing (e.g., peak/off-peak)
    time_window_start TIME,                -- Optional time-of-day pricing
    time_window_end TIME,
    
    metadata JSONB,                        -- Additional rule metadata
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_maas_price_rules_version ON maas_price_rules(price_version_id);
CREATE INDEX idx_maas_price_rules_model_sku ON maas_price_rules(model_sku);
CREATE INDEX idx_maas_price_rules_lookup ON maas_price_rules(price_version_id, model_sku, region, commitment_type);

-- Price modifiers define discounts, markups, and campaigns
-- Applied on top of base price rules
CREATE TABLE IF NOT EXISTS maas_price_modifiers (
    id BIGSERIAL PRIMARY KEY,
    modifier_id TEXT NOT NULL UNIQUE,      -- mod_*
    price_version_id TEXT NOT NULL REFERENCES maas_price_versions(price_version_id) ON DELETE CASCADE,
    
    modifier_type TEXT NOT NULL CHECK (modifier_type IN ('discount', 'markup', 'campaign', 'volume')),
    target_type TEXT NOT NULL CHECK (target_type IN ('global', 'model', 'tenant', 'tenant_model')),
    
    -- Target scope
    model_sku TEXT,                        -- NULL for global modifiers
    tenant_id TEXT,                        -- NULL for non-tenant-specific modifiers
    
    -- Modifier value
    modifier_value_type TEXT NOT NULL CHECK (modifier_value_type IN ('percentage', 'fixed_amount')),
    modifier_value TEXT NOT NULL,          -- Decimal string (e.g., "10" for 10%, "500" for $5.00)
    modifier_currency TEXT,                -- Required if modifier_value_type='fixed_amount'
    
    -- Application rules
    min_quantity TEXT,                     -- Minimum quantity to qualify (decimal string)
    priority INT NOT NULL DEFAULT 0,       -- Higher priority applied first
    
    -- Time validity
    valid_from TIMESTAMPTZ NOT NULL,
    valid_until TIMESTAMPTZ,               -- NULL = no expiration
    
    -- Campaign tracking
    campaign_code TEXT,                    -- Optional campaign/promo code
    max_uses INT,                          -- NULL = unlimited
    current_uses INT NOT NULL DEFAULT 0,
    
    metadata JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_maas_price_modifiers_version ON maas_price_modifiers(price_version_id);
CREATE INDEX idx_maas_price_modifiers_target ON maas_price_modifiers(target_type, model_sku, tenant_id);
CREATE INDEX idx_maas_price_modifiers_validity ON maas_price_modifiers(valid_from, valid_until) 
    WHERE valid_until IS NULL OR valid_until > NOW();
CREATE INDEX idx_maas_price_modifiers_campaign ON maas_price_modifiers(campaign_code) 
    WHERE campaign_code IS NOT NULL;

-- Quote line items (detailed breakdown of quotes)
-- Denormalized for audit and historical reporting
CREATE TABLE IF NOT EXISTS maas_quote_lines (
    id BIGSERIAL PRIMARY KEY,
    quote_id TEXT NOT NULL,                -- FK to maas_quotes.quote_id (to be added)
    line_number INT NOT NULL,              -- Sequential line number within quote
    
    model_sku TEXT NOT NULL,
    quantity_amount TEXT NOT NULL,         -- Decimal string
    quantity_unit TEXT NOT NULL,
    
    -- Pricing breakdown
    unit_price_minor TEXT NOT NULL,        -- Base unit price (minor currency units)
    subtotal_minor TEXT NOT NULL,          -- quantity * unit_price
    discount_minor TEXT,                   -- Total discount applied
    total_minor TEXT NOT NULL,             -- Subtotal - discount
    currency TEXT NOT NULL,
    
    -- Applied modifiers (for audit trail)
    applied_modifiers JSONB,               -- Array of {modifier_id, modifier_type, value}
    
    -- Metadata
    price_version_id TEXT NOT NULL,
    price_rule_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_maas_quote_lines_quote_id ON maas_quote_lines(quote_id);
CREATE INDEX idx_maas_quote_lines_model_sku ON maas_quote_lines(model_sku);

-- ============================================================================
-- HELPER FUNCTIONS
-- ============================================================================

-- Get active price version at a given timestamp
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

-- Calculate price for a model and quantity
-- Returns (unit_price_minor, total_minor, currency)
-- Note: This is a simplified version; real implementation needs modifier application
CREATE OR REPLACE FUNCTION maas_calculate_price(
    p_price_version_id TEXT,
    p_model_sku TEXT,
    p_quantity TEXT,
    p_region TEXT DEFAULT NULL,
    p_commitment_type TEXT DEFAULT 'on_demand'
)
RETURNS TABLE (
    unit_price_minor TEXT,
    total_minor TEXT,
    currency TEXT,
    rule_id TEXT
) AS $$
BEGIN
    -- Find applicable price rule
    -- Priority: region + commitment > region only > commitment only > base
    RETURN QUERY
    SELECT 
        pr.unit_price_minor,
        -- 单位归一：unit_price_minor 以规则 unit 为基准('1m_tokens'=每百万token),
        -- p_quantity 为原始量;不归一则超收 1e6 倍(2026-11-21 合并验证轮实测抓出)。
        (pr.unit_price_minor::DECIMAL * p_quantity::DECIMAL
           / CASE pr.unit
               WHEN '1m_tokens' THEN 1000000
               WHEN '1k_tokens' THEN 1000
               ELSE 1
             END)::TEXT as total_minor,
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
        (pr.region IS NOT NULL)::INT DESC,           -- Prefer region-specific
        (pr.commitment_type IS NOT NULL)::INT DESC,  -- Prefer commitment-specific
        pr.min_quantity::DECIMAL DESC NULLS LAST     -- Prefer higher tier
    LIMIT 1;
END;
$$ LANGUAGE plpgsql STABLE;

-- ============================================================================
-- COMMENTS & DESIGN NOTES
-- ============================================================================

COMMENT ON TABLE maas_price_versions IS 'Immutable pricing snapshots with time-based versioning';
COMMENT ON COLUMN maas_price_versions.effective_until IS 'NULL means currently active; superseded versions have a set end date';

COMMENT ON TABLE maas_price_rules IS 'Base unit pricing rules with optional tiering and dimensional pricing';
COMMENT ON COLUMN maas_price_rules.unit_price_minor IS 'Price in minor currency units (e.g., cents). Stored as decimal string for precision';
COMMENT ON COLUMN maas_price_rules.min_quantity IS 'Volume tier threshold. NULL means starts from 0';

COMMENT ON TABLE maas_price_modifiers IS 'Discounts, markups, and campaign rules applied on top of base pricing';
COMMENT ON COLUMN maas_price_modifiers.priority IS 'Higher priority modifiers applied first (e.g., campaign before volume discount)';
COMMENT ON COLUMN maas_price_modifiers.max_uses IS 'Usage limit for campaigns. NULL = unlimited';

COMMENT ON TABLE maas_quote_lines IS 'Denormalized quote line items for audit and historical reporting';
COMMENT ON COLUMN maas_quote_lines.applied_modifiers IS 'JSONB array preserving which modifiers were applied for audit trail';

COMMENT ON FUNCTION maas_get_active_price_version IS 'Helper to find the currently active price version at a given timestamp';
COMMENT ON FUNCTION maas_calculate_price IS 'Simplified price calculation. Real implementation should apply modifiers in priority order';

-- ============================================================================
-- SEED DATA (Example - should be replaced with actual pricing)
-- ============================================================================

-- Example: Create initial price version
INSERT INTO maas_price_versions (price_version_id, effective_from, version_number, status)
VALUES ('prv_2026_11_initial', '2026-11-01 00:00:00+00', 1, 'published')
ON CONFLICT (price_version_id) DO NOTHING;

-- Example: Add base pricing for GPT-4
INSERT INTO maas_price_rules (price_version_id, rule_id, model_sku, unit_price_minor, currency, unit)
VALUES 
    ('prv_2026_11_initial', 'rule_gpt4_input', 'gpt-4', '3000', 'USD', '1m_tokens'),   -- $0.03 per 1M input tokens
    ('prv_2026_11_initial', 'rule_gpt4_output', 'gpt-4-output', '6000', 'USD', '1m_tokens')  -- $0.06 per 1M output tokens
ON CONFLICT (rule_id) DO NOTHING;

-- Example: Add volume discount (>10M tokens get 10% off)
INSERT INTO maas_price_modifiers (modifier_id, price_version_id, modifier_type, target_type, model_sku, 
                                  modifier_value_type, modifier_value, min_quantity, priority, valid_from)
VALUES ('mod_volume_10m', 'prv_2026_11_initial', 'volume', 'model', 'gpt-4', 
        'percentage', '10', '10000000', 100, '2026-11-01 00:00:00+00')
ON CONFLICT (modifier_id) DO NOTHING;
