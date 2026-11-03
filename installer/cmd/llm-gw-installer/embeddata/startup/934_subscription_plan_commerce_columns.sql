-- ===========================================================================
-- File:          sql/migrations/startup/934_subscription_plan_commerce_columns.sql
-- Migration:     934
-- Purpose:       「运营管理与价格计费体系」P0 地基（总体设计 §2.3，审计档
--                docs/audit/2026-11-03-运营管理与价格计费体系-两规划合并审计与总体实施设计.md）：
--                商业骨架补列 —— subscription_plans 加 9 列（计费周期/配额/
--                超额价/超额开关/生效窗/自动续订/并发上限/SLA），
--                topup_packages 加 4 列（赠送积分/生效窗/首充限定）。
--                全部带 DEFAULT ⇒ 对既有 3 行种子零回填成本，NOT NULL 安全。
--
-- Down: 934_subscription_plan_commerce_columns.down.sql
-- ===========================================================================

BEGIN;

-- ★ 锁与超时守卫（照 933 形态）：ALTER TABLE 拿 AccessExclusiveLock（短暂）
--   ⇒ 自带 lock_timeout 快速失败；列带 DEFAULT 的补列在 PG11+ 是元数据级
--   操作，不重写表。
--   ★ 实测（2026-11-04，211 真库 acc-pg17/llm_gateway，W2-T0）：
--     subscription_plans=3 行 / topup_packages=3 行（种子量级），
--     本地 PG17 空库跑完整脚本墙钟 <0.1s ⇒ 与 10min 预算差多个数量级；
--   ★ 本文件无 CREATE INDEX / VALIDATE CONSTRAINT，预算按同族保守声明。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

ALTER TABLE public.subscription_plans
    ADD COLUMN IF NOT EXISTS billing_period      varchar(16)  NOT NULL DEFAULT 'monthly',
    ADD COLUMN IF NOT EXISTS quota_credits       bigint       NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS overage_price_cents integer      NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS overage_allowed     boolean      NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS effective_from      timestamptz,
    ADD COLUMN IF NOT EXISTS effective_to        timestamptz,
    ADD COLUMN IF NOT EXISTS auto_renew          boolean      NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS concurrency_limit   integer      NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS sla                 varchar(128) NOT NULL DEFAULT '';

-- billing_period 闭集：monthly | quarterly | yearly。
-- 先 DROP 再 ADD 保证重放幂等（重放时 DEFAULT 'monthly' 使既有行满足 CHECK）。
ALTER TABLE public.subscription_plans
    DROP CONSTRAINT IF EXISTS subscription_plans_billing_period_check;
ALTER TABLE public.subscription_plans
    ADD CONSTRAINT subscription_plans_billing_period_check
    CHECK (billing_period IN ('monthly', 'quarterly', 'yearly'));

ALTER TABLE public.topup_packages
    ADD COLUMN IF NOT EXISTS bonus_credits    bigint      NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS effective_from   timestamptz,
    ADD COLUMN IF NOT EXISTS effective_to     timestamptz,
    ADD COLUMN IF NOT EXISTS first_time_only  boolean     NOT NULL DEFAULT false;

COMMENT ON COLUMN public.subscription_plans.billing_period IS '计费周期：monthly|quarterly|yearly（A-10：套餐超额允许，overage_price_cents 可配）。';
COMMENT ON COLUMN public.subscription_plans.quota_credits IS '周期内含积分配额；0 = 不限量（仅价格包）。';
COMMENT ON COLUMN public.subscription_plans.overage_price_cents IS '超额积分单价（分/积分，账单币种分 —— cents_per_credit 同语义锚点，A-14）。';
COMMENT ON COLUMN public.subscription_plans.concurrency_limit IS '并发请求上限；0 = 不限。';
COMMENT ON COLUMN public.topup_packages.bonus_credits IS '充值赠送积分（与 credits_amount 分列记账）。';
COMMENT ON COLUMN public.topup_packages.first_time_only IS '首充限定标记。';

INSERT INTO public.schema_migrations (version, description)
VALUES ('934', 'commerce skeleton columns: subscription_plans +9 (billing_period/quota_credits/overage/effective window/auto_renew/concurrency/sla), topup_packages +4 (bonus/effective window/first_time_only)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
