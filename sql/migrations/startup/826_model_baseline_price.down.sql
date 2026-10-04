-- ===========================================================================
-- File:          sql/migrations/startup/826_model_baseline_price.down.sql
-- Migration:     826 (down)
-- Database:      llm_gateway
--
-- 回滚顺序与 up 相反：先删视图（它依赖 models_canonical 的基准价列与
-- credential_model_bindings），再删对账台账，最后摘列。
--
-- ⚠ **本 down 会丢弃全部基准价与出处**：
--    models_canonical 上的 baseline_* 九列与 model_baseline_price_reconciliation
--    的全部对账历史一起消失。回滚后：
--      - 「供应商实付 / 原厂标准」这个倍率无从算起，成本核算退回人工对账；
--      - 已有的漂移记录（verdict='drift'）无法追溯，运营会失去「什么时候
--        发现过某个模型的价格对不上」这条线索。
--    生产回滚前先导出该表。
--
-- 幂等：DROP ... IF EXISTS / IF EXISTS，可安全重放。
-- ===========================================================================
BEGIN;

DROP VIEW IF EXISTS public.v_supplier_price_vs_baseline;

DROP TABLE IF EXISTS public.model_baseline_price_reconciliation;

ALTER TABLE public.models_canonical
    DROP CONSTRAINT IF EXISTS models_canonical_baseline_price_check;

ALTER TABLE public.models_canonical
    DROP COLUMN IF EXISTS baseline_price_fetched_at,
    DROP COLUMN IF EXISTS baseline_price_source_url,
    DROP COLUMN IF EXISTS baseline_price_source,
    DROP COLUMN IF EXISTS baseline_price_vendor,
    DROP COLUMN IF EXISTS baseline_cache_write_price_per_1m,
    DROP COLUMN IF EXISTS baseline_cache_read_price_per_1m,
    DROP COLUMN IF EXISTS baseline_output_price_per_1m,
    DROP COLUMN IF EXISTS baseline_input_price_per_1m,
    DROP COLUMN IF EXISTS baseline_price_currency;

COMMIT;
