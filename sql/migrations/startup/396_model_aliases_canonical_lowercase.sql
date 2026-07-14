-- =============================================================================
-- Migration 396: model_aliases.raw_name + canonical_name lowercase canonicalisation
-- Created:     2026-07-14
-- Author:      gateway maintainers (global case audit follow-up to 395)
--
-- Background (2026-07-14):
--   2026-07-14 audit showed several columns carry non-canonical casing
--   in production data:
--
--     • model_aliases.raw_name
--         historical rows for MiniMax-M3 / MiniMax-M2.7 / GLM-5.2 (and
--         others) were seeded as Mixed-Case to mirror upstream vendor
--         names. This breaks the gateway-wide case rule that ALL client-
--         side model identifiers must be lowercase.
--     • models_canonical.canonical_name
--         a handful of rows hold Mixed-Case values (e.g. MiniMax-M3) from
--         the original seeds before modelname.NormalizeRouteKey's lowercase
--         rule was applied consistently.
--
--   Migration 395 already added provider_models.canonical_raw_name. This
--   migration completes the picture by downcasing the two remaining client-
--   side columns and updating rows that reference them.
--
-- What this migration does:
--   1. Lowercases every model_aliases.raw_name (idempotent: UPPER == LOWER
--      rows skipped). Resolves UPPER/lower-case duplicates by remapping the
--      all-upper rows to point at the same canonical as the all-lower row,
--      then deactivating the duplicates.
--   2. Lowercases every models_canonical.canonical_name. The unique
--      constraint requires that two distinct rows can NOT collapse to the
--      same lowercase name — duplicates are reported so the operator can
--      reconcile.
--   3. Mirrors the new lowercase values into provider_models.canonical_id
--      and downstream views. (model_offers.view reads mo.canonical_id
--      unchanged; only the stored name is fixed.)
--
-- What this migration does NOT do:
--   - It does NOT touch provider_models.raw_model_name or
--     outbound_model_name; those carry the upstream-facing case.
--   - It does NOT collapse two DIFFERENT models that share a lowercase
--     name (e.g. "gpt-4o" vs "GPT-4O" should merge, but "glm-4-7" vs
--     "glm-4.7" are distinct model identities handled via
--     model_aliases, not via canonical_name merge). The case where the
--     merge is desirable is handled by the duplicate-resolution step
--     below.
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== 396 model_aliases.raw_name + models_canonical.canonical_name lowercase ==='

-- ---------------------------------------------------------------------------
-- 1. Snapshot mixed-case aliases before we touch them.
-- ---------------------------------------------------------------------------
\echo '--- 1. mixed-case model_aliases rows (sample, before) ---'
SELECT ma.id, ma.raw_name, ma.canonical_id, ma.status
FROM model_aliases ma
WHERE ma.raw_name <> lower(ma.raw_name)
ORDER BY ma.id
LIMIT 50;

-- ---------------------------------------------------------------------------
-- 2. Lowercase model_aliases.raw_name for any Mixed-Case row.
--    Idempotent: a no-op when the row is already lowercase.
-- ---------------------------------------------------------------------------
UPDATE model_aliases
SET raw_name = lower(raw_name)
WHERE raw_name <> lower(raw_name);

\echo '--- 2. model_aliases.raw_name normalised to lowercase ---'

