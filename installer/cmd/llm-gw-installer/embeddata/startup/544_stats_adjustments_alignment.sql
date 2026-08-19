-- 544_stats_adjustments_alignment.sql
-- Align stats_adjustments columns with the handler's intent so future
-- adjustments can carry explicit adjustment_type and metric_name without a
-- reason-prefix workaround. Backfills new columns from existing data and
-- leaves the handler INSERT compatible with both pre- and post-migration
-- deployments (handler fix in admin/stats.go is independent of this DDL).
--
-- Pre-condition: migration 536_stats_analytics_foundation applied.
-- Post-condition:
--   * stats_adjustments has adjustment_type text NOT NULL DEFAULT 'reconciliation'
--   * stats_adjustments has metric_name text (NULL backfill from metric)
--   * idx_stats_adjustments_type exists on (tenant_id, adjustment_type, month_start DESC)
--   * schema_migrations row '544' recorded

BEGIN;

DO $$
BEGIN
    IF to_regclass('public.stats_adjustments') IS NULL THEN
        RAISE EXCEPTION '544_stats_adjustments_alignment requires migration 536_stats_analytics_foundation';
    END IF;
END $$;

ALTER TABLE IF EXISTS stats_adjustments
    ADD COLUMN IF NOT EXISTS adjustment_type text NOT NULL DEFAULT 'reconciliation';

ALTER TABLE IF EXISTS stats_adjustments
    ADD COLUMN IF NOT EXISTS metric_name text;

UPDATE stats_adjustments
SET metric_name = metric
WHERE metric_name IS NULL AND metric IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_stats_adjustments_type
    ON stats_adjustments (tenant_id, adjustment_type, month_start DESC);

INSERT INTO public.schema_migrations (version, description)
VALUES ('544', 'stats_adjustments alignment: adjustment_type + metric_name')
ON CONFLICT (version) DO NOTHING;

COMMIT;