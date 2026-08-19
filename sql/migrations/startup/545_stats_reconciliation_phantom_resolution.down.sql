-- 545_stats_reconciliation_phantom_resolution.down.sql
-- Rollback: reclassify any 'phantom_open' rows back to 'open' before
-- tightening the CHECK constraint to the pre-545 set. Note: if any
-- 'phantom_open' rows exist, the operator must reclassify or delete
-- them first; the UPDATE below converts them to 'open' so the
-- tightened constraint can be applied.

BEGIN;

DO $$
BEGIN
    IF to_regclass('public.stats_reconciliation_diffs') IS NULL THEN
        RAISE EXCEPTION '545_stats_reconciliation_phantom_resolution.down requires migration 536_stats_analytics_foundation';
    END IF;
END $$;

UPDATE stats_reconciliation_diffs
  SET resolution = 'open'
  WHERE resolution = 'phantom_open';

ALTER TABLE stats_reconciliation_diffs
  DROP CONSTRAINT IF EXISTS stats_reconciliation_diffs_resolution_check;

ALTER TABLE stats_reconciliation_diffs
  ADD CONSTRAINT stats_reconciliation_diffs_resolution_check
  CHECK (resolution IN ('open','auto_repair_pending','auto_repaired','rejected','approved'));

DELETE FROM public.schema_migrations
  WHERE version = '545';

COMMIT;