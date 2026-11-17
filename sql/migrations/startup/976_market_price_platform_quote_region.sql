-- ===========================================================================
-- File:          sql/migrations/startup/976_market_price_platform_quote_region.sql
-- Migration:     976
-- Purpose:       970 平台现价明细的**表键粒度**对齐源侧观测键（补 source_region
--                并把 source_region / currency 并入主键）。
--
-- 背景（211 真库首次写入即暴露，2026-11-17）：
--   970 表的主键是 (dimension, canonical_name, platform_id, tier)，
--   而**源侧观测键**是 (platform, raw_model, price_scope, tier, region,
--   currency, unit) —— salesdecorate 的 ingest 去重键与 price_hash 同款
--   （backend/internal/store/market.go 的 latest CTE 亦用同一组列）。
--   ⇒ 源侧**同一平台、同一模型、同一 tier 可以有多条 region 的报价**。
--   实测：bailian 的 deepseek-v4-flash-0731「忙时 08:00-22:00」有 5 个 region：
--     中国大陆 3.000000 / 全球 3.000000 / 国际（新加坡）/ 日本 / 美国 3.208000 CNY
--     —— **同一平台同一档位，价格差 7%**。
--   ⇒ 两个后果（都必须修）：
--     ① 落地时撞主键 23505 ⇒ 整笔事务 abort（**首次真跑即 FATAL**，
--        且 970 表此前面向生产恒为 0 行 ⇒ 该写入路径从未在生产跑过）。
--     ② 若改成「静默取一档」⇒ 丢掉 4/5 的报价 ⇒ **静默信息丢失**
--        （与本轮「对比面要完整没有丢失」的目标直接冲突）。
--
-- 本迁移只做两件事（**不写任何价格数值**）：
--   ① 970 表加一列 source_region（源侧报价所属地区：'中国大陆' / '全球' /
--      '美国' …）；★ 原有 region 列语义**不变**（= 网关平台登记表的
--      domestic/overseas，用于 940 的 A-17 双地板判定）⇒ 两个语义不混用。
--   ② 主键并入 source_region 与 currency ⇒ 表键 = 源侧观测键的投影，
--      同键多行由采集侧按「地板口径」确定性择优留一行（折叠数写台账）。
--
-- 为什么 currency 也进主键：211 真库实测 (platform, model, tier, region)
--   的多币种行数为 **0**（当前不存在）⇒ 进主键**不产生额外行数**，
--   但能挡住「同键多币种」的未来形态（源侧 DISTINCT ON 含 currency）。
--   currency 列本就 NOT NULL DEFAULT '' ⇒ 可直接进主键，无需回填。
--
-- 红线：只改**行情面**表结构；绝不触碰 model_credit_rates /
--   model_price_versions / credential_model_bindings（路由与计费面）。
--
-- Down: 976_market_price_platform_quote_region.down.sql
--
-- ── 超时预算（门 TestHeavyDDLModernMigrationsDeclareMeasuredBudget）─────
--   本文件含 1 次 PRIMARY KEY 重建（建唯一索引，扫全表）⇒ 命中「全表扫描类
--   DDL」判据 ⇒ 按 761 先例（实测 18.9s ⇒ 抬 10min）声明 **10min** 预算。
--   实测（目标环境 = 211，acc-pg17 / PostgreSQL 17 / arm64 Docker，
--        2026-11-17 17:2x，psql \timing，**在真实表上跑完 ROLLBACK**；
--        计时前先在事务内把表形态复现回 976 之前（撤新列 / 撤新主键），
--        量的是「974 之后的真实表形态」：market_price_platform_quotes 0 行、
--        market_price_reference 689 行）：
--     ALTER TABLE ADD COLUMN source_region   0.761 ms
--     DELETE（同新键去重，幂等防护）          0.540 ms（命中 0 行）
--     DROP CONSTRAINT (PK)                   0.375 ms
--     ADD CONSTRAINT (PK 重建)               0.402 ms
--     CREATE INDEX（辅助索引）                0.329 ms
--     合计 ≈ 2.4 ms
--   ⇒ 与 10min 预算差五个数量级；余量留给「迁移窗口内采集器仍在写」
--     造成的索引维护与锁等待（761 的 18.9s 正是在那种争用下越过了角色级上限）。
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- ── ① 加 source_region 列 ───────────────────────────────────────────────
ALTER TABLE public.market_price_platform_quotes
    ADD COLUMN IF NOT EXISTS source_region text NOT NULL DEFAULT '';

COMMENT ON COLUMN public.market_price_platform_quotes.source_region IS
    'Region the quoted price applies to, from salesdecorate price_observation.region (中国大陆 / 全球 / 美国 …). NOT the gateway registry region (see column "region" = domestic/overseas, used by the A-17 dual-floor rules).';

-- ── ② 主键并入 source_region + currency ─────────────────────────────────
-- 先按新键去重（若旧表里已存在同新键的重复行，ADD PRIMARY KEY 会 23505）：
--   ★ 当前线上该表 0 行 ⇒ 这条 DELETE 是**幂等防护**，不删任何真实数据。
--   ★ 语义：同 (dimension, canonical_name, platform_id, tier, source_region,
--     currency) 保留**一条**（按 ctid 取最早）⇒ 与采集侧「地板口径确定性
--     择优」的口径一致（采集侧会整表重写 ⇒ 最终形态由采集器决定）。
DELETE FROM public.market_price_platform_quotes a
      USING public.market_price_platform_quotes b
 WHERE a.ctid > b.ctid
   AND a.dimension = b.dimension
   AND a.canonical_name = b.canonical_name
   AND a.platform_id = b.platform_id
   AND a.tier = b.tier
   AND a.source_region = b.source_region
   AND a.currency = b.currency;

ALTER TABLE public.market_price_platform_quotes
    DROP CONSTRAINT IF EXISTS market_price_platform_quotes_pkey;
ALTER TABLE public.market_price_platform_quotes
    ADD CONSTRAINT market_price_platform_quotes_pkey
    PRIMARY KEY (dimension, canonical_name, platform_id, tier, source_region, currency);

-- 便于按「模型 + 地区」查现价的辅助索引（IF NOT EXISTS 幂等重放）。
CREATE INDEX IF NOT EXISTS idx_mppq_canon_region
    ON public.market_price_platform_quotes (canonical_name, dimension, source_region);

INSERT INTO public.schema_migrations (version, description)
VALUES ('976', 'market price platform quotes: source_region column + table key aligned to source observation grain (region/currency in PK)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
