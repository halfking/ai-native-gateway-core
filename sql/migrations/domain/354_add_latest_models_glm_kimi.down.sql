-- 354_add_latest_models_glm_kimi.down.sql
-- Rollback GLM-5.3 and Kimi K3/K2.6/K2.7-code models

BEGIN;

-- Remove model aliases
DELETE FROM model_aliases
WHERE canonical_id IN (
    SELECT id FROM models_canonical 
    WHERE canonical_name IN ('glm-5.3', 'kimi-k3', 'kimi-k2.6', 'kimi-k2.7-code', 'kimi-k2.7-code-highspeed')
);

-- Remove models from models_canonical
DELETE FROM models_canonical
WHERE canonical_name IN ('glm-5.3', 'kimi-k3', 'kimi-k2.6', 'kimi-k2.7-code', 'kimi-k2.7-code-highspeed');

COMMIT;
