-- Migration 336 (down): remove promote_*_default_batch functions + request_logs_bodies_default
--
-- Rollback plan:
--   1. Drop the 8 promote_*_default_batch functions (no-op if absent).
--   2. Detach + drop request_logs_bodies_default (rows in the default
--      partition, if any, are migrated into the monthly partitions so
--      they remain queryable through the parent table).
--
-- The other *_default partitions (request_logs_default, request_wal_default,
-- etc.) are NOT dropped here — they were created by earlier migrations
-- (317, 330, 332, 333, 334, 335) and dropping them would orphan data
-- that lives in those tables. They stay.

BEGIN;

-- 1. Drop the 8 promote_* functions
DROP FUNCTION IF EXISTS public.promote_request_logs_default_batch(interval, int);
DROP FUNCTION IF EXISTS public.promote_request_wal_default_batch(interval, int);
DROP FUNCTION IF EXISTS public.promote_usage_ledger_default_batch(interval, int);
DROP FUNCTION IF EXISTS public.promote_routing_decision_log_default_batch(interval, int);
DROP FUNCTION IF EXISTS public.promote_credential_model_index_default_batch(interval, int);
DROP FUNCTION IF EXISTS public.promote_request_logs_bodies_default_batch(interval, int);
DROP FUNCTION IF EXISTS public.promote_credit_ledger_default_batch(interval, int);
DROP FUNCTION IF EXISTS public.promote_tool_usage_stats_default_batch(interval, int);

-- 2. Detach + drop request_logs_bodies_default.
--    Detaching first (instead of DROP TABLE ... CASCADE) keeps the
--    rows queryable through the parent table — they just become rows
--    that are NOT covered by any partition. (In practice when running
--    this down migration the system should not be receiving writes
--    against request_logs_bodies.)
ALTER TABLE public.request_logs_bodies
    DETACH PARTITION public.request_logs_bodies_default;
DROP TABLE IF EXISTS public.request_logs_bodies_default;

COMMIT;

\echo 'Migration 336 down: 8 promote_*_default_batch functions dropped; '
\echo 'request_logs_bodies_default detached + dropped.'
