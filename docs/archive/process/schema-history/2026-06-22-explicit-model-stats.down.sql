-- Rollback for 2026-06-22-explicit-model-stats.sql
BEGIN;
DROP INDEX IF EXISTS idx_request_logs_explicit_model;
COMMIT;
