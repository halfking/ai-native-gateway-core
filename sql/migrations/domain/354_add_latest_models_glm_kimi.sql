-- 354_add_latest_models_glm_kimi.sql
-- Add officially documented GLM-5.3 and Kimi models.

BEGIN;

INSERT INTO models_canonical (canonical_name, family, context_window, modality, status, source, created_at, updated_at)
VALUES ('glm-5.3', 'glm', 1000, 'text', 'active', 'migration-354', NOW(), NOW())
ON CONFLICT (canonical_name) DO UPDATE
SET context_window = EXCLUDED.context_window, modality = EXCLUDED.modality, updated_at = NOW();

INSERT INTO model_aliases (canonical_id, raw_name, status, notes, created_at, updated_at)
SELECT id, 'glm-5.3', 'active', 'Z.AI GLM-5.3; 1M context; reasoning is mandatory', NOW(), NOW()
FROM models_canonical WHERE canonical_name = 'glm-5.3'
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

INSERT INTO model_aliases (canonical_id, raw_name, status, notes, created_at, updated_at)
SELECT id, 'z-ai/glm-5.3', 'active', 'Z.AI alias for glm-5.3', NOW(), NOW()
FROM models_canonical WHERE canonical_name = 'glm-5.3'
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

INSERT INTO models_canonical (canonical_name, family, context_window, modality, status, source, created_at, updated_at)
VALUES
    ('kimi-k3', 'kimi', 1000, 'vision', 'active', 'migration-354', NOW(), NOW()),
    ('kimi-k2.6', 'kimi', 256, 'vision', 'active', 'migration-354', NOW(), NOW()),
    ('kimi-k2.7-code', 'kimi', 256, 'text', 'active', 'migration-354', NOW(), NOW()),
    ('kimi-k2.7-code-highspeed', 'kimi', NULL, 'text', 'active', 'migration-354', NOW(), NOW())
ON CONFLICT (canonical_name) DO UPDATE
SET context_window = EXCLUDED.context_window, modality = EXCLUDED.modality, updated_at = NOW();

INSERT INTO model_aliases (canonical_id, raw_name, status, notes, created_at, updated_at)
SELECT id, canonical_name, 'active', 'Moonshot/Kimi officially documented model', NOW(), NOW()
FROM models_canonical
WHERE canonical_name IN ('kimi-k3', 'kimi-k2.6', 'kimi-k2.7-code', 'kimi-k2.7-code-highspeed')
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

COMMIT;
