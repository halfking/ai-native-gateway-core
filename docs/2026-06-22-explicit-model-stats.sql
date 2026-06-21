-- ============================================================================
-- 2026-06-22 explicit-model stats index
-- ============================================================================
--
-- Adds an index tailored to the new "client specified a model" analytics
-- path. After 2026-06-22, the routing-v2 analytics endpoints (matrix,
-- flow, audit, work-types) no longer pre-filter by is_auto_request = TRUE
-- — they bucket explicit-model requests under the synthetic __specified__
-- task label instead.
--
-- Without this index, the broader WHERE clause (dropping the
-- is_auto_request = TRUE filter) falls back to a sequential scan on
-- request_logs for the 7d / 24h window. The partial index below keeps
-- the query plan tight for the non-auto path.
--
-- The auto-only path is still served by the existing
-- idx_request_logs_auto (is_auto_request, task_type, ts DESC); we leave
-- it alone so behaviour is unchanged for the old KPI queries.

BEGIN;

CREATE INDEX IF NOT EXISTS idx_request_logs_explicit_model
  ON request_logs (client_model, ts DESC)
  WHERE is_auto_request = FALSE
    AND client_model IS NOT NULL
    AND client_model <> '';

COMMENT ON INDEX idx_request_logs_explicit_model IS
  'Supports the routing-v2 explicit-model analytics path (handleMatrix/handleFlow/handleAudit) where client_model is used in place of outbound_model.';

COMMIT;
