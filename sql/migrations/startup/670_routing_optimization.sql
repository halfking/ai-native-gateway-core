-- 670_routing_optimization.sql
-- P2.2 AUTO 路由优化插件数据表
-- 设计文档: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md §3

-- =============================================================================
-- 1. 优化状态表 (routing_optimization_state)
-- 存储插件的优化参数和性能指标
-- =============================================================================

CREATE TABLE IF NOT EXISTS routing_optimization_state (
    id BIGSERIAL PRIMARY KEY,
    version INTEGER NOT NULL DEFAULT 1,
    
    -- 分类器参数 (PostClassify 置信度调整)
    classifier_weights JSONB NOT NULL DEFAULT '{}',  -- {task_type: weight}
    confidence_thresholds JSONB NOT NULL DEFAULT '{}',  -- {task_type: min_confidence}
    
    -- 推荐器参数 (RecommendModel 多目标优化)
    recommender_weights JSONB NOT NULL DEFAULT '{"quality": 0.4, "cost": 0.3, "latency": 0.2, "availability": 0.1}',
    exploration_rate FLOAT NOT NULL DEFAULT 0.05,  -- ε-greedy 探索率
    
    -- 在线学习参数 (AdaptiveLearner)
    learning_rate FLOAT NOT NULL DEFAULT 0.01,
    adaptation_window INTEGER NOT NULL DEFAULT 1000,  -- 滑动窗口大小
    
    -- 性能指标 (从 routing_optimization_metrics 聚合)
    overall_accuracy FLOAT,  -- 整体准确率 [0, 1]
    accuracy_by_task JSONB,  -- {task_type: accuracy}
    accuracy_by_provider JSONB,  -- {provider: accuracy}
    
    -- 版本管理
    activated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deactivated_at TIMESTAMP,  -- NULL = 当前激活版本
    created_by TEXT NOT NULL DEFAULT 'system',
    notes TEXT,  -- 版本说明 (e.g., "Week 2 Day 5: 启用人工标注x2权重")
    
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    -- 约束
    CONSTRAINT accuracy_range CHECK (overall_accuracy IS NULL OR (overall_accuracy >= 0 AND overall_accuracy <= 1)),
    CONSTRAINT exploration_rate_range CHECK (exploration_rate >= 0 AND exploration_rate <= 1),
    CONSTRAINT learning_rate_range CHECK (learning_rate > 0 AND learning_rate <= 1)
);

-- 只保留一个激活版本 (deactivated_at IS NULL)
CREATE UNIQUE INDEX IF NOT EXISTS idx_opt_state_active ON routing_optimization_state (activated_at DESC)
    WHERE deactivated_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_opt_state_version ON routing_optimization_state (version DESC);
CREATE INDEX IF NOT EXISTS idx_opt_state_created ON routing_optimization_state (created_at DESC);

COMMENT ON TABLE routing_optimization_state IS 'P2.2 路由优化插件参数和状态 (版本化存储)';
COMMENT ON COLUMN routing_optimization_state.version IS '参数版本号 (单调递增)';
COMMENT ON COLUMN routing_optimization_state.recommender_weights IS 'Multi-objective 权重: quality/cost/latency/availability';
COMMENT ON COLUMN routing_optimization_state.exploration_rate IS 'ε-greedy 探索率 (5% = 探索新模型)';
COMMENT ON COLUMN routing_optimization_state.adaptation_window IS '在线学习滑动窗口大小 (最近N次请求)';

-- =============================================================================
-- 2. 实时反馈日志表 (routing_feedback_log)
-- 存储每次路由决策的实时反馈 (5分钟后聚合到 metrics 表)
-- =============================================================================

