-- Migration 361: Create intent_analysis_adjustments table
--
-- Purpose:
--   意图分析调整记录表：追踪配置变更历史、原因和效果评估。
--   支持配置版本管理、回滚和自动优化效果分析。
--
-- Design notes:
--   - 每次配置调整一条记录（keyword_add/threshold_change等）
--   - 记录调整前后的准确率对比
--   - 支持人工调整和自动优化两种触发方式
--   - status字段支持回滚操作（active/rolled_back/superseded）
--
-- Date: 2026-07-08

CREATE TABLE IF NOT EXISTS intent_analysis_adjustments (
    id BIGSERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    
    -- 调整内容
    adjustment_type TEXT NOT NULL,  
    -- 调整类型：
    --   keyword_add: 添加关键词
    --   keyword_remove: 移除关键词
    --   pattern_add: 添加正则模式
    --   pattern_remove: 移除正则模式
    --   threshold_change: 调整置信度阈值
    --   strategy_change: 切换分类策略
    --   drift_threshold_change: 调整漂移阈值
    --   memory_window_change: 调整记忆窗口
    
    target_intent TEXT,  -- 影响的意图类型（如果是全局调整则为NULL）
    
    adjustment_detail JSONB NOT NULL,
    -- 详细调整内容，示例：
    -- {"action":"add_keyword","intent":"code","keywords":["refactor","optimize"],"language":"en"}
    -- {"action":"change_threshold","old_value":0.60,"new_value":0.65,"threshold_type":"medium"}
    -- {"action":"change_strategy","old_strategy":"baseline_heuristic","new_strategy":"pattern_layered"}
    
    -- 调整原因和来源
    reason TEXT,  -- 调整原因（人工输入或自动生成）
    triggered_by TEXT NOT NULL DEFAULT 'manual',  
    -- 触发方式：manual（人工）/ auto_optimization（自动优化）/ ab_test（A/B测试）
    
    operator_id TEXT,  -- 操作人ID（人工调整时必填）
    
    -- 效果评估
    effectiveness_score FLOAT CHECK (effectiveness_score >= 0 AND effectiveness_score <= 1),
    -- 效果评分（0-1）：通过准确率提升、用户满意度等综合计算
    
    evaluation_sample_size INT,  -- 评估样本数
    before_accuracy FLOAT CHECK (before_accuracy >= 0 AND before_accuracy <= 1),  -- 调整前准确率
    after_accuracy FLOAT CHECK (after_accuracy >= 0 AND after_accuracy <= 1),     -- 调整后准确率
    
    -- 状态管理
    status TEXT NOT NULL DEFAULT 'active',
    -- 状态：
    --   active: 当前生效
    --   rolled_back: 已回滚
    --   superseded: 被新配置覆盖
    
    rollback_reason TEXT,  -- 回滚原因（status=rolled_back时填写）
    superseded_by BIGINT REFERENCES intent_analysis_adjustments(id),  -- 被哪条记录覆盖
    
    -- 时间戳
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    evaluated_at TIMESTAMPTZ,  -- 效果评估时间
    rolled_back_at TIMESTAMPTZ,  -- 回滚时间
    
    -- 约束
    CHECK (
        (status = 'active' AND rollback_reason IS NULL AND rolled_back_at IS NULL) OR
        (status = 'rolled_back' AND rollback_reason IS NOT NULL AND rolled_back_at IS NOT NULL) OR
        (status = 'superseded' AND superseded_by IS NOT NULL)
    )
);

-- 租户调整历史查询
CREATE INDEX IF NOT EXISTS idx_adjustments_tenant 
    ON intent_analysis_adjustments (tenant_id, created_at DESC);

-- 按类型统计调整频率
CREATE INDEX IF NOT EXISTS idx_adjustments_type 
    ON intent_analysis_adjustments (adjustment_type, status, created_at DESC);

-- 效果评估分析（找出最有效的调整）
CREATE INDEX IF NOT EXISTS idx_adjustments_effectiveness 
    ON intent_analysis_adjustments (effectiveness_score DESC, tenant_id) 
    WHERE effectiveness_score IS NOT NULL;

-- 活跃配置查询（获取当前生效的所有调整）
CREATE INDEX IF NOT EXISTS idx_adjustments_active 
    ON intent_analysis_adjustments (tenant_id, status) 
    WHERE status = 'active';

-- 回滚历史分析
CREATE INDEX IF NOT EXISTS idx_adjustments_rolled_back 
    ON intent_analysis_adjustments (tenant_id, rolled_back_at DESC) 
    WHERE status = 'rolled_back';

-- 表注释
COMMENT ON TABLE intent_analysis_adjustments IS
    '意图分析调整记录 — 追踪配置变更历史、原因和效果评估，支持版本管理和回滚';

COMMENT ON COLUMN intent_analysis_adjustments.adjustment_type IS
    '调整类型：keyword_add/keyword_remove/pattern_add/threshold_change/strategy_change等';

COMMENT ON COLUMN intent_analysis_adjustments.adjustment_detail IS
    '详细调整内容（JSONB）：包含action、old_value、new_value等字段，具体结构按调整类型不同';

COMMENT ON COLUMN intent_analysis_adjustments.triggered_by IS
    '触发方式：manual（人工）/ auto_optimization（自动优化）/ ab_test（A/B测试）';

COMMENT ON COLUMN intent_analysis_adjustments.effectiveness_score IS
    '效果评分（0-1）：综合准确率提升、用户满意度等指标计算。>0.5为有效调整';

COMMENT ON COLUMN intent_analysis_adjustments.status IS
    '状态：active（当前生效）/ rolled_back（已回滚）/ superseded（被覆盖）';

COMMENT ON COLUMN intent_analysis_adjustments.superseded_by IS
    '被哪条记录覆盖（外键指向新调整记录）。用于追踪配置演化链';
