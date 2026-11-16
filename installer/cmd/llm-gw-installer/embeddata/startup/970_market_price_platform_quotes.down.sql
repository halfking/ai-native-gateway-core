-- 970_market_price_platform_quotes.down.sql —— 只 DROP 本表 + 撤 schema_migrations 行。
-- 不触碰 940 三表 / 958 registry / 959 列 / 961 台账。

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DROP TABLE IF EXISTS public.market_price_platform_quotes;
DELETE FROM public.schema_migrations WHERE version = '970';

COMMIT;
