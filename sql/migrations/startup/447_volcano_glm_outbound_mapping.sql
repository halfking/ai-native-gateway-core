-- ===========================================================================
-- File:          sql/migrations/startup/447_volcano_glm_outbound_mapping.sql
-- Database:      llm_gateway
-- Object Type:   MIGRATION (DML, provider mapping correction)
-- Purpose:       Map Volcano GLM standard names to the supplier model ID
--
-- Status:        active
-- Idempotent:    YES
-- Changelog:
--   2026-07-19  v1.0  Fix Volcano GLM outbound model mapping after direct API verification
-- Rollback:      sql/migrations/startup/447_volcano_glm_outbound_mapping.down.sql
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;

-- Preserve only the rows this migration modifies. The backup makes the
-- companion down migration deterministic without guessing prior mappings.
CREATE TABLE IF NOT EXISTS public.provider_models_v1000_backup (
    provider_model_id BIGINT PRIMARY KEY,
    canonical_id BIGINT,
    standardized_name TEXT,
    outbound_model_name TEXT
);

INSERT INTO public.provider_models_v1000_backup (
    provider_model_id,
    canonical_id,
    standardized_name,
    outbound_model_name
)
SELECT pm.id, pm.canonical_id, pm.standardized_name, pm.outbound_model_name
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
WHERE p.code IN ('volcano-normal', 'volcano-tokenplan')
  AND pm.raw_model_name IN ('glm-5.1', 'glm-5-2-260617')
ON CONFLICT (provider_model_id) DO NOTHING;

-- The supplier accepts glm-5-2-260617 (verified with HTTP 200). Keep the
-- gateway standard name glm-5.1, but send the verified supplier model ID.
UPDATE provider_models pm
SET outbound_model_name = 'glm-5-2-260617',
    standardized_name = 'glm-5.1',
    updated_at = NOW()
FROM providers p
WHERE pm.provider_id = p.id
  AND p.code IN ('volcano-normal', 'volcano-tokenplan')
  AND pm.raw_model_name = 'glm-5.1'
  AND pm.canonical_id = (
      SELECT id FROM models_canonical WHERE canonical_name = 'glm-5.1'
  );

-- The discovered supplier row is the source for the gateway standard name
-- glm-5.2. Its raw value is already the verified supplier model ID, so the
-- fallback COALESCE(outbound_model_name, raw_model_name) is intentional.
UPDATE provider_models pm
SET canonical_id = (
        SELECT id FROM models_canonical WHERE canonical_name = 'glm-5.2'
    ),
    standardized_name = 'glm-5.2',
    outbound_model_name = NULL,
    updated_at = NOW()
FROM providers p
WHERE pm.provider_id = p.id
  AND p.code IN ('volcano-normal', 'volcano-tokenplan')
  AND pm.raw_model_name = 'glm-5-2-260617';

COMMIT;
