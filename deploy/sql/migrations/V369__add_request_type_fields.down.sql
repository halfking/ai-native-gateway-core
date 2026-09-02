-- V369 回滚脚本: 移除请求类型相关字段
-- 日期：2026-09-02

-- 1. 删除索引
DROP INDEX IF EXISTS idx_request_logs_terminal_outbound;
DROP INDEX IF EXISTS idx_request_logs_client_requests;
DROP INDEX IF EXISTS idx_request_logs_request_type;
DROP INDEX IF EXISTS idx_request_logs_parent_request_id;

-- 2. 删除字段
ALTER TABLE request_logs 
    DROP COLUMN IF EXISTS is_terminal,
    DROP COLUMN IF EXISTS request_depth,
    DROP COLUMN IF EXISTS parent_request_id,
    DROP COLUMN IF EXISTS request_type;

-- 3. 确认回滚
DO $$
BEGIN
    RAISE NOTICE 'V369迁移已回滚：请求类型字段和索引已删除';
END $$;
