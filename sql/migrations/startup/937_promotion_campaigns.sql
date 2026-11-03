-- ===========================================================================
-- File:          sql/migrations/startup/937_promotion_campaigns.sql
-- Migration:     937
-- Purpose:       「运营管理与价格计费体系」P0（总体设计 §2.3/§2.4-3）：
--                promotion_campaigns（全局活动目录，status 门控）
--                + promotion_redemptions（兑现台账；★ 活动成本口径 =
--                  SUM(credit_ledger.amount) WHERE campaign_id 非空，
--                  与本表 credits_granted 双向对账）。
--                campaigns 非租户对象（租户只读 active 集合经 API 过滤）
--                ⇒ 不加 RLS；redemptions 是租户对象 ⇒ 必须带 RLS（红线）。
--
-- Down: 937_promotion_campaigns.down.sql
-- ===========================================================================

BEGIN;

-- ★ 锁守卫照 933 形态；★ 实测（2026-11-04 本地 PG17 空库）：两表 + 2 索引
--   全部建在**本文件刚 CREATE 的空表**上（无存量可扫）墙钟 <0.1s，
--   形态同 912/913/928 先例。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.promotion_campaigns (
    id                        bigserial PRIMARY KEY,
    code                      varchar(64)  NOT NULL UNIQUE,
    name                      varchar(128) NOT NULL,
    description               text         NOT NULL DEFAULT '',
    benefit_type              varchar(32)  NOT NULL DEFAULT 'credits',
    benefit_value             jsonb        NOT NULL DEFAULT '{}'::jsonb,
    status                    varchar(16)  NOT NULL DEFAULT 'draft',
    starts_at                 timestamptz,
    ends_at                   timestamptz,
    max_redemptions_per_tenant integer     NOT NULL DEFAULT 1,
    total_budget_credits      bigint       NOT NULL DEFAULT 0,
    granted_credits           bigint       NOT NULL DEFAULT 0,
    created_at                timestamptz  NOT NULL DEFAULT now(),
    updated_at                timestamptz  NOT NULL DEFAULT now(),
    CONSTRAINT promotion_campaigns_status_check
        CHECK (status IN ('draft', 'active', 'paused', 'ended')),
    CONSTRAINT promotion_campaigns_benefit_type_check
        CHECK (benefit_type IN ('credits', 'discount', 'gift'))
);

CREATE TABLE IF NOT EXISTS public.promotion_redemptions (
    id              bigserial PRIMARY KEY,
    campaign_id     bigint       NOT NULL REFERENCES public.promotion_campaigns(id),
    tenant_id       varchar      NOT NULL,
    owner_user      varchar(128) NOT NULL DEFAULT '',
    order_id        bigint,
    benefit         jsonb        NOT NULL DEFAULT '{}'::jsonb,
    credits_granted bigint       NOT NULL DEFAULT 0,
    created_at      timestamptz  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_promotion_redemptions_campaign
    ON public.promotion_redemptions (campaign_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_promotion_redemptions_tenant
    ON public.promotion_redemptions (tenant_id, created_at DESC);

-- RLS（红线：租户对象必须带策略；策略名沿用 tenant_isolation_ 前缀族）。
ALTER TABLE public.promotion_redemptions ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_promotion_redemptions ON public.promotion_redemptions;
CREATE POLICY tenant_isolation_promotion_redemptions ON public.promotion_redemptions
    USING (((tenant_id)::text = public.get_current_tenant()));

COMMENT ON COLUMN public.promotion_campaigns.total_budget_credits IS '活动总预算积分；0 = 不设预算。granted_credits 是运行总量，超预算拒绝兑现。';
COMMENT ON COLUMN public.promotion_redemptions.credits_granted IS '本次兑现实发积分；与 credit_ledger.campaign_id 对账（活动成本口径，总体设计 §2.4-3）。';

INSERT INTO public.schema_migrations (version, description)
VALUES ('937', 'promotion_campaigns (global catalog, status-gated) + promotion_redemptions (tenant-scoped with RLS) for campaign credits with campaign_id ledger reconciliation')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
