-- 393_request_logs_hot_partial_index.down.sql
-- Reverses the request_logs_hot partial index migration.

BEGIN;

DROP INDEX IF EXISTS idx_request_logs_hot_success_true_ts;
DROP INDEX IF EXISTS idx_request_logs_hot_success_false_ts;

-- Restore original full-column index
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_success_ts
    ON request_logs_hot (success, ts DESC);

COMMIT;
