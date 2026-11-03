-- Down for 936: drop subscription_plan_entitlements.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
DROP TABLE IF EXISTS public.subscription_plan_entitlements;
DELETE FROM public.schema_migrations WHERE version = '936';
COMMIT;
