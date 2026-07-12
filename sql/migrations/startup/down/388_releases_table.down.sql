-- Migration: 388_releases_table rollback
-- Description: Drop releases and related tables

DROP TABLE IF EXISTS instance_release_status CASCADE;
DROP TABLE IF EXISTS upgrade_logs CASCADE;
DROP TABLE IF EXISTS gray_release_rules CASCADE;
DROP TABLE IF EXISTS releases CASCADE;

-- Remove columns from gateway_instances
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'gateway_instances' AND column_name = 'current_version'
    ) THEN
        ALTER TABLE gateway_instances DROP COLUMN current_version;
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'gateway_instances' AND column_name = 'build_seq'
    ) THEN
        ALTER TABLE gateway_instances DROP COLUMN build_seq;
    END IF;
END $$;
