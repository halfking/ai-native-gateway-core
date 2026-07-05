-- Rollback for migration 054.

DROP INDEX IF EXISTS idx_request_logs_client_request_id;
ALTER TABLE request_logs DROP COLUMN IF EXISTS client_request_id;