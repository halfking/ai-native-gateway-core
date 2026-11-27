-- ============================================================
-- 迁移 1003: 组织层级 + 组织/用户积分钱包（三级配额制：租户→组织→用户）
-- 目的：补齐组织层（表全缺）与用户层钱包，使三级额度链可落地
-- 范式：复用 tenant_credit_wallets（007）+ credit_limit/credit_policy（1000）
-- 口径：余额/限额 NULL = 不限制（沿用 budget_usd NULL 先例）；
--       credit_policy IN ('warn','block')，默认 warn
-- 依赖：007（tenants/钱包范式）、001（users 表）；不依赖 1000-1002 的列
-- 日期：2026-11-27
-- 影响：organizations(新)、org_credit_wallets(新)、user_credit_wallets(新)、users.org_id(新列)
-- ============================================================

-- 锁守卫（installer 门 startup_ddl_lock_guard_gate_test.go：版本 > 870 且含强锁 DDL
--   ⇒ 必须自带 lock_timeout）：本件含 CREATE TABLE ＋ ALTER TABLE ＋ CREATE INDEX
--   ⇒ 声明 5s lock_timeout / 10min statement_timeout（同 999a/999d/1000 口径）。
-- ★ 实测（2026-11-27 三级配额制轮，PG17 本机一次性容器 casdoor-e2e-pg:13.3.0）：
--   全新基线（00/01/02 三件后）单件首跑墙钟 <0.1s（CREATE TABLE×3 + ALTER ADD
--   COLUMN＋索引全为元数据级/空表操作）、幂等重放 0.05s、down 0.04s；
--   fresh 建造器链 416/417 实证通过（唯一失败件为并行车道 1004 的自身问题）
--   ⇒ 与 10min 预算差五个数量级，无 914 型「拍小了中止部署」风险。
SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

-- ──────────────────────────────────────────────────────────
-- Part 1: organizations 表（组织层级）
-- ──────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS organizations (
    id          SERIAL PRIMARY KEY,
    tenant_id   VARCHAR(64) NOT NULL REFERENCES tenants(code) ON DELETE CASCADE,
    code        VARCHAR(64) NOT NULL,            -- 租户内唯一组织代码
    name        VARCHAR(128) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT organizations_tenant_code_unique UNIQUE (tenant_id, code)
);

CREATE INDEX IF NOT EXISTS idx_organizations_tenant
    ON organizations(tenant_id);

COMMENT ON TABLE organizations IS
  '组织层级：租户下的二级分组，支撑三级配额制（租户→组织→用户）';

-- ──────────────────────────────────────────────────────────
-- Part 2: users.org_id（用户↔组织关联，可空=不属于任何组织）
-- ──────────────────────────────────────────────────────────
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS org_id INT REFERENCES organizations(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_users_org ON users(org_id);

COMMENT ON COLUMN users.org_id IS
  '所属组织（organizations.id）；NULL = 不属于任何组织（直接挂在租户下）';

-- ──────────────────────────────────────────────────────────
-- Part 3: org_credit_wallets（组织钱包）
-- ──────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS org_credit_wallets (
    org_id              INT PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    tenant_id           VARCHAR(64) NOT NULL,
    balance_credits     BIGINT NOT NULL DEFAULT 0,
    locked_credits      BIGINT NOT NULL DEFAULT 0,
    -- NULL = 不限制（沿用 budget_usd NULL 先例）
    credit_limit_credits BIGINT DEFAULT NULL,
    credit_policy       VARCHAR(8) NOT NULL DEFAULT 'warn',
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT org_credit_wallets_policy_check
        CHECK (credit_policy IN ('warn', 'block'))
);

CREATE INDEX IF NOT EXISTS idx_org_credit_wallets_tenant
    ON org_credit_wallets(tenant_id);

COMMENT ON COLUMN org_credit_wallets.credit_limit_credits IS
  '信用额度（积分）；NULL = 不限制（余额可为任意负值仍放行，仅由 policy 决定语义）';
COMMENT ON COLUMN org_credit_wallets.credit_policy IS
  '超额策略：warn=允许进入负债并告警 | block=余额<预估成本即阻断';

-- ──────────────────────────────────────────────────────────
-- Part 4: user_credit_wallets（用户钱包）
-- ──────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS user_credit_wallets (
    user_id             INT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    tenant_id           VARCHAR(64) NOT NULL,
    balance_credits     BIGINT NOT NULL DEFAULT 0,
    locked_credits      BIGINT NOT NULL DEFAULT 0,
    credit_limit_credits BIGINT DEFAULT NULL,     -- NULL = 不限制
    credit_policy       VARCHAR(8) NOT NULL DEFAULT 'warn',
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT user_credit_wallets_policy_check
        CHECK (credit_policy IN ('warn', 'block'))
);

CREATE INDEX IF NOT EXISTS idx_user_credit_wallets_tenant
    ON user_credit_wallets(tenant_id);

COMMENT ON COLUMN user_credit_wallets.credit_limit_credits IS
  '信用额度（积分）；NULL = 不限制';
COMMENT ON COLUMN user_credit_wallets.credit_policy IS
  '超额策略：warn=允许进入负债并告警 | block=余额<预估成本即阻断';

COMMIT;
