-- Migration 642 rollback: drop the BEFORE INSERT trigger and helper
-- function, and revert sentinel rows back to empty strings.
-- Note that running this down re-enables the original bug; the empty
-- raw_model rows will once again collide on
-- (bucket, credential_id, raw_model) during the next concurrent
-- rollup. Apply only as a temporary measure while debugging; the
-- intended state is to keep the trigger active.

BEGIN;

-- ── 1. Drop the BEFORE INSERT trigger ───────────────────────────────────────
DROP TRIGGER IF EXISTS trg_credential_model_index_hot_replace
    ON public.credential_model_index_hot;

-- ── 2. Drop the helper function ─────────────────────────────────────────────
DROP FUNCTION IF EXISTS public.replace_credential_model_index_row();

-- ── 3. Revert sentinel rows ─────────────────────────────────────────────────
-- Note: re-introduces the original collision risk. Empty raw_model
-- rows may collide on the next rollup (this is the bug we are
-- downgrading to undo).
UPDATE public.credential_model_index_hot
SET raw_model = ''
WHERE raw_model = '__empty_raw_model__';

-- ── 4. Remove schema_migrations row ─────────────────────────────────────────
DELETE FROM public.schema_migrations WHERE version = '642';

COMMIT;
