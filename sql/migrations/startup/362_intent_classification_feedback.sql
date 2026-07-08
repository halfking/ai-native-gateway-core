-- Migration 362: Create intent_classification_feedback table
--
-- Purpose:
--   意图分类反馈表：收集人工标注和用户行为反馈，用于评估分类准确率和自动优化。
--   支持显式反馈（人工标注）和隐式反馈（用户行为信号）。
--
-- Design notes:
--   - 每次分类一条记录（可选存储，按采样率）
--   - 人工标注用于训练和评估（actual_intent, is_correct）
--   - 用户行为作为隐式反馈（accepted_model, retry_count, session_duration）
--   - 内容哈希存储保护隐私
--
-- Date: 2026-07-08

CREATE TABLE IF NOT EXISTS intent_classification_feedback (
    id BIGSERIAL PRIMARY KEY,
    session_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    tenant_id TEXT NOT NULL,
    
    -- 分类结果（系统预测）
    predicted_intent TEXT NOT NULL,  -- 预测的意图类型
    predicted_confidence FLOAT NOT NULL CHECK (predicted_confidence >= 0 AND predicted_confidence <= 1),
    
    -- 人工标注（显式反馈）
    actual_intent TEXT,  -- 人工标注的真实意图
    is_correct BOOLEAN,  -- 分类是否正确（由actual_intent自动判断或人工标注）
    annotator_id TEXT,   -- 标注人ID
    annotated_at TIMESTAMPTZ,  -- 标注时间
    annotation_notes TEXT,  -- 标注备注（说明为什么分类错误等）
    
    -- 用户行为反馈（隐式信号）
    user_accepted_model BOOLEAN,  -- 用户是否接受推荐的模型（未切换模型=接受）
    user_switched_to_model TEXT,  -- 用户手动切换到的模型（如果有）
    user_retry_count INT DEFAULT 0 CHECK (user_retry_count >= 0),  -- 用户重试次数（高重试=可能分类错误）
    session_duration_sec INT CHECK (session_duration_sec >= 0),  -- 会话持续时长（秒）
    user_satisfaction_score INT CHECK (user_satisfaction_score >= 1 AND user_satisfaction_score <= 5),  -- 用户满意度评分1-5
    
    -- 上下文信息
    user_content_hash TEXT,  -- 用户输入内容的SHA256哈希（隐私保护，不存储原文）
    classification_context JSONB,  -- 分类时的上下文信息
    -- 示例：{"context_length":1024,"has_images":false,"tool_count":2,"classifier_version":"v2_pattern"}
    
    -- 关联信息
    evolution_id BIGINT,  -- 关联的session_intent_evolution记录ID（可选）
    
    -- 时间戳
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- 索引
    CONSTRAINT intent_feedback_unique_request UNIQUE (request_id)
);

-- 租户反馈查询（用于效果评估）
CREATE INDEX IF NOT EXISTS idx_feedback_tenant 
    ON intent_classification_feedback (tenant_id, created_at DESC);

-- 准确率统计（按意图类型）
CREATE INDEX IF NOT EXISTS idx_feedback_correct 
    ON intent_classification_feedback (is_correct, predicted_intent, tenant_id) 
    WHERE is_correct IS NOT NULL;

-- 待标注队列（优先标注低置信度样本）
CREATE INDEX IF NOT EXISTS idx_feedback_unannotated 
    ON intent_classification_feedback (predicted_confidence ASC, created_at DESC) 
    WHERE actual_intent IS NULL;

-- 用户行为分析（识别不满意的分类）
CREATE INDEX IF NOT EXISTS idx_feedback_user_behavior 
    ON intent_classification_feedback (user_retry_count DESC, tenant_id) 
    WHERE user_retry_count > 0;

-- 会话关联查询
CREATE INDEX IF NOT EXISTS idx_feedback_session 
    ON intent_classification_feedback (session_id, created_at DESC);

-- 内容哈希去重（识别相似查询）
CREATE INDEX IF NOT EXISTS idx_feedback_content_hash 
    ON intent_classification_feedback (user_content_hash, predicted_intent) 
    WHERE user_content_hash IS NOT NULL;

-- 标注时间排序（追踪标注进度）
CREATE INDEX IF NOT EXISTS idx_feedback_annotated 
    ON intent_classification_feedback (annotated_at DESC) 
    WHERE annotated_at IS NOT NULL;

-- 表注释
COMMENT ON TABLE intent_classification_feedback IS
    '意图分类反馈 — 收集人工标注和用户行为反馈，用于评估准确率和自动优化';

COMMENT ON COLUMN intent_classification_feedback.predicted_intent IS
    '系统预测的意图类型（chat/code/reasoning/agent/creative/long_context/vision/function_call）';

COMMENT ON COLUMN intent_classification_feedback.is_correct IS
    '分类是否正确：TRUE=正确，FALSE=错误，NULL=未标注。由actual_intent与predicted_intent对比得出';

COMMENT ON COLUMN intent_classification_feedback.user_accepted_model IS
    '用户是否接受推荐的模型（隐式反馈）：TRUE=未切换模型，FALSE=手动切换了模型';

COMMENT ON COLUMN intent_classification_feedback.user_retry_count IS
    '用户重试次数（隐式反馈）：高重试次数可能表示模型推荐不准确或结果不满意';

COMMENT ON COLUMN intent_classification_feedback.user_content_hash IS
    'SHA256内容哈希（隐私保护）：不存储原始用户输入，仅用于相似查询识别和去重';

COMMENT ON COLUMN intent_classification_feedback.classification_context IS
    '分类上下文（JSONB）：{"context_length":1024,"has_images":false,"classifier_version":"v2_pattern"}';

-- 触发器：自动计算 is_correct
CREATE OR REPLACE FUNCTION update_intent_feedback_correctness()
RETURNS TRIGGER AS $$
BEGIN
    -- 当actual_intent被设置时，自动计算is_correct
    IF NEW.actual_intent IS NOT NULL AND OLD.actual_intent IS NULL THEN
        NEW.is_correct := (NEW.predicted_intent = NEW.actual_intent);
        NEW.annotated_at := NOW();
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trigger_intent_feedback_correctness
BEFORE UPDATE ON intent_classification_feedback
FOR EACH ROW
EXECUTE FUNCTION update_intent_feedback_correctness();

COMMENT ON FUNCTION update_intent_feedback_correctness() IS
    '触发器函数：当actual_intent被标注时，自动计算is_correct并设置annotated_at';
