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

-- ===========================================================================
-- Pre-check: detect existing duplicates before adding UNIQUE constraints.
-- This guards against ALTER ADD CONSTRAINT failure on production data
-- where historical duplicates may exist.
-- ===========================================================================

DO $$
DECLARE
    dup_count INT;
    dup_detail TEXT;
    total_issues INT := 0;
BEGIN
    -- 1. credential_model_bindings (credential_id, provider_model_id)
    SELECT count(*) INTO dup_count FROM (
        SELECT credential_id, provider_model_id
        FROM credential_model_bindings
        GROUP BY credential_id, provider_model_id
        HAVING count(*) > 1
    ) d;
    IF dup_count > 0 THEN
        SELECT string_agg(format('  credential_id=%s, provider_model_id=%s (%s rows)', d.credential_id, d.provider_model_id, d.cnt), E'\n')
        INTO dup_detail
        FROM (SELECT credential_id, provider_model_id, count(*) AS cnt FROM credential_model_bindings GROUP BY credential_id, provider_model_id HAVING count(*) > 1 ORDER BY cnt DESC LIMIT 10) d;
        RAISE WARNING 'credential_model_bindings: % dup groups:%', dup_count, E'\n' || dup_detail;
        total_issues := total_issues + dup_count;
    ELSE
        RAISE NOTICE 'credential_model_bindings: clean';
    END IF;

    -- 2. provider_models (provider_id, raw_model_name)
    SELECT count(*) INTO dup_count FROM (
        SELECT provider_id, raw_model_name FROM provider_models
        GROUP BY provider_id, raw_model_name HAVING count(*) > 1
    ) d;
    IF dup_count > 0 THEN
        SELECT string_agg(format('  provider_id=%s, raw_model_name=%s (%s rows)', d.provider_id, d.raw_model_name, d.cnt), E'\n')
        INTO dup_detail
        FROM (SELECT provider_id, raw_model_name, count(*) AS cnt FROM provider_models GROUP BY provider_id, raw_model_name HAVING count(*) > 1 ORDER BY cnt DESC LIMIT 10) d;
        RAISE WARNING 'provider_models: % dup groups:%', dup_count, E'\n' || dup_detail;
        total_issues := total_issues + dup_count;
    ELSE
        RAISE NOTICE 'provider_models: clean';
    END IF;

    -- 3. models_canonical (canonical_name)
    SELECT count(*) INTO dup_count FROM (
        SELECT canonical_name FROM models_canonical
        GROUP BY canonical_name HAVING count(*) > 1
    ) d;
    IF dup_count > 0 THEN
        SELECT string_agg(format('  canonical_name=%s (%s rows)', d.canonical_name, d.cnt), E'\n')
        INTO dup_detail
        FROM (SELECT canonical_name, count(*) AS cnt FROM models_canonical GROUP BY canonical_name HAVING count(*) > 1 ORDER BY cnt DESC LIMIT 10) d;
        RAISE WARNING 'models_canonical: % dup groups:%', dup_count, E'\n' || dup_detail;
        total_issues := total_issues + dup_count;
    ELSE
        RAISE NOTICE 'models_canonical: clean';
    END IF;

    -- 4. model_aliases (raw_name)
    SELECT count(*) INTO dup_count FROM (
        SELECT raw_name FROM model_aliases
        GROUP BY raw_name HAVING count(*) > 1
    ) d;
    IF dup_count > 0 THEN
        SELECT string_agg(format('  raw_name=%s (%s rows)', d.raw_name, d.cnt), E'\n')
        INTO dup_detail
        FROM (SELECT raw_name, count(*) AS cnt FROM model_aliases GROUP BY raw_name HAVING count(*) > 1 ORDER BY cnt DESC LIMIT 10) d;
        RAISE WARNING 'model_aliases: % dup groups:%', dup_count, E'\n' || dup_detail;
        total_issues := total_issues + dup_count;
    ELSE
        RAISE NOTICE 'model_aliases: clean';
    END IF;

    IF total_issues > 0 THEN
        RAISE EXCEPTION '% duplicate groups found — resolve before ALTER ADD CONSTRAINT', total_issues;
    END IF;
END;
$$;

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
