-- 353_update_xai_provider_add_grok_4_6.down.sql
-- Remove only the grok-4.6 manifest entry added by migration 353.

BEGIN;

UPDATE provider_catalog
SET models_manifest_json = COALESCE(
    (SELECT jsonb_agg(item)
     FROM jsonb_array_elements(COALESCE(models_manifest_json, '[]'::jsonb)) item
     WHERE item->>'id' <> 'grok-4.6'),
    '[]'::jsonb
), updated_at = NOW()
WHERE code = 'xai';

COMMIT;
