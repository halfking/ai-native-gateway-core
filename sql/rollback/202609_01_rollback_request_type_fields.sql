-- Rollback: Remove Request Type Classification Fields
-- Version: V3 Phase 1 Rollback
-- Date: 2026-09-02
-- Purpose: Rollback migration 202609_01_add_request_type_fields.sql

BEGIN;

-- Drop indexes first (dependency order)
DROP INDEX IF EXISTS idx_request_logs_escalation;
DROP INDEX IF EXISTS idx_request_logs_tier_terminal_ts;
DROP INDEX IF EXISTS idx_request_logs_terminal;
DROP INDEX IF EXISTS idx_request_logs_task_tier_ts;
DROP INDEX IF EXISTS idx_request_logs_parent_request_id;
DROP INDEX IF EXISTS idx_request_logs_request_type_ts;

-- Remove columns
ALTER TABLE request_logs 
    DROP COLUMN IF EXISTS escalation_count,
    DROP COLUMN IF EXISTS auto_decision,
    DROP COLUMN IF EXISTS task_tier,
    DROP COLUMN IF EXISTS is_terminal,
    DROP COLUMN IF EXISTS request_depth,
    DROP COLUMN IF EXISTS parent_request_id,
    DROP COLUMN IF EXISTS request_type;

-- Verify rollback
DO $$
DECLARE
    v_column_exists BOOLEAN;
BEGIN
    SELECT EXISTS (
        SELECT 1 
        FROM information_schema.columns 
        WHERE table_name = 'request_logs' 
          AND column_name = 'request_type'
    ) INTO v_column_exists;
    
    IF v_column_exists THEN
        RAISE WARNING 'Rollback incomplete: request_type column still exists';
    ELSE
        RAISE NOTICE 'Rollback successful: All V3 columns removed from request_logs ✓';
    END IF;
END $$;

COMMIT;
