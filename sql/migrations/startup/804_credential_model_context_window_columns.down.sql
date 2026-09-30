-- Rollback Migration 804.
-- Only remove the columns 804 can have introduced (context_window_source /
-- context_window_updated_at). context_window_override predates 804 on every
-- lineage (baseline / 523) and is never dropped. Run only after all writers
-- have been rolled back and the columns verified empty on deploy-lineage
-- databases where 523 introduced them long ago.

BEGIN;

ALTER TABLE IF EXISTS public.credential_model_bindings
    DROP COLUMN IF EXISTS context_window_source,
    DROP COLUMN IF EXISTS context_window_updated_at;

COMMIT;
