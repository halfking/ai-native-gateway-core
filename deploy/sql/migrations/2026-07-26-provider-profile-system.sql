-- Provider Profile System Migration
-- Created: 2026-07-26
-- Purpose: 供应商画像系统数据表
-- Author: AI Assistant
-- 
-- 本迁移脚本创建供应商画像系统所需的所有数据表
-- 包括：小时级指标、天级聚合、可信度测试、告警、费用对账、白名单

BEGIN;

-- 1. provider_profile_metrics (小时级原始数据，保留7天)
CREATE TABLE IF NOT EXISTS provider_profile_metrics (
    id BIGSERIAL PRIMARY KEY,
    credential_id BIGINT NOT NULL,
    provider_id BIGINT NOT NULL,
    metric_time TIMESTAMPTZ NOT NULL,
    time_slot TEXT NOT NULL, -- dawn/morning/afternoon/evening/night
    
    -- 网络延迟维度 (ms)
    network_latency_p50 INTEGER,
    network_latency_p95 INTEGER,
    network_latency_p99 INTEGER,
    
    -- 可用性维度
    availability_total_requests INTEGER DEFAULT 0,
    availability_success_requests INTEGER DEFAULT 0,
    availability_ttft_avg_ms INTEGER,
    availability_duration_avg_ms INTEGER,
    
    -- 稳定性维度
    stability_error_count INTEGER DEFAULT 0,
    stability_error_types JSONB,
    
    -- 规模维度
    scale_total_models INTEGER,
    scale_available_models INTEGER,
    
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ppm_credential_time ON provider_profile_metrics(credential_id, metric_time DESC);
CREATE INDEX IF NOT EXISTS idx_ppm_provider_time ON provider_profile_metrics(provider_id, metric_time DESC);
CREATE INDEX IF NOT EXISTS idx_ppm_cleanup ON provider_profile_metrics(created_at);

COMMENT ON TABLE provider_profile_metrics IS '供应商画像小时级原始指标数据（保留7天）';
COMMENT ON COLUMN provider_profile_metrics.time_slot IS '时段标签: dawn(0-6)/morning(6-12)/afternoon(12-18)/evening(18-22)/night(22-24)';

-- 2. provider_profile_daily (天级聚合数据，保留365天)
CREATE TABLE IF NOT EXISTS provider_profile_daily (
    id BIGSERIAL PRIMARY KEY,
    credential_id BIGINT NOT NULL,
    provider_id BIGINT NOT NULL,
    profile_date DATE NOT NULL,
    
    -- 各维度分数 (0-100)
    network_score NUMERIC(5,2),
    credibility_score NUMERIC(5,2),
    availability_score NUMERIC(5,2),
    stability_score NUMERIC(5,2),
    scale_score NUMERIC(5,2),
    cost_accuracy_score NUMERIC(5,2),
    price_score NUMERIC(5,2),
    total_score NUMERIC(5,2),
    
    -- 时段分布
    timeslot_scores JSONB, -- {"morning": 85.5, "afternoon": 82.3, ...}
    score_stddev NUMERIC(5,2), -- 当天分数标准差
    best_timeslot TEXT,
    worst_timeslot TEXT,
    
    -- 原始统计数据
    raw_stats JSONB,
    
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(credential_id, profile_date)
);

CREATE INDEX IF NOT EXISTS idx_ppd_credential_date ON provider_profile_daily(credential_id, profile_date DESC);
CREATE INDEX IF NOT EXISTS idx_ppd_provider_date ON provider_profile_daily(provider_id, profile_date DESC);
CREATE INDEX IF NOT EXISTS idx_ppd_total_score ON provider_profile_daily(total_score);
CREATE INDEX IF NOT EXISTS idx_ppd_date ON provider_profile_daily(profile_date DESC);

COMMENT ON TABLE provider_profile_daily IS '供应商画像天级聚合数据和评分（保留365天）';
COMMENT ON COLUMN provider_profile_daily.total_score IS '总分：各维度加权平均，权重见设计文档';

-- 3. provider_credibility_tests (可信度测试记录)
CREATE TABLE IF NOT EXISTS provider_credibility_tests (
    id BIGSERIAL PRIMARY KEY,
    credential_id BIGINT NOT NULL,
    provider_id BIGINT NOT NULL,
    model_name TEXT NOT NULL,
    test_time TIMESTAMPTZ NOT NULL,
    test_type TEXT NOT NULL, -- capability_probe/cost_analysis/standard_testset/fingerprint
    
    -- 子维度分数 (0-100)
    authenticity_score NUMERIC(5,2), -- 真实性
    compliance_score NUMERIC(5,2),   -- 合规性
    consistency_score NUMERIC(5,2),  -- 稳定性
    version_score NUMERIC(5,2),      -- 版本一致性
    
    -- 测试详情
    test_details JSONB, -- 详细测试结果
    anomalies JSONB,    -- 发现的异常 [{severity, category, description, evidence}]
    
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_pct_credential_time ON provider_credibility_tests(credential_id, test_time DESC);
CREATE INDEX IF NOT EXISTS idx_pct_model ON provider_credibility_tests(model_name, test_time DESC);
CREATE INDEX IF NOT EXISTS idx_pct_provider ON provider_credibility_tests(provider_id, test_time DESC);

COMMENT ON TABLE provider_credibility_tests IS '模型可信度测试详细记录';
COMMENT ON COLUMN provider_credibility_tests.test_type IS '测试类型：capability_probe(能力探针)/cost_analysis(成本倒推)/standard_testset(标准测试集)/fingerprint(输出指纹)';

-- 4. provider_profile_alerts (告警记录)
CREATE TABLE IF NOT EXISTS provider_profile_alerts (
    id BIGSERIAL PRIMARY KEY,
    credential_id BIGINT NOT NULL,
    provider_id BIGINT NOT NULL,
    alert_type TEXT NOT NULL, -- score_drop/trend_drop/auto_disabled/auto_enabled/dimension_low
    alert_level TEXT NOT NULL, -- critical/warning/info
    trigger_date DATE NOT NULL,
    
    current_score NUMERIC(5,2),
    previous_score NUMERIC(5,2),
    score_change NUMERIC(5,2),
    
    dimension TEXT, -- 触发维度（可选）
    message TEXT NOT NULL,
    details JSONB,
    
    action_taken TEXT, -- disabled/enabled/degraded/none
    resolved_at TIMESTAMPTZ,
    
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ppa_credential ON provider_profile_alerts(credential_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ppa_provider ON provider_profile_alerts(provider_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ppa_type_level ON provider_profile_alerts(alert_type, alert_level, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ppa_unresolved ON provider_profile_alerts(resolved_at) WHERE resolved_at IS NULL;

COMMENT ON TABLE provider_profile_alerts IS '供应商画像告警和自动处理记录';
COMMENT ON COLUMN provider_profile_alerts.alert_type IS '告警类型：score_drop(分数下降)/trend_drop(趋势下降)/auto_disabled(自动禁用)/auto_enabled(自动恢复)/dimension_low(维度过低)';
COMMENT ON COLUMN provider_profile_alerts.action_taken IS '已采取动作：disabled(已禁用)/enabled(已恢复)/degraded(已降权)/none(仅告警)';

-- 5. provider_cost_reconciliation (费用对账记录，月度)
CREATE TABLE IF NOT EXISTS provider_cost_reconciliation (
    id BIGSERIAL PRIMARY KEY,
    provider_id BIGINT NOT NULL,
    reconciliation_month DATE NOT NULL, -- 月份（每月1日）
    
    -- 网关统计
    gateway_total_tokens BIGINT,
    gateway_input_tokens BIGINT,
    gateway_output_tokens BIGINT,
    gateway_total_cost NUMERIC(12,4),
    
    -- 供应商账单
    provider_total_tokens BIGINT,
    provider_input_tokens BIGINT,
    provider_output_tokens BIGINT,
    provider_total_cost NUMERIC(12,4),
    
    -- 差异率
    token_diff_rate NUMERIC(5,4), -- (gateway - provider) / provider
    cost_diff_rate NUMERIC(5,4),  -- (gateway - provider) / provider
    
    -- 数据来源
    data_source TEXT, -- api/manual
    notes TEXT,
    
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    
    UNIQUE(provider_id, reconciliation_month)
);

CREATE INDEX IF NOT EXISTS idx_pcr_provider_month ON provider_cost_reconciliation(provider_id, reconciliation_month DESC);

COMMENT ON TABLE provider_cost_reconciliation IS '供应商费用对账数据（月度）';
COMMENT ON COLUMN provider_cost_reconciliation.data_source IS '数据来源：api(自动获取)/manual(手动录入)';

-- 6. provider_profile_whitelist (自动处理白名单)
CREATE TABLE IF NOT EXISTS provider_profile_whitelist (
    id BIGSERIAL PRIMARY KEY,
    provider_id BIGINT NOT NULL UNIQUE,
    reason TEXT,
    added_by TEXT,
    added_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ppw_provider ON provider_profile_whitelist(provider_id);

COMMENT ON TABLE provider_profile_whitelist IS '供应商自动处理白名单（白名单中的供应商不会被自动禁用）';

-- 7. 扩展 credentials 表（如果字段不存在则添加）
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS auto_disabled_at TIMESTAMPTZ;
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS auto_disabled_reason TEXT;
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS auto_enabled_at TIMESTAMPTZ;
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS auto_enabled_reason TEXT;

COMMENT ON COLUMN credentials.auto_disabled_at IS '自动禁用时间';
COMMENT ON COLUMN credentials.auto_disabled_reason IS '自动禁用原因';
COMMENT ON COLUMN credentials.auto_enabled_at IS '自动恢复时间';
COMMENT ON COLUMN credentials.auto_enabled_reason IS '自动恢复原因';

COMMIT;

-- 迁移完成提示
DO $$
BEGIN
    RAISE NOTICE '=================================================================';
    RAISE NOTICE '供应商画像系统数据表创建完成';
    RAISE NOTICE '=================================================================';
    RAISE NOTICE '已创建的表：';
    RAISE NOTICE '  1. provider_profile_metrics      - 小时级原始数据（7天）';
    RAISE NOTICE '  2. provider_profile_daily         - 天级聚合数据（365天）';
    RAISE NOTICE '  3. provider_credibility_tests     - 可信度测试记录';
    RAISE NOTICE '  4. provider_profile_alerts        - 告警记录';
    RAISE NOTICE '  5. provider_cost_reconciliation   - 费用对账记录';
    RAISE NOTICE '  6. provider_profile_whitelist     - 自动处理白名单';
    RAISE NOTICE '';
    RAISE NOTICE '已扩展的表：';
    RAISE NOTICE '  - credentials (新增auto_disabled/enabled相关字段)';
    RAISE NOTICE '';
    RAISE NOTICE '下一步：';
    RAISE NOTICE '  1. 实现数据采集器和聚合器';
    RAISE NOTICE '  2. 配置定时任务';
    RAISE NOTICE '  3. 实现前端展示界面';
    RAISE NOTICE '=================================================================';
END $$;