CREATE TABLE IF NOT EXISTS routing_feedback_log (
    id BIGSERIAL PRIMARY KEY,
    request_id TEXT NOT NULL,
    
    -- 路由决策 (插件预测)
    task_type TEXT NOT NULL,  -- autoroute.TaskType
    predicted_provider TEXT NOT NULL,  -- "openai", "anthropic", etc.
    confidence FLOAT NOT NULL,  -- 分类置信度 [0, 1]
    
    -- 实际结果 (从 request_logs 异步采集)
    actual_latency_ms INTEGER,  -- 实际延迟 (毫秒)
    actual_cost FLOAT,  -- 实际成本 (USD)
    success BOOLEAN NOT NULL,  -- 请求是否成功 (2xx)
    error_type TEXT,  -- 失败类型: "timeout", "rate_limit", "5xx", etc.
    
    -- 请求上下文
    profile TEXT,  -- autoroute.Profile: "smart", "speed_first", etc.
    user_id TEXT,  -- API key ID
    session_id TEXT,  -- X-Gw-Session-Id
    
    -- 人工纠正 (来自 P2.1 training_human_annotations 表)
    -- 人工标注权重 ×2 (ground truth)
    has_human_correction BOOLEAN NOT NULL DEFAULT FALSE,
    correct_provider TEXT,  -- 人工标注的正确 provider
    correction_reason TEXT,  -- 标注原因: "performance", "cost", "quality"
    annotator TEXT,  -- 标注人员
    annotated_at TIMESTAMP,
    
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    -- 约束
    CONSTRAINT confidence_range CHECK (confidence >= 0 AND confidence <= 1),
    CONSTRAINT cost_non_negative CHECK (actual_cost IS NULL OR actual_cost >= 0),
    CONSTRAINT latency_non_negative CHECK (actual_latency_ms IS NULL OR actual_latency_ms >= 0)
);

