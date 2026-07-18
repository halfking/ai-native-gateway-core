-- Migration: 433_system_metrics_local_ingest
-- Down migration: Rollback local metrics ingestion table

-- Drop trigger first
DROP TRIGGER IF EXISTS system_metrics_local_update_trigger ON system_metrics_local;

-- Drop function
DROP FUNCTION IF EXISTS system_metrics_local_update_timestamp();

-- Drop table
DROP TABLE IF EXISTS system_metrics_local;

-- Drop indexes (cascade will handle this, but explicit for clarity)
-- Already dropped by DROP TABLE CASCADE
