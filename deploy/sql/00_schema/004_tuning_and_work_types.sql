-- ============================================================================
-- llm-gateway-go 工作类型和调优相关表结构
-- ============================================================================

-- ----------------------------------------------------------------------------
-- Work Type Config 表 - 工作类型配置
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.work_type_config (
    key TEXT PRIMARY KEY,
    label TEXT NOT NULL,
    category TEXT NOT NULL,
    l1_task_type TEXT NOT NULL,
    default_profile TEXT NOT NULL DEFAULT 'smart'
        CHECK (default_profile IN ('smart', 'speed_first', 'cost_first')),
    tags TEXT[] NOT NULL DEFAULT '{}',
    prompt_keywords TEXT[] NOT NULL DEFAULT '{}',
    acc_task_type TEXT,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order INT NOT NULL DEFAULT 0,
    synced_from_acc_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    system_prompt TEXT
);
CREATE INDEX IF NOT EXISTS idx_work_type_config_category ON work_type_config(category, sort_order);
CREATE INDEX IF NOT EXISTS idx_work_type_config_l1 ON work_type_config(l1_task_type);

-- ----------------------------------------------------------------------------
-- Work Type Model Route 表 - 工作类型模型路由
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.work_type_model_route (
    id SERIAL PRIMARY KEY,
    work_type_key TEXT NOT NULL REFERENCES work_type_config(key) ON DELETE CASCADE,
    canonical_name TEXT NOT NULL,
    weight NUMERIC(5,2) NOT NULL DEFAULT 1.0,
    min_score NUMERIC(8,4) NOT NULL DEFAULT 0,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    UNIQUE (work_type_key, canonical_name)
);
CREATE INDEX IF NOT EXISTS idx_wtmr_work_type ON work_type_model_route(work_type_key);

-- ----------------------------------------------------------------------------
-- Tuning Signals 表 - 调优信号（用于自动路由）
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.tuning_signals (
    id BIGSERIAL PRIMARY KEY,
    request_id TEXT NOT NULL,
    session_id TEXT,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    task_type TEXT NOT NULL,
    classifier TEXT NOT NULL,
    confidence NUMERIC(4,3),
    chosen_model TEXT,
    canonical_id INT,
    success_score NUMERIC(3,2) NOT NULL DEFAULT 0.5,
    latency_score NUMERIC(3,2) NOT NULL DEFAULT 0.5,
    cost_score NUMERIC(3,2) NOT NULL DEFAULT 0.5,
    drift_flag BOOLEAN NOT NULL DEFAULT FALSE,
    quality_score NUMERIC(3,2) NOT NULL DEFAULT 0.5,
    latency_ms INT,
    cost_usd NUMERIC(10,6),
    prompt_tokens INT,
    completion_tokens INT,
    signal_payload JSONB,
    strategy TEXT NOT NULL DEFAULT 'pattern_layered'
        CHECK (strategy IN ('baseline_heuristic', 'pattern_layered', 'llm_fallback')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_tuning_signals_ts ON tuning_signals(ts DESC);
CREATE INDEX IF NOT EXISTS idx_tuning_signals_task ON tuning_signals(task_type);
CREATE INDEX IF NOT EXISTS idx_tuning_signals_session ON tuning_signals(session_id);
CREATE INDEX IF NOT EXISTS idx_tuning_signals_strategy_ts ON tuning_signals(strategy, ts DESC);
CREATE INDEX IF NOT EXISTS idx_tuning_signals_strategy_task ON tuning_signals(strategy, task_type, ts DESC) WHERE task_type IS NOT NULL;

-- ----------------------------------------------------------------------------
-- Tuning Signals Materialized Views - 调优信号物化视图（预聚合）
-- ----------------------------------------------------------------------------
-- 5分钟桶物化视图（保留7天）
CREATE MATERIALIZED VIEW IF NOT EXISTS tuning_signals_5m AS
SELECT
    date_trunc('hour', ts)
        + (FLOOR(EXTRACT(MINUTE FROM ts)::int / 5) * interval '5 minutes')
        AS bucket,
    task_type,
    classifier,
    COUNT(*) AS total,
    AVG(quality_score) AS avg_quality,
    AVG(success_score) AS avg_success,
    AVG(latency_score) AS avg_latency,
    AVG(cost_score) AS avg_cost,
    SUM(CASE WHEN drift_flag THEN 1 ELSE 0 END)::float / NULLIF(COUNT(*), 0) AS drift_rate
FROM tuning_signals
WHERE ts >= NOW() - INTERVAL '7 days'
GROUP BY 1, 2, 3;

CREATE UNIQUE INDEX IF NOT EXISTS idx_tuning_signals_5m_pk ON tuning_signals_5m(bucket, task_type, classifier);
CREATE INDEX IF NOT EXISTS idx_tuning_signals_5m_task_ts ON tuning_signals_5m(task_type, classifier, bucket DESC);

-- 1天桶物化视图（保留90天）
CREATE MATERIALIZED VIEW IF NOT EXISTS tuning_signals_daily AS
SELECT
    date_trunc('day', ts) AS bucket,
    task_type,
    classifier,
    COUNT(*) AS total,
    AVG(quality_score) AS avg_quality,
    AVG(success_score) AS avg_success,
    AVG(latency_score) AS avg_latency,
    AVG(cost_score) AS avg_cost,
    SUM(CASE WHEN drift_flag THEN 1 ELSE 0 END)::float / NULLIF(COUNT(*), 0) AS drift_rate
FROM tuning_signals
WHERE ts >= NOW() - INTERVAL '90 days'
GROUP BY 1, 2, 3;

CREATE UNIQUE INDEX IF NOT EXISTS idx_tuning_signals_daily_pk ON tuning_signals_daily(bucket, task_type, classifier);
CREATE INDEX IF NOT EXISTS idx_tuning_signals_daily_task_ts ON tuning_signals_daily(task_type, classifier, bucket DESC);

-- ----------------------------------------------------------------------------
-- Tuning Params 表 - 调优参数
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.tuning_params (
    id SERIAL PRIMARY KEY,
    task_type TEXT NOT NULL,
    profile TEXT NOT NULL,
    param_key TEXT NOT NULL,
    param_value TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(task_type, profile, param_key)
);
CREATE INDEX IF NOT EXISTS idx_tuning_params_task_profile ON tuning_params(task_type, profile);

-- ----------------------------------------------------------------------------
-- Tuning Proposals 表 - 调优建议
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.tuning_proposals (
    id SERIAL PRIMARY KEY,
    task_type TEXT NOT NULL,
    profile TEXT NOT NULL,
    model_name TEXT NOT NULL,
    score NUMERIC(5,4) NOT NULL,
    sample_count INT NOT NULL DEFAULT 0,
    status VARCHAR(32) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'rejected', 'auto_approved')),
    proposed_by TEXT NOT NULL,
    approved_by TEXT,
    auto_tuned BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_tuning_proposals_task_profile ON tuning_proposals(task_type, profile);
CREATE INDEX IF NOT EXISTS idx_tuning_proposals_status ON tuning_proposals(status);

-- ----------------------------------------------------------------------------
-- Auto Tune Audit 表 - 自动调优审计
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.auto_tune_audit (
    id SERIAL PRIMARY KEY,
    action TEXT NOT NULL,
    task_type TEXT,
    profile TEXT,
    model_name TEXT,
    actor TEXT,
    details JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_auto_tune_audit_ts ON auto_tune_audit(created_at DESC);