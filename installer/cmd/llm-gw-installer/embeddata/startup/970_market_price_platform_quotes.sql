-- ===========================================================================
-- File:          sql/migrations/startup/970_market_price_platform_quotes.sql
-- Migration:     970
-- Purpose:       运营定价参考 · 按模型跨平台现价明细（对齐 salesdecorate
--                Compare 现价模式）。salesdecorate.price_observation 经
--                market-price-sync.py 单向落本表；只服务运营读面，禁止回流
--                路由/计费（与 940 同红线）。
--                PK(dimension, canonical_name, platform_id)；NULL 价 = 未知
--                （NULL≠0）。平台表 ⇒ 不加 RLS。
--
-- Down: 970_market_price_platform_quotes.down.sql
--
-- ── 超时预算（2026-11-17 补测；门 TestHeavyDDLModernMigrationsDeclareMeasuredBudget）
--   本文件含 2 条**非并发 CREATE INDEX** ⇒ 命中「全表扫描类 DDL」判据 ⇒
--   预算必须按目标环境最大表实测后抬够（761 先例：实测 18.9s ⇒ 抬 10min；
--   914 反例：拍 120s ⇒ 245 部署中止）。本文件的 10min 预算已就位，
--   此处补上**实测**依据（没有量过的预算就是拍脑袋）。
--   实测（目标环境 = 211，acc-pg17 / PostgreSQL 17 / arm64 Docker，
--        2026-11-17 06:6x，psql \timing）：同结构 TEMP 表灌 **100,000 行**
--        （本表一行 = 一个模型 × 一个平台的现价，PK 不含 run_date ⇒ 行数上界
--         = 模型数 × 平台数；211 上 models_canonical = 1855 个模型，
--         按 ~60 个平台取整上界 100,000 行；211 上该表真实行数 = 0）
--        后建同形索引：
--     (canonical_name, dimension) 80.285 ms
--     (platform_id, run_date DESC) 89.610 ms   ⇒ 最慢单条 **89.610 ms**
--   10min 预算的余量理由（与 965/966/968/969 同一惯例）：上面是**空载**
--   共享库上的单次测量，迁移窗口内网关仍在写、ensure 链与索引维护叠加
--   （761 的 18.9s 正是在这种争用下越过了角色级上限）⇒ 留足余量。
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.market_price_platform_quotes (
    dimension       text NOT NULL DEFAULT 'standard',
    canonical_name  text NOT NULL,
    platform_id     text NOT NULL,
    platform_name   text NOT NULL DEFAULT '',
    region          text NOT NULL DEFAULT '',
    native_currency text NOT NULL DEFAULT '',
    is_vendor       boolean NOT NULL DEFAULT false,
    vendor          text NOT NULL DEFAULT '',
    currency        text NOT NULL DEFAULT '',
    input_per_1m    numeric(14,6),
    output_per_1m   numeric(14,6),
    cny_in_per_1m   numeric(14,6),
    cny_out_per_1m  numeric(14,6),
    run_date        date NOT NULL,
    source_url      text NOT NULL DEFAULT '',
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dimension, canonical_name, platform_id)
);

CREATE INDEX IF NOT EXISTS idx_mppq_canon
    ON public.market_price_platform_quotes (canonical_name, dimension);
CREATE INDEX IF NOT EXISTS idx_mppq_platform
    ON public.market_price_platform_quotes (platform_id, run_date DESC);

COMMENT ON TABLE public.market_price_platform_quotes IS
    'Per-platform latest standard quotes mirrored from salesdecorate (970). Read-only ops surface; never feeds routing/billing.';
COMMENT ON COLUMN public.market_price_platform_quotes.input_per_1m IS
    'Native-currency input price per 1M tokens; NULL = unknown (NULL≠0).';

INSERT INTO public.schema_migrations (version, description)
VALUES ('970', 'market_price_platform_quotes: per-platform latest quotes for ops pricing reference (salesdecorate compare parity)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
