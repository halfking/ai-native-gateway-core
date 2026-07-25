-- ===========================================================================
-- Fix: Add missing UNIQUE constraints and column defaults for model discovery
--
-- Root cause: The Go code uses ON CONFLICT (...) clauses on 4 tables,
-- but 2 tables lacked UNIQUE constraints and 1 table was missing its
-- column DEFAULT, causing every upsert to fail with:
--   "no unique constraint matching ON CONFLICT specification" (42P10)
--   "null value in column id violates not-null constraint" (23502)
--
-- Impact: Model discovery returns credentials=65 models=0 → all API
-- requests fail with 503 model_not_found / no_candidates
-- ===========================================================================

-- 1. credential_model_bindings: missing UNIQUE (credential_id, provider_model_id)
--    Used by upsertCredentialModelSQL ON CONFLICT (credential_id, provider_model_id)
ALTER TABLE credential_model_bindings
  ADD CONSTRAINT uq_cmb_credential_provider_model
  UNIQUE (credential_id, provider_model_id);

-- 2. provider_models: missing UNIQUE (provider_id, raw_model_name)
--    Used by upsertCredentialModelSQL ON CONFLICT (provider_id, raw_model_name)
ALTER TABLE provider_models
  ADD CONSTRAINT uq_provider_models_provider_raw
  UNIQUE (provider_id, raw_model_name);

-- 3. models_canonical: missing UNIQUE (canonical_name)
--    Used by upsertModel ON CONFLICT (canonical_name)
ALTER TABLE models_canonical
  ADD CONSTRAINT uq_models_canonical_name
  UNIQUE (canonical_name);

-- 4. models_canonical.id: missing DEFAULT (sequence existed but was never
--    attached to the column after the column type change from SERIAL)
ALTER TABLE models_canonical
  ALTER COLUMN id SET DEFAULT nextval('models_canonical_id_seq');

-- 5. model_aliases: missing UNIQUE (raw_name)
--    Used by upsertModel ON CONFLICT (raw_name)
ALTER TABLE model_aliases
  ADD CONSTRAINT uq_model_aliases_raw_name
  UNIQUE (raw_name);
