-- Migration 134: Tool Execution Tracking
-- 工具执行追踪系统，记录所有工具调用的详细信息

-- 工具执行记录表
CREATE TABLE IF NOT EXISTS tool_executions (
    execution_id VARCHAR(64) PRIMARY KEY,
    session_id VARCHAR(64) NOT NULL,
    request_id VARCHAR(64) NOT NULL,
    tenant_id VARCHAR(64) NOT NULL,
    
    -- 工具信息
    tool_name VARCHAR(128) NOT NULL,
    tool_call_id VARCHAR(128),                           -- OpenAI: tool_call_id, Anthropic: tool_use.id
    arguments JSONB,                                     -- 工具调用参数
    result JSONB,                                        -- 工具执行结果
    
    -- 执行状态
    status VARCHAR(16) NOT NULL,                         -- pending, success, error, timeout
    error_message TEXT,
    error_type VARCHAR(32),                              -- network_error, timeout, invalid_args, execution_failed
    
    -- 时间统计
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    duration_ms BIGINT,
    
    -- 关联信息
    identity_hash VARCHAR(64),                           -- 客户端标识
    model VARCHAR(128),                                  -- 使用的模型
    
    -- 元数据
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 索引（多维度查询优化）
CREATE INDEX IF NOT EXISTS idx_tool_exec_session ON tool_executions(session_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_tool_exec_request ON tool_executions(request_id);
CREATE INDEX IF NOT EXISTS idx_tool_exec_tenant ON tool_executions(tenant_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_tool_exec_identity ON tool_executions(identity_hash, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_tool_exec_tool_name ON tool_executions(tool_name, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_tool_exec_status ON tool_executions(status, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_tool_exec_started_at ON tool_executions(started_at DESC);

-- 工具使用统计表（聚合视图，定期更新）
CREATE TABLE IF NOT EXISTS tool_usage_stats (
    id BIGSERIAL PRIMARY KEY,
    tool_name VARCHAR(128) NOT NULL,
    date DATE NOT NULL,
    
    -- 统计指标
    total_calls BIGINT DEFAULT 0,
    success_calls BIGINT DEFAULT 0,
    failed_calls BIGINT DEFAULT 0,
    timeout_calls BIGINT DEFAULT 0,
    avg_duration_ms FLOAT,
    p50_duration_ms BIGINT,
    p95_duration_ms BIGINT,
    p99_duration_ms BIGINT,
    
    -- 用户统计
    unique_users INT DEFAULT 0,                          -- 唯一identity_hash数量
    unique_sessions INT DEFAULT 0,                       -- 唯一session_id数量
    
    -- Top用户（JSON数组）
    top_users JSONB DEFAULT '[]'::jsonb,                 -- [{"identity_hash": "xxx", "call_count": 100}]
    
    -- 元数据
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    UNIQUE(tool_name, date)
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_tool_usage_stats_tool_date ON tool_usage_stats(tool_name, date DESC);
CREATE INDEX IF NOT EXISTS idx_tool_usage_stats_date ON tool_usage_stats(date DESC);

-- 自动计算执行时长
CREATE OR REPLACE FUNCTION calculate_tool_execution_duration()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.completed_at IS NOT NULL AND NEW.started_at IS NOT NULL THEN
        NEW.duration_ms = EXTRACT(EPOCH FROM (NEW.completed_at - NEW.started_at))::BIGINT * 1000;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_tool_executions_duration
    BEFORE INSERT OR UPDATE ON tool_executions
    FOR EACH ROW
    EXECUTE FUNCTION calculate_tool_execution_duration();

-- 自动更新 tool_usage_stats updated_at
CREATE OR REPLACE FUNCTION update_tool_usage_stats_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_tool_usage_stats_updated_at
    BEFORE UPDATE ON tool_usage_stats
    FOR EACH ROW
    EXECUTE FUNCTION update_tool_usage_stats_updated_at();

-- 注释
COMMENT ON TABLE tool_executions IS '工具执行记录表，跟踪所有LLM工具调用的详细信息';
COMMENT ON TABLE tool_usage_stats IS '工具使用统计聚合表，按天聚合工具调用指标';
COMMENT ON COLUMN tool_executions.tool_call_id IS 'LLM返回的工具调用ID（OpenAI: tool_call_id, Anthropic: tool_use.id）';
COMMENT ON COLUMN tool_executions.status IS '执行状态：pending, success, error, timeout';
COMMENT ON COLUMN tool_executions.duration_ms IS '执行时长（毫秒），自动从started_at和completed_at计算';
COMMENT ON COLUMN tool_usage_stats.top_users IS 'JSON数组，Top用户列表（按调用次数排序）';
