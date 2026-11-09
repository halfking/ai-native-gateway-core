-- Roll back 955: drop the passive fingerprint baseline table.
-- 无写方依赖前的安全回退：先停 bg 侧基线聚合 worker 再执行 down。
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
DROP TABLE IF EXISTS public.passive_model_fingerprints;
DELETE FROM public.schema_migrations WHERE version = '955';
COMMIT;
