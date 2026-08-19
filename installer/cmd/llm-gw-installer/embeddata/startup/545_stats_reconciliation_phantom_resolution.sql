-- 545_stats_reconciliation_phantom_resolution.sql
-- Add an explicit CHECK constraint on
-- stats_reconciliation_diffs.resolution and extend its allowed set to
-- include 'phantom_open' and 'adjusted'. Projections that exist in
-- stats_usage_daily but have no matching usage_facts source row are
-- recorded with resolution='phantom_open' to distinguish them from
-- regular "mismatch" diffs and to prevent accidental auto-repair via
-- threshold misconfiguration. 'adjusted' was already read by
-- admin/stats.go:405 as a terminal state, so it must be in the allowed
-- set even though no current writer produces it.
--
-- The constraint is added with NOT VALID so the migration does not
-- take an ACCESS EXCLUSIVE lock on stats_reconciliation_diffs (which
-- can be unboundedly large and is written row-by-row in a hot
-- reconciliation loop). VALIDATE CONSTRAINT runs separately, taking
-- only SHARE UPDATE EXCLUSIVE, so it does not block readers or
-- writers.
--
-- Pre-condition: migration 536_stats_analytics_foundation applied.
-- Post-condition:
--   * stats_reconciliation_diffs.resolution has a CHECK constraint
--     allowing ('open','auto_repair_pending','auto_repaired',
--     'phantom_open','rejected','approved','adjusted').
--   * schema_migrations row '545' recorded.

BEGIN;

DO $$
BEGIN
    IF to_regclass('public.stats_reconciliation_diffs') IS NULL THEN
        RAISE EXCEPTION '545_stats_reconciliation_phantom_resolution requires migration 536_stats_analytics_foundation';
    END IF;
END $$;

ALTER TABLE stats_reconciliation_diffs
  DROP CONSTRAINT IF EXISTS stats_reconciliation_diffs_resolution_check;

ALTER TABLE stats_reconciliation_diffs
  ADD CONSTRAINT stats_reconciliation_diffs_resolution_check
  CHECK (resolution IN ('open','auto_repair_pending','auto_repaired','phantom_open','rejected','approved','adjusted'))
  NOT VALID;

-- VALIDATE outside the ALTER ... ADD chain. Takes SHARE UPDATE
-- EXCLUSIVE only; readers and writers are not blocked.
ALTER TABLE stats_reconciliation_diffs
  VALIDATE CONSTRAINT stats_reconciliation_diffs_resolution_check;

INSERT INTO public.schema_migrations (version, description)
VALUES ('545', 'stats_reconciliation_diffs: allow phantom_open and adjusted resolutions')
ON CONFLICT (version) DO NOTHING;

COMMIT;
