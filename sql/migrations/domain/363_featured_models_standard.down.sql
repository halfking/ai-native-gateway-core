-- 363_featured_models_standard.down.sql
-- Remove the 5 standard family IDs added by 363.
-- Preserves any operator-edited featured_models entries.

BEGIN;

UPDATE routing_policy
SET featured_models = (
    SELECT COALESCE(array_agg(m) FILTER (WHERE m NOT IN (
        'grok-4.6', 'kimi-k3', 'kimi-k2.6',
        'gemini-3.5-flash', 'gemini-3-flash-preview'
    )), ARRAY[]::text[])
    FROM unnest(featured_models) AS m
),
updated_at = NOW()
WHERE id = 1;

COMMIT;