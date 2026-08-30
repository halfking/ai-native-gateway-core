-- Migration 627 DOWN: undo the credential 'deleted' status + provider
-- deleted_at column. Only safe to run BEFORE the new DELETE endpoints have
-- been used; once any rows are in the new soft-deleted state the rollback
-- will block on the CHECK constraint.

BEGIN;

DROP INDEX IF EXISTS public.idx_providers_live;
ALTER TABLE public.providers DROP COLUMN IF EXISTS deleted_at;

-- Roll the CHECK back to its pre-627 list. Fail loudly if any row still
-- carries the new 'deleted' value so operators notice instead of silently
-- losing the protection.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM public.credentials WHERE status = 'deleted') THEN
        RAISE EXCEPTION 'migration 627 down blocked: credentials with status=''deleted'' still exist; restore them first';
    END IF;
END
$$;

ALTER TABLE public.credentials
    DROP CONSTRAINT IF EXISTS credentials_status_check;

ALTER TABLE public.credentials
    ADD CONSTRAINT credentials_status_check
    CHECK (status = ANY (ARRAY[
        'active'::text,
        'cooling'::text,
        'degraded'::text,
        'quarantine'::text,
        'quota_expired'::text,
        'disabled'::text
    ]));

COMMIT;
