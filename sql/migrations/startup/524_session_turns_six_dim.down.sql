-- Migration 524 down: remove scoped dimensions from session_turns.
-- DATA LOSS ORDER: stop writers/readers, back up all four columns, drop the
-- dependent indexes, then drop the columns. Column drops are irreversible.

BEGIN;

DROP INDEX IF EXISTS public.idx_session_turns_tenant_task_type;
DROP INDEX IF EXISTS public.idx_session_turns_tenant_parent_request;
DROP INDEX IF EXISTS public.idx_session_turns_tenant_namespace;
DROP INDEX IF EXISTS public.idx_session_turns_tenant_project;

ALTER TABLE public.session_turns
    DROP COLUMN IF EXISTS task_type,
    DROP COLUMN IF EXISTS parent_request_id,
    DROP COLUMN IF EXISTS namespace,
    DROP COLUMN IF EXISTS project_id;

COMMIT;
