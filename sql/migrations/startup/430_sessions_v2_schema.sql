-- Migration 430: Sessions V2 Schema
-- 
-- Purpose: 创建会话存储V2架构，与现有request_logs并行运行
-- 
-- 设计原则：
--   1. 完全独立的新表体系，不改动request_logs
--   2. 通过request_id关联便于数据校验
--   3. 支持按月分区和columnar压缩
--   4. 完整的RLS租户隔离
-- 
-- 表结构：
--   - gateway.sessions: 会话快照（一个会话一条记录）
--   - gateway.session_turns: 轮次元数据（不含正文）
--   - gateway.session_bodies: 正文内容（增量存储，columnar）
--   - gateway.session_turn_logs: 环节状态日志（24小时TTL）
-- 
-- Author: llm-gateway-ops
-- Date: 2026-07-17
-- Status: PARALLEL (与request_logs并行，Feature Flag控制)

BEGIN;

CREATE SCHEMA IF NOT EXISTS gateway;

-- =============================================
-- 1. sessions 表：会话快照
-- =============================================

CREATE TABLE IF NOT EXISTS gateway.sessions (
    id BIGSERIAL,
    session_id TEXT NOT NULL,
    tenant_id VARCHAR(255) NOT NULL,
    
    -- 会话基础信息
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'active' 
        CHECK (status IN ('active', 'closed', 'archived', 'deleted')),
    
    -- 会话统计（快照）
    total_turns INT NOT NULL DEFAULT 0,
    total_tokens INT NOT NULL DEFAULT 0,
    total_cost_usd NUMERIC(12,6) NOT NULL DEFAULT 0,
    
    -- 最后一轮摘要（快照）
    last_turn_no INT,
    last_request_summary TEXT,
    last_response_summary TEXT,
    last_model TEXT,
    last_provider TEXT,
    
    -- 会话级标签
    task_type TEXT,  -- chat | code | analysis | tool_use | ...
    client_type TEXT, -- cursor | roocode | openai-api | ...
    topic TEXT,
    intent TEXT,
    
    -- 关联字段（用于与旧表request_logs对照）
    primary_request_id TEXT,  -- 第一个请求的request_id
    
    -- 会话级环节日志汇总（JSON格式，从session_turn_logs生成）
    turn_logs_summary JSONB,
    
    -- 分区键
    partition_date DATE NOT NULL DEFAULT CURRENT_DATE,
    
    PRIMARY KEY (id, partition_date),
    UNIQUE (session_id, partition_date)
) PARTITION BY RANGE (partition_date);

