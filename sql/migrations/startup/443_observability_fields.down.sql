-- Rollback: 2026-07-11 Observability Fields Extension
-- Purpose: Remove all observability fields added in the forward migration

BEGIN;

-- Drop indexes first
DROP INDEX IF EXISTS idx_request_logs_client_ip;
DROP INDEX IF EXISTS idx_request_logs_customer_id;
DROP INDEX IF EXISTS idx_request_logs_task_type;
DROP INDEX IF EXISTS idx_request_logs_protocol_conversion;
DROP INDEX IF EXISTS idx_request_logs_rate_limit_status;
DROP INDEX IF EXISTS idx_request_logs_agent_type;

-- Drop columns (order doesn't matter for DROP)
ALTER TABLE request_logs 
    DROP COLUMN IF EXISTS client_ip,
    DROP COLUMN IF EXISTS client_forwarded_for,
    DROP COLUMN IF EXISTS agent_name,
    DROP COLUMN IF EXISTS agent_type,
    DROP COLUMN IF EXISTS api_key_fingerprint,
    DROP COLUMN IF EXISTS customer_id,
    DROP COLUMN IF EXISTS upstream_endpoint,
    DROP COLUMN IF EXISTS session_title,
    DROP COLUMN IF EXISTS session_summary,
    DROP COLUMN IF EXISTS task_id,
    DROP COLUMN IF EXISTS task_title,
    DROP COLUMN IF EXISTS compression_start_index,
    DROP COLUMN IF EXISTS compression_end_index,
    DROP COLUMN IF EXISTS compression_ratio,
    DROP COLUMN IF EXISTS cache_hit,
    DROP COLUMN IF EXISTS cache_tokens_saved,
    DROP COLUMN IF EXISTS content_safety_score,
    DROP COLUMN IF EXISTS dlp_violations,
    DROP COLUMN IF EXISTS sensitive_keywords,
    DROP COLUMN IF EXISTS rate_limit_status,
    DROP COLUMN IF EXISTS client_protocol,
    DROP COLUMN IF EXISTS upstream_protocol,
    DROP COLUMN IF EXISTS protocol_conversion,
    DROP COLUMN IF EXISTS ir_extensions,
    DROP COLUMN IF EXISTS sanitizer_mutations,
    DROP COLUMN IF EXISTS vendor_metadata;

-- Validation
DO $$
DECLARE
    remaining_cols TEXT[];
BEGIN
    -- Check all columns are removed
    SELECT ARRAY_AGG(column_name) INTO remaining_cols
    FROM information_schema.columns
    WHERE table_name = 'request_logs' 
      AND column_name IN (
        'client_ip', 'client_forwarded_for', 'agent_name', 'agent_type',
        'api_key_fingerprint', 'customer_id', 'upstream_endpoint',
        'session_title', 'session_summary', 'task_id', 'task_title',
        'compression_start_index', 'compression_end_index', 'compression_ratio',
        'cache_hit', 'cache_tokens_saved',
        'content_safety_score', 'dlp_violations', 'sensitive_keywords', 'rate_limit_status',
        'client_protocol', 'upstream_protocol', 'protocol_conversion',
        'ir_extensions', 'sanitizer_mutations', 'vendor_metadata'
      );
    
    IF array_length(remaining_cols, 1) > 0 THEN
        RAISE EXCEPTION 'Failed to drop columns: %', array_to_string(remaining_cols, ', ');
    END IF;
    
    RAISE NOTICE 'Rollback 2026-07-11-observability-fields completed successfully';
    RAISE NOTICE 'Removed 26 observability fields and 6 indexes';
END $$;

COMMIT;
