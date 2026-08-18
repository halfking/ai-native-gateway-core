-- 536_stats_analytics_foundation.down.sql
-- Destructive rollback for the statistics foundation. Export facts before use.

BEGIN;
DROP TABLE IF EXISTS stats_reconciliation_diffs;
DROP TABLE IF EXISTS stats_reconciliation_runs;
DROP TABLE IF EXISTS stats_adjustments;
DROP TABLE IF EXISTS stats_usage_monthly;
DROP TABLE IF EXISTS stats_usage_daily;
DROP TABLE IF EXISTS stats_event_inbox;
DROP TABLE IF EXISTS stats_event_dedup;
COMMIT;
