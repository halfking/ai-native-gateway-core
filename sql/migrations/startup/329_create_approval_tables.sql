-- Migration 329: Approval workflow tables
-- Creates tables for managing approval requests and configurations

-- approval_configs: Stores approval configuration per tenant
CREATE TABLE IF NOT EXISTS approval_configs (
    id SERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL UNIQUE,
    enabled BOOLEAN DEFAULT false,
    mode VARCHAR(32) NOT NULL DEFAULT 'disabled'
        CHECK (mode IN ('disabled', 'automatic', 'manual')),
    timeout_seconds INT DEFAULT 3600,
    auto_reject_on_timeout BOOLEAN DEFAULT true,
    config JSONB NOT NULL DEFAULT '{}'::jsonb, -- Full configuration JSON (approvers, channels, rules)
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_approval_configs_tenant ON approval_configs(tenant_id);
CREATE INDEX IF NOT EXISTS idx_approval_configs_enabled ON approval_configs(enabled) WHERE enabled = true;

-- approval_requests: Stores individual approval requests
CREATE TABLE IF NOT EXISTS approval_requests (
    id SERIAL PRIMARY KEY,
    request_id VARCHAR(64) NOT NULL UNIQUE,
    session_id VARCHAR(64) NOT NULL,
    tenant_id VARCHAR(64) NOT NULL,
    
    -- Trigger information
    trigger_type VARCHAR(32) NOT NULL
        CHECK (trigger_type IN ('sensitive_content', 'high_cost', 'tool_call', 'policy_match', 'manual_mode')),
    trigger_reason TEXT,
    risk_level VARCHAR(16) NOT NULL
        CHECK (risk_level IN ('LOW', 'MEDIUM', 'HIGH', 'CRITICAL')),
    
    -- Session context
    session_summary JSONB,
    sensitive_info JSONB,
    user_message TEXT,
    full_context JSONB, -- Complete message history for admin review
    
    -- Cost estimation
    estimated_cost DECIMAL(10, 4),
    estimated_tokens INT,
    
    -- Status tracking
    status VARCHAR(32) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'rejected', 'timeout', 'canceled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    
    -- Approval result
    approved_by VARCHAR(64),
    approved_at TIMESTAMPTZ,
    approval_note TEXT,
    rejected BOOLEAN DEFAULT false,
    rejection_reason TEXT,
    
    -- Extensibility
    metadata JSONB DEFAULT '{}'::jsonb
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_approval_requests_request_id ON approval_requests(request_id);
CREATE INDEX IF NOT EXISTS idx_approval_requests_session_id ON approval_requests(session_id);
CREATE INDEX IF NOT EXISTS idx_approval_requests_tenant_id ON approval_requests(tenant_id);
CREATE INDEX IF NOT EXISTS idx_approval_requests_status ON approval_requests(status);
CREATE INDEX IF NOT EXISTS idx_approval_requests_created_at ON approval_requests(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_approval_requests_tenant_status ON approval_requests(tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_approval_requests_expires_at ON approval_requests(expires_at) WHERE status = 'pending';

-- approval_approvers: Stores approver information per tenant
CREATE TABLE IF NOT EXISTS approval_approvers (
    id SERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    user_id VARCHAR(64) NOT NULL,
    name VARCHAR(128) NOT NULL,
    email VARCHAR(128),
    phone VARCHAR(32),
    role VARCHAR(32) NOT NULL
        CHECK (role IN ('admin', 'auditor', 'manager', 'reviewer')),
    priority INT DEFAULT 0,
    enabled BOOLEAN DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    
    UNIQUE(tenant_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_approval_approvers_tenant ON approval_approvers(tenant_id);
CREATE INDEX IF NOT EXISTS idx_approval_approvers_enabled ON approval_approvers(tenant_id, enabled) WHERE enabled = true;
CREATE INDEX IF NOT EXISTS idx_approval_approvers_priority ON approval_approvers(tenant_id, priority DESC);

-- approval_rules: Stores approval rules per tenant
CREATE TABLE IF NOT EXISTS approval_rules (
    id SERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    name VARCHAR(128) NOT NULL,
    enabled BOOLEAN DEFAULT true,
    priority INT DEFAULT 0,
    conditions JSONB NOT NULL, -- Array of rule conditions
    action JSONB NOT NULL, -- Rule action (type, risk_level, reason)
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    
    UNIQUE(tenant_id, name)
);

CREATE INDEX IF NOT EXISTS idx_approval_rules_tenant ON approval_rules(tenant_id);
CREATE INDEX IF NOT EXISTS idx_approval_rules_enabled ON approval_rules(tenant_id, enabled) WHERE enabled = true;
CREATE INDEX IF NOT EXISTS idx_approval_rules_priority ON approval_rules(tenant_id, priority DESC);

-- Add foreign key constraints for data integrity
ALTER TABLE approval_requests 
    ADD CONSTRAINT fk_approval_requests_tenant 
    FOREIGN KEY (tenant_id) REFERENCES tenants(code) 
    ON DELETE CASCADE;

ALTER TABLE approval_configs 
    ADD CONSTRAINT fk_approval_configs_tenant 
    FOREIGN KEY (tenant_id) REFERENCES tenants(code) 
    ON DELETE CASCADE;

ALTER TABLE approval_approvers 
    ADD CONSTRAINT fk_approval_approvers_tenant 
    FOREIGN KEY (tenant_id) REFERENCES tenants(code) 
    ON DELETE CASCADE;

ALTER TABLE approval_rules 
    ADD CONSTRAINT fk_approval_rules_tenant 
    FOREIGN KEY (tenant_id) REFERENCES tenants(code) 
    ON DELETE CASCADE;

-- Create a function to automatically update updated_at timestamp
CREATE OR REPLACE FUNCTION update_approval_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Add triggers to auto-update updated_at
CREATE TRIGGER approval_configs_updated_at
    BEFORE UPDATE ON approval_configs
    FOR EACH ROW
    EXECUTE FUNCTION update_approval_updated_at();

CREATE TRIGGER approval_approvers_updated_at
    BEFORE UPDATE ON approval_approvers
    FOR EACH ROW
    EXECUTE FUNCTION update_approval_updated_at();

CREATE TRIGGER approval_rules_updated_at
    BEFORE UPDATE ON approval_rules
    FOR EACH ROW
    EXECUTE FUNCTION update_approval_updated_at();
