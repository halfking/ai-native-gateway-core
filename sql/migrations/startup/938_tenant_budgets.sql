-- ===========================================================================
-- File:          sql/migrations/startup/938_tenant_budgets.sql
-- Migration:     938
-- Purpose:       「运营管理与价格计费体系」P0（总体设计 §2.3/§2.4-2）：
--                tenant_budgets —— 租户月度预算 + 告警阈值 + 通知渠道。
--                预算告警的第二触发源 = 欠费可见化（unpaid_estimated_credits，
--                A-15：欠费默认放行 + 告警，不硬拦截）。
--                租户对象 ⇒ RLS（红线）。
--
-- Down: 938_tenant_budgets.down.sql
-- ===========================================================================

BEGIN;

-- ★ 锁守卫照 933 形态；★ 实测（2026-11-04 本地 PG17 空库）：
--   新建空表 PK <0.05s；无独立 CREATE INDEX。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.tenant_budgets (
    tenant_id               varchar      PRIMARY KEY,
    monthly_budget_cents    bigint       NOT NULL DEFAULT 0,
    balance_alert_threshold numeric(5,2) NOT NULL DEFAULT 0.20,
    quota_alert_threshold   numeric(5,2) NOT NULL DEFAULT 0.80,
    notify_channels         jsonb        NOT NULL DEFAULT '{}'::jsonb,
    created_at              timestamptz  NOT NULL DEFAULT now(),
    updated_at              timestamptz  NOT NULL DEFAULT now(),
    CONSTRAINT tenant_budgets_thresholds_check
        CHECK (balance_alert_threshold >= 0 AND balance_alert_threshold <= 1
           AND quota_alert_threshold >= 0 AND quota_alert_threshold <= 1)
);

ALTER TABLE public.tenant_budgets ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_tenant_budgets ON public.tenant_budgets;
CREATE POLICY tenant_isolation_tenant_budgets ON public.tenant_budgets
    USING (((tenant_id)::text = public.get_current_tenant()));

COMMENT ON COLUMN public.tenant_budgets.monthly_budget_cents IS '月度预算（账单币种分，cents_per_credit 同锚点语义）；0 = 不设预算。';
COMMENT ON COLUMN public.tenant_budgets.notify_channels IS '告警通道配置：{"inbox":true,"email":"a@b.c","wechat":"..."}（复用 domains/notification 三通道，AF-14）。';

INSERT INTO public.schema_migrations (version, description)
VALUES ('938', 'tenant_budgets: monthly budget cents + balance/quota alert thresholds + notify channels (tenant-scoped with RLS)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
