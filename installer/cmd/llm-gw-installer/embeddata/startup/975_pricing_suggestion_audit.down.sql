-- 975_pricing_suggestion_audit.down.sql —— 撤销 975。
--
-- ★ 不删批次行里已写下的历史（批次表是审计面，删它＝删审计痕迹）。
--   只撤结构：批次表本身**保留**（审计痕迹不随结构回滚消失），
--   仅把 model_price_versions 的 source 闭集收回（不含 market_suggestion）。
--
-- ★ 注意：收回 source 闭集后，历史行里 source='market_suggestion' 的期次会
--   **违反**新 CHECK ⇒ 因此 down 脚本先把那些期的 source 改写为 'manual'
--   （保留价格与 note，不丢审计内容，只是不再带这个来源标签）。

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- 收回闭集前：把 market_suggestion 期次改标为 manual（价格与 note 全部保留）。
UPDATE public.model_price_versions
   SET source = 'manual'
 WHERE source = 'market_suggestion';

ALTER TABLE public.model_price_versions
    DROP CONSTRAINT IF EXISTS model_price_versions_source_check;
ALTER TABLE public.model_price_versions
    ADD CONSTRAINT model_price_versions_source_check
    CHECK (source::text = ANY (ARRAY[
        'manual', 'batch', 'global_fill', 'reset', 'seed'
    ]::text[]));

-- ★ 批次表**保留**（审计面不随结构回滚删除）。如确需清理，显式执行：
--   DELETE FROM public.pricing_suggestion_batches;

DELETE FROM public.schema_migrations WHERE version = '975';

COMMIT;
