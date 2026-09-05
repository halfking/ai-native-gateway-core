-- Rollback: Drop Task Type Tier Configuration Table
-- Version: V3 Phase 1 Rollback
-- Date: 2026-09-02
-- Purpose: Rollback migration 202609_02_create_task_type_tier_config.sql

BEGIN;

-- Drop indexes first
DROP INDEX IF EXISTS idx_task_type_tier_config_lookup;

-- Drop table (CASCADE to remove any dependencies)
DROP TABLE IF EXISTS task_type_tier_config CASCADE;

-- Verify rollback
DO $$
DECLARE
    v_table_exists BOOLEAN;
BEGIN
    SELECT EXISTS (
        SELECT 1 
        FROM information_schema.tables 
        WHERE table_name = 'task_type_tier_config'
    ) INTO v_table_exists;
    
    IF v_table_exists THEN
        RAISE WARNING 'Rollback incomplete: task_type_tier_config table still exists';
    ELSE
        RAISE NOTICE 'Rollback successful: task_type_tier_config table removed ✓';
    END IF;
END $$;

COMMIT;
