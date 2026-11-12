-- 961 down：只退自己那一步（drop 本迁移建的台账表；不触碰 940 三表与 959 列）。
BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

DROP TABLE IF EXISTS public.market_scoring_runs;

COMMIT;
