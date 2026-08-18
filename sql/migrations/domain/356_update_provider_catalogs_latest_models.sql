-- 356_update_provider_catalogs_latest_models.sql
-- Append officially documented model IDs without replacing existing manifests.

BEGIN;

UPDATE provider_catalog
SET models_manifest_json = (
    SELECT jsonb_agg(item ORDER BY item->>'id')
    FROM (
        SELECT DISTINCT item FROM jsonb_array_elements(COALESCE(models_manifest_json, '[]'::jsonb)) item
        UNION SELECT jsonb_build_object('id', 'glm-5.3', 'ctx_k', 1000, 'display_name', 'GLM-5.3')
    ) items
), updated_at = NOW()
WHERE code = 'zhipu';

UPDATE provider_catalog
SET models_manifest_json = (
    SELECT jsonb_agg(item ORDER BY item->>'id')
    FROM (
        SELECT DISTINCT item FROM jsonb_array_elements(COALESCE(models_manifest_json, '[]'::jsonb)) item
        UNION SELECT jsonb_build_object('id', 'kimi-k3', 'ctx_k', 1000, 'display_name', 'Kimi K3')
        UNION SELECT jsonb_build_object('id', 'kimi-k2.6', 'ctx_k', 256, 'display_name', 'Kimi K2.6')
        UNION SELECT jsonb_build_object('id', 'kimi-k2.7-code', 'ctx_k', 256, 'display_name', 'Kimi K2.7 Code')
        UNION SELECT jsonb_build_object('id', 'kimi-k2.7-code-highspeed', 'display_name', 'Kimi K2.7 Code Highspeed')
    ) items
), updated_at = NOW()
WHERE code = 'moonshot';

UPDATE provider_catalog
SET models_manifest_json = (
    SELECT jsonb_agg(item ORDER BY item->>'id')
    FROM (
        SELECT DISTINCT item FROM jsonb_array_elements(COALESCE(models_manifest_json, '[]'::jsonb)) item
        UNION SELECT jsonb_build_object('id', 'gemini-3.6-flash', 'display_name', 'Gemini 3.6 Flash')
        UNION SELECT jsonb_build_object('id', 'gemini-3.5-flash', 'display_name', 'Gemini 3.5 Flash')
        UNION SELECT jsonb_build_object('id', 'gemini-3.5-flash-lite', 'display_name', 'Gemini 3.5 Flash Lite')
        UNION SELECT jsonb_build_object('id', 'gemini-3.1-flash-lite', 'display_name', 'Gemini 3.1 Flash Lite')
        UNION SELECT jsonb_build_object('id', 'gemini-3.1-flash-lite-image', 'display_name', 'Gemini 3.1 Flash Lite Image')
        UNION SELECT jsonb_build_object('id', 'gemini-3.1-pro-preview', 'display_name', 'Gemini 3.1 Pro Preview')
        UNION SELECT jsonb_build_object('id', 'gemini-3.1-flash-image', 'display_name', 'Gemini 3.1 Flash Image')
        UNION SELECT jsonb_build_object('id', 'gemini-3-pro-image', 'display_name', 'Gemini 3 Pro Image')
        UNION SELECT jsonb_build_object('id', 'gemini-3-flash-preview', 'display_name', 'Gemini 3 Flash Preview')
        UNION SELECT jsonb_build_object('id', 'gemini-omni-flash', 'display_name', 'Gemini Omni Flash')
    ) items
), updated_at = NOW()
WHERE code = 'google-gemini';

COMMIT;
