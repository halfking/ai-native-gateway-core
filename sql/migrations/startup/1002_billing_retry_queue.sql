-- ============================================================
-- 迁移 1002: 计费重试队列
-- 目的：P0-3阶段2 - 异步扣减失败自动重试
-- 日期：2026-11-26
-- 影响：新增 billing_retry_queue 表
-- ============================================================

-- 计费重试队列表
CREATE TABLE IF NOT EXISTS billing_retry_queue (
    id bigserial PRIMARY KEY,
    tenant_id text NOT NULL,
    owner_user text,
    request_id text NOT NULL,
    credits bigint NOT NULL,
    retry_type text NOT NULL DEFAULT 'deduct_credits', -- deduct_credits / refund_credits
    attempts int NOT NULL DEFAULT 0,
    max_attempts int NOT NULL DEFAULT 10,
    last_error text,
    last_attempted_at timestamptz,
    next_retry_at timestamptz NOT NULL DEFAULT NOW(),
    created_at timestamptz NOT NULL DEFAULT NOW(),
    completed_at timestamptz,
    status text NOT NULL DEFAULT 'pending' -- pending / retrying / completed / failed
);

COMMENT ON TABLE billing_retry_queue IS 
  'P0-3阶段2：计费重试队列 - 异步扣减失败时自动重试';

COMMENT ON COLUMN billing_retry_queue.retry_type IS 
  '重试类型：deduct_credits（扣减积分）/ refund_credits（退还积分）';

COMMENT ON COLUMN billing_retry_queue.attempts IS 
  '已尝试次数（初始为0，每次重试后+1）';

COMMENT ON COLUMN billing_retry_queue.max_attempts IS 
  '最大重试次数（默认10次）';

COMMENT ON COLUMN billing_retry_queue.next_retry_at IS 
  '下次重试时间（指数退避：初始1分钟，每次失败翻倍，最多1小时）';

COMMENT ON COLUMN billing_retry_queue.status IS 
  '状态：pending（待重试）/ retrying（重试中）/ completed（已完成）/ failed（永久失败）';

-- 索引：快速查询待重试任务
CREATE INDEX IF NOT EXISTS idx_billing_retry_queue_pending 
  ON billing_retry_queue(next_retry_at, status) 
  WHERE status IN ('pending', 'retrying');

-- 索引：快速查询特定请求的重试记录
CREATE INDEX IF NOT EXISTS idx_billing_retry_queue_request 
  ON billing_retry_queue(request_id, status);

-- 索引：快速查询特定租户的重试记录
CREATE INDEX IF NOT EXISTS idx_billing_retry_queue_tenant 
  ON billing_retry_queue(tenant_id, created_at DESC);

-- 视图：重试统计（用于监控）
CREATE OR REPLACE VIEW v_billing_retry_stats AS
SELECT 
    DATE(created_at) AS date,
    COUNT(*) AS total_retries,
    COUNT(*) FILTER (WHERE status = 'pending') AS pending_count,
    COUNT(*) FILTER (WHERE status = 'retrying') AS retrying_count,
    COUNT(*) FILTER (WHERE status = 'completed') AS completed_count,
    COUNT(*) FILTER (WHERE status = 'failed') AS failed_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE status = 'completed') / COUNT(*), 2) AS success_rate,
    AVG(attempts) FILTER (WHERE status = 'completed') AS avg_attempts_on_success,
    MAX(attempts) AS max_attempts_seen
FROM billing_retry_queue
WHERE created_at > NOW() - INTERVAL '30 days'
GROUP BY DATE(created_at)
ORDER BY date DESC;

COMMENT ON VIEW v_billing_retry_stats IS 
  '计费重试统计：每日成功率、平均重试次数等指标';

-- 验证迁移成功
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.tables 
        WHERE table_name = 'billing_retry_queue'
    ) THEN
        RAISE EXCEPTION 'Migration 1002 failed: billing_retry_queue table not created';
    END IF;
    
    RAISE NOTICE 'Migration 1002 completed successfully';
END $$;

-- 生成统计报告
SELECT '=== Migration 1002 Statistics ===' AS report
UNION ALL
SELECT format('Billing retry queue table created: %s', 
    CASE WHEN EXISTS (
        SELECT 1 FROM information_schema.tables 
        WHERE table_name = 'billing_retry_queue'
    ) THEN 'YES' ELSE 'NO' END
);
