-- 974_market_price_identity_and_tier.down.sql —— 撤销 974 的结构改动。
--
-- ★ 不删 940 的行：本迁移只**清理**了身份漂移的遗留行（那些行在旧口径下
--   本来就是错的），回滚不把它们塞回去（塞回去＝把错误身份写回真库）。
--   同步器下一次跑会按旧口径重新落地（若脚本也回滚），否则按新口径落地。
--
-- ★ 970 表的 tier / model_kind / cache_hit 列与 tier 感知 PK 一并撤销
--   （回到 970 的原始形状）。

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- PK 退回 (dimension, canonical_name, platform_id)：先删可能因 tier 产生的重复
DELETE FROM public.market_price_platform_quotes a
      USING public.market_price_platform_quotes b
 WHERE a.ctid < b.ctid
   AND a.dimension = b.dimension
   AND a.canonical_name = b.canonical_name
   AND a.platform_id = b.platform_id;

ALTER TABLE public.market_price_platform_quotes
    DROP CONSTRAINT IF EXISTS market_price_platform_quotes_pkey;
ALTER TABLE public.market_price_platform_quotes
    ADD CONSTRAINT market_price_platform_quotes_pkey
    PRIMARY KEY (dimension, canonical_name, platform_id);

ALTER TABLE public.market_price_platform_quotes
    DROP COLUMN IF EXISTS tier,
    DROP COLUMN IF EXISTS model_kind,
    DROP COLUMN IF EXISTS cache_hit_per_1m,
    DROP COLUMN IF EXISTS cny_cache_hit_per_1m;

DELETE FROM public.schema_migrations WHERE version = '974';

COMMIT;
