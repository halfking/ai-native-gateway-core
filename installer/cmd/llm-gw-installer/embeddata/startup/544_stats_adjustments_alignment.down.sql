-- 544_stats_adjustments_alignment.down.sql
-- Reverse 544: drop the alignment columns and index added for the
-- stats_reconciliation approval handler. metric_name data is intentionally
-- dropped (backfill is one-way; the original `metric` column is preserved).

BEGIN;

DROP INDEX IF EXISTS idx_stats_adjustments_type;

ALTER TABLE IF EXISTS stats_adjustments DROP COLUMN IF EXISTS metric_name;
ALTER TABLE IF EXISTS stats_adjustments DROP COLUMN IF EXISTS adjustment_type;

DELETE FROM public.schema_migrations WHERE version = '544';

COMMIT;