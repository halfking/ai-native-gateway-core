-- Migration 340 (down): Drop partition query VIEWs
--
-- Rollback: Remove all *_with_current_month VIEWs created by migration 340.
--
-- Note: This does NOT affect the underlying data. The VIEWs are just
-- query convenience layers. The actual data remains in the partitioned
-- tables and *_default tables.

BEGIN;

DROP VIEW IF EXISTS public.request_logs_with_current_month;
DROP VIEW IF EXISTS public.request_wal_with_current_month;
DROP VIEW IF EXISTS public.usage_ledger_with_current_month;
DROP VIEW IF EXISTS public.routing_decision_log_with_current_month;
DROP VIEW IF EXISTS public.credential_model_index_with_current_month;
DROP VIEW IF EXISTS public.request_logs_bodies_with_current_month;
DROP VIEW IF EXISTS public.credit_ledger_with_current_month;
DROP VIEW IF EXISTS public.tool_usage_stats_with_current_month;

COMMIT;

\echo 'Migration 340 down: all 8 *_with_current_month VIEWs dropped.'
\echo 'Note: Underlying data in partitioned tables is unchanged.'
