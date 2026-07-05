-- Rollback for migration 020: Remove UNIQUE constraint on (request_id, ts)
-- Date: 2026-06-20

DROP INDEX IF EXISTS idx_request_logs_request_id_ts_unique;
