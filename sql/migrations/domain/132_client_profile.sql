-- Migration 132: Client Profile System
-- 客户端画像系统，用于跨会话聚合客户端行为分析

-- 客户端画像主表
CREATE TABLE IF NOT EXISTS client_profiles (
    identity_hash VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    virtual_client_id VARCHAR(32) NOT NULL,
    
    -- 统计数据
    total_sessions BIGINT DEFAULT 0 NOT NULL,
    total_requests BIGINT DEFAULT 0 NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    
    -- 行为特征（JSONB存储复杂结构）
    preferred_models JSONB DEFAULT '[]'::jsonb,           -- [{"model": "gpt-4", "usage_count": 100, "success_rate": 0.95, "avg_latency_ms": 1200}]
    task_distribution JSONB DEFAULT '{}'::jsonb,          -- {"code": 50, "chat": 30, "reasoning": 20}
    avg_session_length FLOAT DEFAULT 0 NOT NULL,          -- 平均会话轮次
    avg_tokens_per_turn FLOAT DEFAULT 0 NOT NULL,         -- 平均每轮Token数
    
    -- 质量指标
    error_rate FLOAT DEFAULT 0 NOT NULL,                  -- 错误率 (0-1)
    approval_rate FLOAT DEFAULT 0 NOT NULL,               -- 高风险审批占比 (0-1)
    
    -- 时间模式
    active_hours JSONB DEFAULT '[]'::jsonb,               -- [10, 14, 15, 16] (UTC小时，0-23)
    peak_usage_day INT,                                   -- 0=Sunday, 6=Saturday
    
    -- 元数据
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_client_profiles_tenant ON client_profiles(tenant_id);
CREATE INDEX IF NOT EXISTS idx_client_profiles_last_seen ON client_profiles(last_seen_at DESC);
CREATE INDEX IF NOT EXISTS idx_client_profiles_tenant_last_seen ON client_profiles(tenant_id, last_seen_at DESC);

-- 客户端行为事件表（时序数据，用于分析）
CREATE TABLE IF NOT EXISTS client_behavior_events (
    event_id VARCHAR(64) PRIMARY KEY,
    identity_hash VARCHAR(64) NOT NULL,
    tenant_id VARCHAR(64) NOT NULL,
    session_id VARCHAR(64),
    request_id VARCHAR(64),
    
    -- 事件详情
    event_type VARCHAR(32) NOT NULL,                     -- session_start, request_completed, approval_required, error
    model VARCHAR(128),
    task_type VARCHAR(32),                               -- code, chat, reasoning, unknown
    
    -- 指标
    tokens_used INT,
    latency_ms BIGINT,
    success BOOLEAN DEFAULT true,
    
    -- 时间戳
    event_time TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 索引（时序查询优化）
CREATE INDEX IF NOT EXISTS idx_client_behavior_identity_time ON client_behavior_events(identity_hash, event_time DESC);
CREATE INDEX IF NOT EXISTS idx_client_behavior_tenant_time ON client_behavior_events(tenant_id, event_time DESC);
CREATE INDEX IF NOT EXISTS idx_client_behavior_event_type ON client_behavior_events(event_type, event_time DESC);
CREATE INDEX IF NOT EXISTS idx_client_behavior_session ON client_behavior_events(session_id);

-- 自动更新 updated_at 触发器
CREATE OR REPLACE FUNCTION update_client_profile_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_client_profiles_updated_at
    BEFORE UPDATE ON client_profiles
    FOR EACH ROW
    EXECUTE FUNCTION update_client_profile_updated_at();

-- 注释
COMMENT ON TABLE client_profiles IS '客户端画像主表，跨会话聚合客户端行为特征';
COMMENT ON TABLE client_behavior_events IS '客户端行为事件时序表，用于画像分析和趋势追踪';
COMMENT ON COLUMN client_profiles.identity_hash IS '客户端稳定身份哈希（来自identity.ClientIdentity）';
COMMENT ON COLUMN client_profiles.preferred_models IS 'JSON数组，按使用频次排序的模型偏好';
COMMENT ON COLUMN client_profiles.task_distribution IS 'JSON对象，任务类型分布统计';
COMMENT ON COLUMN client_profiles.active_hours IS 'JSON数组，活跃时段（UTC小时0-23）';
