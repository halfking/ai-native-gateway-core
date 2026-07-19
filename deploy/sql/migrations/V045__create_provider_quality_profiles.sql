-- V045: 创建供应商质量画像表
-- 用途: 存储 LLM 供应商的质量评分
-- 作者: AI Agent
-- 日期: 2026-07-19

-- 创建质量画像表
CREATE TABLE IF NOT EXISTS provider_quality_profiles (
    id BIGSERIAL PRIMARY KEY,
    
    -- 供应商标识
    provider_id BIGINT NOT NULL,
    model_name VARCHAR(255) NOT NULL,
    
    -- 5个维度评分 (0-100)
    availability_score NUMERIC(5,2) DEFAULT 0,      -- L1 可用性 (35%权重)
    performance_score NUMERIC(5,2) DEFAULT 0,       -- L2 性能 (25%权重)
    reliability_score NUMERIC(5,2) DEFAULT 0,       -- L3 可信度 (20%权重)
    stability_score NUMERIC(5,2) DEFAULT 0,         -- L4 稳定性 (15%权重)
    cost_efficiency_score NUMERIC(5,2) DEFAULT 0,   -- L5 成本效益 (5%权重)
    
    -- 综合质量分 (0-100)
    quality_score NUMERIC(5,2) DEFAULT 0,
    
    -- 时间戳
    calculated_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    
    -- 唯一约束
    UNIQUE(provider_id, model_name)
);

-- 创建索引
CREATE INDEX IF NOT EXISTS idx_quality_profiles_provider ON provider_quality_profiles(provider_id);
CREATE INDEX IF NOT EXISTS idx_quality_profiles_model ON provider_quality_profiles(model_name);
CREATE INDEX IF NOT EXISTS idx_quality_profiles_quality_score ON provider_quality_profiles(quality_score DESC);
CREATE INDEX IF NOT EXISTS idx_quality_profiles_calculated_at ON provider_quality_profiles(calculated_at DESC);

-- 注释
COMMENT ON TABLE provider_quality_profiles IS '供应商质量画像表';
COMMENT ON COLUMN provider_quality_profiles.availability_score IS 'L1 可用性评分 (35%权重)';
COMMENT ON COLUMN provider_quality_profiles.performance_score IS 'L2 性能评分 (25%权重)';
COMMENT ON COLUMN provider_quality_profiles.reliability_score IS 'L3 可信度评分 (20%权重)';
COMMENT ON COLUMN provider_quality_profiles.stability_score IS 'L4 稳定性评分 (15%权重)';
COMMENT ON COLUMN provider_quality_profiles.cost_efficiency_score IS 'L5 成本效益评分 (5%权重)';
COMMENT ON COLUMN provider_quality_profiles.quality_score IS '综合质量分 (加权平均)';
