-- ============================================================
-- 迁移 1000 回滚: 删除计费状态追踪和信用额度
-- 日期：2026-11-26
-- 警告：此操作会删除计费状态和信用额度相关的所有数据
-- ============================================================

-- Part 6: 删除触发器
DROP TRIGGER IF EXISTS trg_notify_negative_balance ON tenant_credit_wallets;
DROP FUNCTION IF EXISTS notify_negative_balance();

-- Part 4: 删除视图
DROP VIEW IF EXISTS v_tenants_in_debt;

-- Part 3: 删除预检日志表
DROP TABLE IF EXISTS billing_precheck_log;

-- Part 2: 删除信用额度字段
DROP INDEX IF EXISTS idx_tenant_credit_wallets_negative_balance;

ALTER TABLE tenant_credit_wallets 
  DROP COLUMN IF EXISTS credit_limit_credits,
  DROP COLUMN IF EXISTS credit_policy;

-- Part 1: 删除计费状态字段
DROP INDEX IF EXISTS idx_session_turns_billing_pending;
DROP INDEX IF EXISTS idx_session_turns_billing_failed;

ALTER TABLE session_turns 
  DROP COLUMN IF EXISTS billing_status,
  DROP COLUMN IF EXISTS billing_error,
  DROP COLUMN IF EXISTS billing_retry_count,
  DROP COLUMN IF EXISTS billing_policy_applied;

ALTER TABLE session_turns_hot 
  DROP COLUMN IF EXISTS billing_status,
  DROP COLUMN IF EXISTS billing_error,
  DROP COLUMN IF EXISTS billing_retry_count,
  DROP COLUMN IF EXISTS billing_policy_applied;

-- 验证回滚完成
DO $$
BEGIN
    RAISE NOTICE 'Migration 1000 rollback completed';
END $$;
