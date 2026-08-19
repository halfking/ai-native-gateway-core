-- 358_fix_kimi_k3_modality.sql
-- Align kimi-k3 modality with the gateway's Go model registry, which declares
-- kimi-k3 as 'multimodal' (modelname/modality_defaults.go). Migration 354 wrote
-- 'vision' into models_canonical, so the DB and the running gateway disagreed.
-- kimi-k3 officially supports text + image + video, so 'multimodal' is correct.
--
-- This is a separate, additive migration (per the migration-immutability
-- convention: 354 is NOT modified). Idempotent: re-running is a no-op once
-- modality = 'multimodal'.

BEGIN;

UPDATE models_canonical
SET modality = 'multimodal', updated_at = NOW()
WHERE canonical_name = 'kimi-k3'
  AND modality IS DISTINCT FROM 'multimodal';

COMMIT;
