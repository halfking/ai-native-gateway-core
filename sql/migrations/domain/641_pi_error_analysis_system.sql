-- Migration: 641_pi_error_analysis_system
-- Purpose: PI 智能体错误自动分析系统
-- Phase: 1 - 基础设施（错误队列和分析结果表）
-- Author: System
-- Date: 2026-11-17

-- ============================================================================
-- 1. 错误分析队列表（error_analysis_queue）
-- ============================================================================
-- 用途：收集待分析的 PI 检测错误，支持优先级排序和去重
CREATE TABLE IF NOT EXISTS error_analysis_queue (
    id BIGSERIAL PRIMARY KEY,
    
    -- 错误标识和分类
    error_type TEXT NOT NULL,                    -- 错误类型（如 'llm_detection_failed', 'rule_false_positive'）
    error_hash TEXT NOT NULL,                    -- 错误指纹（基于核心特征的 hash，用于去重）
    priority INTEGER NOT NULL DEFAULT 5,         -- 优先级 1-10（10=最高）
    
    -- 错误上下文
    tenant_id TEXT NOT NULL,                     -- 租户 ID
    session_id TEXT,                             -- 会话 ID（可选）
    request_id TEXT,                             -- 请求 ID（可选）
    
    -- 错误详情
    input_text TEXT,                             -- 触发错误的输入文本（可能被截断）
    input_hash TEXT,                             -- 完整输入的 hash（用于追溯）
    error_message TEXT,                          -- 错误消息
    error_details JSONB,                         -- 详细错误信息（JSON 格式）
    
    -- 检测上下文
    detection_layers JSONB,                      -- 已执行的检测层信息
    matched_rules JSONB,                         -- 已匹配的规则（如果有）
    policy_config JSONB,                         -- 当时的策略配置快照
    
    -- 处理状态
    status TEXT NOT NULL DEFAULT 'pending',      -- 状态：pending, processing, analyzed, failed, skipped
    retry_count INTEGER NOT NULL DEFAULT 0,      -- 重试次数
    max_retries INTEGER NOT NULL DEFAULT 3,      -- 最大重试次数
    
    -- 时间戳
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,                      -- 开始分析时间
    completed_at TIMESTAMPTZ,                    -- 完成分析时间
    
    -- 元数据
    metadata JSONB,                              -- 扩展元数据
    
    -- 索引字段
    CONSTRAINT error_analysis_queue_status_check 
        CHECK (status IN ('pending', 'processing', 'analyzed', 'failed', 'skipped'))
);

-- 创建索引以支持高效查询
CREATE INDEX idx_error_analysis_queue_status_priority 
    ON error_analysis_queue(status, priority DESC, created_at);

CREATE INDEX idx_error_analysis_queue_error_hash 
    ON error_analysis_queue(error_hash);

CREATE INDEX idx_error_analysis_queue_tenant 
    ON error_analysis_queue(tenant_id, created_at DESC);

CREATE INDEX idx_error_analysis_queue_error_type 
    ON error_analysis_queue(error_type, created_at DESC);

-- ============================================================================
-- 2. 错误分析结果表（error_analysis_results）
-- ============================================================================
-- 用途：存储 LLM 分析的结果，支持追溯和统计
CREATE TABLE IF NOT EXISTS error_analysis_results (
    id BIGSERIAL PRIMARY KEY,
    
    -- 关联队列项
    queue_id BIGINT NOT NULL REFERENCES error_analysis_queue(id) ON DELETE CASCADE,
    
    -- 分析标识
    analysis_id TEXT NOT NULL UNIQUE,            -- 分析 ID（UUID）
    analyzer_model TEXT NOT NULL,                -- 使用的分析模型（如 'claude-3-5-sonnet'）
    analyzer_version TEXT,                       -- 分析器版本
    
    -- 分析结果
    root_cause TEXT,                             -- 根本原因分析
    error_category TEXT,                         -- 错误分类
    is_false_positive BOOLEAN,                   -- 是否误报
    confidence_score DECIMAL(3,2),               -- 置信度 0.00-1.00
    
    -- 建议和修复
    suggested_fix TEXT,                          -- 建议的修复方案
    rule_adjustment JSONB,                       -- 规则调整建议（JSON 格式）
    code_changes JSONB,                          -- 代码修改建议（JSON 格式）
    
    -- 影响评估
    severity_level TEXT,                         -- 严重程度：low, medium, high, critical
    impact_scope TEXT,                           -- 影响范围：session, tenant, global
    affected_users_estimate INTEGER,             -- 预估受影响用户数
    
    -- 相似案例
    similar_cases JSONB,                         -- 相似历史案例（JSON 数组）
    pattern_frequency INTEGER,                   -- 模式出现频率
    
    -- 详细分析
    analysis_details JSONB,                      -- 完整的分析详情（JSON 格式）
    llm_response_raw TEXT,                       -- 原始 LLM 响应
    
    -- 执行状态
    auto_applied BOOLEAN DEFAULT FALSE,          -- 是否已自动应用修复
    applied_at TIMESTAMPTZ,                      -- 应用时间
    applied_by TEXT,                             -- 应用者（'system' 或用户 ID）
    
    -- 反馈和验证
    human_verified BOOLEAN,                      -- 人工验证状态
    verification_feedback TEXT,                  -- 验证反馈
    verified_at TIMESTAMPTZ,                     -- 验证时间
    verified_by TEXT,                            -- 验证者
    
    -- 性能统计
    analysis_duration_ms INTEGER,                -- 分析耗时（毫秒）
    llm_tokens_used INTEGER,                     -- 消耗的 token 数
    
    -- 时间戳
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- 元数据
    metadata JSONB,
    
    -- 约束
    CONSTRAINT error_analysis_results_severity_check 
        CHECK (severity_level IN ('low', 'medium', 'high', 'critical')),
    CONSTRAINT error_analysis_results_impact_check 
        CHECK (impact_scope IN ('session', 'tenant', 'global')),
    CONSTRAINT error_analysis_results_confidence_check 
        CHECK (confidence_score >= 0.00 AND confidence_score <= 1.00)
);

