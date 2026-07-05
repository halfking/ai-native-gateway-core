-- Migration 321: 清理遗留的 in_progress 记录并添加自动清理功能
-- Created: 2026-06-30
-- Purpose: 修复历史遗留的 in_progress 状态记录，并创建定期清理函数

-- 1. 清理现有的遗留 in_progress 记录（超过 5 分钟未完成的）
UPDATE request_logs
SET 
    success = false,
    request_status = 'failure',
    error_kind = 'gateway_timeout',
    failure_stage = 'gateway',
    failure_detail_code = 'gw_processing_timeout'
WHERE 
    request_status = 'in_progress'
    AND ts < NOW() - INTERVAL '5 minutes'
    AND success = false;

-- 2. 创建自动清理函数
CREATE OR REPLACE FUNCTION cleanup_stale_in_progress_requests()
RETURNS TABLE(cleaned_count bigint) AS $$
DECLARE
    updated_rows bigint;
BEGIN
    -- 将超过 5 分钟仍为 in_progress 的记录标记为超时失败
    UPDATE request_logs
    SET 
        success = false,
        request_status = 'failure',
        error_kind = 'gateway_timeout',
        failure_stage = 'gateway',
        failure_detail_code = 'gw_processing_timeout'
    WHERE 
        request_status = 'in_progress'
        AND ts < NOW() - INTERVAL '5 minutes'
        AND success = false;
    
    GET DIAGNOSTICS updated_rows = ROW_COUNT;
    
    -- 记录清理操作
    IF updated_rows > 0 THEN
        RAISE NOTICE 'Cleaned up % stale in_progress request_logs records', updated_rows;
    END IF;
    
    RETURN QUERY SELECT updated_rows;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION cleanup_stale_in_progress_requests() IS
    '清理超过 5 分钟仍为 in_progress 状态的 request_logs 记录，标记为 gateway_timeout 失败。
    应该通过 pg_cron 或应用层定期调用（建议每 10 分钟一次）。';

-- 3. （可选）如果安装了 pg_cron 扩展，可以设置自动清理任务
-- 取消注释以下内容以启用自动清理：
/*
-- 检查 pg_cron 扩展是否存在
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_cron') THEN
        -- 每 10 分钟运行一次清理
        PERFORM cron.schedule(
            'cleanup-stale-requests',
            '*/10 * * * *',  -- 每 10 分钟
            'SELECT cleanup_stale_in_progress_requests();'
        );
        RAISE NOTICE 'pg_cron job scheduled: cleanup-stale-requests runs every 10 minutes';
    ELSE
        RAISE NOTICE 'pg_cron extension not installed. Manual cleanup required.';
    END IF;
END $$;
*/

-- 4. 手动清理命令（部署后立即执行一次）
SELECT cleanup_stale_in_progress_requests();
