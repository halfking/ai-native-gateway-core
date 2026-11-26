-- ============================================================
-- 迁移 1000: 计费状态追踪 + 信用额度机制
-- 目的：修复 P0-1（计费失败不阻断请求）
-- 原则：余额不足双策略（block/warn）+ 信用额度
-- 日期：2026-11-26
-- 影响：session_turns, session_turns_hot, tenant_credit_wallets
-- ============================================================

-- ──────────────────────────────────────────────────────────
-- Part 1: session_turns 计费状态追踪
-- ──────────────────────────────────────────────────────────

-- 1.1 添加计费状态字段
ALTER TABLE session_turns 
  ADD COLUMN IF NOT EXISTS billing_status text DEFAULT 'charged',
  ADD COLUMN IF NOT EXISTS billing_error text,
  ADD COLUMN IF NOT EXISTS billing_retry_count int DEFAULT 0,
  ADD COLUMN IF NOT EXISTS billing_policy_applied text;

COMMENT ON COLUMN session_turns.billing_status IS 
  'charged: 计费成功 | pending_charge: 待补扣费 | failed: 计费失败（已超重试上限）| skipped: 跳过计费（免费额度等）';

COMMENT ON COLUMN session_turns.billing_error IS 
  '计费失败时的错误信息（用于调试和追踪）';

COMMENT ON COLUMN session_turns.billing_retry_count IS 
  '补扣费重试次数（0=首次尝试成功，>0=重试过）';

COMMENT ON COLUMN session_turns.billing_policy_applied IS 
  '记录本次请求应用的策略：block（阻断）/ warn（警告）/ skipped（跳过）';

-- 1.2 索引：快速查询待补扣费的请求
CREATE INDEX IF NOT EXISTS idx_session_turns_billing_pending 
  ON session_turns(billing_status, created_at) 
  WHERE billing_status = 'pending_charge';

-- 1.3 索引：快速查询计费失败的请求
CREATE INDEX IF NOT EXISTS idx_session_turns_billing_failed 
  ON session_turns(billing_status, created_at) 
  WHERE billing_status = 'failed';

-- 1.4 session_turns_hot 同步添加（热表）
ALTER TABLE session_turns_hot 
  ADD COLUMN IF NOT EXISTS billing_status text DEFAULT 'charged',
  ADD COLUMN IF NOT EXISTS billing_error text,
  ADD COLUMN IF NOT EXISTS billing_retry_count int DEFAULT 0,
  ADD COLUMN IF NOT EXISTS billing_policy_applied text;

-- ──────────────────────────────────────────────────────────
-- Part 2: 租户信用额度 + 策略配置
-- ──────────────────────────────────────────────────────────

-- 2.1 tenant_credit_wallets 添加信用额度字段
ALTER TABLE tenant_credit_wallets 
  ADD COLUMN IF NOT EXISTS credit_limit_credits bigint DEFAULT 1000,
  ADD COLUMN IF NOT EXISTS credit_policy text DEFAULT 'warn';

-- 2.2 添加约束（如果字段已存在则跳过）
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint 
    WHERE conname = 'tenant_credit_wallets_credit_policy_check'
  ) THEN
    ALTER TABLE tenant_credit_wallets 
      ADD CONSTRAINT tenant_credit_wallets_credit_policy_check 
      CHECK (credit_policy IN ('block', 'warn'));
  END IF;
END $$;

COMMENT ON COLUMN tenant_credit_wallets.credit_limit_credits IS 
  '信用额度（积分）。余额为负时，可透支到此额度内。默认1000积分。';

COMMENT ON COLUMN tenant_credit_wallets.credit_policy IS 
  'warn: 余额不足时警告但允许使用（进入负债）| block: 余额不足立即阻断（即使在信用额度内）';

-- 2.3 索引：快速查询负债租户
CREATE INDEX IF NOT EXISTS idx_tenant_credit_wallets_negative_balance 
  ON tenant_credit_wallets(balance_credits) 
  WHERE balance_credits < 0;

