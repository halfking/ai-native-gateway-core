-- Migration 642: credential_model_index_hot — concurrent-rollup safe
-- via BEFORE INSERT trigger with pg_advisory_xact_lock.
--
-- Bug fix (2026-09-03): bg/auto_index_refresher.go's rollup runs a
-- DELETE-then-INSERT pair (commit 5dad2526f, 2026-07-20) instead of
-- INSERT ... ON CONFLICT, because the original UNION ALL (half-1
-- traffic + half-2 cold-start) could trip "ON CONFLICT DO UPDATE
-- command cannot affect row a second time" (SQLSTATE 21000) when
-- both halves produced the same (bucket, credential_id, raw_model)
-- triple.
--
-- DELETE-then-INSERT is racy under the auto-route listener's
-- NOTIFY-driven refresh model: a single credential binding update
-- fires the rollup, and many updates per minute are common in this
-- path. Two rollups can interleave such that the second's INSERT
-- arrives before the first's DELETE has committed, raising
-- SQLSTATE 23505:
--
--   "duplicate key value violates unique constraint
--    credential_model_index_hot_bucket_credential_id_raw_model_idx"
--
-- Collisions fall in two flavors:
--
--   1) raw_model = '' (empty string). Cause: half-1's
--      COALESCE(rl.outbound_model, rl.client_model) yields '' when
--      request_logs_hot.outbound_model is the empty string
--      (~69% of rows in a 5-minute window); half-2's pm.raw_model_name
--      passes `IS NOT NULL` even when empty. DISTINCT ON
--      (bucket, credential_id, '') still groups these into one row,
--      but a second cycle over the same bucket leaves a previously
--      committed (bucket, credential_id, '') that the next INSERT
--      collides on.
--
--   2) raw_model = some real name like 'deepseek-v4-pro'. Cause:
--      two concurrent rollups targeting the same
--      (bucket, credential_id, raw_model) when the first's
--      DELETE has not committed yet. This is a pure race condition
--      between background goroutines.
--
-- The fix is a BEFORE INSERT trigger that wraps DELETE-then-INSERT
-- into a single statement and serializes concurrent writers per
-- (bucket, credential_id, raw_model) tuple via
-- pg_advisory_xact_lock.
--
-- How the trigger works:
--
--   * BEFORE INSERT acquires a transaction-scoped advisory lock keyed
--     on a hash of (bucket, credential_id, raw_model). Two
--     concurrent INSERTs targeting the same tuple serialize on this
--     lock; non-conflicting INSERTs in the same transaction or
--     different tuples are not blocked.
--
--   * Inside the lock the trigger DELETE-s the existing row
--     matching the tuple, so the original INSERT lands cleanly.
--     This makes the rollup's DELETE-then-INSERT semantics
--     idempotent and race-free.
--
--   * Sentinel value handling: this migration also rewrites every
--     pre-existing '' row to '__empty_raw_model__'. Combined with
--     the rollup's DISTINCT ON (bucket, credential_id, raw_model),
--     this keeps (bucket, credential_id, '') from racing across
--     cycles. New '' rows coming in through this trigger are
--     rewritten by the trigger function to the same sentinel so
--     the invariant holds for future rollups too.
--
-- Why a trigger instead of a partial index / rule:
--
--   * An INSTEAD OF INSERT RULE would self-trigger when its
--     rewritten INSERT statement re-evaluates the rule, causing
--     "infinite recursion detected in rules for relation
--     credential_model_index_hot" (verified locally; the recursion
--     error stops the rollup entirely).
--
--   * A partial UNIQUE index on (bucket, credential_id, raw_model)
--     WHERE raw_model <> '__empty_raw_model__' / '<>' '' does
--     silence empty-string collisions but leaves real-name
--     collisions exposed. The trigger + advisory lock covers both
--     flavors.
--
-- Down migration drops the trigger + helper function and reverts
-- sentinel rows back to '' (which re-enables the original bug).

BEGIN;

