-- Rollback: Remove Tier Classification from Provider Models
-- Version: V3 Phase 1 Rollback
-- Date: 2026-09-02
-- Purpose: Rollback migration 202609_03_add_tier_to_provider_models.sql

BEGIN;

-- Drop indexes first
DROP INDEX IF EXISTS idx_provider_models_tier_available;
DROP INDEX IF EXISTS idx_provider_models_tier;

-- Remove tier column
ALTER TABLE provider_models 
    DROP COLUMN IF EXISTS tier;

-- Verify rollback
DO $$
DECLARE
    v_column_exists BOOLEAN;
BEGIN
    SELECT EXISTS (
        SELECT 1 
        FROM information_schema.columns 
        WHERE table_name = 'provider_models' 
          AND column_name = 'tier'
    ) INTO v_column_exists;
    
    IF v_column_exists THEN
        RAISE WARNING 'Rollback incomplete: tier column still exists in provider_models';
    ELSE
        RAISE NOTICE 'Rollback successful: tier column removed from provider_models ✓';
    END IF;
END $$;

COMMIT;
