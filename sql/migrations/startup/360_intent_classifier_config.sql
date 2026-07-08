-- Migration 360: Create intent_classifier_config table
--
-- Purpose:
--   意图分类器配置表：支持租户级可配置的分类策略、关键词、模式和阈值。
--   实现配置热更新，无需重启服务即可调整分类逻辑。
--
-- Design notes:
--   - tenant_id = NULL 表示平台级默认配置
--   - 租户配置继承并覆盖平台配置
--   - JSONB 存储灵活配置（keywords_config, patterns_config, enabled_layers）
--   - 支持 A/B 测试和灰度发布（通过 strategy 字段）
--
-- Date: 2026-07-08

CREATE TABLE IF NOT EXISTS intent_classifier_config (
    id SERIAL PRIMARY KEY,
    tenant_id TEXT,  -- NULL表示平台级配置，非NULL表示租户级覆盖
    
    -- 分类器策略
    strategy TEXT NOT NULL DEFAULT 'pattern_layered',  
    -- 策略选项：
    --   baseline_heuristic: 仅关键词匹配（快速但准确率低）
    --   pattern_layered: 硬规则→模式→关键词（生产默认）
    --   llm_fallback: 低置信度时LLM二次分类（高准确率但慢）
    
    -- 启用的检测层（按策略不同组合）
    enabled_layers JSONB NOT NULL DEFAULT '{"hard_rules":true,"pattern_match":true,"keyword_score":true,"llm_fallback":false}',
    -- 示例：{"hard_rules":true,"pattern_match":true,"keyword_score":true,"llm_fallback":false}
    
    -- 关键词配置（按 intent_kind 分组，支持多语言）
    keywords_config JSONB NOT NULL DEFAULT '{}',
    -- 示例：{
    --   "code": {"en": ["function", "algorithm", "implement"], "zh": ["函数", "算法", "实现"]},
    --   "reasoning": {"en": ["solve", "prove", "calculate"], "zh": ["证明", "推导", "计算"]},
    --   "creative": {"en": ["write", "translate", "summarize"], "zh": ["写作", "翻译", "总结"]}
    -- }
    
    -- 模式配置（正则表达式+权重）
    patterns_config JSONB NOT NULL DEFAULT '{}',
    -- 示例：{
    --   "code": [
    --     {"pattern": "```", "weight": 0.95, "description": "代码块标记"},
    --     {"pattern": "(?i)def\\s+\\w+\\s*\\(", "weight": 0.85, "description": "函数定义"}
    --   ],
    --   "reasoning": [
    --     {"pattern": "(?i)solve\\s+for\\s+\\w+", "weight": 0.80, "description": "求解方程"}
    --   ]
    -- }
    
    -- 阈值配置
    confidence_thresholds JSONB NOT NULL DEFAULT '{"high":0.80,"medium":0.60,"low":0.40}',
    -- 置信度等级阈值：high >= 0.80, medium >= 0.60, low >= 0.40
    
    drift_threshold FLOAT NOT NULL DEFAULT 0.3 CHECK (drift_threshold >= 0 AND drift_threshold <= 1),
    -- 意图漂移阈值：超过此值触发模型重新推荐
    
    multi_turn_memory INT NOT NULL DEFAULT 5 CHECK (multi_turn_memory > 0 AND multi_turn_memory <= 20),
    -- 多轮记忆窗口：分析最近N轮对话判断意图演化
    
    -- LLM 兜底分析器配置（用于低置信度场景）
    llm_fallback_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    llm_model TEXT DEFAULT 'gpt-4o-mini',           -- 用于意图分析的LLM模型
    llm_confidence_threshold FLOAT DEFAULT 0.50 CHECK (llm_confidence_threshold >= 0 AND llm_confidence_threshold <= 1),
    -- 低于此置信度触发 LLM 二次分类
    
    -- 元数据
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- 约束：每个租户一套配置（NULL 表示平台默认）
    CONSTRAINT intent_classifier_config_unique_tenant UNIQUE (tenant_id)
);

