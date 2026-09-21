-- Rollback Migration 630: drop session_aggregate_outbox table.
-- Note: down is lossy. In-flight outbox rows are lost.
BEGIN;
DROP TABLE IF EXISTS public.session_aggregate_outbox;
COMMIT;
