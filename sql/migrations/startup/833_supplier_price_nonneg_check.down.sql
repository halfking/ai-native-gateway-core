-- ===========================================================================
-- File:          sql/migrations/startup/833_supplier_price_nonneg_check.down.sql
-- Migration:     833 (down)
-- Database:      llm_gateway
--
-- 只删本迁移加的四个 CHECK 约束，不动任何数据、也不碰表。
--
-- ⚠ 回滚它 = 重新打开那个口子：负价再次可写，而 826 的偏差视图会把它算成
--   负倍率（实测 -5.00 / 基准 5.00 ⇒ -1.0，读起来像「便宜 100%」）。
--   所以它不是「无害清理」，而是自愿把「错价可入库」这个状态放回来。
--
-- 不删数据是刻意的：down 只回滚结构约束，绝不碰价格本身。价格是钱。
--
-- 幂等：DROP ... IF EXISTS，可安全重放。
-- ===========================================================================
BEGIN;

ALTER TABLE public.credential_model_bindings
    DROP CONSTRAINT IF EXISTS cmb_price_nonneg_in;
ALTER TABLE public.credential_model_bindings
    DROP CONSTRAINT IF EXISTS cmb_price_nonneg_out;
ALTER TABLE public.credential_model_bindings
    DROP CONSTRAINT IF EXISTS cmb_price_nonneg_cache_read;
ALTER TABLE public.credential_model_bindings
    DROP CONSTRAINT IF EXISTS cmb_price_nonneg_cache_write;

COMMIT;
