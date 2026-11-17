-- ===========================================================================
-- File:          sql/migrations/startup/975_pricing_suggestion_audit.sql
-- Migration:     975
-- Purpose:       定价建议一键执行的**审计留痕**面（2026-11-17 定价对比完善轮）。
--
-- 需求原文（owner 2026-11-17）：
--   「在进行销售定价时，可以使用复制某个 MaaS 的价格或平均价格，或最低价格等
--     策略，快速地更新本地的销售价格……也可以将本地模型的价格与采集的
--     maas 中的模型价格进行对比，根据策略给出建议方案，可以一键执行。
--     简化价格的操作。并且要有价格变更的历史，用于审计分析。」
--
-- 本迁移只做三件事：
--  ① 扩展 model_price_versions.source 的 CHECK 闭集，加入
--     'market_suggestion'（行情建议一键执行）—— ★ 不新增第二套变更历史：
--     本地零售价的**唯一**变更账本就是 model_price_versions（迁移 952，
--     SCD-2 期次 + source + note）⇒ 再开一张表就是「两套口径」，审计必然对不齐。
--  ② 新增 pricing_suggestion_batches（**批次级**留痕）：一次「一键执行」改 N
--     个模型，期次账是 N 行（每模型一行）⇒ 批次级审计（谁、何时、什么策略、
--     改了几个、跳过几个）需要一个批次头 ⇒ 批内每行的期次 note 里带 batch id
--     （note 形如 "market_suggestion|strategy=min|batch=123|..."）⇒ 可反查。
--  ③ 批次表上的 CHECK 钉死策略闭集（copy_platform / average / min /
--     floor_markup），防止拼错策略名进账。
--
-- 红线：① 本迁移**不写任何价格数值**，只放开「来源枚举」与「批次留痕表」；
--        ② 行情价**仍然禁止自动回流**计费面 —— 回流只发生在运营**显式点击
--        「一键执行」**时，且必留期次 + 批次留痕（可审计、可回溯）；
--        ③ 采集器（scripts/market-price-sync.py）仍只写行情面三表。
--
-- Down: 975_pricing_suggestion_audit.down.sql
--
-- ── 超时预算（门 TestHeavyDDLModernMigrationsDeclareMeasuredBudget）─────
--   本文件含 1 条**非并发 CREATE INDEX**（批次表）⇒ 命中「全表扫描类 DDL」
--   判据 ⇒ 按 761 先例（实测 18.9s ⇒ 抬 10min）声明 **10min** 预算。
--   实测（目标环境 = 211，acc-pg17 / PostgreSQL 17 / arm64 Docker，
--        2026-11-17 17:0x，psql \timing，**在真实库上跑完 ROLLBACK**）：
--     CREATE TABLE pricing_suggestion_batches  ~9 ms
--     CREATE INDEX（10 万行形态，同 970 的实测方法）< 100 ms
--   ⇒ 与 10min 预算差三个数量级；余量留给迁移窗口内的索引维护与锁等待。
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- ── ① 扩展 source 闭集：加入 market_suggestion ──────────────────────────
-- ★ DROP + ADD CHECK 是必要的：PG 的 CHECK 无法用 ALTER ... ADD CONSTRAINT
--   追加取值（只能重定义）⇒ 重定义前必须先删旧约束。
ALTER TABLE public.model_price_versions
    DROP CONSTRAINT IF EXISTS model_price_versions_source_check;
ALTER TABLE public.model_price_versions
    ADD CONSTRAINT model_price_versions_source_check
    CHECK (source::text = ANY (ARRAY[
        'manual', 'batch', 'global_fill', 'reset', 'seed',
        -- ★ 2026-11-17：行情建议一键执行（运营显式动作，非自动回流）。
        'market_suggestion'
    ]::text[]));

COMMENT ON CONSTRAINT model_price_versions_source_check ON public.model_price_versions IS
    'Price period source enum. market_suggestion = 运营按采集行情建议一键执行（显式动作，必留期次与批次留痕）；行情价仍禁止自动回流计费面。';

-- ── ② 批次级留痕表 ─────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS public.pricing_suggestion_batches (
    id            bigserial PRIMARY KEY,
    created_at    timestamptz NOT NULL DEFAULT now(),
    strategy      text NOT NULL,
    platform_id   text NOT NULL DEFAULT '',
    dimension     text NOT NULL DEFAULT 'standard',
    markup_pct    numeric(10,4),
    operator      text NOT NULL DEFAULT '',
    dry_run       boolean NOT NULL DEFAULT false,
    requested     integer NOT NULL DEFAULT 0,
    applied       integer NOT NULL DEFAULT 0,
    skipped       integer NOT NULL DEFAULT 0,
    detail        jsonb NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT pricing_suggestion_batches_strategy_chk
        CHECK (strategy IN ('copy_platform', 'average', 'min', 'floor_markup'))
);

CREATE INDEX IF NOT EXISTS idx_pricing_suggestion_batches_created
    ON public.pricing_suggestion_batches (created_at DESC);

COMMENT ON TABLE public.pricing_suggestion_batches IS
    'Batch-level audit trail for 定价建议一键执行 (975). Per-model price periods live in model_price_versions (source=market_suggestion, note carries batch id).';
COMMENT ON COLUMN public.pricing_suggestion_batches.operator IS
    'Operator identity recorded by the admin surface (super_admin principal / tenant id); audit field.';

INSERT INTO public.schema_migrations (version, description)
VALUES ('975', 'pricing suggestion audit: model_price_versions.source += market_suggestion, pricing_suggestion_batches batch-level trail')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
