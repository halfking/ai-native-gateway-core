-- Rollback migration 691: proxy region avoidance + auto-switch selection policy

BEGIN;

DROP TABLE IF EXISTS public.proxy_selection_policy;

ALTER TABLE public.proxy_nodes DROP COLUMN IF EXISTS banned_regions;
ALTER TABLE public.proxy_subscriptions DROP COLUMN IF EXISTS banned_regions;

DELETE FROM public.schema_migrations WHERE version = '691';

COMMIT;
