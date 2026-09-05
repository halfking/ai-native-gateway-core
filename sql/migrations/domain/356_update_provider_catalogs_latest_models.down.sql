-- 356_update_provider_catalogs_latest_models.down.sql
-- Remove only the model IDs added by migration 356.

BEGIN;

-- Remove GLM-5.3 from zhipu provider
UPDATE provider_catalog
SET models_manifest_json = COALESCE(
    (SELECT jsonb_agg(item)
     FROM jsonb_array_elements(COALESCE(models_manifest_json, '[]'::jsonb)) item
     WHERE item->>'id' NOT IN ('glm-5.3')),
    '[]'::jsonb
), updated_at = NOW()
WHERE code = 'zhipu';

-- Remove Kimi K3/K2.6/K2.7 from moonshot provider
UPDATE provider_catalog
SET models_manifest_json = COALESCE(
    (SELECT jsonb_agg(item)
     FROM jsonb_array_elements(COALESCE(models_manifest_json, '[]'::jsonb)) item
     WHERE item->>'id' NOT IN ('kimi-k3', 'kimi-k2.6', 'kimi-k2.7-code', 'kimi-k2.7-code-highspeed')),
    '[]'::jsonb
), updated_at = NOW()
WHERE code = 'moonshot';

-- Remove Gemini 3 series from google-gemini provider
UPDATE provider_catalog
SET models_manifest_json = COALESCE(
    (SELECT jsonb_agg(item)
     FROM jsonb_array_elements(COALESCE(models_manifest_json, '[]'::jsonb)) item
     WHERE item->>'id' NOT IN (
         'gemini-3.6-flash', 'gemini-3.5-flash', 'gemini-3.5-flash-lite',
         'gemini-3.1-flash-lite', 'gemini-3.1-flash-lite-image', 'gemini-3.1-pro-preview',
         'gemini-3.1-flash-image', 'gemini-3-pro-image', 'gemini-3-flash-preview',
         'gemini-omni-flash'
     )),
    '[]'::jsonb
), updated_at = NOW()
WHERE code = 'google-gemini';

COMMIT;
