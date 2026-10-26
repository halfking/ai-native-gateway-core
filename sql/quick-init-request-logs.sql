-- V1-FAMILY-RETIRED: manual-only | 本脚本会建 v1 族的 request_logs_hot。
--   ★ v1 `request_logs` 族已随迁移 890 退役（物理删除）⇒ 本脚本**不在迁移链里**
--   ⇒ 它建出来的表**不会被 890 删掉** ⇒ 在已退役的库上跑它 = v1 族复活。
--   ★ 本脚本全仓**零引用**；其唯一目的就是造一张已退役的表。
--   ⇒ **只在 890 之前的遗留库上手跑**。
--   ⇒ 门：sql/migrations/startup/v1_family_retirement_completeness_test.go
--
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
    task_type TEXT,
    origin_stage TEXT,
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
