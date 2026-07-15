-- Migration 342 down: remove capability profile foundation.
--
-- The compatibility normalisation of credentials is intentionally retained:
-- recreating an invalid availability_state value would violate the current
-- schema contract. The recent_success_rate function is retained as well,
-- because request_logs_hot is the live source for the fixed signature.

BEGIN;

DROP TABLE IF EXISTS model_substitution_overrides;
DROP TABLE IF EXISTS model_capability_profile_audit;
DROP INDEX IF EXISTS idx_model_capability_profiles_modality_caps;
DROP INDEX IF EXISTS idx_model_capability_profiles_score;
DROP TABLE IF EXISTS model_capability_profiles;

COMMIT;
