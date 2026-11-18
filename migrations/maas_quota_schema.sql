-- Add entitlement quota tracking table
-- This table tracks quota consumption for each entitlement

CREATE TABLE IF NOT EXISTS maas_entitlement_quotas (
    id BIGSERIAL PRIMARY KEY,
    entitlement_id TEXT NOT NULL REFERENCES maas_entitlements(entitlement_id) ON DELETE CASCADE,
    model_sku TEXT NOT NULL,
    
    -- Allocated quota (from purchase)
    allocated_amount TEXT NOT NULL,        -- Decimal string
    allocated_unit TEXT NOT NULL,
    allocated_window TEXT,                 -- e.g., "monthly", "daily"
    
    -- Consumed quota (from actual usage)
    consumed_amount TEXT NOT NULL DEFAULT '0',  -- Decimal string
    consumed_unit TEXT NOT NULL,
    
    -- Held quota (pending usage holds)
    held_amount TEXT NOT NULL DEFAULT '0',      -- Decimal string
    held_unit TEXT NOT NULL,
    
    -- Quota state
    quota_version BIGINT NOT NULL DEFAULT 1,    -- OCC version for concurrent updates
    last_reset_at TIMESTAMPTZ,                  -- Last quota reset time (for recurring)
    next_reset_at TIMESTAMPTZ,                  -- Next quota reset time
    
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- Ensure one quota record per entitlement + model
    UNIQUE (entitlement_id, model_sku)
);

CREATE INDEX idx_maas_entitlement_quotas_entitlement ON maas_entitlement_quotas(entitlement_id);
CREATE INDEX idx_maas_entitlement_quotas_model ON maas_entitlement_quotas(model_sku);

-- Helper function to check if quota is available
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
    -- Get quota info
    SELECT 
        allocated_amount::DECIMAL,
        consumed_amount::DECIMAL,
        held_amount::DECIMAL
    INTO v_allocated, v_consumed, v_held
    FROM maas_entitlement_quotas
    WHERE entitlement_id = p_entitlement_id
      AND model_sku = p_model_sku;
    
    -- If no quota record, return false
    IF NOT FOUND THEN
        RETURN FALSE;
    END IF;
    
    -- Calculate available: allocated - (consumed + held)
    v_requested := p_requested_amount::DECIMAL;
    v_available := v_allocated - (v_consumed + v_held);
    
    -- Return true if requested <= available
    RETURN v_requested <= v_available;
END;
$$ LANGUAGE plpgsql STABLE;

-- Function to increment held quota (during UsageHold)
CREATE OR REPLACE FUNCTION maas_increment_held_quota(
    p_entitlement_id TEXT,
    p_model_sku TEXT,
    p_amount TEXT
)
RETURNS VOID AS $$
BEGIN
    UPDATE maas_entitlement_quotas
    SET 
        held_amount = (held_amount::DECIMAL + p_amount::DECIMAL)::TEXT,
        quota_version = quota_version + 1,
        updated_at = NOW()
    WHERE entitlement_id = p_entitlement_id
      AND model_sku = p_model_sku;
END;
$$ LANGUAGE plpgsql;

-- Function to settle quota (during UsageSettle)
-- Moves from held to consumed
CREATE OR REPLACE FUNCTION maas_settle_quota(
    p_entitlement_id TEXT,
    p_model_sku TEXT,
    p_held_amount TEXT,
    p_actual_amount TEXT
)
RETURNS VOID AS $$
DECLARE
    v_released DECIMAL;
BEGIN
    v_released := p_held_amount::DECIMAL - p_actual_amount::DECIMAL;
    
    UPDATE maas_entitlement_quotas
    SET 
        held_amount = (held_amount::DECIMAL - p_held_amount::DECIMAL)::TEXT,
        consumed_amount = (consumed_amount::DECIMAL + p_actual_amount::DECIMAL)::TEXT,
        quota_version = quota_version + 1,
        updated_at = NOW()
    WHERE entitlement_id = p_entitlement_id
      AND model_sku = p_model_sku;
END;
$$ LANGUAGE plpgsql;

COMMENT ON TABLE maas_entitlement_quotas IS 'Tracks quota allocation and consumption for entitlements';
COMMENT ON COLUMN maas_entitlement_quotas.allocated_amount IS 'Total quota allocated from purchase';
COMMENT ON COLUMN maas_entitlement_quotas.consumed_amount IS 'Actually consumed quota';
COMMENT ON COLUMN maas_entitlement_quotas.held_amount IS 'Temporarily held quota (pending usage)';
COMMENT ON COLUMN maas_entitlement_quotas.quota_version IS 'OCC version for optimistic locking';

-- Seed test quota data
INSERT INTO maas_entitlement_quotas (entitlement_id, model_sku, allocated_amount, allocated_unit, consumed_unit, held_unit)
SELECT 'ent_test_001', 'gpt-4', '1000000', 'token', 'token', 'token'
WHERE NOT EXISTS (
    SELECT 1 FROM maas_entitlement_quotas 
    WHERE entitlement_id = 'ent_test_001' AND model_sku = 'gpt-4'
);
