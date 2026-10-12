-- Rollback Migration 852.
-- Only remove the objects 852 can have introduced on fresh-install lineages
-- where 469/478 never ran. On deploy-lineage databases 469/478 introduced
-- them long ago (context_window_override provenance tracked, reasoning_caps
-- possibly operator-curated) — run only after all writers have been rolled
-- back and the columns verified unused.

BEGIN;

DROP INDEX IF EXISTS idx_models_canonical_reasoning_caps_dialect;
DROP INDEX IF EXISTS idx_models_canonical_reasoning_caps_supported;

ALTER TABLE IF EXISTS public.models_canonical
    DROP COLUMN IF EXISTS reasoning_caps,
    DROP COLUMN IF EXISTS context_window_override,
    DROP COLUMN IF EXISTS context_window_source,
    DROP COLUMN IF EXISTS context_window_updated_at;

COMMIT;
