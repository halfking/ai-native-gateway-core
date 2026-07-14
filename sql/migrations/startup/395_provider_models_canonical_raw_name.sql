-- =============================================================================
-- Migration 395: provider_models.canonical_raw_name (lowercase client-side key)
-- Created:     2026-07-14
-- Author:      gateway maintainers (NIM model_not_found + global case audit)
--
-- Background (2026-07-14):
--   The gateway uses two parallel model-name spaces:
--
--     • CLIENT space: canonical_name / canonical_raw_name / standardized_name
--       / model_aliases.raw_name — these are matched internally and ALWAYS
--       use the lowercase form so SQL equality can replace lower(col)=lower($1).
--     • PROVIDER space: provider_models.raw_model_name /
--       outbound_model_name — these are the names the upstream requires and
--       MUST preserve the provider's original casing (e.g. NVIDIA NIM
--       "z-ai/glm-5.2", Meta "meta/llama-3.3-70b-instruct").
--
--   Previously the gateway stored only the provider-cased raw_model_name and
--   then matched clients via `lower(col) = lower($1)`. This made per-tenant
--   model matching case-fragile (any casing drift created duplicate rows,
--   broke indexes, and triggered model_not_found from upstreams when the
--   wrong case leaked into a request body).
--
-- What this migration does:
--   1. Adds provider_models.canonical_raw_name text (nullable initially to
--      support backfill on populated DBs; transition to NOT NULL after the
--      backfill is verified by the post-flight SELECT below).
--   2. Backfills it from raw_model_name using LOWER(...) since every existing
--      row's raw_model_name was either a vendor-prefixed all-lowercase id
--      (NVIDIA NIM, Meta, OpenAI) or a fully lowercase Chinese-market id
--      (glm-5.1, minimax-m3, ...). If a row stored a mixed-case legacy value
--      the backfill downcases it so the new key is the canonical form.
--   3. Adds UNIQUE (provider_id, canonical_raw_name) so a credential cannot
--      register the same model twice under two different casings.
--   4. Adds btree index idx_provider_models_canonical_raw_name.
--
-- What this migration does NOT do:
--   - It does NOT alter provider_models.raw_model_name or
--     outbound_model_name; both remain the upstream-facing names.
--   - It does NOT rename existing indexes. idx_provider_models_standardized
--     stays usable as-is.
--   - It does NOT change models_canonical.canonical_name in this migration.
--     A follow-up 396 (planned) will normalise that table + model_aliases.
--
-- Idempotency:
--   - ADD COLUMN IF NOT EXISTS (Postgres 9.6+).
--   - Backfill is an idempotent UPDATE.
--   - UNIQUE constraint is added with a one-shot try/catch via DO $$; if the
--     DB already has duplicate (provider_id, canonical_raw_name) rows
--     (from historical drift), the script logs them so the operator can
--     reconcile manually before re-running.
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== 395 provider_models.canonical_raw_name backfill ==='

-- ---------------------------------------------------------------------------
-- 1. Add the column. NOT NULL is deferred until after backfill.
-- ---------------------------------------------------------------------------
ALTER TABLE public.provider_models
    ADD COLUMN IF NOT EXISTS canonical_raw_name text;

\echo '--- 1. column added (nullable) ---'

-- ---------------------------------------------------------------------------
-- 2. Backfill canonical_raw_name from raw_model_name using LOWER(...).
--    Trims because some seed rows historically had trailing/leading spaces.
-- ---------------------------------------------------------------------------
UPDATE public.provider_models
SET canonical_raw_name = LOWER(TRIM(raw_model_name))
WHERE canonical_raw_name IS NULL
   OR canonical_raw_name <> LOWER(TRIM(raw_model_name));

\echo '--- 2. backfilled canonical_raw_name from raw_model_name ---'

-- ---------------------------------------------------------------------------
-- 3. Promote to NOT NULL once we know no NULL remains.
-- ---------------------------------------------------------------------------
ALTER TABLE public.provider_models
    ALTER COLUMN canonical_raw_name SET NOT NULL;

\echo '--- 3. column set NOT NULL ---'

-- ---------------------------------------------------------------------------
-- 4. Report any (provider_id, canonical_raw_name) duplicates that would
--    block the UNIQUE index. The migration does NOT silently merge these;
--    the operator decides. After cleanup, re-run the migration.
-- ---------------------------------------------------------------------------
\echo '--- 4. duplicate (provider_id, canonical_raw_name) groups (must be 0) ---'
SELECT provider_id, canonical_raw_name, COUNT(*) AS n
FROM public.provider_models
GROUP BY provider_id, canonical_raw_name
HAVING COUNT(*) > 1
ORDER BY n DESC, provider_id, canonical_raw_name;

-- ---------------------------------------------------------------------------
-- 5. Add the UNIQUE index.
--    We use a plain CREATE UNIQUE INDEX rather than ADD CONSTRAINT to avoid
--    constraint-name churn across environments (the constraint name is the
--    same as the index name; recreating is idempotent).
-- ---------------------------------------------------------------------------
CREATE UNIQUE INDEX IF NOT EXISTS uq_provider_models_canonical_raw_name
    ON public.provider_models (provider_id, canonical_raw_name);

\echo '--- 5. uq_provider_models_canonical_raw_name created ---'

-- ---------------------------------------------------------------------------
-- 6. Companion btree index for non-UNIQUE lookups (e.g. existence probes).
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_provider_models_canonical_raw_name
    ON public.provider_models (canonical_raw_name);

\echo '--- 6. idx_provider_models_canonical_raw_name created ---'

-- ---------------------------------------------------------------------------
-- 7. Post-flight: confirm coverage and integrity.
-- ---------------------------------------------------------------------------
\echo '--- 7. coverage: rows with NULL canonical_raw_name (expect 0) ---'
SELECT COUNT(*) AS null_count
FROM public.provider_models
WHERE canonical_raw_name IS NULL;

\echo '--- 7. coverage: rows where canonical_raw_name differs from raw_model_name ---'
SELECT COUNT(*) AS mismatch_count
FROM public.provider_models
WHERE canonical_raw_name <> raw_model_name;

\echo '--- 7. sample of mismatches (operator review; do NOT touch raw_model_name) ---'
SELECT id, provider_id, raw_model_name, canonical_raw_name
FROM public.provider_models
WHERE canonical_raw_name <> raw_model_name
ORDER BY provider_id, raw_model_name
LIMIT 50;

\echo '=== 395 provider_models.canonical_raw_name backfill: done ==='