-- ──────────────────────────────────────────────────────────
-- Part 3: 计费预检日志表（用于审计和分析）
-- ──────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS billing_precheck_log (
    id bigserial PRIMARY KEY,
    request_id text NOT NULL,
    tenant_id text NOT NULL,
    balance_before_credits bigint NOT NULL,
    credit_limit_credits bigint NOT NULL,
    estimated_cost_credits bigint NOT NULL,
    policy text NOT NULL,
    decision text NOT NULL,
    reason text,
    created_at timestamptz DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_billing_precheck_log_tenant 
  ON billing_precheck_log(tenant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_billing_precheck_log_decision 
  ON billing_precheck_log(decision, created_at DESC) 
  WHERE decision = 'reject';

COMMENT ON TABLE billing_precheck_log IS 
  '计费预检日志：记录每次请求的余额检查结果，用于审计和分析';

COMMENT ON COLUMN billing_precheck_log.policy IS 
  '应用的策略：block（阻断）/ warn（警告）/ skipped（跳过）';

COMMENT ON COLUMN billing_precheck_log.decision IS 
  '预检决策：allow（允许）/ reject（拒绝）';

-- ──────────────────────────────────────────────────────────
-- Part 4: 视图：负债租户监控
-- ──────────────────────────────────────────────────────────

CREATE OR REPLACE VIEW v_tenants_in_debt AS
SELECT 
    w.tenant_id,
    w.balance_credits,
    w.credit_limit_credits,
    w.balance_credits + w.credit_limit_credits AS available_credits,
    w.credit_policy,
    CASE 
        WHEN w.balance_credits + w.credit_limit_credits < 0 THEN 'over_limit'
        WHEN w.balance_credits < 0 THEN 'in_debt'
        ELSE 'healthy'
    END AS debt_status,
    COUNT(st.id) FILTER (WHERE st.created_at > NOW() - INTERVAL '24 hours') AS requests_last_24h,
    SUM(st.credits_charged) FILTER (WHERE st.created_at > NOW() - INTERVAL '24 hours') AS credits_used_24h
FROM tenant_credit_wallets w
LEFT JOIN session_turns st ON st.tenant_id = w.tenant_id
WHERE w.balance_credits < 0
GROUP BY w.tenant_id, w.balance_credits, w.credit_limit_credits, w.credit_policy
ORDER BY w.balance_credits ASC;

COMMENT ON VIEW v_tenants_in_debt IS 
  '负债租户监控：显示所有余额为负的租户及其负债状况';

-- ──────────────────────────────────────────────────────────
-- Part 5: 默认值设置（为现有租户补齐字段）
-- ──────────────────────────────────────────────────────────

-- 5.1 为现有租户设置默认信用额度和策略
-- 注意：UPDATE会锁行，但tenant_credit_wallets表通常不大
UPDATE tenant_credit_wallets 
SET credit_limit_credits = COALESCE(credit_limit_credits, 1000),
    credit_policy = COALESCE(credit_policy, 'warn')
WHERE credit_limit_credits IS NULL 
   OR credit_policy IS NULL;

-- 5.2 为近30天历史数据回填 billing_status（用于统计分析）
-- 策略：有 credits_charged 的记为 'charged'，否则根据 cost_usd 判断
-- 注意：这是批量更新，在大表上可能较慢，可考虑分批执行
UPDATE session_turns 
SET billing_status = CASE 
    WHEN credits_charged IS NOT NULL AND credits_charged > 0 THEN 'charged'
    WHEN cost_usd > 0 AND credits_charged IS NULL THEN 'pending_charge'
    WHEN cost_usd IS NULL OR cost_usd = 0 THEN 'skipped'
    ELSE 'charged'
END
WHERE billing_status IS NULL 
  AND created_at > NOW() - INTERVAL '30 days';

-- ──────────────────────────────────────────────────────────
-- Part 6: 触发器：余额变为负数时自动记录日志
-- ──────────────────────────────────────────────────────────

CREATE OR REPLACE FUNCTION notify_negative_balance() 
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.balance_credits < 0 AND OLD.balance_credits >= 0 THEN
        -- 记录日志（使用PostgreSQL的RAISE NOTICE）
        RAISE NOTICE 'Tenant % balance went negative: % credits (credit_limit: %)', 
                     NEW.tenant_id, NEW.balance_credits, NEW.credit_limit_credits;
        
        -- 可选：插入到专门的告警表（如果有）
        -- INSERT INTO billing_alerts (tenant_id, alert_type, message, created_at)
        -- VALUES (NEW.tenant_id, 'negative_balance', 
        --         format('Balance went negative: %s credits', NEW.balance_credits), 
        --         NOW());
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_notify_negative_balance ON tenant_credit_wallets;

CREATE TRIGGER trg_notify_negative_balance
AFTER UPDATE OF balance_credits ON tenant_credit_wallets
FOR EACH ROW
WHEN (NEW.balance_credits < 0 AND OLD.balance_credits >= 0)
EXECUTE FUNCTION notify_negative_balance();

COMMENT ON TRIGGER trg_notify_negative_balance ON tenant_credit_wallets IS 
  '触发器：当租户余额首次变为负数时记录日志（用于监控告警）';

-- ──────────────────────────────────────────────────────────
-- Part 7: 验证和总结
-- ──────────────────────────────────────────────────────────

-- 7.1 验证字段已添加
DO $$
DECLARE
    missing_columns text[];
BEGIN
    -- 检查 session_turns
    SELECT ARRAY_AGG(col) INTO missing_columns
    FROM (VALUES 
        ('billing_status'),
        ('billing_error'),
        ('billing_retry_count'),
        ('billing_policy_applied')
    ) AS expected(col)
    WHERE NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'session_turns' 
          AND column_name = expected.col
    );
    
    IF array_length(missing_columns, 1) > 0 THEN
        RAISE EXCEPTION 'Migration 1000 incomplete: session_turns missing columns: %', 
                        array_to_string(missing_columns, ', ');
    END IF;
    
    -- 检查 tenant_credit_wallets
    SELECT ARRAY_AGG(col) INTO missing_columns
    FROM (VALUES 
        ('credit_limit_credits'),
        ('credit_policy')
    ) AS expected(col)
    WHERE NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'tenant_credit_wallets' 
          AND column_name = expected.col
    );
    
    IF array_length(missing_columns, 1) > 0 THEN
        RAISE EXCEPTION 'Migration 1000 incomplete: tenant_credit_wallets missing columns: %', 
                        array_to_string(missing_columns, ', ');
    END IF;
    
    RAISE NOTICE 'Migration 1000 completed successfully';
END $$;

-- 7.2 生成统计报告
SELECT 
    '=== Migration 1000 Statistics ===' AS report
UNION ALL
SELECT format('session_turns: %s rows with billing_status added', COUNT(*))
FROM session_turns
WHERE billing_status IS NOT NULL
UNION ALL
SELECT format('tenant_credit_wallets: %s tenants with credit_limit configured', COUNT(*))
FROM tenant_credit_wallets
WHERE credit_limit_credits IS NOT NULL
UNION ALL
SELECT format('Tenants currently in debt: %s', COUNT(*))
FROM tenant_credit_wallets
WHERE balance_credits < 0
UNION ALL
SELECT format('Tenants over credit limit: %s', COUNT(*))
FROM tenant_credit_wallets
WHERE balance_credits + credit_limit_credits < 0;
