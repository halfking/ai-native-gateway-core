-- Rollback for migration 020: Remove UNIQUE constraint on request_logs.request_id
-- Date: 2026-06-20

DROP INDEX IF EXISTS idx_request_logs_request_id_unique;
