-- Migration 966: Model lifecycle management — released_at timestamp
--
-- Context:
--   Models released years ago clutter the routing candidate pool. This adds
--   released_at so the router can drop stale models (default threshold 2 years,
--   configurable via settings_kv 'model_lifecycle.stale_threshold').
--
-- ★ Design note — NULL means "unknown age" and is fail-OPEN at the router:
--   a model with no known release date keeps routing. Only a *known* date that
--   is older than the threshold excludes the model. A wrong date is far more
--   dangerous than a missing one (it silently removes a live model), which is
--   why cmd/tools/backfill-release-dates does exact-name matching only.
--
-- ★ Idempotent by design: the column/index/setting are all guarded with
--   IF NOT EXISTS / ON CONFLICT. This migration was first applied by hand on
--   211 during the 2026-11-14 investigation (column existed as `date` before
--   this file was written), so a naive ALTER would abort the whole migration
--   chain on that host. The DO block widens `date` → `timestamptz` in place.

BEGIN;

-- 1. released_at column (created as `date` on 211 by hand; widen to timestamptz).
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'models_canonical'
      AND column_name = 'released_at'
  ) THEN
    ALTER TABLE models_canonical ADD COLUMN released_at TIMESTAMPTZ;
  ELSE
    -- Already present (possibly as `date`). Widen without losing data.
    ALTER TABLE models_canonical
      ALTER COLUMN released_at TYPE TIMESTAMPTZ
      USING released_at::timestamptz;
  END IF;
END $$;

COMMENT ON COLUMN models_canonical.released_at IS
  'Vendor release date. NULL = unknown age (router fail-open). Known dates older than '
  'model_lifecycle.stale_threshold are excluded from routing unless manually prioritized.';

-- 2. Lifecycle query index. Keep the name the manual 211 setup used so both
--    paths converge on one index instead of creating a duplicate.
CREATE INDEX IF NOT EXISTS idx_models_canonical_released
  ON models_canonical(released_at DESC NULLS LAST);

-- 3. Threshold setting (jsonb string holding a PostgreSQL interval literal).
INSERT INTO settings_kv (key, value, value_type, scope, category, updated_at)
VALUES (
  'model_lifecycle.stale_threshold',
  '"2 years"'::jsonb,
  'string',
  'platform',
  'routing',
  now()
) ON CONFLICT (key) DO NOTHING;

COMMIT;
