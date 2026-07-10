-- 快速创建 request_logs_hot 表（简化版，仅用于测试）
-- 该脚本仅创建基本结构，足以让服务启动

BEGIN;

-- 创建基础 request_logs_hot 表
CREATE TABLE IF NOT EXISTS request_logs_hot (
    id BIGSERIAL PRIMARY KEY,
    request_id TEXT NOT NULL,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    tenant_id TEXT,
    session_id TEXT,
    client_model TEXT,
    provider TEXT,
    upstream_model TEXT,
    status_code INTEGER,
    error_message TEXT,
    latency_ms INTEGER,
    prompt_tokens INTEGER,
    completion_tokens INTEGER,
    total_tokens INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 基础索引
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_ts ON request_logs_hot (ts DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_tenant ON request_logs_hot (tenant_id, ts DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_request_logs_hot_request_id ON request_logs_hot (request_id, ts);

COMMENT ON TABLE request_logs_hot IS 'Hot request logs table (simplified for testing)';

COMMIT;
