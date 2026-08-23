-- Migration 566: globally ordered credential-governor policy revisions.
--
-- credentials_revision carries only the revision number, so revisions must be
-- monotonic across every credential rather than per credential.

BEGIN;

ALTER TABLE public.credentials
    ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 0;

CREATE SEQUENCE IF NOT EXISTS public.credentials_governor_revision_seq
    AS BIGINT
    START WITH 1
    MINVALUE 1;

SELECT setval(
    'public.credentials_governor_revision_seq',
    GREATEST(COALESCE((SELECT MAX(revision) FROM public.credentials), 0), 1),
    COALESCE((SELECT MAX(revision) FROM public.credentials), 0) > 0
);

CREATE INDEX IF NOT EXISTS credentials_revision_idx
    ON public.credentials (revision);

COMMENT ON COLUMN public.credentials.revision IS
    'Globally monotonic governor policy revision. Bumped by governor-relevant credential writes and consumed by domains/dispatch/policy_publisher.go via LISTEN credentials_revision.';

CREATE OR REPLACE FUNCTION public.bump_credentials_governor_revision()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.revision := nextval('public.credentials_governor_revision_seq');
    ELSIF OLD.concurrency_limit IS DISTINCT FROM NEW.concurrency_limit
       OR OLD.concurrency_mode IS DISTINCT FROM NEW.concurrency_mode
       OR OLD.rpm_limit IS DISTINCT FROM NEW.rpm_limit
       OR OLD.tpm_limit IS DISTINCT FROM NEW.tpm_limit
       OR OLD.fp_slot_limit IS DISTINCT FROM NEW.fp_slot_limit
       OR OLD.max_queue_depth IS DISTINCT FROM NEW.max_queue_depth
       OR OLD.max_queue_wait_ms IS DISTINCT FROM NEW.max_queue_wait_ms THEN
        NEW.revision := nextval('public.credentials_governor_revision_seq');
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.notify_credentials_governor_revision()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_notify('credentials_revision', NEW.revision::text);
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_bump_credentials_governor_revision ON public.credentials;
CREATE TRIGGER trg_bump_credentials_governor_revision
BEFORE INSERT OR UPDATE OF concurrency_limit, concurrency_mode, rpm_limit,
    tpm_limit, fp_slot_limit, max_queue_depth, max_queue_wait_ms
ON public.credentials
FOR EACH ROW
EXECUTE FUNCTION public.bump_credentials_governor_revision();

DROP TRIGGER IF EXISTS trg_notify_credentials_governor_revision_insert ON public.credentials;
CREATE TRIGGER trg_notify_credentials_governor_revision_insert
AFTER INSERT ON public.credentials
FOR EACH ROW
EXECUTE FUNCTION public.notify_credentials_governor_revision();

DROP TRIGGER IF EXISTS trg_notify_credentials_governor_revision_update ON public.credentials;
CREATE TRIGGER trg_notify_credentials_governor_revision_update
AFTER UPDATE OF concurrency_limit, concurrency_mode, rpm_limit, tpm_limit,
    fp_slot_limit, max_queue_depth, max_queue_wait_ms
ON public.credentials
FOR EACH ROW
WHEN (OLD.revision IS DISTINCT FROM NEW.revision)
EXECUTE FUNCTION public.notify_credentials_governor_revision();

DROP TRIGGER IF EXISTS trg_notify_auto_route_creds ON public.credentials;
CREATE TRIGGER trg_notify_auto_route_creds
AFTER UPDATE OF status, availability_state, quota_state, circuit_state,
    concurrency_limit, concurrency_mode, rpm_limit, tpm_limit, fp_slot_limit,
    max_queue_depth, max_queue_wait_ms, lifecycle_status, manual_disabled
ON public.credentials
FOR EACH ROW
WHEN (OLD.* IS DISTINCT FROM NEW.*)
EXECUTE FUNCTION public.notify_auto_route_refresh();

COMMIT;
