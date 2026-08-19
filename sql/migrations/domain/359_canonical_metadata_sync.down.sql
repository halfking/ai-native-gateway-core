-- 359_canonical_metadata_sync.down.sql
-- Revert reasoning_caps JSONB backfill for the standard 3-family rollout.
-- Modality values are NOT rolled back (those changes belong to 352-358).
-- Idempotent: re-running is a no-op once reasoning_caps IS NULL.

BEGIN;

UPDATE models_canonical
SET reasoning_caps = NULL, updated_at = NOW()
WHERE canonical_name = 'grok-4.6'
  AND reasoning_caps IS NOT NULL
  AND reasoning_caps->>'source' IS NULL;  -- never clobber operator overrides

UPDATE models_canonical
SET reasoning_caps = NULL, updated_at = NOW()
WHERE canonical_name IN (
    'kimi-k3', 'kimi-k2.6', 'kimi-k2.7-code', 'kimi-k2.7-code-highspeed'
)
  AND reasoning_caps IS NOT NULL
  AND reasoning_caps->>'source' IS NULL;

UPDATE models_canonical
SET reasoning_caps = NULL, updated_at = NOW()
WHERE family = 'google-gemini'
  AND canonical_name LIKE 'gemini-3%'
  AND reasoning_caps IS NOT NULL
  AND reasoning_caps->>'source' IS NULL;

COMMIT;