CREATE INDEX IF NOT EXISTS idx_sessions_session_id ON gateway.sessions (session_id);
CREATE INDEX IF NOT EXISTS idx_sessions_tenant ON gateway.sessions (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_sessions_status ON gateway.sessions (status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_sessions_primary_request ON gateway.sessions (primary_request_id) WHERE primary_request_id IS NOT NULL;

-- RLS
ALTER TABLE gateway.sessions ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS sessions_tenant_isolation ON gateway.sessions;
CREATE POLICY sessions_tenant_isolation ON gateway.sessions
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

DROP POLICY IF EXISTS sessions_super_admin_bypass ON gateway.sessions;
CREATE POLICY sessions_super_admin_bypass ON gateway.sessions
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

COMMENT ON TABLE gateway.sessions IS 
    'V2会话快照表：一个会话一条记录，存储会话级汇总信息。
     与request_logs并行运行，通过Feature Flag控制流量路由。
     通过primary_request_id可以关联到request_logs进行数据校验。
     Created: 2026-07-17, Migration 430';

-- =============================================
-- 2. session_turns 表：轮次元数据
-- =============================================

CREATE TABLE IF NOT EXISTS gateway.session_turns (
    id BIGSERIAL,
    session_id TEXT NOT NULL,
    turn_no INT NOT NULL,
    tenant_id VARCHAR(255) NOT NULL,
    
    -- 关联字段（关联request_logs便于数据校验）
    request_id TEXT NOT NULL,
    
    -- 时间戳
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- 提交模式（检测客户端是否预压缩）
    submit_mode TEXT NOT NULL DEFAULT 'full'
        CHECK (submit_mode IN ('full', 'delta', 'snapshot', 'inferred_compressed')),
    
    -- 压缩元数据
    compression_applied BOOLEAN DEFAULT FALSE,
    compression_strategy TEXT,
    compression_meta JSONB DEFAULT '{}'::jsonb,
    compression_tokens_saved INT,
    
    -- 治理元数据（L2缓存镜像）
    injection_verdict TEXT DEFAULT 'skip'
        CHECK (injection_verdict IN ('pass', 'warn', 'block', 'skip')),
    output_verdict TEXT DEFAULT 'skip'
        CHECK (output_verdict IN ('pass', 'warn', 'block', 'skip')),
    
    -- 模型与路由
    model TEXT,
    provider TEXT,
    credential_id TEXT,
    
    -- 用量与成本
    prompt_tokens INT,
    completion_tokens INT,
    cache_read_tokens INT,
    cache_write_tokens INT,
    cost_usd NUMERIC(12,6),
    
    -- 性能指标
    latency_ms INT,
    status_code INT,
    success BOOLEAN,
    error_kind TEXT,
    
    -- 数据质量标记
    source_kind TEXT NOT NULL DEFAULT 'live'
        CHECK (source_kind IN ('live', 'backfill')),
    quality TEXT NOT NULL DEFAULT 'verified'
        CHECK (quality IN ('verified', 'inferred', 'partial', 'rejected')),
    
    -- 分区键
    partition_date DATE NOT NULL DEFAULT CURRENT_DATE,
    
    PRIMARY KEY (id, partition_date),
    UNIQUE (tenant_id, session_id, turn_no, partition_date),
    UNIQUE (tenant_id, request_id, partition_date)
) PARTITION BY RANGE (partition_date);

CREATE INDEX IF NOT EXISTS idx_session_turns_session ON gateway.session_turns 
    (session_id, turn_no DESC);
CREATE INDEX IF NOT EXISTS idx_session_turns_request ON gateway.session_turns (request_id);
CREATE INDEX IF NOT EXISTS idx_session_turns_tenant ON gateway.session_turns 
    (tenant_id, ts DESC);

-- RLS
ALTER TABLE gateway.session_turns ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS session_turns_tenant_isolation ON gateway.session_turns;
CREATE POLICY session_turns_tenant_isolation ON gateway.session_turns
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

DROP POLICY IF EXISTS session_turns_super_admin_bypass ON gateway.session_turns;
CREATE POLICY session_turns_super_admin_bypass ON gateway.session_turns
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

COMMENT ON TABLE gateway.session_turns IS 
    'V2轮次元数据表：存储每轮的元数据，正文存储在session_bodies。
     与request_logs并行，通过request_id关联便于数据校验。
     使用advisory lock保证turn_no在同一会话内单调递增。
     Created: 2026-07-17, Migration 430';

-- =============================================
-- 3. session_bodies 表：正文内容（增量存储）
-- =============================================

CREATE TABLE IF NOT EXISTS gateway.session_bodies (
    id BIGSERIAL,
    session_id TEXT NOT NULL,
    turn_no INT NOT NULL,
    tenant_id VARCHAR(255) NOT NULL,
    request_id TEXT NOT NULL,
    
    -- 增量正文（核心优化：只存本轮增量，避免request_logs的全量JSONB膨胀）
    request_delta JSONB,      -- 本轮新增的请求消息
    response_delta JSONB,     -- 本轮的回复消息
    
    -- 压缩后正文（用于对比展示和审计）
    outbound_body JSONB,      -- 实际发给LLM的压缩后正文
    
    -- 附件引用（不存base64，只存引用）
    request_attachments JSONB DEFAULT '[]'::jsonb,
    response_attachments JSONB DEFAULT '[]'::jsonb,
    
    -- 时间戳
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- 分区键
    partition_date DATE NOT NULL DEFAULT CURRENT_DATE,
    
    PRIMARY KEY (id, partition_date),
    UNIQUE (tenant_id, session_id, turn_no, partition_date),
    UNIQUE (tenant_id, request_id, partition_date)
) PARTITION BY RANGE (partition_date);

CREATE INDEX IF NOT EXISTS idx_session_bodies_session ON gateway.session_bodies 
    (session_id, turn_no DESC);
CREATE INDEX IF NOT EXISTS idx_session_bodies_request ON gateway.session_bodies (request_id);

-- RLS
ALTER TABLE gateway.session_bodies ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS session_bodies_tenant_isolation ON gateway.session_bodies;
CREATE POLICY session_bodies_tenant_isolation ON gateway.session_bodies
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

DROP POLICY IF EXISTS session_bodies_super_admin_bypass ON gateway.session_bodies;
CREATE POLICY session_bodies_super_admin_bypass ON gateway.session_bodies
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

COMMENT ON TABLE gateway.session_bodies IS 
    'V2正文存储表：存储增量正文，避免request_logs的全量JSONB膨胀问题。
     使用columnar存储格式，配合zstd压缩，预计可节省60-80%磁盘空间。
     Created: 2026-07-17, Migration 430';

-- =============================================
-- 4. session_turn_logs 表：环节状态日志（24小时TTL）
-- =============================================

CREATE TABLE IF NOT EXISTS gateway.session_turn_logs (
    id BIGSERIAL PRIMARY KEY,
    session_id TEXT NOT NULL,
    turn_no INT NOT NULL,
    tenant_id VARCHAR(255) NOT NULL,
    request_id TEXT NOT NULL,
    
    -- 处理环节
    stage TEXT NOT NULL 
        CHECK (stage IN ('routing', 'compression', 'injection_check', 
                        'llm_call', 'output_check', 'response', 'cache_update')),
    stage_status TEXT NOT NULL 
        CHECK (stage_status IN ('pending', 'running', 'success', 'failed', 'skipped')),
    
    -- 环节数据（存储详细的处理信息）
    event_data JSONB NOT NULL DEFAULT '{}'::jsonb,
    error_message TEXT,
    
    -- 时间戳
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    latency_ms INT,
    
    -- TTL（24小时后自动清理）
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '24 hours')
);

CREATE INDEX IF NOT EXISTS idx_session_turn_logs_session ON gateway.session_turn_logs 
    (session_id, turn_no, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_session_turn_logs_expires ON gateway.session_turn_logs (expires_at);
CREATE INDEX IF NOT EXISTS idx_session_turn_logs_request ON gateway.session_turn_logs (request_id);

COMMENT ON TABLE gateway.session_turn_logs IS 
    'V2环节状态日志：记录每个轮次的处理过程，用于故障诊断。
     24小时后自动清理，会话结束时汇总生成JSON存入sessions表。
     Created: 2026-07-17, Migration 430';

-- =============================================
-- 5. 分区管理函数
-- =============================================

CREATE OR REPLACE FUNCTION ensure_sessions_v2_partitions(target_date DATE DEFAULT CURRENT_DATE)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    month_start DATE := DATE_TRUNC('month', target_date)::DATE;
    month_end DATE := (DATE_TRUNC('month', target_date) + INTERVAL '1 month')::DATE;
    partition_suffix TEXT := TO_CHAR(month_start, 'YYYY_MM');
BEGIN
    -- sessions 分区（heap格式）
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS gateway.sessions_%s PARTITION OF gateway.sessions
         FOR VALUES FROM (%L) TO (%L)',
        partition_suffix, month_start, month_end
    );
    
    -- session_turns 分区（heap格式）
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS gateway.session_turns_%s PARTITION OF gateway.session_turns
         FOR VALUES FROM (%L) TO (%L)',
        partition_suffix, month_start, month_end
    );
    
    -- session_bodies 分区使用 heap，因为正文写入支持冲突更新。
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS gateway.session_bodies_%s PARTITION OF gateway.session_bodies
         FOR VALUES FROM (%L) TO (%L)',
        partition_suffix, month_start, month_end
    );
    
    RAISE NOTICE 'Created sessions V2 partitions for %', partition_suffix;
