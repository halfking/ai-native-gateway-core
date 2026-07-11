-- Rollback: Migration 358: P1.4 Observability Hot Table Sync
-- Purpose: Remove P1.4 observability fields from request_logs_hot

BEGIN;

-- Drop indexes first
DROP INDEX IF EXISTS idx_request_logs_hot_client_ip;
DROP INDEX IF EXISTS idx_request_logs_hot_agent_type;
DROP INDEX IF EXISTS idx_request_logs_hot_protocol_conversion;

-- Drop columns from hot table
ALTER TABLE request_logs_hot 
    DROP COLUMN IF EXISTS client_ip,
    DROP COLUMN IF EXISTS client_forwarded_for,
    DROP COLUMN IF EXISTS agent_name,
    DROP COLUMN IF EXISTS agent_type,
    DROP COLUMN IF EXISTS api_key_fingerprint,
    DROP COLUMN IF EXISTS session_title,
    DROP COLUMN IF EXISTS task_id,
    DROP COLUMN IF EXISTS upstream_endpoint,
    DROP COLUMN IF EXISTS upstream_protocol,
    DROP COLUMN IF EXISTS protocol_conversion;

-- Validation
DO $$
DECLARE
    remaining_cols TEXT[];
BEGIN
    -- Check all columns are removed
    SELECT ARRAY_AGG(column_name) INTO remaining_cols
    FROM information_schema.columns
    WHERE table_name = 'request_logs_hot' 
      AND column_name IN (
        'client_ip', 'client_forwarded_for', 'agent_name', 'agent_type',
        'api_key_fingerprint', 'session_title', 'task_id',
        'upstream_endpoint', 'upstream_protocol', 'protocol_conversion'
      );
    
    IF array_length(remaining_cols, 1) > 0 THEN
        RAISE EXCEPTION 'Failed to drop columns from request_logs_hot: %', array_to_string(remaining_cols, ', ');
    END IF;
    
    RAISE NOTICE '✓ Rollback 358: Removed 10 observability fields from request_logs_hot';
    RAISE NOTICE '✓ Removed 3 indexes';
END $$;

COMMIT;
