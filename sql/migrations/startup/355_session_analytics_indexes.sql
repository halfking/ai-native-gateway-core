-- Migration 355: Session Analytics Query Optimization Indexes
-- 2026-07-06: 为会话分析中心时间序列和分布查询创建优化索引
-- Ref: docs/session-management-analytics-plan.md 第 11.5.1 节

-- 1. request_logs 核心查询索引
CREATE INDEX IF NOT EXISTS idx_request_logs_gw_session_id
    ON request_logs (gw_session_id);

CREATE INDEX IF NOT EXISTS idx_request_logs_tenant_ts
    ON request_logs (tenant_id, ts DESC);

CREATE INDEX IF NOT EXISTS idx_request_logs_ts_day
    ON request_logs (date_trunc('day', ts));

COMMENT ON INDEX idx_request_logs_gw_session_id IS '会话维度查询（全景图时间线）';
COMMENT ON INDEX idx_request_logs_tenant_ts IS '租户时间序列查询';
COMMENT ON INDEX idx_request_logs_ts_day IS '按日聚合查询（活动趋势）';

-- 2. session_summaries 健康与分布查询索引
CREATE INDEX IF NOT EXISTS idx_session_summaries_health_grade
    ON session_summaries (health_grade) WHERE health_grade IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_session_summaries_outcome
    ON session_summaries (outcome) WHERE outcome IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_session_summaries_health_score
    ON session_summaries (health_score DESC) WHERE health_score IS NOT NULL;

COMMENT ON INDEX idx_session_summaries_health_grade IS '健康等级分布查询';
COMMENT ON INDEX idx_session_summaries_outcome IS '结果分类查询';
COMMENT ON INDEX idx_session_summaries_health_score IS '按健康分排序查询';
