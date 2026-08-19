-- 545_stats_reconciliation_phantom_resolution.down.sql
-- Rollback: refuse to silently reclassify 'phantom_open' rows.
--
-- Why fail-loud: reclassifying phantom_open -> 'open' is dangerous.
-- admin/stats.go treats 'open' as approvable, and approving a phantom
-- diff (projection with no source fact) would create a
-- stats_adjustments row crediting or debiting a tenant for a
-- difference that has no source-of-truth behind it. That is
-- financial-grade data corruption. The operator must either
-- reclassify phantom rows to a terminal non-approvable state
-- ('rejected') or delete them before rolling back this migration.
--
-- Pre-conditions to apply this rollback successfully:
--   1. Zero rows with resolution='phantom_open' in
--      stats_reconciliation_diffs.
--   2. Zero rows with resolution='adjusted' in
--      stats_reconciliation_diffs (pre-545 constraint set excludes it).
--
-- Both checks are enforced via DO blocks below; on failure the
-- transaction rolls back and the migration is a no-op.

BEGIN;

DO $$
DECLARE
    phantom_count bigint;
    adjusted_count bigint;
BEGIN
    IF to_regclass('public.stats_reconciliation_diffs') IS NULL THEN
        RAISE EXCEPTION '545_stats_reconciliation_phantom_resolution.down requires migration 536_stats_analytics_foundation';
    END IF;

    SELECT count(*) INTO phantom_count
      FROM stats_reconciliation_diffs
      WHERE resolution = 'phantom_open';
    IF phantom_count > 0 THEN
        RAISE EXCEPTION '545.down: % rows with resolution=''phantom_open'' exist; reclassify to ''rejected'' or delete before rolling back 545 to avoid turning them into approvable ''open'' rows', phantom_count;
    END IF;

    SELECT count(*) INTO adjusted_count
      FROM stats_reconciliation_diffs
      WHERE resolution = 'adjusted';
    IF adjusted_count > 0 THEN
        RAISE EXCEPTION '545.down: % rows with resolution=''adjusted'' exist; the pre-545 constraint does not allow this value, so 545 cannot be rolled back without first reclassifying or deleting them', adjusted_count;
    END IF;
END $$;

ALTER TABLE stats_reconciliation_diffs
  DROP CONSTRAINT IF EXISTS stats_reconciliation_diffs_resolution_check;

ALTER TABLE stats_reconciliation_diffs
  ADD CONSTRAINT stats_reconciliation_diffs_resolution_check
  CHECK (resolution IN ('open','auto_repair_pending','auto_repaired','rejected','approved'));

DELETE FROM public.schema_migrations
  WHERE version = '545';

COMMIT;
