-- Migration: Casdoor approval integration mapping table
-- Version: 202611150001
-- Description: Stores local mapping between llm-gateway request IDs and Casdoor task IDs

-- Create approval_casdoor_mapping table
CREATE TABLE IF NOT EXISTS approval_casdoor_mapping (
    request_id VARCHAR(100) PRIMARY KEY,
    session_id VARCHAR(100) NOT NULL,
    tenant_id VARCHAR(100) NOT NULL,
    status VARCHAR(20) NOT NULL CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled', 'expired', 'timeout')),
    provider VARCHAR(20) NOT NULL CHECK (provider IN ('native', 'casdoor')),
    casdoor_task_id INTEGER,
    approved_by VARCHAR(200),
    approved_at TIMESTAMPTZ,
    reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Indexes for efficient queries
CREATE INDEX idx_casdoor_mapping_session ON approval_casdoor_mapping(session_id);
CREATE INDEX idx_casdoor_mapping_status ON approval_casdoor_mapping(status, created_at DESC);
CREATE INDEX idx_casdoor_mapping_tenant ON approval_casdoor_mapping(tenant_id, status);
CREATE INDEX idx_casdoor_mapping_casdoor_task ON approval_casdoor_mapping(casdoor_task_id) WHERE casdoor_task_id IS NOT NULL;

-- RLS policy for tenant isolation (following existing pattern)
ALTER TABLE approval_casdoor_mapping ENABLE ROW LEVEL SECURITY;

CREATE POLICY approval_casdoor_mapping_tenant_isolation ON approval_casdoor_mapping
    USING (tenant_id = current_setting('app.current_tenant', true));

-- Trigger to update updated_at
CREATE OR REPLACE FUNCTION update_approval_casdoor_mapping_timestamp()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trigger_update_approval_casdoor_mapping_timestamp
    BEFORE UPDATE ON approval_casdoor_mapping
    FOR EACH ROW
    EXECUTE FUNCTION update_approval_casdoor_mapping_timestamp();

-- Comment
COMMENT ON TABLE approval_casdoor_mapping IS 'Maps llm-gateway approval requests to Casdoor tasks for dual-layer approval architecture';
COMMENT ON COLUMN approval_casdoor_mapping.request_id IS 'llm-gateway request ID (unique)';
COMMENT ON COLUMN approval_casdoor_mapping.casdoor_task_id IS 'Casdoor platform task ID (null for native approvals)';
COMMENT ON COLUMN approval_casdoor_mapping.provider IS 'Approval provider: native (L1 fast) or casdoor (L2 complex)';
