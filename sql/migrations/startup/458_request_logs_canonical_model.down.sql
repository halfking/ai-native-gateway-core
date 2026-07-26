-- 458 migration down: remove canonical_model + client perception columns
-- 2026-07-27: companion to 458_request_logs_canonical_model.sql
--
-- DROP COLUMN IF EXISTS removes the new columns. PostgreSQL propagates
-- the drop to all existing partitions of the partitioned parent table
-- (request_logs_2026_07, request_logs_2026_08, etc.) automatically.
--
-- This script is idempotent (DROP COLUMN IF EXISTS).

BEGIN;

ALTER TABLE request_logs
    DROP COLUMN IF EXISTS canonical_model;

ALTER TABLE request_logs_hot
    DROP COLUMN IF EXISTS canonical_model;

-- 2026-07-27 agent perception write-back columns (companion to the original
-- client-perception-hot-table migration, not a separate migration file).
ALTER TABLE request_logs_hot
    DROP COLUMN IF EXISTS agent_name;
ALTER TABLE request_logs_hot
    DROP COLUMN IF EXISTS agent_type;
ALTER TABLE request_logs_hot
    DROP COLUMN IF EXISTS client_protocol;
ALTER TABLE request_logs_hot
    DROP COLUMN IF EXISTS virtual_client_id;

-- Index cleanup (the index files were 458_request_logs_canonical_model.sql)
DROP INDEX IF EXISTS idx_request_logs_hot_canonical_model_ts;
DROP INDEX IF EXISTS idx_request_logs_canonical_model_ts;

COMMIT;