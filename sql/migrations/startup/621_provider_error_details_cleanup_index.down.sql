-- Rollback Migration 621: provider error cleanup index.

BEGIN;

DROP INDEX IF EXISTS public.idx_ped_resolved_updated_at;

COMMIT;
