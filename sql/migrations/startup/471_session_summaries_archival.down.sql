-- Migration 471 down: remove session_summaries archival columns

BEGIN;

DROP INDEX IF EXISTS public.idx_session_summaries_archival;
ALTER TABLE public.session_summaries DROP COLUMN IF EXISTS last_accessed_at;
ALTER TABLE public.session_summaries DROP COLUMN IF EXISTS archived_at;

COMMIT;