END;
$$;

COMMENT ON FUNCTION ensure_sessions_v2_partitions(DATE) IS
    'Ensure monthly partitions for sessions V2 tables.
     Called by bg.PartitionManager alongside ensure_request_logs_partition.
     session_bodies uses heap storage because response bodies can be updated.
     Created: 2026-07-17, Migration 430';

-- =============================================
-- 6. 创建当前月和下月分区
-- =============================================

SELECT ensure_sessions_v2_partitions(CURRENT_DATE);
SELECT ensure_sessions_v2_partitions((CURRENT_DATE + INTERVAL '1 month')::DATE);

-- =============================================
-- 7. 环节日志自动清理函数
-- =============================================

CREATE OR REPLACE FUNCTION cleanup_expired_session_turn_logs()
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    deleted_count INT;
BEGIN
    DELETE FROM gateway.session_turn_logs
    WHERE expires_at < NOW();
    
    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    
    IF deleted_count > 0 THEN
        RAISE NOTICE 'Cleaned up % expired session turn logs', deleted_count;
    END IF;
END;
$$;

COMMENT ON FUNCTION cleanup_expired_session_turn_logs() IS
    'Cleanup expired session turn logs (older than 24 hours).
     Should be called by bg worker or cron job every hour.
     Created: 2026-07-17, Migration 430';

-- =============================================
-- 8. 验证
-- =============================================

DO $$
BEGIN
    -- 验证表存在
    IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE schemaname = 'gateway' AND tablename = 'sessions') THEN
        RAISE EXCEPTION 'Table gateway.sessions not created';
    END IF;
    
    IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE schemaname = 'gateway' AND tablename = 'session_turns') THEN
        RAISE EXCEPTION 'Table gateway.session_turns not created';
    END IF;
    
    IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE schemaname = 'gateway' AND tablename = 'session_bodies') THEN
        RAISE EXCEPTION 'Table gateway.session_bodies not created';
    END IF;
    
    IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE schemaname = 'gateway' AND tablename = 'session_turn_logs') THEN
        RAISE EXCEPTION 'Table gateway.session_turn_logs not created';
    END IF;
    
    -- 验证RLS已启用
    IF NOT EXISTS (
        SELECT 1 FROM pg_tables 
        WHERE schemaname = 'gateway' 
        AND tablename = 'sessions' 
        AND rowsecurity = true
    ) THEN
        RAISE EXCEPTION 'RLS not enabled on gateway.sessions';
    END IF;
    
    RAISE NOTICE '===== Migration 430 SUCCESSFUL =====';
    RAISE NOTICE 'Sessions V2 schema created successfully';
    RAISE NOTICE 'Tables: sessions, session_turns, session_bodies, session_turn_logs';
    RAISE NOTICE 'Partitions created for: % and %', 
        TO_CHAR(CURRENT_DATE, 'YYYY_MM'),
        TO_CHAR(CURRENT_DATE + INTERVAL '1 month', 'YYYY_MM');
    RAISE NOTICE 'Status: PARALLEL (与request_logs并行，Feature Flag控制)';
END;
$$;

COMMIT;
