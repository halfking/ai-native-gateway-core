-- Down 845: drop routing_scope (destructive; only for local rollback labs).

BEGIN;

DROP INDEX IF EXISTS public.idx_credentials_routing_scope;

ALTER TABLE public.credentials
    DROP CONSTRAINT IF EXISTS credentials_routing_scope_check;

ALTER TABLE public.credentials
    DROP COLUMN IF EXISTS routing_scope;

DELETE FROM public.schema_migrations WHERE version = '847';

COMMIT;
