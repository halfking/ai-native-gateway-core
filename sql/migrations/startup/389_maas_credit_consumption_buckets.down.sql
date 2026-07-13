BEGIN;

DROP INDEX IF EXISTS public.idx_mccb_recent;
DROP TABLE IF EXISTS public.maas_credit_consumption_buckets;

COMMIT;
