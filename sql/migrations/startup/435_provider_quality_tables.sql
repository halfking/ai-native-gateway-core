-- ===========================================================================
-- File:          sql/migrations/startup/435_provider_quality_tables.sql
-- Database:      llm_gateway
-- Purpose:       创建供应商质量画像系统的6张核心表
--
-- Related:       docs/供应商画像/02-数据库设计.md
-- Status:        active
-- Idempotent:    YES (使用 IF NOT EXISTS)
--
-- Changelog:
--   2026-07-19  v1.0  初始创建 - 6张表 + 索引 + 视图
-- ===========================================================================
--
-- 表清单:
--   1. provider_quality_profiles      - 质量画像主表（76个字段）
--   2. provider_metrics_minute        - 分钟级聚合指标
--   3. provider_metrics_hour          - 小时级聚合指标
--   4. provider_error_details         - 错误详情聚合
--   5. provider_health_events         - 健康事件日志
--   6. provider_quality_configs       - 质量配置
--
-- 视图:
--   1. provider_health_status         - 健康状态概览
--   2. provider_error_distribution    - 错误分布统计
--
-- Rollback:
--   执行 435_provider_quality_tables.down.sql
-- ===========================================================================

\set ON_ERROR_STOP on

-- ============================================================================
-- 表 1: provider_quality_profiles (质量画像主表)
-- ============================================================================

