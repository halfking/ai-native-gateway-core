-- Rollback migration 320: request_logs upstream diagnostics fields

DROP INDEX IF EXISTS idx_request_logs_stream_errors;
DROP INDEX IF EXISTS idx_request_logs_client_timeout;
DROP INDEX IF EXISTS idx_request_logs_upstream_status;

-- 注意：不删除列，因为可能有历史数据依赖这些字段
-- 如果需要完全回滚，手动执行：
-- ALTER TABLE request_logs 
--     DROP COLUMN IF EXISTS stream_chunks_sent,
--     DROP COLUMN IF EXISTS stream_chunk_errors,
--     DROP COLUMN IF EXISTS client_endpoint,
--     DROP COLUMN IF EXISTS client_timeout,
--     DROP COLUMN IF EXISTS upstream_status_code;
