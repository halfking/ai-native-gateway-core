-- Migration 032: Session Tenant Binding (Cross-Talk Detection)
-- Purpose: Track gw_session_id → tenant_id binding to detect cross-tenant access
-- Author: Official Deploy Team
-- Date: 2026-06-22

-- Table: session_tenant_binding
-- Records the first and last tenant_id seen for each gw_session_id.
-- If a session is accessed by multiple tenants, the tenant_id field
-- will be set to 'CONFLICT:<tenant1>,<tenant2>' for alerting.

CREATE TABLE IF NOT EXISTS session_tenant_binding (
    gw_session_id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    request_count INT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_session_tenant_binding_tenant 
    ON session_tenant_binding(tenant_id);

CREATE INDEX IF NOT EXISTS idx_session_tenant_binding_last_seen 
    ON session_tenant_binding(last_seen_at DESC);

-- Comment
COMMENT ON TABLE session_tenant_binding IS 
    'Tracks gw_session_id to tenant_id binding. CONFLICT: prefix indicates cross-tenant access (串话).';

COMMENT ON COLUMN session_tenant_binding.tenant_id IS 
    'Normal: single tenant_id. Cross-talk: CONFLICT:<tenant1>,<tenant2>';

-- Trigger Function: update_session_tenant_binding
-- Called on every request_logs INSERT to upsert session_tenant_binding.
-- Detects if a session switches tenants and marks it as CONFLICT.

CREATE OR REPLACE FUNCTION update_session_tenant_binding()
RETURNS TRIGGER AS $$
BEGIN
    -- Only process if both gw_session_id and tenant_id are present
    IF NEW.gw_session_id IS NOT NULL AND NEW.tenant_id IS NOT NULL THEN
        INSERT INTO session_tenant_binding (
            gw_session_id, 
            tenant_id, 
            first_seen_at, 
            last_seen_at, 
            request_count
        )
        VALUES (
            NEW.gw_session_id, 
            NEW.tenant_id, 
            NEW.ts, 
            NEW.ts, 
            1
        )
        ON CONFLICT (gw_session_id) DO UPDATE SET
            last_seen_at = EXCLUDED.last_seen_at,
            request_count = session_tenant_binding.request_count + 1,
            -- CRITICAL: Detect tenant switch (cross-talk)
            tenant_id = CASE
                WHEN session_tenant_binding.tenant_id != EXCLUDED.tenant_id 
                     AND NOT session_tenant_binding.tenant_id LIKE 'CONFLICT:%'
                THEN 'CONFLICT:' || session_tenant_binding.tenant_id || ',' || EXCLUDED.tenant_id
                WHEN session_tenant_binding.tenant_id LIKE 'CONFLICT:%'
                     AND session_tenant_binding.tenant_id NOT LIKE '%' || EXCLUDED.tenant_id || '%'
                THEN session_tenant_binding.tenant_id || ',' || EXCLUDED.tenant_id
                ELSE session_tenant_binding.tenant_id
            END;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION update_session_tenant_binding() IS 
    'Auto-updates session_tenant_binding on request_logs INSERT. Detects cross-tenant session access.';

-- Trigger: trg_session_tenant_binding
-- Fires AFTER INSERT on request_logs to maintain session_tenant_binding table.

DROP TRIGGER IF EXISTS trg_session_tenant_binding ON request_logs;

CREATE TRIGGER trg_session_tenant_binding
AFTER INSERT ON request_logs
FOR EACH ROW
EXECUTE FUNCTION update_session_tenant_binding();

COMMENT ON TRIGGER trg_session_tenant_binding ON request_logs IS 
    'Maintains session_tenant_binding table for cross-talk detection';
