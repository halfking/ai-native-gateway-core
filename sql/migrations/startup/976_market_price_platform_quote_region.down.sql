-- ===========================================================================
-- File:          sql/migrations/startup/976_market_price_platform_quote_region.down.sql
-- Migration:     976（撤销）
-- Purpose:       撤销 976 的结构改动（source_region 列 + 表键粒度）。
--
-- ★ 撤销语义（先量后改）：
--   ① 先把 source_region 列从主键里摘掉（恢复 974 的表键）
--      ⇒ 必须**先摘列再删列**（删列会自动从索引/PK 中移除，但显式重建
--      可控且幂等）。
--   ② 再删 source_region 列与辅助索引。
--   ③ 台账行按版本号删除 —— ★ 只删自己的版本号（'976'），
--      不碰别人的记账行。
--
-- ★ 回滚会丢数据：source_region 是 2026-11-17 新增的观测维度，删列后
--   同 (dimension, canonical_name, platform_id, tier) 的多 region 报价会
--   **重新变回多行** ⇒ 旧主键无法容纳 ⇒ ADD PRIMARY KEY 可能 23505。
--   ⇒ 因此本 down 先按旧键去重（保留 ctid 最早的一条 = 最早落地的那档），
--     再重建旧主键 ⇒ down 一定能跑完，但**会丢掉同键多 region 的其余报价**
--     （该损失由「回滚语义」本身决定，不是缺陷）⇒ 因此 down 文件里
--     显式记录这一条，避免回滚后无人知道明细少了行。
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- ── ① 旧表键去重（回滚后同键多 region 会冲突）──────────────────────────
DELETE FROM public.market_price_platform_quotes a
      USING public.market_price_platform_quotes b
 WHERE a.ctid > b.ctid
   AND a.dimension = b.dimension
   AND a.canonical_name = b.canonical_name
   AND a.platform_id = b.platform_id
   AND a.tier = b.tier;

-- ── ② 主键回退到 974 的粒度 ────────────────────────────────────────────
ALTER TABLE public.market_price_platform_quotes
    DROP CONSTRAINT IF EXISTS market_price_platform_quotes_pkey;
ALTER TABLE public.market_price_platform_quotes
    ADD CONSTRAINT market_price_platform_quotes_pkey
    PRIMARY KEY (dimension, canonical_name, platform_id, tier);

-- ── ③ 删辅助索引与新列 ──────────────────────────────────────────────────
DROP INDEX IF EXISTS idx_mppq_canon_region;

ALTER TABLE public.market_price_platform_quotes
    DROP COLUMN IF EXISTS source_region;

DELETE FROM public.schema_migrations WHERE version = '976';
