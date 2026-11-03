-- Down for 937: drop promotion tables.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
DROP TABLE IF EXISTS public.promotion_redemptions;
DROP TABLE IF EXISTS public.promotion_campaigns;
DELETE FROM public.schema_migrations WHERE version = '937';
COMMIT;
