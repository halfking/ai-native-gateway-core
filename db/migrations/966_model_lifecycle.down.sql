-- Rollback migration 966: Model lifecycle management

BEGIN;

DELETE FROM settings_kv WHERE key = 'model_lifecycle.stale_threshold';

DROP INDEX IF EXISTS idx_models_canonical_released;

ALTER TABLE models_canonical DROP COLUMN IF EXISTS released_at;

COMMIT;
