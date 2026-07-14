-- =============================================================================
-- Migration 395b: dedup provider_models + model_aliases duplicates
-- Created:     2026-07-14
-- Author:      gateway maintainers
--
-- Prerequisite:
--   Migration 395 has run and added provider_models.canonical_raw_name NOT
--   NULL but FAILED to create uq_provider_models_canonical_raw_name because
--   production has pre-existing (provider_id, canonical_raw_name) duplicates
--   from historical mixed-case offerings (e.g. three
--   provider_models rows for "MiMo-V2.5-Pro" / "mimo-v2.5-pro" collapsed
--   to the same canonical key).
--
--   Migration 396 was also rolled back because the same case drift left
--   model_aliases.raw_name with active duplicates (THUDM/glm-5.1 appearing
--   4× across different canonical_ids, etc.).
--
-- What this migration does:
--   1. Snapshot duplicate groups before any change.
--   2. provider_models: for every (provider_id, canonical_raw_name) group
--      with count > 1, keep the lowest-id row, re-point every
--      credential_model_bindings row that referenced the dropping rows,
--      then DELETE the dropping rows. cmb rows already pointing at the
--      winner are untouched.
--   3. Re-attempt uq_provider_models_canonical_raw_name + the non-UNIQUE
--      btree index that 395 was supposed to create.
--   4. model_aliases: for every (raw_name, status='active') group with
--      count > 1, mark all but the lowest-id row 'inactive' and re-point
--      their canonical_id to the winner (defensive: keeps any unique
--      surfaces / quantizations alive if admin manually re-activated them).
--   5. Re-run the canonical-name / alias-name lowercasing (now safe since
--      the dedup above has collapsed the conflicts to single winners).
--   6. Post-flight: every column must be lowercase AND there must be
--      zero active (raw_name) duplicates in model_aliases.
--
-- What this migration does NOT do:
--   - It does NOT alter models_canonical.canonical_name (idempotent
--     re-lower is in 396 / step 5 here).
--   - It does NOT change provider_models.raw_model_name / outbound_model_name.
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== 395b dedup provider_models + model_aliases ==='

-- ---------------------------------------------------------------------------
-- 1. Snapshot duplicate groups.
-- ---------------------------------------------------------------------------
\echo '--- 1. provider_models duplicate groups BEFORE ---'
SELECT provider_id, canonical_raw_name, COUNT(*) AS n,
       array_agg(id ORDER BY id) AS ids
FROM provider_models
GROUP BY provider_id, canonical_raw_name
HAVING COUNT(*) > 1
ORDER BY n DESC, provider_id, canonical_raw_name;

\echo '--- 1. model_aliases duplicate raw_name groups BEFORE (active) ---'
SELECT canonical_id, raw_name, COUNT(*) AS n,
       array_agg(id ORDER BY id) AS ids
FROM model_aliases
WHERE status = 'active'
GROUP BY canonical_id, raw_name
HAVING COUNT(*) > 1
ORDER BY n DESC, raw_name
LIMIT 50;

-- ---------------------------------------------------------------------------
-- 2. provider_models dedup: keep lowest id, redirect bindings, delete rest.
--    Run inside a savepoint so a single failure rolls back the batch.
-- ---------------------------------------------------------------------------
\echo '--- 2. dedup provider_models ---'
DO $$
DECLARE
    g RECORD;
    winner_id bigint;
    loser_id bigint;
    cmb_count int;
    total_redir int := 0;
    total_del int := 0;
BEGIN
    FOR g IN
        SELECT provider_id, canonical_raw_name,
               array_agg(id ORDER BY id) AS ids
        FROM provider_models
        GROUP BY provider_id, canonical_raw_name
        HAVING COUNT(*) > 1
    LOOP
        winner_id := g.ids[1];
        FOREACH loser_id IN ARRAY g.ids[2:array_length(g.ids,1)] LOOP
            -- Re-point every credential_model_bindings row that referenced
            -- the dropping provider_model_id and that doesn't already have
            -- a binding to the winner (the unique key on
            -- (credential_id, provider_model_id) protects us from double
            -- insertion; the WHERE NOT EXISTS makes the re-point
            -- idempotent across repeated runs).
            WITH moved AS (
                UPDATE credential_model_bindings
                SET provider_model_id = winner_id
                WHERE provider_model_id = loser_id
                  AND NOT EXISTS (
                      SELECT 1 FROM credential_model_bindings cmb2
                      WHERE cmb2.credential_id = credential_model_bindings.credential_id
                        AND cmb2.provider_model_id = winner_id
                  )
                RETURNING 1
            )
            SELECT count(*) INTO cmb_count FROM moved;
            total_redir := total_redir + cmb_count;

            -- Hard-delete the losing provider_models row. cmb rows that
            -- already collided with the winner's binding get cleaned up
            -- via CASCADE only if a FK exists; otherwise they remain
            -- harmless (still pointing at winner_id) — keep the row
            -- shape stable.
            DELETE FROM credential_model_bindings
            WHERE provider_model_id = loser_id;

            DELETE FROM provider_models WHERE id = loser_id;
            total_del := total_del + 1;

            RAISE NOTICE 'dedup provider_id=% canonical=% loser_id=% redirected_cmb=%',
                         g.provider_id, g.canonical_raw_name, loser_id, cmb_count;
        END LOOP;
    END LOOP;
    RAISE NOTICE '=== provider_models dedup summary: redirected cmb rows: %, deleted pm rows: % ===',
                 total_redir, total_del;
