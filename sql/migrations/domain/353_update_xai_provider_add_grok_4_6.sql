-- 353_update_xai_provider_add_grok_4_6.sql
-- Append grok-4.6 to xAI provider manifest without replacing existing entries.

BEGIN;

UPDATE provider_catalog
SET models_manifest_json = (
    SELECT jsonb_agg(item ORDER BY item->>'id')
    FROM (
        SELECT DISTINCT item FROM jsonb_array_elements(COALESCE(models_manifest_json, '[]'::jsonb)) item
        UNION SELECT jsonb_build_object('id', 'grok-4.6', 'ctx_k', 500, 'display_name', 'Grok 4.6')
    ) items
), updated_at = NOW()
WHERE code = 'xai';

COMMIT;
