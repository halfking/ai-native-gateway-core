-- V369: Auto模型优化V2 - Phase 1: 数据架构准备
-- 目标：扩展request_logs表以支持入口/出口请求分离
-- 日期：2026-09-02
-- 作者：ZCode AI Assistant

-- 1. 增加请求类型相关字段
ALTER TABLE request_logs 
    ADD COLUMN IF NOT EXISTS request_type TEXT DEFAULT 'outbound',
    ADD COLUMN IF NOT EXISTS parent_request_id TEXT,
    ADD COLUMN IF NOT EXISTS request_depth INT DEFAULT 0,
    ADD COLUMN IF NOT EXISTS is_terminal BOOLEAN DEFAULT FALSE;

-- 2. 添加字段注释
COMMENT ON COLUMN request_logs.request_type IS '请求类型: client（客户端入口请求）或 outbound（发送给LLM的出口请求）';
COMMENT ON COLUMN request_logs.parent_request_id IS '父请求ID，用于建立请求树关系。client类型为NULL，outbound类型指向其父请求';
COMMENT ON COLUMN request_logs.request_depth IS '请求深度：client=0, 直接子请求=1, 孙请求=2（用于子代理链追踪）';
COMMENT ON COLUMN request_logs.is_terminal IS '是否为终态请求（成功或最终失败的出口请求）。用于快速筛选有效响应';

-- 3. 创建索引以优化查询性能
-- 3.1 父子关系查询索引（查询某个client请求的所有子请求）
CREATE INDEX IF NOT EXISTS idx_request_logs_parent_request_id 
    ON request_logs(parent_request_id, ts DESC) 
    WHERE parent_request_id IS NOT NULL;

-- 3.2 按请求类型查询索引（总览页查询客户端请求数）
CREATE INDEX IF NOT EXISTS idx_request_logs_request_type 
    ON request_logs(request_type, tenant_id, ts DESC);

-- 3.3 客户端请求专用索引（快速统计客户端请求）
CREATE INDEX IF NOT EXISTS idx_request_logs_client_requests 
    ON request_logs(tenant_id, ts DESC) 
    WHERE request_type = 'client';

-- 3.4 终态请求索引（快速查询成功的出口请求）
CREATE INDEX IF NOT EXISTS idx_request_logs_terminal_outbound 
    ON request_logs(parent_request_id, is_terminal, ts DESC) 
    WHERE request_type = 'outbound' AND is_terminal = TRUE;

-- 4. 数据迁移：将现有记录标记为outbound类型
-- 注意：不需要显式更新，因为默认值已设置为'outbound'
-- 但为了明确性，我们更新一下已有记录的深度和终态标记
UPDATE request_logs 
SET 
    is_terminal = TRUE,  -- 现有记录都是已完成的请求
    request_depth = 0    -- 默认深度为0（没有父子关系）
WHERE 
    is_terminal IS NULL;  -- 只更新新增字段为NULL的记录

-- 5. 验证数据一致性
DO $$
DECLARE
    total_count BIGINT;
    outbound_count BIGINT;
BEGIN
    SELECT COUNT(*) INTO total_count FROM request_logs;
    SELECT COUNT(*) INTO outbound_count FROM request_logs WHERE request_type = 'outbound';
    
    RAISE NOTICE '迁移完成统计:';
    RAISE NOTICE '  总记录数: %', total_count;
    RAISE NOTICE '  outbound类型记录数: %', outbound_count;
    RAISE NOTICE '  client类型记录数: %', total_count - outbound_count;
END $$;
