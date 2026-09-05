-- 359_canonical_metadata_sync.down.sql
-- Revert reasoning_caps JSONB backfill for the standard 3-family rollout.
-- Modality values are NOT rolled back (those changes belong to 352-358).
-- Idempotent: re-running is a no-op once reasoning_caps IS NULL.
--
-- 审计修复（2026-08-20）：仅在 reasoning_caps->>'source' = 'migration-359' 时清空，
-- 避免误清运营热改的 reasoning_caps。

BEGIN;

UPDATE models_canonical
SET reasoning_caps = NULL, updated_at = NOW()
WHERE canonical_name = 'grok-4.6'
  AND reasoning_caps IS NOT NULL
  AND reasoning_caps->>'source' = 'migration-359';

UPDATE models_canonical
SET reasoning_caps = NULL, updated_at = NOW()
WHERE canonical_name IN (
    'kimi-k3', 'kimi-k2.6', 'kimi-k2.7-code', 'kimi-k2.7-code-highspeed'
)
  AND reasoning_caps IS NOT NULL
  AND reasoning_caps->>'source' = 'migration-359';

UPDATE models_canonical
SET reasoning_caps = NULL, updated_at = NOW()
WHERE family = 'google-gemini'
  AND canonical_name LIKE 'gemini-3%'
  AND reasoning_caps IS NOT NULL
  AND reasoning_caps->>'source' = 'migration-359';

COMMIT;