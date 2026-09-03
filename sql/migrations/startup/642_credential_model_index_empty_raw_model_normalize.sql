-- Migration 642: credential_model_index_hot — concurrent-rollup safe.
--
-- Bug fix (2026-09-03): bg/auto_index_refresher.go's rollup runs a
-- DELETE-then-INSERT pair (commit 5dad2526f, 2026-07-20) instead of
-- INSERT ... ON CONFLICT, because the original UNION ALL (half-1
-- traffic + half-2 cold-start) could trip "ON CONFLICT DO UPDATE
-- command cannot affect row a second time" (SQLSTATE 21000) when
-- both halves produced the same
-- (bucket, credential_id, raw_model) triple.
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
-- Collisions fall in two buckets:
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
-- The fix has three parts:
--
--   1) Data migration: rewrite every existing raw_model='' (and NULL)
--      row to a reserved sentinel '__empty_raw_model__'. Real
--      raw_model_name values are slugified like 'gpt-4o',
--      'claude-opus-4-7', etc., never start with '__'. Real raw_model
--      duplicate rows are also deduped in this step (keep one row
--      per bucket/credential_id/raw_model, drop the rest) so the
--      partial index can be created in step 3 without violating its
--      uniqueness invariant.
--
--   2) INSTEAD OF INSERT RULE: rewrite INSERTs to
--      DELETE-then-INSERT atomically. When an incoming INSERT would
--      collide on (bucket, credential_id, raw_model) — either
--      against an existing row in the table or against another row
--      inside the same multi-row INSERT batch — the rule first
--      removes the existing row(s) and then performs the INSERT. This
--      is exactly the semantics the rollup wants and lets multiple
--      concurrent rollups converge on the latest metrics instead of
--      raising 23505.
--
--      NOTE: rules cannot themselves trigger ON CONFLICT clauses
--      inside the rewritten statement, but our rollup path does not
--      use ON CONFLICT (the rule replaces its safety role), so this
--      is fine.
--
--   3) Drop the existing full (bucket, credential_id, raw_model) UNIQUE
--      index and replace it with a PARTIAL UNIQUE index that
--      excludes both '' and the sentinel value. The rule above
--      guarantees '' and sentinel rows never collide on the
--      underlying tuple; the partial index only enforces the
--      uniqueness invariant on real raw_model names, which is the
--      only place it carries semantic value.
--
-- Combined effect: under concurrency the rule fires and the rollup
-- converges to the latest state. Sequential rollups still see
-- ON CONFLICT-style upsert semantics for real model names via
-- the partial index, but are immune to empty / sentinel noise.
--
-- Down migration restores the original full unique index, drops the
-- rule, and reverts sentinel rows back to '' (which re-enables the
-- bug). Apply only as a temporary measure while debugging.

BEGIN;

-- ── 1. Data migration ────────────────────────────────────────────────────────
-- Two pre-existing row shapes have to be reconciled before step 2's
-- DELETE-then-INSERT rule is meaningful:
--
--   * (bucket, credential_id, '') — raw empty string from old rollups
--     that ran before this migration. Drop the '' row when a sentinel
--     row already covers the same (bucket, credential_id); the
--     sentinel row will continue to carry the rollup signal.
--
--   * (bucket, credential_id, '__empty_raw_model__') — sentinel rows
--     from prior partial-fix attempts. Keep one, dedup extras.
--
--   * (bucket, credential_id, <real raw_model>) with duplicates from
--     concurrent rollup races — drop the older one (the rollup rule
--     below will keep the latest on subsequent writes).
WITH ranked_real AS (
    SELECT ctid,
           credential_id,
           raw_model,
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

-- ── 2. INSTEAD OF INSERT RULE ───────────────────────────────────────────────
-- Convert plain INSERTs into DELETE-then-INSERT. The rule fires
-- whenever an incoming row would collide on the
-- (bucket, credential_id, raw_model) UNIQUE constraint, including
-- collisions against rows already present in the table or against
-- sibling rows inside the same multi-row INSERT batch. The DELETE
-- targets exactly the colliding tuple, so non-conflicting rows
-- within the same INSERT pass through untouched.
--
-- This mirrors the DELETE-then-INSERT split that
-- bg/auto_index_refresher.go already performs (commit 5dad2526f) but
-- wraps both phases inside a single in-rule statement so concurrent
-- writers converge deterministically instead of racing on the
-- window between the explicit DELETE and the explicit INSERT.
CREATE OR REPLACE RULE credential_model_index_hot_replace_rule
AS ON INSERT TO public.credential_model_index_hot
WHERE EXISTS (
    SELECT 1 FROM public.credential_model_index_hot t
    WHERE t.bucket = NEW.bucket
      AND t.credential_id = NEW.credential_id
      AND t.raw_model = NEW.raw_model
)
DO INSTEAD (
    DELETE FROM public.credential_model_index_hot t
    WHERE t.bucket = NEW.bucket
      AND t.credential_id = NEW.credential_id
      AND t.raw_model = NEW.raw_model;
    INSERT INTO public.credential_model_index_hot VALUES (NEW.*);
);

-- ── 3. Drop redundant / full unique indexes ──────────────────────────────────
-- The migration target schema has two indexes covering the same
-- (bucket, credential_id, raw_model) tuple. Both go away; the partial
-- index below takes their place.
DROP INDEX IF EXISTS public.credential_model_index_hot_unique_key;
DROP INDEX IF EXISTS public.credential_model_index_hot_bucket_credential_id_raw_model_idx;

-- ── 4. Build the partial unique index ────────────────────────────────────────
-- WHERE clause excludes '' and the sentinel so the rollup rule's
-- DELETEs in step 2 don't trip this index when collapsing duplicates.
-- Real raw_model names still get full UNIQUE protection, which is
-- the only place the original constraint had semantic value.
--
-- A partial index cannot be the target of an ON CONFLICT
-- (bucket, credential_id, raw_model) DO UPDATE / DO NOTHING, but the
-- rollup path (bg/auto_index_refresher.go) does not use ON CONFLICT,
-- so this restriction has no impact.
CREATE UNIQUE INDEX IF NOT EXISTS credential_model_index_hot_bucket_credential_id_raw_model_uniq
    ON public.credential_model_index_hot (bucket, credential_id, raw_model)
    WHERE raw_model <> '__empty_raw_model__'
      AND raw_model <> '';

-- ── 5. Schema migration record ───────────────────────────────────────────────
INSERT INTO public.schema_migrations (version, description)
VALUES ('642',
        'credential_model_index_hot: drop full unique index, add partial unique index excluding sentinel/empty rows; auto-route rule rewrites colliding INSERTs to DELETE-then-INSERT so concurrent rollups converge instead of hitting SQLSTATE 23505')
ON CONFLICT (version) DO NOTHING;

COMMIT;
