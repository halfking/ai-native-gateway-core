-- Migration 320: 确保 request_logs 表包含上游诊断字段
-- Created: 2026-06-30
-- Purpose: 同步手动添加的字段到迁移系统，确保字段存在并有正确的索引
-- Related: docs/fix-request-logs-missing-fields.md

-- 添加字段（如果不存在）
ALTER TABLE request_logs 
    ADD COLUMN IF NOT EXISTS upstream_status_code INT,
    ADD COLUMN IF NOT EXISTS client_timeout BOOLEAN,
    ADD COLUMN IF NOT EXISTS client_endpoint TEXT,
    ADD COLUMN IF NOT EXISTS stream_chunk_errors INT,
    ADD COLUMN IF NOT EXISTS stream_chunks_sent INT NOT NULL DEFAULT 0;

-- 为上游状态码添加索引，用于快速查询特定状态码的错误
CREATE INDEX IF NOT EXISTS idx_request_logs_upstream_status 
    ON request_logs (upstream_status_code, ts DESC) 
    WHERE upstream_status_code IS NOT NULL;

-- 为客户端超时添加索引
CREATE INDEX IF NOT EXISTS idx_request_logs_client_timeout 
    ON request_logs (client_timeout, ts DESC) 
    WHERE client_timeout = TRUE;

-- 为流错误添加索引
CREATE INDEX IF NOT EXISTS idx_request_logs_stream_errors 
    ON request_logs (stream_chunk_errors, ts DESC) 
    WHERE stream_chunk_errors IS NOT NULL AND stream_chunk_errors > 0;

-- 注释
COMMENT ON COLUMN request_logs.upstream_status_code IS 
    '上游 HTTP 状态码（从 upstream.Error.StatusCode 提取）。NULL 表示网关阶段错误或未到达上游。';

COMMENT ON COLUMN request_logs.client_timeout IS 
    '客户端超时标记。TRUE 表示客户端断开连接或超时，与服务端超时区分。';

COMMENT ON COLUMN request_logs.client_endpoint IS 
    '客户端请求的端点路径（如 /v1/chat/completions），用于区分不同 API 的错误模式。';

COMMENT ON COLUMN request_logs.stream_chunk_errors IS 
    '流式传输中发生的块级错误次数。用于诊断部分失败的流。';

COMMENT ON COLUMN request_logs.stream_chunks_sent IS 
    '成功发送的流块数量。与 stream_chunk_count 区分（后者是从响应提取的总块数）。';
