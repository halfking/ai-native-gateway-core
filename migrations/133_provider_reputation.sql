-- Migration 133: Provider Reputation System
-- LLM供应商信誉时序分析系统

-- 供应商信誉时序表（每日指标聚合）
CREATE TABLE IF NOT EXISTS provider_reputation_timeseries (
    id BIGSERIAL PRIMARY KEY,
    provider_id VARCHAR(64) NOT NULL,
    model VARCHAR(128) NOT NULL,
    date DATE NOT NULL,
    
    -- 每日指标
    reliability_score FLOAT,                              -- 可靠性评分 (0-1)
    avg_latency_ms FLOAT,                                 -- 平均延迟（毫秒）
    error_rate FLOAT,                                     -- 错误率 (0-1)
    request_count BIGINT DEFAULT 0,                       -- 请求总数
    success_count BIGINT DEFAULT 0,                       -- 成功请求数
    
    -- 来自 credential bandit scoring
    bandit_alpha FLOAT,                                   -- Thompson Sampling alpha参数
    bandit_beta FLOAT,                                    -- Thompson Sampling beta参数
    success_rate FLOAT,                                   -- 成功率 (0-1)
    
    -- 错误分类
    rate_limit_errors INT DEFAULT 0,
    quota_errors INT DEFAULT 0,
    auth_errors INT DEFAULT 0,
    timeout_errors INT DEFAULT 0,
    network_errors INT DEFAULT 0,
    other_errors INT DEFAULT 0,
    
    -- 元数据
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    UNIQUE(provider_id, model, date)
);

-- 索引（时序查询优化）
CREATE INDEX IF NOT EXISTS idx_provider_rep_ts_provider_model_date ON provider_reputation_timeseries(provider_id, model, date DESC);
CREATE INDEX IF NOT EXISTS idx_provider_rep_ts_date ON provider_reputation_timeseries(date DESC);
CREATE INDEX IF NOT EXISTS idx_provider_rep_ts_model ON provider_reputation_timeseries(model, date DESC);

-- 供应商事件表（事故追踪）
CREATE TABLE IF NOT EXISTS provider_incidents (
    id BIGSERIAL PRIMARY KEY,
    provider_id VARCHAR(64) NOT NULL,
    model VARCHAR(128),                                   -- NULL表示影响所有模型
    
    -- 事件详情
    incident_type VARCHAR(32) NOT NULL,                  -- rate_limit_spike, outage, auth_failure, degraded_performance
    impact_level VARCHAR(16) NOT NULL,                   -- low, medium, high, critical
    description TEXT,
    
    -- 时间范围
    started_at TIMESTAMPTZ NOT NULL,
    ended_at TIMESTAMPTZ,
    duration_seconds INT,                                 -- 自动计算或手动设置
    
    -- 影响统计
    affected_requests BIGINT,
    affected_tenants INT,
    
    -- 状态
    resolved BOOLEAN DEFAULT FALSE,
    resolution_notes TEXT,
    
    -- 元数据
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_provider_incidents_provider ON provider_incidents(provider_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_provider_incidents_model ON provider_incidents(model, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_provider_incidents_type ON provider_incidents(incident_type, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_provider_incidents_unresolved ON provider_incidents(resolved, started_at DESC) WHERE resolved = FALSE;

-- 自动更新 updated_at 触发器
CREATE OR REPLACE FUNCTION update_provider_reputation_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_provider_rep_ts_updated_at
    BEFORE UPDATE ON provider_reputation_timeseries
    FOR EACH ROW
    EXECUTE FUNCTION update_provider_reputation_updated_at();

CREATE TRIGGER trg_provider_incidents_updated_at
    BEFORE UPDATE ON provider_incidents
    FOR EACH ROW
    EXECUTE FUNCTION update_provider_reputation_updated_at();

-- 自动计算事件持续时间
CREATE OR REPLACE FUNCTION calculate_incident_duration()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.ended_at IS NOT NULL AND NEW.started_at IS NOT NULL THEN
        NEW.duration_seconds = EXTRACT(EPOCH FROM (NEW.ended_at - NEW.started_at))::INT;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_provider_incidents_duration
    BEFORE INSERT OR UPDATE ON provider_incidents
    FOR EACH ROW
    EXECUTE FUNCTION calculate_incident_duration();

-- 注释
COMMENT ON TABLE provider_reputation_timeseries IS 'LLM供应商信誉时序表，每日指标聚合';
COMMENT ON TABLE provider_incidents IS '供应商事件追踪表，记录故障、降级等事件';
COMMENT ON COLUMN provider_reputation_timeseries.bandit_alpha IS 'Thompson Sampling Beta分布参数alpha（成功数+1）';
COMMENT ON COLUMN provider_reputation_timeseries.bandit_beta IS 'Thompson Sampling Beta分布参数beta（失败数+1）';
COMMENT ON COLUMN provider_incidents.incident_type IS '事件类型：rate_limit_spike, outage, auth_failure, degraded_performance';
COMMENT ON COLUMN provider_incidents.impact_level IS '影响级别：low, medium, high, critical';