-- 快速查询租户配置
CREATE INDEX IF NOT EXISTS idx_intent_config_tenant 
    ON intent_classifier_config (tenant_id) 
    WHERE tenant_id IS NOT NULL;

-- 按策略查询（用于 A/B 测试分析）
CREATE INDEX IF NOT EXISTS idx_intent_config_strategy 
    ON intent_classifier_config (strategy, updated_at DESC);

-- 表注释
COMMENT ON TABLE intent_classifier_config IS
    '意图分类器配置 — 租户级可配置的分类策略、关键词、模式和阈值，支持热更新';

COMMENT ON COLUMN intent_classifier_config.tenant_id IS
    'NULL=平台级默认配置，非NULL=租户级覆盖。租户配置优先级高于平台配置';

COMMENT ON COLUMN intent_classifier_config.strategy IS
    '分类策略：baseline_heuristic（快速）/ pattern_layered（平衡，默认）/ llm_fallback（准确但慢）';

COMMENT ON COLUMN intent_classifier_config.keywords_config IS
    '关键词配置（JSONB）：{"intent_kind":{"en":[...],"zh":[...]}}，支持多语言和动态扩展';

COMMENT ON COLUMN intent_classifier_config.patterns_config IS
    '模式配置（JSONB）：{"intent_kind":[{"pattern":"regex","weight":0.95}]}，正则匹配+权重';

COMMENT ON COLUMN intent_classifier_config.drift_threshold IS
    '意图漂移阈值（0-1）：超过此值触发模型重新推荐。默认0.3（30%变化）';

COMMENT ON COLUMN intent_classifier_config.multi_turn_memory IS
    '多轮记忆窗口：分析最近N轮对话。范围1-20，默认5轮';

-- 插入平台级默认配置
INSERT INTO intent_classifier_config (
    tenant_id, 
    strategy, 
    enabled_layers,
    keywords_config,
    patterns_config,
    confidence_thresholds,
    drift_threshold,
    multi_turn_memory,
    llm_fallback_enabled,
    llm_model,
    llm_confidence_threshold
) VALUES (
    NULL,  -- 平台级
    'pattern_layered',
    '{"hard_rules":true,"pattern_match":true,"keyword_score":true,"llm_fallback":false}'::jsonb,
    '{
        "code": {
            "en": ["function", "algorithm", "implement", "code", "debug", "refactor", "class", "method"],
            "zh": ["函数", "算法", "实现", "代码", "调试", "重构", "类", "方法"]
        },
        "reasoning": {
            "en": ["solve", "prove", "calculate", "reason", "derive", "logic", "theorem"],
            "zh": ["证明", "推导", "计算", "推理", "求解", "逻辑", "定理"]
        },
        "creative": {
            "en": ["write", "translate", "summarize", "compose", "draft", "rephrase"],
            "zh": ["写作", "翻译", "总结", "撰写", "草稿", "改写"]
        },
        "chat": {
            "en": ["hello", "hi", "how are you", "thank you", "bye"],
            "zh": ["你好", "您好", "谢谢", "再见", "问候"]
        }
    }'::jsonb,
    '{
        "code": [
            {"pattern": "```", "weight": 0.95, "description": "代码块标记"},
            {"pattern": "(?i)(def|function|class)\\s+\\w+", "weight": 0.85, "description": "函数/类定义"}
        ],
        "reasoning": [
            {"pattern": "(?i)(solve|prove|calculate|证明|推导|计算)", "weight": 0.80, "description": "推理动词"}
        ]
    }'::jsonb,
    '{"high":0.80,"medium":0.60,"low":0.40}'::jsonb,
    0.3,
    5,
    false,
    'gpt-4o-mini',
    0.50
) ON CONFLICT (tenant_id) DO NOTHING;
