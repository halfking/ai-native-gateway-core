-- Migration 642 rollback: drop the INSTEAD OF INSERT rule, restore
-- the full unique indexes, and revert sentinel rows back to empty
-- strings. Note that running this down re-enables the original bug;
-- empty raw_model rows will once again collide on
-- (bucket, credential_id, raw_model) during the next concurrent
-- rollup. Apply only as a temporary measure while debugging; the
-- intended state is to keep the rule + partial index active.

BEGIN;

-- ── 1. Drop the INSTEAD OF INSERT rule ──────────────────────────────────────
DROP RULE IF EXISTS credential_model_index_hot_replace_rule ON public.credential_model_index_hot;

-- ── 2. Drop the partial unique index ────────────────────────────────────────
DROP INDEX IF EXISTS public.credential_model_index_hot_bucket_credential_id_raw_model_uniq;

-- ── 3. Restore the full unique index ────────────────────────────────────────
CREATE UNIQUE INDEX credential_model_index_hot_bucket_credential_id_raw_model_idx
    ON public.credential_model_index_hot (bucket, credential_id, raw_model);

-- ── 4. Restore the redundant unique_key index ──────────────────────────────
-- Pre-migration schema had two indexes covering the same tuple; this
-- keeps shape parity with the original baseline so old code paths
-- that reference the second name still resolve.
CREATE UNIQUE INDEX credential_model_index_hot_unique_key
    ON public.credential_model_index_hot (bucket, credential_id, raw_model);

-- ── 5. Revert sentinel rows ─────────────────────────────────────────────────
-- Note: re-introduces the original collision risk. Empty raw_model
-- rows may collide on the next rollup (this is the bug we are
-- downgrading to undo).
UPDATE public.credential_model_index_hot
SET raw_model = ''
WHERE raw_model = '__empty_raw_model__';

-- ── 6. Remove schema_migrations row ─────────────────────────────────────────
DELETE FROM public.schema_migrations WHERE version = '642';

COMMIT;
