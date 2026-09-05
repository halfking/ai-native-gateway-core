-- 355_add_gemini_3_series.sql
-- Add Google Gemini model IDs confirmed by the official model catalog.

BEGIN;

INSERT INTO models_canonical (canonical_name, family, context_window, modality, status, source, created_at, updated_at)
VALUES
    ('gemini-3.6-flash', 'google-gemini', NULL, 'multimodal', 'active', 'migration-355', NOW(), NOW()),
    ('gemini-3.5-flash', 'google-gemini', NULL, 'multimodal', 'active', 'migration-355', NOW(), NOW()),
    ('gemini-3.5-flash-lite', 'google-gemini', NULL, 'multimodal', 'active', 'migration-355', NOW(), NOW()),
    ('gemini-3.1-flash-lite', 'google-gemini', NULL, 'multimodal', 'active', 'migration-355', NOW(), NOW()),
    ('gemini-3.1-flash-lite-image', 'google-gemini', NULL, 'multimodal', 'active', 'migration-355', NOW(), NOW()),
    ('gemini-3.1-pro-preview', 'google-gemini', NULL, 'multimodal', 'active', 'migration-355', NOW(), NOW()),
    ('gemini-3.1-flash-image', 'google-gemini', NULL, 'multimodal', 'active', 'migration-355', NOW(), NOW()),
    ('gemini-3-pro-image', 'google-gemini', NULL, 'multimodal', 'active', 'migration-355', NOW(), NOW()),
    ('gemini-3-flash-preview', 'google-gemini', NULL, 'multimodal', 'active', 'migration-355', NOW(), NOW()),
    ('gemini-omni-flash', 'google-gemini', NULL, 'multimodal', 'active', 'migration-355', NOW(), NOW())
ON CONFLICT (canonical_name) DO UPDATE
SET modality = EXCLUDED.modality, updated_at = NOW();

INSERT INTO model_aliases (canonical_id, raw_name, status, notes, created_at, updated_at)
SELECT id, canonical_name, 'active', 'Google Gemini official model catalog ID', NOW(), NOW()
FROM models_canonical
WHERE canonical_name IN (
    'gemini-3.6-flash', 'gemini-3.5-flash', 'gemini-3.5-flash-lite',
    'gemini-3.1-flash-lite', 'gemini-3.1-flash-lite-image', 'gemini-3.1-pro-preview',
    'gemini-3.1-flash-image', 'gemini-3-pro-image', 'gemini-3-flash-preview',
    'gemini-omni-flash'
)
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

COMMIT;
