-- ===========================================================================
-- File:          sql/migrations/startup/996_tenant_budget_enforcement_mode.sql
-- Migration:     996
-- Purpose:       「优化完善方案 2026-11-21（青岛联通交流会输入）」W1-Q1：
--                租户月度预算硬拦截开关 —— tenant_budgets 增 enforcement_mode
--                ∈ {warn, hard}：
--                  warn（默认）= 仅告警不拦截（A-15 延续：欠费默认放行 + 告警）
--                                ⇒ 存量行为零变化；
--                  hard        = 当月已计费花费（credits × cents_per_credit，
--                                与 maas_monthly_bills.amount_cents 同源
--                                sumPeriodConsumption）达 monthly_budget_cents
--                                ⇒ 网关请求面 429 tenant_budget_exceeded
--                                （default 租户与内部回环 key 豁免；
--                                monthly_budget_cents = 0 即未设预算不拦截）。
--                ★ 为什么是新迁移而不是改 938 的字节：938 已进部署台账
--                  （applied+verified，2026-11-03 deploy 245 build_seq 2929）
--                  ⇒ 改字节走 content replay 通道并连锁 break（914/928 教训，
--                  同 929/930 的处置：down 只退自己引入的那部分）。
-- Down: 996_tenant_budget_enforcement_mode.down.sql
-- ===========================================================================

-- ★ 会话级超时守卫（995 同口径；docker 流式通道上 SET LOCAL 静默失效
--   —— 994 实测先例）。tenant_budgets 每 tenant 一行，表极小；
--   ADD COLUMN NOT NULL DEFAULT 在 PG11+ 为元数据级变更。
SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

-- ★ 幂等：IF NOT EXISTS ⇒「表已在但列不齐」的环境修得回来（932 补列教训）。
ALTER TABLE public.tenant_budgets
    ADD COLUMN IF NOT EXISTS enforcement_mode varchar NOT NULL DEFAULT 'warn';

-- ★ 闭集 CHECK 幂等（PostgreSQL 无 ADD CONSTRAINT IF NOT EXISTS ⇒ DO 块；
--   存量行由 DEFAULT 'warn' 填充 ⇒ 约束即建即验，无需 NOT VALID）。
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'tenant_budgets_enforcement_mode_check'
          AND conrelid = 'public.tenant_budgets'::regclass
    ) THEN
        ALTER TABLE public.tenant_budgets
            ADD CONSTRAINT tenant_budgets_enforcement_mode_check
            CHECK (enforcement_mode IN ('warn', 'hard'));
    END IF;
END $$;

COMMENT ON COLUMN public.tenant_budgets.enforcement_mode IS
'预算执行模式（W1-Q1）：warn=仅告警不拦截（默认，存量行为零变化）；hard=当月花费(credits×cents_per_credit,与maas_monthly_bills同源sumPeriodConsumption)达monthly_budget_cents即网关429 tenant_budget_exceeded（default租户与内部key豁免）。monthly_budget_cents=0即未设预算不拦截。';

INSERT INTO public.schema_migrations (version, description)
VALUES ('996', 'tenant_budgets.enforcement_mode: warn|hard monthly-budget enforcement (default warn = alert only; hard = 429 tenant_budget_exceeded at budget, default-tenant/internal-key exempt)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
