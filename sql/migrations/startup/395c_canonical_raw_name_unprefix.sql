-- =============================================================================
-- Migration 395c: canonical_raw_name / standardized_name vendor-prefix strip
-- Created:     2026-07-14
-- Author:      gateway maintainers (NIM model_not_found follow-up)
--
-- Background (2026-07-14):
--   Migration 395 introduced provider_models.canonical_raw_name and the
--   396/395b chain set its value to `lower(raw_model_name)`. With
--   NIM's publisher-prefixed model names (e.g. "z-ai/glm-5.2",
--   "minimaxai/minimax-m3") this means the canonical key kept the
--   vendor prefix, so a client request for "glm-5.2" never matched
--   the NIM offer (canonical_raw_name = "z-ai/glm-5.2" != "glm-5.2").
--
--   modelname.CanonicalizeClientModel was therefore updated to also
--   strip the vendor prefix (mirrors NormalizeRouteKey), so going
--   forward new discoveries will write
--     provider_models.canonical_raw_name = "glm-5.2"            (no prefix)
--     provider_models.standardized_name = "glm-5.2"            (lowercase)
--   for NIM offers.
--
--   This migration backfills the existing rows so that the
--   loadCandidatesByModalityDB equality lookups work without
--   `lower(col)` wrappers. It also widens the unique index to
--   (provider_id, canonical_raw_name, raw_model_name) so two
--   publishers (e.g. "nvidia/llama-3.3-70b-instruct" and
--   "meta/llama-3.3-70b-instruct") can co-exist under the same
--   stripped canonical name on the same provider.
--
-- Strip semantics: this migration uses `lower(split_part(raw, '/', -1))`,
-- matching the NEW CanonicalizeClientModel contract. Date suffix
-- stripping (`-20251201`, `[1M]`) is intentionally NOT done here —
-- those edge cases (e.g. "z-ai/claude-opus-4-7-20251201") remain
-- `claude-opus-4-7-20251201` in canonical_raw_name, while a client
-- sending "claude-opus-4-7-20251201" would match. Operators who need
-- cross-form (date-stripped) matching should add a model_aliases row.
--
-- What this migration does:
--   1. Backfill provider_models.canonical_raw_name = lower(split_part(raw_model_name, '/', -1))
--      for every row whose current value does not match.
--   2. Backfill provider_models.standardized_name similarly.
--   3. Recreate uq_provider_models_canonical_raw_name as
--      (provider_id, canonical_raw_name, raw_model_name).
--   4. Post-flight: every row's canonical_raw_name ==
--      lower(split_part(raw_model_name, '/', -1)).
--
-- What this migration does NOT do:
--   - It does NOT touch model_aliases.raw_name or models_canonical.canonical_name
--     (those were already lowercased in migration 396).
--   - It does NOT touch provider_models.raw_model_name / outbound_model_name.
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== 395c canonical_raw_name / standardized_name vendor-prefix strip ==='

-- ---------------------------------------------------------------------------
-- 1. Snapshot pre-state.
-- ---------------------------------------------------------------------------
\echo '--- 1. snapshot pre-state (mixed counts) ---'
SELECT
    (SELECT COUNT(*) FROM provider_models
       WHERE canonical_raw_name <> lower(split_part(raw_model_name, '/', -1))) AS mixed_canonical_raw_name,
    (SELECT COUNT(*) FROM provider_models
       WHERE standardized_name IS NOT NULL
         AND standardized_name <> lower(split_part(raw_model_name, '/', -1))) AS mixed_standardized_name;

-- ---------------------------------------------------------------------------
-- 2. Backfill canonical_raw_name.
-- ---------------------------------------------------------------------------
\echo '--- 2. backfill canonical_raw_name = lower(split_part(raw_model_name, '/'/'/''/'-1)) ---'
UPDATE provider_models
SET canonical_raw_name = lower(split_part(raw_model_name, '/', -1)),
    updated_at = NOW()
WHERE canonical_raw_name <> lower(split_part(raw_model_name, '/', -1));

\echo '--- 2. post-flight canonical_raw_name coverage ---'
SELECT COUNT(*) AS mixed_canonical_raw_name
FROM provider_models
WHERE canonical_raw_name <> lower(split_part(raw_model_name, '/', -1));

-- ---------------------------------------------------------------------------
-- 3. Backfill standardized_name.
-- ---------------------------------------------------------------------------
\echo '--- 3. backfill standardized_name ---'
UPDATE provider_models
SET standardized_name = lower(split_part(raw_model_name, '/', -1)),
    updated_at = NOW()
WHERE standardized_name IS NULL
   OR standardized_name <> lower(split_part(raw_model_name, '/', -1));

\echo '--- 3. post-flight standardized_name coverage ---'
SELECT COUNT(*) AS mixed_standardized_name
FROM provider_models
WHERE standardized_name IS NOT NULL
  AND standardized_name <> lower(split_part(raw_model_name, '/', -1));

-- ---------------------------------------------------------------------------
-- 4. Widen the unique index so two publishers of the same model can
--    co-exist on one provider (e.g. nvidia/llama-3.3-70b-instruct +
--    meta/llama-3.3-70b-instruct both offered by NVIDIA NIM).
-- ---------------------------------------------------------------------------
\echo '--- 4. widen uq_provider_models_canonical_raw_name ---'
DROP INDEX IF EXISTS public.uq_provider_models_canonical_raw_name;
CREATE UNIQUE INDEX IF NOT EXISTS uq_provider_models_canonical_raw_name
    ON public.provider_models (provider_id, canonical_raw_name, raw_model_name);

\echo '--- 4. post-flight: any (provider_id, canonical_raw_name, raw_model_name) duplicates? ---'
SELECT provider_id, canonical_raw_name, raw_model_name, count(*)
FROM provider_models
GROUP BY provider_id, canonical_raw_name, raw_model_name
HAVING count(*) > 1
ORDER BY count(*) DESC, provider_id, canonical_raw_name
LIMIT 20;

\echo '=== 395c canonical_raw_name / standardized_name strip: done ==='
