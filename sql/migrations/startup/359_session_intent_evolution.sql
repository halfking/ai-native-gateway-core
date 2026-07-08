-- Migration 359: Create session_intent_evolution table
--
-- Purpose:
--   多轮意图分析框架的核心表：记录每轮对话的意图判断、置信度变化和演化轨迹。
--   支持识别用户意图漂移，为智能模型选择提供更精准的依据。
--
-- Design notes:
--   - 每轮对话一条记录，通过 turn_number 排序
--   - intent_candidates 存储多方向判断（JSONB数组）
--   - intent_drift_score 量化意图变化程度（KL散度）
--   - 支持后续分析：意图切换模式、用户行为预测等
--
-- Date: 2026-07-08

CREATE TABLE IF NOT EXISTS session_intent_evolution (
    id BIGSERIAL PRIMARY KEY,
    session_id TEXT NOT NULL,
    tenant_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    turn_number INT NOT NULL,  -- 第几轮对话（从1开始）
    
    -- 意图判断（支持多方向候选）
    intent_candidates JSONB NOT NULL DEFAULT '[]',  -- [{"kind":"code","confidence":0.85,"signals":{"has_code_block":true}}, ...]
    primary_intent TEXT NOT NULL,                   -- 本轮主要意图（chat/code/reasoning/agent/creative/long_context/vision/function_call）
    primary_confidence FLOAT NOT NULL CHECK (primary_confidence >= 0 AND primary_confidence <= 1),
    
    -- 演化跟踪
    previous_primary_intent TEXT,                   -- 上一轮主意图（用于快速判断切换）
    intent_drift_score FLOAT CHECK (intent_drift_score >= 0 AND intent_drift_score <= 1),  -- 意图漂移分数（0=无变化，1=完全不同）
    is_intent_changed BOOLEAN DEFAULT FALSE,        -- 是否发生意图切换
    
    -- 分类器信息
    classifier_version TEXT NOT NULL DEFAULT 'v2_pattern',  -- 分类器版本（v1_keyword/v2_pattern/v3_llm）
    classification_latency_ms INT,                  -- 分类耗时（毫秒）
    
    -- 上下文信息（用于后续分析和调优）
    user_content TEXT,                              -- 用户输入（可选存储，用于误分类分析）
    user_content_hash TEXT,                         -- 内容SHA256哈希（隐私保护）
    context_length INT DEFAULT 0,                   -- 上下文token数
    has_images BOOLEAN DEFAULT FALSE,               -- 是否包含图片
    tool_count INT DEFAULT 0,                       -- 工具数量
    
    classified_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- 索引优化
    CONSTRAINT session_intent_evolution_unique_turn UNIQUE (session_id, turn_number)
);

-- 按会话查询历史意图（多轮分析核心查询）
CREATE INDEX IF NOT EXISTS idx_session_intent_session 
    ON session_intent_evolution (session_id, turn_number DESC);

-- 租户级统计和分析
CREATE INDEX IF NOT EXISTS idx_session_intent_tenant 
    ON session_intent_evolution (tenant_id, classified_at DESC);

-- 意图切换分析（识别用户行为模式）
CREATE INDEX IF NOT EXISTS idx_session_intent_changed 
    ON session_intent_evolution (is_intent_changed, tenant_id) 
    WHERE is_intent_changed = TRUE;

-- 主意图分布统计
CREATE INDEX IF NOT EXISTS idx_session_intent_primary 
    ON session_intent_evolution (primary_intent, tenant_id, classified_at DESC);

-- 内容哈希去重（用于相似查询识别）
CREATE INDEX IF NOT EXISTS idx_session_intent_content_hash 
    ON session_intent_evolution (user_content_hash) 
    WHERE user_content_hash IS NOT NULL;

-- 表注释
COMMENT ON TABLE session_intent_evolution IS
    '多轮意图分析 — 记录每轮对话的意图判断和演化轨迹，支持意图漂移检测';

COMMENT ON COLUMN session_intent_evolution.intent_candidates IS
    '多方向意图候选（JSONB数组）：[{"kind":"code","confidence":0.85,"signals":{"has_code_block":true}}]';

COMMENT ON COLUMN session_intent_evolution.intent_drift_score IS
    '意图漂移分数（KL散度）：0=无变化，1=完全不同。超过drift_threshold触发重新推荐模型';

COMMENT ON COLUMN session_intent_evolution.classifier_version IS
    '分类器版本：v1_keyword（仅关键词）/ v2_pattern（模式+关键词）/ v3_llm（LLM兜底）';

COMMENT ON COLUMN session_intent_evolution.user_content_hash IS
    'SHA256内容哈希（隐私保护）：用于相似查询识别，不存储原始敏感内容';