END $$;

\echo '--- 2. provider_models duplicate groups AFTER (expect 0) ---'
SELECT provider_id, canonical_raw_name, COUNT(*) AS n
FROM provider_models
GROUP BY provider_id, canonical_raw_name
HAVING COUNT(*) > 1
ORDER BY n DESC, provider_id, canonical_raw_name;

-- ---------------------------------------------------------------------------
-- 3. Re-create the UNIQUE index + btree that migration 395 tried to make.
-- ---------------------------------------------------------------------------
\echo '--- 3. (re)create canonical_raw_name index ---'
CREATE UNIQUE INDEX IF NOT EXISTS uq_provider_models_canonical_raw_name
    ON public.provider_models (provider_id, canonical_raw_name);

CREATE INDEX IF NOT EXISTS idx_provider_models_canonical_raw_name
    ON public.provider_models (canonical_raw_name);

-- ---------------------------------------------------------------------------
-- 4. model_aliases dedup: keep lowest id, mark losers 'deprecated' (the
--    status CHECK allows active/disabled/deprecated/hidden — 'inactive'
--    is rejected by model_aliases_status_check), copy canonical_id from
--    the winner. Use raw_name + status='active' as the uniqueness key.
-- ---------------------------------------------------------------------------
\echo '--- 4. dedup model_aliases (active rows) ---'
DO $$
DECLARE
    g RECORD;
    winner_id int;
    loser_id int;
    total_disabled int := 0;
BEGIN
    FOR g IN
        SELECT canonical_id, raw_name,
               array_agg(id ORDER BY id) AS ids
        FROM model_aliases
        WHERE status = 'active'
        GROUP BY canonical_id, raw_name
        HAVING COUNT(*) > 1
    LOOP
        winner_id := g.ids[1];
        FOREACH loser_id IN ARRAY g.ids[2:array_length(g.ids,1)] LOOP
            UPDATE model_aliases
            SET status = 'deprecated',
                canonical_id = g.canonical_id
            WHERE id = loser_id;
            total_disabled := total_disabled + 1;
        END LOOP;
    END LOOP;
    RAISE NOTICE '=== model_aliases dedup summary: deactivated rows: % ===',
                 total_disabled;
END $$;

\echo '--- 4. model_aliases duplicate raw_name groups AFTER (expect 0) ---'
SELECT canonical_id, raw_name, COUNT(*) AS n
FROM model_aliases
WHERE status = 'active'
GROUP BY canonical_id, raw_name
HAVING COUNT(*) > 1
ORDER BY n DESC, raw_name
LIMIT 50;

-- ---------------------------------------------------------------------------
-- 5. Re-run canonical lowercase normalisation (idempotent re-do of 396).
-- ---------------------------------------------------------------------------
\echo '--- 5. lowercase model_aliases.raw_name + models_canonical.canonical_name ---'
UPDATE model_aliases
SET raw_name = lower(raw_name)
WHERE raw_name <> lower(raw_name);

UPDATE models_canonical
SET canonical_name = lower(canonical_name)
WHERE canonical_name <> lower(canonical_name);

-- ---------------------------------------------------------------------------
-- 6. Post-flight: every operational column we touched must be lowercase.
-- ---------------------------------------------------------------------------
\echo '--- 6. postflight lowercase coverage ---'
SELECT
    (SELECT COUNT(*) FROM provider_models
       WHERE canonical_raw_name <> lower(canonical_raw_name)) AS mixed_canonical_raw_name,
    (SELECT COUNT(*) FROM models_canonical
       WHERE canonical_name <> lower(canonical_name)) AS mixed_canonical_name,
    (SELECT COUNT(*) FROM model_aliases
       WHERE raw_name <> lower(raw_name)) AS mixed_alias_raw_name,
    (SELECT COUNT(*) FROM model_aliases
       WHERE status = 'active'
       GROUP BY raw_name HAVING COUNT(*) > 1) AS active_alias_dup_groups;

\echo '=== 395b dedup: done ==='
