BEGIN;

DROP TRIGGER IF EXISTS trg_notify_credentials_governor_revision_insert ON public.credentials;
DROP TRIGGER IF EXISTS trg_notify_credentials_governor_revision_update ON public.credentials;
DROP TRIGGER IF EXISTS trg_bump_credentials_governor_revision ON public.credentials;

-- Restore the prior auto-route trigger definition that 566 widened. The
-- migration only ever widens the UPDATE OF column list, so the restore uses
-- the original 11-column list (migration 307 baseline).
DROP TRIGGER IF EXISTS trg_notify_auto_route_creds ON public.credentials;
CREATE TRIGGER trg_notify_auto_route_creds AFTER UPDATE OF status, availability_state, quota_state, circuit_state, concurrency_limit, lifecycle_status, manual_disabled ON public.credentials FOR EACH ROW WHEN ((old.* IS DISTINCT FROM new.*)) EXECUTE FUNCTION public.notify_auto_route_refresh();

DROP FUNCTION IF EXISTS public.notify_credentials_governor_revision();
DROP FUNCTION IF EXISTS public.bump_credentials_governor_revision();
DROP INDEX IF EXISTS public.credentials_revision_idx;
DROP SEQUENCE IF EXISTS public.credentials_governor_revision_seq;
ALTER TABLE public.credentials DROP COLUMN IF EXISTS revision;

COMMIT;
