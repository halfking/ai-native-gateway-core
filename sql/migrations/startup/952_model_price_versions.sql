-- 952_model_price_versions.sql
-- 2026-11-09：运营审计轮项2 —— 内部零售价**版本化**。
--
-- -----------------------------------------------------------------------
-- 需求（运营审计轮 2026-11-09 用户指令项2）
--
--   「一个模型一个套餐模式一个价格……原则上一个模型，一种模式下
--    一个价格，不要出现重复的价格，每个价格是额效期的，下一个同一
--    模型同一套餐模式会自动将前一期的结束日期进行修正。并且可以
--    看到我们的订价变化模式。」
--
-- -----------------------------------------------------------------------
-- 现状缺陷（2026-11-09 取证）
--
-- model_credit_rates 是 canonical_id 单行的**可变价**：
--   UpsertModelRate（maas/model_rates.go:226）就地覆盖，历史价只留
--   updated_at 一个时刻，无生效窗、无套餐模式轴、无变化史。
--
-- -----------------------------------------------------------------------
-- 设计
--
-- model_price_versions = (canonical_id, billing_mode) 维度的**价格期**
-- 追加表：
--   • PK(canonical_id, billing_mode, effective_from)；
--   • effective_to NULL = 现行期（与 939 tenant_model_rates /
--     pricing_plans(565) 的「effective_to IS NULL = current」口径同构）；
--   • uq_model_price_versions_current 部分唯一索引 ⇒ **同一模型同一
--     套餐模式任意时刻至多一个现行价**（「不要出现重复的价格」的
--     数据库级强制）；
--   • billing_mode 闭集：pay_as_you_go（按量，驱动计费热路径的语义
--     等价于 model_credit_rates 行）| token_plan（包月套餐价）|
--     booster_pack（加油包价）。
-- 写入侧（maas/model_rates.go SyncModelPriceVersion）：
--   • 价格未变化 ⇒ 不开新期（防重复）；
--   • 价格变化 ⇒ 原子地「闭上一期（effective_to=新期起点）+ 开新期」；
--   • 行被清空/删除 ⇒ 只闭期不开新期（空洞 = 走全局默认价，如实）。
-- 计费热路径**零改动**：chargeTokensWithMultiplier 继续读
-- model_credit_rates —— 本表是定价数据层的版本账 + 展示面。
--
-- Down: 952_model_price_versions.down.sql
-- ===========================================================================

BEGIN;

-- ★ 锁守卫照 939 形态；新建空表 + PK + 部分唯一索引，空库实测 <0.05s。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.model_price_versions (
    canonical_id                bigint       NOT NULL,
    billing_mode                varchar(32)  NOT NULL DEFAULT 'pay_as_you_go',
    effective_from              timestamptz  NOT NULL DEFAULT now(),
    effective_to                timestamptz,
    credits_per_1m_in           bigint,
    credits_per_1m_out          bigint,
    credits_per_1m_cache_in     bigint,
    credits_per_1m_cache_out    bigint,
    credits_per_1m_image_tokens bigint,
    credits_per_1m_audio_tokens bigint,
    credits_per_1m_video_tokens bigint,
    source                      varchar(32)  NOT NULL DEFAULT 'manual',
    note                        text         NOT NULL DEFAULT '',
    created_at                  timestamptz  NOT NULL DEFAULT now(),
    PRIMARY KEY (canonical_id, billing_mode, effective_from),
    CONSTRAINT model_price_versions_period_check
        CHECK (effective_to IS NULL OR effective_to > effective_from),
    CONSTRAINT model_price_versions_billing_mode_check
        CHECK (billing_mode IN ('pay_as_you_go', 'token_plan', 'booster_pack')),
    CONSTRAINT model_price_versions_source_check
        CHECK (source IN ('manual', 'batch', 'global_fill', 'reset', 'seed'))
);

-- 「一个模型一种模式至多一个现行价」——数据库级强制。
CREATE UNIQUE INDEX IF NOT EXISTS uq_model_price_versions_current
    ON public.model_price_versions (canonical_id, billing_mode)
    WHERE effective_to IS NULL;

-- 历史时间线读面（按模型×模式倒序）。
CREATE INDEX IF NOT EXISTS idx_model_price_versions_timeline
    ON public.model_price_versions (canonical_id, billing_mode, effective_from DESC);

COMMENT ON TABLE public.model_price_versions IS
    'Versioned retail credit price periods per (canonical model, billing mode), 952. effective_to NULL = current period; the partial unique index guarantees at most one current price per (model, mode). Writers (maas SyncModelPriceVersion) auto-close the previous period when a new price lands; unchanged prices never open a new period. Billing hot path keeps reading model_credit_rates - this table is the pricing ledger + display surface.';
COMMENT ON COLUMN public.model_price_versions.billing_mode IS
    'pay_as_you_go (per-token, drives billing hot path) | token_plan (monthly plan reference price) | booster_pack (booster pack reference price).';
COMMENT ON COLUMN public.model_price_versions.effective_to IS
    'NULL = current period (auto-closed by the next price for the same model+mode).';
COMMENT ON COLUMN public.model_price_versions.source IS
    'manual | batch | global_fill | reset | seed - which admin write path produced this period.';

INSERT INTO public.schema_migrations (version, description)
VALUES ('952', 'model_price_versions: versioned retail price periods per (model, billing mode) with auto-closed validity windows and at-most-one-current uniqueness')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
