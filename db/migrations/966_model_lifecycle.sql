-- Migration 966: Model lifecycle management — released_at timestamp and auto-hide stale models
--
-- Context:
--   Models released >2 years ago clutter the routing candidate pool and
--   consume monitoring resources. This migration adds released_at tracking
--   and provides infrastructure for automatic deprecation.
--
-- Changes:
--   1. Add models_canonical.released_at (nullable, no default — backfill required)
--   2. Add released_at index for lifecycle queries
--   3. Add stale_threshold setting (default '2 years')
--
-- Rollback: down migration drops column and setting

BEGIN;

-- Add released_at column
ALTER TABLE models_canonical
  ADD COLUMN released_at TIMESTAMPTZ;

COMMENT ON COLUMN models_canonical.released_at IS
  'Model release date from official vendor timeline. NULL = unknown/not yet backfilled. '
  'Used for lifecycle management: models older than stale_threshold may be auto-hidden '
  'from routing to reduce candidate pool noise.';

-- Index for lifecycle queries (find stale models, filter by age)
CREATE INDEX idx_models_canonical_released_at
  ON models_canonical(released_at)
  WHERE released_at IS NOT NULL AND status != 'disabled';

-- Add lifecycle management setting
INSERT INTO settings_kv (key, value, value_type, description, updated_at)
VALUES (
  'model_lifecycle.stale_threshold',
  '"2 years"'::jsonb,
  'string',
  'Models older than this threshold are considered stale and may be auto-hidden from routing. '
  'Format: PostgreSQL interval (e.g., "2 years", "730 days"). '
  'Change via admin API: PATCH /api/admin/settings',
  now()
) ON CONFLICT (key) DO NOTHING;

COMMIT;