CREATE TABLE IF NOT EXISTS provider_quality_profiles (
    id BIGSERIAL PRIMARY KEY,
    provider_id BIGINT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    model_name VARCHAR(100),  -- NULL表示provider级别，非NULL表示model级别

    -- L1: 核心可用性指标
    success_rate_5m DECIMAL(5,2),      -- 5分钟成功率
    success_rate_1h DECIMAL(5,2),      -- 1小时成功率
    success_rate_24h DECIMAL(5,2),     -- 24小时成功率
    error_rate_5xx_5m DECIMAL(5,2),    -- 5分钟5xx错误率
    error_rate_5xx_1h DECIMAL(5,2),
    error_rate_5xx_24h DECIMAL(5,2),
    error_rate_4xx_5m DECIMAL(5,2),
    error_rate_4xx_1h DECIMAL(5,2),
    error_rate_4xx_24h DECIMAL(5,2),
    error_rate_timeout_5m DECIMAL(5,2),
    error_rate_timeout_1h DECIMAL(5,2),
    error_rate_timeout_24h DECIMAL(5,2),
    availability_24h DECIMAL(5,2),      -- 24小时可用性

    -- L2: 性能指标 (毫秒)
    latency_p50_5m INT,
    latency_p50_1h INT,
    latency_p50_24h INT,
    latency_p95_5m INT,
    latency_p95_1h INT,
    latency_p95_24h INT,
    latency_p99_5m INT,
    latency_p99_1h INT,
    latency_p99_24h INT,
    ttft_p95_5m INT,                    -- Time To First Token
    ttft_p95_1h INT,
    ttft_p95_24h INT,
    throughput_tokens_per_sec_1h DECIMAL(10,2),

    -- L3: 稳定性指标
    volatility_24h DECIMAL(5,3),        -- 波动性
    mttr_seconds_24h INT,               -- 平均恢复时间(秒)
    error_diversity_score_24h DECIMAL(5,2), -- 错误多样性评分
    consecutive_failures INT DEFAULT 0, -- 连续失败次数
    last_failure_at TIMESTAMPTZ,        -- 最后失败时间
    last_recovery_at TIMESTAMPTZ,       -- 最后恢复时间

    -- L4: 成本效益指标
    cost_per_1k_tokens DECIMAL(10,6),   -- 每1K tokens成本
    quota_usage_percentage DECIMAL(5,2), -- 配额使用率

    -- 综合评分
    availability_score DECIMAL(5,2),    -- 可用性分数 (0-100)
    performance_score DECIMAL(5,2),     -- 性能分数 (0-100)
    stability_score DECIMAL(5,2),       -- 稳定性分数 (0-100)
    cost_efficiency_score DECIMAL(5,2), -- 成本效益分数 (0-100)
    quality_score DECIMAL(5,2),         -- 综合质量分数 (0-100)
    quality_grade VARCHAR(1),           -- 质量等级 S/A/B/C/D

    -- 统计计数
    total_requests_5m BIGINT DEFAULT 0,
    total_requests_1h BIGINT DEFAULT 0,
    total_requests_24h BIGINT DEFAULT 0,
    successful_requests_5m BIGINT DEFAULT 0,
    successful_requests_1h BIGINT DEFAULT 0,
    successful_requests_24h BIGINT DEFAULT 0,

    -- 元数据
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(provider_id, model_name)
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_pqp_provider_id ON provider_quality_profiles(provider_id);
CREATE INDEX IF NOT EXISTS idx_pqp_quality_score ON provider_quality_profiles(quality_score DESC);
CREATE INDEX IF NOT EXISTS idx_pqp_updated_at ON provider_quality_profiles(updated_at);
CREATE INDEX IF NOT EXISTS idx_pqp_provider_model ON provider_quality_profiles(provider_id, model_name)
    WHERE model_name IS NOT NULL;

-- 注释
COMMENT ON TABLE provider_quality_profiles IS '供应商质量画像主表 - 存储实时质量评分和多时间窗口指标';
COMMENT ON COLUMN provider_quality_profiles.model_name IS 'NULL=provider级别聚合, 非NULL=model级别画像';
COMMENT ON COLUMN provider_quality_profiles.quality_score IS '综合质量分数(0-100)，用于路由决策权重';
COMMENT ON COLUMN provider_quality_profiles.quality_grade IS '质量等级: S(90-100) A(80-89) B(70-79) C(60-69) D(<60)';

-- ============================================================================
-- 表 2: provider_metrics_minute (分钟级聚合指标)
-- ============================================================================

CREATE TABLE IF NOT EXISTS provider_metrics_minute (
    id BIGSERIAL PRIMARY KEY,
    provider_id BIGINT NOT NULL,
    model_name VARCHAR(100),
    endpoint VARCHAR(50),  -- 'chat', 'completion', 'embedding'

    -- 时间桶
    bucket TIMESTAMPTZ NOT NULL,  -- 分钟对齐的时间戳

    -- 请求统计
    total_requests INT NOT NULL DEFAULT 0,
    successful_requests INT NOT NULL DEFAULT 0,
    error_5xx INT NOT NULL DEFAULT 0,
    error_4xx INT NOT NULL DEFAULT 0,
    error_timeout INT NOT NULL DEFAULT 0,
    error_other INT NOT NULL DEFAULT 0,

    -- 延迟统计 (毫秒)
    latency_sum BIGINT NOT NULL DEFAULT 0,
    latency_min INT,
    latency_max INT,
    latency_p50 INT,
    latency_p95 INT,
    latency_p99 INT,

    -- TTFT统计 (毫秒)
    ttft_sum BIGINT DEFAULT 0,
    ttft_p95 INT,

    -- Token统计
    total_input_tokens BIGINT DEFAULT 0,
    total_output_tokens BIGINT DEFAULT 0,

    -- 成本统计
    total_cost DECIMAL(12,6) DEFAULT 0,

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(provider_id, model_name, endpoint, bucket)
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_pmm_provider_bucket ON provider_metrics_minute(provider_id, bucket DESC);
CREATE INDEX IF NOT EXISTS idx_pmm_model_bucket ON provider_metrics_minute(provider_id, model_name, bucket DESC);
CREATE INDEX IF NOT EXISTS idx_pmm_bucket ON provider_metrics_minute(bucket DESC);

-- 注释
COMMENT ON TABLE provider_metrics_minute IS '按分钟聚合的供应商指标 - 从request_logs实时聚合';
COMMENT ON COLUMN provider_metrics_minute.bucket IS '分钟对齐的时间戳，如 2026-07-19 01:23:00';

-- ============================================================================
-- 表 3: provider_metrics_hour (小时级聚合指标)
-- ============================================================================

CREATE TABLE IF NOT EXISTS provider_metrics_hour (
    id BIGSERIAL PRIMARY KEY,
    provider_id BIGINT NOT NULL,
    model_name VARCHAR(100),
    endpoint VARCHAR(50),

    bucket TIMESTAMPTZ NOT NULL,  -- 小时对齐

    -- 聚合统计
    total_requests BIGINT NOT NULL DEFAULT 0,
    successful_requests BIGINT NOT NULL DEFAULT 0,
    error_5xx INT NOT NULL DEFAULT 0,
    error_4xx INT NOT NULL DEFAULT 0,
    error_timeout INT NOT NULL DEFAULT 0,

    success_rate DECIMAL(5,2),
    error_rate_5xx DECIMAL(5,2),

    latency_p50 INT,
    latency_p95 INT,
    latency_p99 INT,
    ttft_p95 INT,

    total_input_tokens BIGINT DEFAULT 0,
    total_output_tokens BIGINT DEFAULT 0,
    total_cost DECIMAL(12,6) DEFAULT 0,

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(provider_id, model_name, endpoint, bucket)
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_pmh_provider_bucket ON provider_metrics_hour(provider_id, bucket DESC);
CREATE INDEX IF NOT EXISTS idx_pmh_bucket ON provider_metrics_hour(bucket DESC);

-- 注释
COMMENT ON TABLE provider_metrics_hour IS '按小时聚合的供应商指标 - 从分钟级聚合，保留90天';

-- ============================================================================
-- 表 4: provider_error_details (错误详情聚合)
-- ============================================================================

CREATE TABLE IF NOT EXISTS provider_error_details (
    id BIGSERIAL PRIMARY KEY,
    provider_id BIGINT NOT NULL,
    model_name VARCHAR(100),
    endpoint VARCHAR(50),

    -- 错误分类
    error_type VARCHAR(50) NOT NULL,  -- 'timeout', '5xx', '4xx', 'network', 'rate_limit'
    error_code VARCHAR(50),            -- HTTP状态码或自定义错误码
    error_message TEXT,

    -- 请求上下文
    request_id VARCHAR(100),
    user_id VARCHAR(100),
    tenant_id VARCHAR(100),

    -- 请求特征
    input_tokens INT,
    context JSONB,  -- 详细上下文（已在candidate_failure_logs中）

    -- 聚合计数
    occurrences INT DEFAULT 1,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,

    -- 处理状态
    acknowledged BOOLEAN DEFAULT FALSE,
    resolved BOOLEAN DEFAULT FALSE,
    resolution_note TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_ped_provider_type ON provider_error_details(provider_id, error_type);
CREATE INDEX IF NOT EXISTS idx_ped_last_seen ON provider_error_details(last_seen_at DESC);
CREATE INDEX IF NOT EXISTS idx_ped_unresolved ON provider_error_details(resolved) WHERE NOT resolved;

-- 注释
COMMENT ON TABLE provider_error_details IS '供应商错误详情聚合表 - 用于根因分析和错误趋势';
COMMENT ON COLUMN provider_error_details.occurrences IS '相同错误的出现次数';

-- ============================================================================
-- 表 5: provider_health_events (健康事件日志)
-- ============================================================================

CREATE TABLE IF NOT EXISTS provider_health_events (
    id BIGSERIAL PRIMARY KEY,
    provider_id BIGINT NOT NULL,
    model_name VARCHAR(100),

    -- 事件类型
    event_type VARCHAR(50) NOT NULL,
    -- 'degraded', 'recovered', 'critical', 'warning', 'maintenance'

    severity VARCHAR(10) NOT NULL,  -- 'P0', 'P1', 'P2', 'P3'

    -- 事件描述
    title VARCHAR(200) NOT NULL,
    description TEXT,

    -- 触发指标
    trigger_metric VARCHAR(50),      -- 'error_rate_5xx', 'latency_p99', etc.
    trigger_value DECIMAL(10,2),
    threshold_value DECIMAL(10,2),

    -- 影响评估
    affected_requests_count BIGINT,
    estimated_downtime_seconds INT,

    -- 处理信息
    auto_action VARCHAR(100),        -- 'switch_provider', 'reduce_weight', 'circuit_open'
    manual_action TEXT,
    acknowledged_by VARCHAR(100),
    acknowledged_at TIMESTAMPTZ,
    resolved_at TIMESTAMPTZ,

    -- 通知状态
    notified BOOLEAN DEFAULT FALSE,
    notification_channels TEXT[],    -- ['feishu', 'email', 'dashboard']

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_phe_provider ON provider_health_events(provider_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_phe_severity ON provider_health_events(severity, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_phe_unresolved ON provider_health_events(resolved_at) WHERE resolved_at IS NULL;

-- 注释
COMMENT ON TABLE provider_health_events IS '供应商健康事件日志 - 用于告警和审计追踪';
COMMENT ON COLUMN provider_health_events.auto_action IS '系统自动执行的动作（如降权、切换供应商）';

-- ============================================================================
-- 表 6: provider_quality_configs (质量配置)
-- ============================================================================

CREATE TABLE IF NOT EXISTS provider_quality_configs (
    id BIGSERIAL PRIMARY KEY,
    provider_id BIGINT NOT NULL UNIQUE REFERENCES providers(id) ON DELETE CASCADE,

    -- 告警阈值
    alert_error_rate_5xx_p0 DECIMAL(5,2) DEFAULT 5.0,   -- P0告警: 5xx错误率 > 5%
    alert_error_rate_5xx_p1 DECIMAL(5,2) DEFAULT 1.0,   -- P1告警: > 1%
    alert_availability_p0 DECIMAL(5,2) DEFAULT 95.0,    -- P0告警: 可用性 < 95%
    alert_latency_p99_p0 INT DEFAULT 30000,             -- P0告警: P99延迟 > 30s
    alert_latency_p95_p1 INT DEFAULT 10000,             -- P1告警: P95延迟 > 10s

    -- 质量目标
    target_success_rate DECIMAL(5,2) DEFAULT 99.5,
    target_latency_p95 INT DEFAULT 5000,
    target_latency_p99 INT DEFAULT 10000,

    -- 评分权重（可定制）
    weight_availability DECIMAL(3,2) DEFAULT 0.4,
    weight_performance DECIMAL(3,2) DEFAULT 0.3,
    weight_stability DECIMAL(3,2) DEFAULT 0.2,
    weight_cost_efficiency DECIMAL(3,2) DEFAULT 0.1,

    -- 熔断配置
    circuit_breaker_enabled BOOLEAN DEFAULT TRUE,
    circuit_breaker_threshold INT DEFAULT 5,  -- 连续失败5次触发熔断
    circuit_breaker_timeout_seconds INT DEFAULT 300,  -- 5分钟后自动尝试恢复

    -- 降权配置
    downgrade_on_score_below DECIMAL(5,2) DEFAULT 70.0,
    downgrade_weight_multiplier DECIMAL(3,2) DEFAULT 0.5,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 注释
COMMENT ON TABLE provider_quality_configs IS '供应商质量配置表 - 存储告警阈值和评分权重';
COMMENT ON COLUMN provider_quality_configs.weight_availability IS '可用性权重(默认40%)';

-- ============================================================================
-- 视图 1: provider_health_status (健康状态概览)
-- ============================================================================

CREATE OR REPLACE VIEW provider_health_status AS
SELECT
    p.id as provider_id,
    p.name as provider_name,
    pqp.model_name,
    pqp.quality_score,
    pqp.quality_grade,
    pqp.success_rate_5m,
    pqp.error_rate_5xx_5m,
    pqp.latency_p95_5m,
    pqp.availability_24h,
    pqp.consecutive_failures,
    CASE
        WHEN pqp.consecutive_failures >= 5 THEN 'circuit_open'
        WHEN pqp.quality_score >= 90 THEN 'healthy'
        WHEN pqp.quality_score >= 70 THEN 'degraded'
        ELSE 'critical'
    END as health_status,
    pqp.updated_at
FROM providers p
JOIN provider_quality_profiles pqp ON p.id = pqp.provider_id
WHERE pqp.model_name IS NULL  -- provider级别
ORDER BY pqp.quality_score DESC;

COMMENT ON VIEW provider_health_status IS '供应商健康状态概览 - 用于监控看板';

-- ============================================================================
-- 视图 2: provider_error_distribution (错误分布统计)
-- ============================================================================

CREATE OR REPLACE VIEW provider_error_distribution AS
SELECT
    provider_id,
    error_type,
    error_code,
    COUNT(*) as error_count,
    SUM(occurrences) as total_occurrences,
    MAX(last_seen_at) as last_occurrence,
    COUNT(*) FILTER (WHERE NOT resolved) as unresolved_count
FROM provider_error_details
WHERE last_seen_at > NOW() - INTERVAL '24 hours'
GROUP BY provider_id, error_type, error_code
ORDER BY total_occurrences DESC;

COMMENT ON VIEW provider_error_distribution IS '24小时错误分布统计 - 用于错误分析';

-- ============================================================================
-- 验证
-- ============================================================================

DO $$
BEGIN
    -- 验证表是否创建成功
    ASSERT (SELECT COUNT(*) FROM information_schema.tables 
            WHERE table_name IN (
                'provider_quality_profiles',
                'provider_metrics_minute',
                'provider_metrics_hour',
                'provider_error_details',
                'provider_health_events',
                'provider_quality_configs'
            )) = 6, 'Not all tables created';

    -- 验证视图是否创建成功
    ASSERT (SELECT COUNT(*) FROM information_schema.views
            WHERE table_name IN (
                'provider_health_status',
                'provider_error_distribution'
            )) = 2, 'Not all views created';

    RAISE NOTICE '✅ Migration 435 completed: 6 tables + 2 views created';
END $$;