-- ── 1. Data migration ────────────────────────────────────────────────────────
-- Two pre-existing row shapes have to be reconciled before the
-- trigger can run cleanly:
--
--   * (bucket, credential_id, '') — raw empty string from old rollups
--     that ran before this migration. Drop the '' row when a
--     sentinel row already covers the same (bucket, credential_id);
--     the sentinel row will continue to carry the rollup signal.
--
--   * (bucket, credential_id, '__empty_raw_model__') — sentinel rows
--     from prior partial-fix attempts. Keep one, dedup extras.
--
--   * (bucket, credential_id, <real raw_model>) with duplicates from
--     concurrent rollup races — drop the older one (the rollup
--     trigger below will keep the latest on subsequent writes).
WITH ranked_real AS (
    SELECT ctid,
           ROW_NUMBER() OVER (PARTITION BY bucket, credential_id, raw_model
                             ORDER BY updated_at DESC) AS rn
    FROM public.credential_model_index_hot
    WHERE raw_model NOT IN ('', '__empty_raw_model__')
), dup_real AS (
    DELETE FROM public.credential_model_index_hot
    WHERE ctid IN (SELECT ctid FROM ranked_real WHERE rn > 1)
    RETURNING 1
), ranked_sentinel AS (
    SELECT ctid,
           ROW_NUMBER() OVER (PARTITION BY bucket, credential_id
                             ORDER BY updated_at DESC) AS rn
    FROM public.credential_model_index_hot
    WHERE raw_model = '__empty_raw_model__'
), dup_sentinel AS (
    DELETE FROM public.credential_model_index_hot
    WHERE ctid IN (SELECT ctid FROM ranked_sentinel WHERE rn > 1)
    RETURNING 1
), empty_with_sibling AS (
    DELETE FROM public.credential_model_index_hot
    WHERE raw_model = ''
      AND (bucket, credential_id) IN (
          SELECT bucket, credential_id
          FROM public.credential_model_index_hot
          WHERE raw_model = '__empty_raw_model__'
      )
    RETURNING 1
), remaining_empty AS (
    UPDATE public.credential_model_index_hot
    SET raw_model = '__empty_raw_model__'
    WHERE raw_model = ''
    RETURNING 1
), null_rows AS (
    DELETE FROM public.credential_model_index_hot
    WHERE raw_model IS NULL
    RETURNING 1
)
SELECT
    (SELECT count(*) FROM dup_real)        AS real_duplicates_dropped,
    (SELECT count(*) FROM dup_sentinel)    AS sentinel_duplicates_dropped,
    (SELECT count(*) FROM empty_with_sibling) AS empty_rows_with_sentinel_dropped,
    (SELECT count(*) FROM remaining_empty) AS empty_rows_sentinelized,
    (SELECT count(*) FROM null_rows)       AS null_rows_dropped;

-- ── 2. BEFORE INSERT trigger ────────────────────────────────────────────────
-- Acquire an advisory lock keyed on (bucket, credential_id, raw_model)
-- to serialize concurrent writers, then DELETE the existing row
-- matching that tuple before the original INSERT runs. The DELETE is
-- idempotent — if no row matches, it is a no-op; the INSERT then
-- lands cleanly.
--
-- The lock is transaction-scoped, so it is released automatically at
-- COMMIT/ROLLBACK without explicit unlock. Concurrent INSERTs
-- targeting different tuples do not block each other.
--
-- Empty / NULL raw_model is rewritten to the sentinel value so the
-- (bucket, credential_id, '') shape that the rollup's COALESCE
-- naturally produces never reaches the table. The partial unique
-- invariant on real names is still enforced by the
-- credential_model_index_hot_bucket_credential_id_raw_model_idx
-- index, which this migration leaves intact.
CREATE OR REPLACE FUNCTION public.replace_credential_model_index_row()
RETURNS TRIGGER AS $$
DECLARE
    lock_key bigint;
BEGIN
    -- Normalize empty / NULL raw_model to the sentinel. Real
    -- raw_model values are slugified like 'gpt-4o', 'claude-opus-4-7',
    -- etc., and never start with '__', so this rewrite is safe.
    IF NEW.raw_model IS NULL OR NEW.raw_model = '' THEN
        NEW.raw_model := '__empty_raw_model__';
    END IF;

    -- Serialize concurrent rollups targeting the same
    -- (bucket, credential_id, raw_model) tuple. The lock is
    -- released automatically at COMMIT/ROLLBACK.
    lock_key := hashtext(
        NEW.bucket::text || ':' ||
        NEW.credential_id::text || ':' ||
        NEW.raw_model
    )::bigint;
    PERFORM pg_advisory_xact_lock(lock_key);

    DELETE FROM public.credential_model_index_hot
    WHERE bucket = NEW.bucket
      AND credential_id = NEW.credential_id
      AND raw_model = NEW.raw_model;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Drop and recreate so the trigger definition is authoritative even
-- if an earlier partial run left a stale trigger behind. IF EXISTS
-- keeps this idempotent for fresh databases.
DROP TRIGGER IF EXISTS trg_credential_model_index_hot_replace
    ON public.credential_model_index_hot;

CREATE TRIGGER trg_credential_model_index_hot_replace
    BEFORE INSERT ON public.credential_model_index_hot
    FOR EACH ROW
    EXECUTE FUNCTION public.replace_credential_model_index_row();

-- ── 3. Schema migration record ───────────────────────────────────────────────
INSERT INTO public.schema_migrations (version, description)
VALUES ('642',
        'credential_model_index_hot: BEFORE INSERT trigger with pg_advisory_xact_lock serializes concurrent rollups so DELETE-then-INSERT never trips SQLSTATE 23505 duplicate key')
ON CONFLICT (version) DO NOTHING;

COMMIT;
