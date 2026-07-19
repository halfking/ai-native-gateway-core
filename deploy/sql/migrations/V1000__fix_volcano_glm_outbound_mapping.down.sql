-- ===========================================================================
-- File:          deploy/sql/migrations/V1000__fix_volcano_glm_outbound_mapping.down.sql
-- Database:      llm_gateway
-- Object Type:   ROLLBACK (DML, provider mapping correction)
-- Purpose:       Restore provider-model mappings captured before V1000.
--
-- Status:        active
-- Idempotent:    YES
-- Changelog:
--   2026-07-19  v1.0  Add rollback for V1000 outbound mapping correction
-- Rollback:      Reapply V1000 after validating the supplier mapping.
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;

UPDATE provider_models pm
SET canonical_id = backup.canonical_id,
    standardized_name = backup.standardized_name,
    outbound_model_name = backup.outbound_model_name,
    updated_at = NOW()
FROM public.provider_models_v1000_backup backup
WHERE pm.id = backup.provider_model_id;

COMMIT;
