-- 352_add_grok_4_6.sql
-- Add grok-4.6 model to the gateway.

BEGIN;

INSERT INTO models_canonical (
    canonical_name, family, context_window, modality, status, source, created_at, updated_at
)
VALUES (
    'grok-4.6', 'grok', 500, 'vision', 'active', 'migration-352', NOW(), NOW()
)
ON CONFLICT (canonical_name) DO UPDATE
SET context_window = EXCLUDED.context_window,
    modality = EXCLUDED.modality,
    updated_at = NOW();

INSERT INTO model_aliases (canonical_id, raw_name, status, notes, created_at, updated_at)
SELECT mc.id, 'grok-4.6', 'active', 'xAI Grok 4.6', NOW(), NOW()
FROM models_canonical mc
WHERE mc.canonical_name = 'grok-4.6'
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

INSERT INTO model_aliases (canonical_id, raw_name, status, notes, created_at, updated_at)
SELECT mc.id, 'grok-4-6', 'active', 'Alias for grok-4.6', NOW(), NOW()
FROM models_canonical mc
WHERE mc.canonical_name = 'grok-4.6'
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

COMMIT;
