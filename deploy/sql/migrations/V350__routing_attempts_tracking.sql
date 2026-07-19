-- ===========================================================================
-- File:          deploy/sql/migrations/V350__routing_attempts_tracking.sql
-- Database:      llm_gateway
-- Purpose:       增加路由尝试追踪字段，记录每次 upstream 尝试详情
-- Status:        active
-- Idempotent:    YES (使用 IF NOT EXISTS)
--
-- Changelog:
--   2026-07-19  v1.0  Initial creation - 解决用户困惑"为什么泳道显示火山方舟但URL是NVIDIA"
-- ===========================================================================
--
-- Background:
--   系统有路由回退机制，会尝试多个候选供应商。但目前只记录最终成功/失败的
--   provider_id 到 request_logs_hot，中间尝试的详情丢失。
--
--   案例: f50149e5cbc576295141145ba05f7ef2
--   - 计划了 5 个候选（包括 NVIDIA provider_id=18）
--   - 实际尝试了多个
--   - 最终记录 provider_id=35 (火山方舟)
--   - 用户看到 NVIDIA URL 但不知道为什么
--
-- Solution:
--   新增两个字段：
--   1. routing_attempts (JSONB): 结构化记录每次尝试
--   2. routing_summary (TEXT): 人类可读摘要
--
-- Execution:
--   psql -h "$DB_HOST" -U llm_gateway -d llm_gateway \
--     -f deploy/sql/migrations/V350__routing_attempts_tracking.sql
--
-- Verification:
--   SELECT column_name, data_type 
--   FROM information_schema.columns 
--   WHERE table_name='request_logs_hot' 
--     AND column_name IN ('routing_attempts', 'routing_summary');
--
-- Rollback:
--   ALTER TABLE request_logs_hot DROP COLUMN IF EXISTS routing_attempts;
--   ALTER TABLE request_logs_hot DROP COLUMN IF EXISTS routing_summary;
--   ALTER TABLE request_logs DROP COLUMN IF EXISTS routing_attempts;
--   ALTER TABLE request_logs DROP COLUMN IF EXISTS routing_summary;
-- ===========================================================================

\set ON_ERROR_STOP on

-- 1. 增加字段到 request_logs_hot (heap 存储，支持高频写入)
ALTER TABLE request_logs_hot 
  ADD COLUMN IF NOT EXISTS routing_attempts jsonb,
  ADD COLUMN IF NOT EXISTS routing_summary text;

-- 2. 增加字段到 request_logs 父表 (供未来分区继承)
ALTER TABLE request_logs 
  ADD COLUMN IF NOT EXISTS routing_attempts jsonb,
  ADD COLUMN IF NOT EXISTS routing_summary text;

-- 3. 创建 GIN 索引 (可选，用于查询特定 provider 的失败尝试)
--    例如: WHERE routing_attempts @> '{"attempts": [{"provider_id": 18}]}'
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_routing_attempts 
  ON request_logs_hot USING gin (routing_attempts)
  WHERE routing_attempts IS NOT NULL;

-- 4. 添加注释
COMMENT ON COLUMN request_logs_hot.routing_attempts IS 
  '路由尝试序列（JSONB 数组），记录每个候选供应商的尝试结果。
   结构: {"attempts": [{"seq": 1, "provider_id": 35, "provider_name": "火山方舟", 
   "credential_id": 12, "raw_model": "glm-5.2", 
   "upstream_url": "https://...", "result": "model_not_found", 
   "latency_ms": 1523, "http_status": 404, "error_message": "..."}]}
   仅在多次尝试或失败时才写入，首次成功时为 NULL 以节省空间。';

COMMENT ON COLUMN request_logs_hot.routing_summary IS 
  '路由尝试人类可读摘要，格式: 候选1(结果 耗时) → 候选2(结果 耗时) → ...
   例如: "候选1: 火山方舟(35) 模型未找到 1.5s → 候选2: NVIDIA(18) 取消 120s"
   便于快速浏览，不需要解析 JSONB。';

COMMENT ON COLUMN request_logs.routing_attempts IS 
  '路由尝试序列（JSONB 数组），记录每个候选供应商的尝试结果。
   继承到所有月度分区。详见 request_logs_hot 的注释。';

COMMENT ON COLUMN request_logs.routing_summary IS 
  '路由尝试人类可读摘要。继承到所有月度分区。详见 request_logs_hot 的注释。';

-- 验证
DO $$
DECLARE
    hot_attempts_exists boolean;
    hot_summary_exists boolean;
    parent_attempts_exists boolean;
    parent_summary_exists boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name='request_logs_hot' AND column_name='routing_attempts'
    ) INTO hot_attempts_exists;
    
    SELECT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name='request_logs_hot' AND column_name='routing_summary'
    ) INTO hot_summary_exists;
    
    SELECT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name='request_logs' AND column_name='routing_attempts'
    ) INTO parent_attempts_exists;
    
    SELECT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name='request_logs' AND column_name='routing_summary'
    ) INTO parent_summary_exists;
    
    IF NOT (hot_attempts_exists AND hot_summary_exists AND 
            parent_attempts_exists AND parent_summary_exists) THEN
        RAISE EXCEPTION 'Migration V350 failed: columns not created';
    END IF;
    
    RAISE NOTICE 'Migration V350 completed successfully';
END $$;