CREATE INDEX IF NOT EXISTS idx_feedback_request ON routing_feedback_log (request_id);
CREATE INDEX IF NOT EXISTS idx_feedback_task_type ON routing_feedback_log (task_type, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_feedback_provider ON routing_feedback_log (predicted_provider, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_feedback_created ON routing_feedback_log (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_feedback_user ON routing_feedback_log (user_id, created_at DESC) WHERE user_id IS NOT NULL;

-- 人工标注快速查询 (用于计算加权准确率)
CREATE INDEX IF NOT EXISTS idx_feedback_correction ON routing_feedback_log (has_human_correction, created_at DESC)
    WHERE has_human_correction = TRUE;

COMMENT ON TABLE routing_feedback_log IS 'P2.2 路由决策实时反馈日志 (支持人工标注×2权重)';
COMMENT ON COLUMN routing_feedback_log.has_human_correction IS '是否包含人工标注 (标注权重×2)';
COMMENT ON COLUMN routing_feedback_log.correct_provider IS '人工标注的正确 provider (ground truth)';

-- =============================================================================
-- 3. 优化指标聚合表 (routing_optimization_metrics)
-- 5分钟粒度聚合指标 (从 routing_feedback_log 定时聚合)
-- =============================================================================

CREATE TABLE IF NOT EXISTS routing_optimization_metrics (
    id BIGSERIAL PRIMARY KEY,
    time_bucket TIMESTAMP NOT NULL,  -- 5分钟时间桶 (truncated to 5min)
    
    -- 分组维度
    task_type TEXT,  -- NULL = 全局聚合
    predicted_provider TEXT,  -- NULL = 按 task_type 聚合
    
    -- 聚合指标
    total_requests INTEGER NOT NULL DEFAULT 0,
    successful_requests INTEGER NOT NULL DEFAULT 0,
    failed_requests INTEGER NOT NULL DEFAULT 0,
    accuracy_rate FLOAT,  -- successful / total
    
    avg_confidence FLOAT,  -- 平均置信度
    avg_latency_ms INTEGER,  -- 平均延迟
    avg_cost FLOAT,  -- 平均成本
    
    p50_latency_ms INTEGER,  -- 中位延迟
    p95_latency_ms INTEGER,  -- P95延迟
    p99_latency_ms INTEGER,  -- P99延迟
    
    -- 人工标注反馈统计
    human_corrections INTEGER NOT NULL DEFAULT 0,  -- 人工标注数量
    human_accuracy_rate FLOAT,  -- 人工标注准确率 (加权×2)
    
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    -- 约束
    CONSTRAINT unique_metric_bucket UNIQUE (time_bucket, task_type, predicted_provider),
    CONSTRAINT accuracy_rate_range CHECK (accuracy_rate IS NULL OR (accuracy_rate >= 0 AND accuracy_rate <= 1)),
    CONSTRAINT human_accuracy_rate_range CHECK (human_accuracy_rate IS NULL OR (human_accuracy_rate >= 0 AND human_accuracy_rate <= 1))
);

CREATE INDEX IF NOT EXISTS idx_metrics_bucket ON routing_optimization_metrics (time_bucket DESC);
CREATE INDEX IF NOT EXISTS idx_metrics_task ON routing_optimization_metrics (task_type, time_bucket DESC) WHERE task_type IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_metrics_provider ON routing_optimization_metrics (predicted_provider, time_bucket DESC) WHERE predicted_provider IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_metrics_created ON routing_optimization_metrics (created_at DESC);

COMMENT ON TABLE routing_optimization_metrics IS 'P2.2 路由优化指标聚合表 (5分钟粒度)';
COMMENT ON COLUMN routing_optimization_metrics.time_bucket IS '时间桶 (5分钟对齐): date_trunc(''minute'', ts)::timestamp - (EXTRACT(minute FROM ts)::int % 5) * interval ''1 minute''';
COMMENT ON COLUMN routing_optimization_metrics.human_corrections IS '该时间桶内的人工标注数量';
COMMENT ON COLUMN routing_optimization_metrics.human_accuracy_rate IS '人工标注准确率 (权重×2)';

-- =============================================================================
-- 4. 用户亲和力缓存表 (routing_user_affinity)
-- 用户历史任务类型分布和 provider 偏好 (Redis + PostgreSQL 双写)
-- =============================================================================

CREATE TABLE IF NOT EXISTS routing_user_affinity (
    id BIGSERIAL PRIMARY KEY,
    user_id TEXT NOT NULL,  -- API key ID
    
    -- 任务类型分布 (最近100次请求)
    task_type_distribution JSONB NOT NULL DEFAULT '{}',  -- {task_type: count}
    preferred_providers JSONB NOT NULL DEFAULT '{}',  -- {provider: preference_score [0, 1]}
    
    -- 统计数据
    total_requests INTEGER NOT NULL DEFAULT 0,
    last_request_at TIMESTAMP NOT NULL,
    
    -- 会话模式识别 (基于请求模式自动识别)
    session_pattern TEXT,  -- "ide", "cli", "web", "api"
    
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    -- 约束
    CONSTRAINT unique_user_affinity UNIQUE (user_id),
    CONSTRAINT total_requests_non_negative CHECK (total_requests >= 0)
);

CREATE INDEX IF NOT EXISTS idx_affinity_user ON routing_user_affinity (user_id);
CREATE INDEX IF NOT EXISTS idx_affinity_updated ON routing_user_affinity (updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_affinity_last_request ON routing_user_affinity (last_request_at DESC);

COMMENT ON TABLE routing_user_affinity IS 'P2.2 用户亲和力缓存 (任务类型分布和 provider 偏好)';
COMMENT ON COLUMN routing_user_affinity.task_type_distribution IS '任务类型分布: {"code": 60, "chat": 30, "reasoning": 10}';
COMMENT ON COLUMN routing_user_affinity.preferred_providers IS 'Provider偏好分数: {"anthropic": 0.8, "openai": 0.6}';
COMMENT ON COLUMN routing_user_affinity.session_pattern IS '会话模式: ide(高频code), cli(reasoning+code混合), web(chat为主)';

-- =============================================================================
-- 初始化数据：插入默认优化状态 (Week 1 baseline)
-- =============================================================================

INSERT INTO routing_optimization_state (
    version,
    classifier_weights,
    confidence_thresholds,
    recommender_weights,
    exploration_rate,
    learning_rate,
    adaptation_window,
    activated_at,
    created_by,
    notes
) VALUES (
    1,
    '{}',  -- 初始权重为空 (Week 1: no-op 模式)
    '{}',  -- 初始阈值为空
    '{"quality": 0.4, "cost": 0.3, "latency": 0.2, "availability": 0.1}',
    0.05,  -- 5% 探索率
    0.01,  -- 1% 学习率
    1000,  -- 最近1000次请求
    CURRENT_TIMESTAMP,
    'system',
    'Week 1 baseline: no-op mode (插件框架验证)'
) ON CONFLICT DO NOTHING;