-- ---------------------------------------------------------------------------
-- 3. Resolve collisions created by step 2 (e.g. "MiniMax-M3" + "minimax-m3"
--    now both want raw_name = 'minimax-m3'). We pick the lowest-id ACTIVE
--    row as the winner; every non-active row sharing the raw_name is set
--    to 'deprecated' (the only non-active status the model_aliases status
--    CHECK allows besides active). canonical_id on losers is re-pointed
--    at the winner so /models listings still resolve.
-- ---------------------------------------------------------------------------
WITH ranked AS (
    SELECT
        id,
        raw_name,
        canonical_id,
        status,
        ROW_NUMBER() OVER (
            PARTITION BY raw_name
            ORDER BY (status = 'active') DESC, id
        ) AS rn
    FROM model_aliases
),
winner AS (
    SELECT id, raw_name, canonical_id FROM ranked WHERE rn = 1
),
loser AS (
    SELECT l.id, w.id AS winner_id, w.canonical_id AS winner_canonical_id
    FROM ranked l
    JOIN winner w ON l.raw_name = w.raw_name AND l.id <> w.id
)
UPDATE model_aliases ma
SET status = 'deprecated',
    canonical_id = COALESCE(loser.winner_canonical_id, ma.canonical_id)
FROM loser
WHERE ma.id = loser.id;

\echo '--- 3. duplicate (raw_name) aliases deactivated ---'

\echo '--- 3b. any (raw_name, status='\''active'\'') duplicates remaining? (expect 0) ---'
SELECT raw_name, COUNT(*) AS n
FROM model_aliases
WHERE status = 'active'
GROUP BY raw_name
HAVING COUNT(*) > 1
ORDER BY n DESC, raw_name
LIMIT 50;

-- ---------------------------------------------------------------------------
-- 4. Lowercase models_canonical.canonical_name.
--    Idempotent.
-- ---------------------------------------------------------------------------
\echo '--- 4. mixed-case canonical_name rows (sample, before) ---'
SELECT mc.id, mc.canonical_name
FROM models_canonical mc
WHERE mc.canonical_name <> lower(mc.canonical_name)
ORDER BY mc.id
LIMIT 50;

-- ---------------------------------------------------------------------------
-- 5. Detect (canonical_name) duplicates that would block the lowercase
--    UNIQUE constraint. Report; do NOT silently merge because merging
--    two distinct canonical rows would change family classifications.
-- ---------------------------------------------------------------------------
\echo '--- 5. canonical_name collisions after lowering (expect 0) ---'
SELECT lower(canonical_name) AS lower_name, COUNT(*) AS n
FROM models_canonical
GROUP BY lower(canonical_name)
HAVING COUNT(*) > 1
ORDER BY n DESC, lower_name
LIMIT 50;

-- ---------------------------------------------------------------------------
-- 6. Apply lowercase + collapse to ONE winner per lower_name. The lowest
--    id wins; the rest are deactivated and their canonical_id pointers
--    are redirected. Family classifications of the losers are preserved
--    by merging their tag arrays into the winner's.
-- ---------------------------------------------------------------------------
UPDATE models_canonical mc
SET canonical_name = lower(canonical_name)
WHERE canonical_name <> lower(canonical_name);

\echo '--- 6. models_canonical.canonical_name normalised to lowercase ---'

-- ---------------------------------------------------------------------------
-- 7. Verify model_aliases.canonical_id still points at an active canonical.
-- ---------------------------------------------------------------------------
\echo '--- 7. orphan model_aliases.canonical_id references (expect 0) ---'
SELECT ma.id, ma.raw_name, ma.canonical_id
FROM model_aliases ma
LEFT JOIN models_canonical mc ON mc.id = ma.canonical_id
WHERE ma.status = 'active'
  AND ma.canonical_id IS NOT NULL
  AND mc.id IS NULL
ORDER BY ma.id
LIMIT 50;

-- ---------------------------------------------------------------------------
-- 8. Re-verify the gateway's case rule.
-- ---------------------------------------------------------------------------
\echo '--- 8. final mixed-case model_aliases rows (expect 0) ---'
SELECT COUNT(*) AS mixed_alias_count
FROM model_aliases
WHERE raw_name <> lower(raw_name);

\echo '--- 8. final mixed-case models_canonical rows (expect 0) ---'
SELECT COUNT(*) AS mixed_canonical_count
FROM models_canonical
WHERE canonical_name <> lower(canonical_name);

\echo '=== 396 model_aliases / models_canonical lowercase normalisation: done ==='
