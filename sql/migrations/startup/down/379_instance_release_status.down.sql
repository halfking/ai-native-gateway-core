-- 379_instance_release_status.down.sql
-- Rollback instance_release_status schema changes and gateway_instances.current_version

-- Remove foreign key constraint if exists
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.table_constraints 
               WHERE constraint_name = 'fk_upgrade_logs_instance' 
               AND table_name = 'upgrade_logs') THEN
        ALTER TABLE upgrade_logs DROP CONSTRAINT fk_upgrade_logs_instance;
    END IF;
END $$;

-- Drop indexes
DROP INDEX IF EXISTS idx_irs_release;
DROP INDEX IF EXISTS idx_irs_status;
DROP INDEX IF EXISTS idx_irs_instance;
DROP INDEX IF EXISTS idx_irs_instance_id_unique;

-- Remove columns added in 379
ALTER TABLE instance_release_status DROP COLUMN IF EXISTS duration_ms;
ALTER TABLE instance_release_status DROP COLUMN IF EXISTS from_version;

-- Restore primary key to instance_id if it was changed
DO $$
BEGIN
    -- If id column exists and is primary key, revert to instance_id as primary key
    IF EXISTS (SELECT 1 FROM information_schema.columns 
               WHERE table_name = 'instance_release_status' AND column_name = 'id') THEN
        -- Drop current primary key
        ALTER TABLE instance_release_status DROP CONSTRAINT IF EXISTS instance_release_status_pkey;
        -- Remove id column
        ALTER TABLE instance_release_status DROP COLUMN IF EXISTS id;
        -- Restore instance_id as primary key
        ALTER TABLE instance_release_status ADD PRIMARY KEY (instance_id);
    END IF;
END $$;

-- Remove current_version from gateway_instances
ALTER TABLE gateway_instances DROP COLUMN IF EXISTS current_version;
