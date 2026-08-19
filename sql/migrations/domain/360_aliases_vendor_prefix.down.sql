-- 360_aliases_vendor_prefix.down.sql
-- Revert vendor-prefix aliases added by 360.
-- Only removes the alias rows; canonical rows from 352/354/355 are untouched.

BEGIN;

DELETE FROM model_aliases ma
USING models_canonical mc
WHERE ma.canonical_id = mc.id
  AND (
        (mc.canonical_name = 'grok-4.6'
            AND ma.raw_name IN ('openai/grok-4.6', 'grok-4-6'))
     OR (mc.canonical_name = 'kimi-k3'
            AND ma.raw_name IN ('moonshot-v1/kimi-k3', 'kimi-k3-0528'))
     OR (mc.canonical_name = 'kimi-k2.6'
            AND ma.raw_name IN ('moonshot-v1/kimi-k2.6', 'kimi-k2-6'))
     OR (mc.canonical_name = 'kimi-k2.7-code'
            AND ma.raw_name = 'moonshot-v1/kimi-k2.7-code')
     OR (mc.canonical_name = 'kimi-k2.7-code-highspeed'
            AND ma.raw_name = 'moonshot-v1/kimi-k2.7-code-highspeed')
     OR (mc.family = 'google-gemini' AND mc.canonical_name LIKE 'gemini-3%'
            AND (ma.raw_name LIKE 'google/gemini-3%' OR ma.raw_name LIKE 'models/gemini-3%'))
  );

COMMIT;