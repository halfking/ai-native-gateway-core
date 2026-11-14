-- Rollback migration 966: Model lifecycle management

BEGIN;

-- Remove lifecycle setting
DELETE FROM settings_kv WHERE key = 'model_lifecycle.stale_threshold';

-- Drop index
DROP INDEX IF EXISTS idx_models_canonical_released_at;

-- Drop column
ALTER TABLE models_canonical DROP COLUMN IF EXISTS released_at;

COMMIT;