-- 创建索引
CREATE INDEX idx_error_analysis_results_queue 
    ON error_analysis_results(queue_id);

CREATE INDEX idx_error_analysis_results_category 
    ON error_analysis_results(error_category, created_at DESC);

CREATE INDEX idx_error_analysis_results_severity 
    ON error_analysis_results(severity_level, created_at DESC);

CREATE INDEX idx_error_analysis_results_false_positive 
    ON error_analysis_results(is_false_positive, created_at DESC);

CREATE INDEX idx_error_analysis_results_auto_applied 
    ON error_analysis_results(auto_applied, created_at DESC);

-- ============================================================================
-- 3. 分析统计视图（v_error_analysis_stats）
-- ============================================================================
-- 用途：提供错误分析的统计信息
CREATE OR REPLACE VIEW v_error_analysis_stats AS
SELECT 
    q.error_type,
    COUNT(*) as total_errors,
    COUNT(CASE WHEN q.status = 'analyzed' THEN 1 END) as analyzed_count,
    COUNT(CASE WHEN q.status = 'pending' THEN 1 END) as pending_count,
    COUNT(CASE WHEN r.is_false_positive = TRUE THEN 1 END) as false_positive_count,
    AVG(r.confidence_score) as avg_confidence,
    AVG(r.analysis_duration_ms) as avg_analysis_time_ms,
    SUM(r.llm_tokens_used) as total_tokens_used,
    MAX(q.created_at) as last_error_at
FROM error_analysis_queue q
LEFT JOIN error_analysis_results r ON q.id = r.queue_id
GROUP BY q.error_type;

-- ============================================================================
-- 4. 触发器：自动更新 updated_at
-- ============================================================================
CREATE OR REPLACE FUNCTION update_error_analysis_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trigger_error_analysis_results_updated_at
    BEFORE UPDATE ON error_analysis_results
    FOR EACH ROW
    EXECUTE FUNCTION update_error_analysis_updated_at();

-- ============================================================================
-- 5. 数据保留策略函数
-- ============================================================================
-- 用途：清理超过 90 天的已分析错误（保留关键统计信息）
CREATE OR REPLACE FUNCTION cleanup_old_error_analysis(retention_days INTEGER DEFAULT 90)
RETURNS TABLE(deleted_queue_count BIGINT, deleted_result_count BIGINT) AS $$
DECLARE
    v_deleted_results BIGINT;
    v_deleted_queue BIGINT;
BEGIN
    -- 删除超过保留期的已分析结果（保留未解决的）
    DELETE FROM error_analysis_results
    WHERE created_at < NOW() - (retention_days || ' days')::INTERVAL
      AND auto_applied = TRUE
      AND human_verified = TRUE
    RETURNING * INTO v_deleted_results;
    
    GET DIAGNOSTICS v_deleted_results = ROW_COUNT;
    
    -- 删除对应的队列项（级联删除）
    DELETE FROM error_analysis_queue
    WHERE id NOT IN (SELECT queue_id FROM error_analysis_results)
      AND status IN ('analyzed', 'skipped')
      AND created_at < NOW() - (retention_days || ' days')::INTERVAL;
    
    GET DIAGNOSTICS v_deleted_queue = ROW_COUNT;
    
    deleted_result_count := v_deleted_results;
    deleted_queue_count := v_deleted_queue;
    
    RETURN NEXT;
END;
$$ LANGUAGE plpgsql;

-- ============================================================================
-- 6. 注释和文档
-- ============================================================================
COMMENT ON TABLE error_analysis_queue IS 'PI 检测错误队列，存储待分析的错误';
COMMENT ON TABLE error_analysis_results IS 'LLM 智能分析结果，包含根因分析和修复建议';
COMMENT ON VIEW v_error_analysis_stats IS '错误分析统计视图，用于监控和报告';
COMMENT ON FUNCTION cleanup_old_error_analysis IS '清理超过指定天数的已分析错误记录';

-- ============================================================================
-- 7. 初始数据：示例配置
-- ============================================================================
-- 插入示例错误类型优先级配置（可选）
-- 这些可以在应用层配置，此处仅作示例

-- Migration complete
-- 验证点：
-- 1. 两个核心表已创建且带有适当的约束
-- 2. 所有索引已建立以支持高效查询
-- 3. 统计视图可用于监控
-- 4. 触发器确保 updated_at 自动更新
-- 5. 清理函数支持数据保留策略
