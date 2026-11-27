-- ============================================================
-- 迁移 1002: 计费重试队列
-- 目的：P0-3阶段2 - 异步扣减失败自动重试
-- 日期：2026-11-26
-- 影响：新增 billing_retry_queue 表
-- ============================================================

-- 锁守卫（installer 门 startup_ddl_lock_guard_gate_test.go：版本 > 870 且含强锁 DDL
--   ⇒ 必须自带 lock_timeout）：本件含 ALTER TABLE ＋ CREATE INDEX ⇒ 声明
--   5s lock_timeout / 10min statement_timeout（同 999a/999d 口径）。
-- ★ 实测（2026-11-27 部署轮，PG17.10 本机一次性容器 kx-citus-pg17:13.3.0）：★
--   首跑墙钟 0.055s、幂等重放 0.056s（全部 DDL 建在【本文件刚 CREATE 的新表】
--   billing_retry_queue 上 ⇒ 无存量可扫）
--   ⇒ 与 10min 预算差五个数量级，无 914 型「拍小了中止部署」风险。
SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;
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

-- ★ 2026-11-27 12h 审计轮（P2-A 兜底）：同请求在途唯一约束 —— 并发入队
--   （双网关同时 EnqueueCreditDeduction 同一 requestID）时，应用层的
--   check-then-insert 在无此约束下会插出两行在途任务。部分唯一索引只约束
--   在途行（pending/retrying），终态行（completed/failed）允许同请求历史
--   多条。maas/billing_retry_queue.go 的 INSERT 已配 ON CONFLICT 同谓词
--   幂等写入。建在本文件刚 CREATE 的新表上 ⇒ 无存量冲突风险。
CREATE UNIQUE INDEX IF NOT EXISTS uq_billing_retry_queue_inflight
  ON billing_retry_queue(request_id)
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


-- ★ 台账自登记（≥952 双账本纪律：迁移必须自己把行写进 public.schema_migrations，
--   否则部署通道应用后运行时台账无行 ⇒ 连续性门／漂移报告假红；492/633/983k 同款。
--   2026-11-28 L142 轮补登记：本族四位数件此前每轮部署幂等重执行但从不落账）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('1002', 'billing retry queue (billing P0)')
ON CONFLICT (version) DO NOTHING;

COMMIT;
