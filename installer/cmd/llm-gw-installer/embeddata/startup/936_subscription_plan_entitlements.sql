-- ===========================================================================
-- File:          sql/migrations/startup/936_subscription_plan_entitlements.sql
-- Migration:     936
-- Purpose:       「运营管理与价格计费体系」P0（总体设计 §2.3）：
--                subscription_plan_entitlements —— 套餐按模型的包含额度
--                （plan_id × canonical_id）。canonical_id 对齐网关侧
--                models_canonical.id（AF-8：租户配置空间用 id，
--                行情同步空间用名字，消费侧互跳在同步器做）。
--                平台表（plan 目录同族），不加 RLS。
--
-- Down: 936_subscription_plan_entitlements.down.sql
-- ===========================================================================

BEGIN;

-- ★ 锁守卫照 933 形态；★ 实测（2026-11-04 本地 PG17 空库）：全新空表
--   CREATE TABLE + PK <0.05s；★ 无 CREATE INDEX（PK 即索引，建在新建空表上）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.subscription_plan_entitlements (
    plan_id            integer      NOT NULL REFERENCES public.subscription_plans(id) ON DELETE CASCADE,
    canonical_id       bigint       NOT NULL,
    included_credits   bigint       NOT NULL DEFAULT 0,
    overage_multiplier numeric(8,4) NOT NULL DEFAULT 1.0,
    notes              text         NOT NULL DEFAULT '',
    created_at         timestamptz  NOT NULL DEFAULT now(),
    updated_at         timestamptz  NOT NULL DEFAULT now(),
    PRIMARY KEY (plan_id, canonical_id),
    CONSTRAINT subscription_plan_entitlements_overage_positive
        CHECK (overage_multiplier >= 0)
);

COMMENT ON TABLE public.subscription_plan_entitlements IS
    'Per-model credit entitlements for a subscription plan (936). canonical_id references models_canonical.id (gateway-side configuration namespace, AF-8). included_credits=0 with overage_multiplier<1 means "model included in plan at discounted overage" rather than a lump quota.';

INSERT INTO public.schema_migrations (version, description)
VALUES ('936', 'subscription_plan_entitlements: per-model included credits / overage multiplier keyed by (plan_id, canonical_id)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
