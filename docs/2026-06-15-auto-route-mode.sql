-- 2026-06-15-auto-route-mode.sql
-- v2.0 Auto Route Mode — 智能路由（model=auto + 三模式）
--
-- 设计目标：
--   1. model=auto 时自动识别任务类型 → 6 维加权选最优模型
--   2. 三种 profile：smart / speed_first / cost_first
--   3. 实时指数表（5min 滚动）+ 客户成本表
--
-- 新增 3 张表：
--   - model_task_index         : 模型 × 任务类型 × 时间桶 的 5min 聚合
--   - credential_model_index   : 凭据 × 模型 × 时间桶 的实时评分（含三模式预计算）
--   - api_key_auto_profile     : API Key profile 粘性记忆
--
-- request_logs 新增 5 列：
--   - is_auto_request   : 是否 auto 请求
--   - task_type         : 任务类型
--   - auto_profile      : 当时 profile
--   - auto_decision     : 决策快照 JSONB（top-3 candidates + 评分明细）
--   - auto_confidence   : 分类置信度 0-1

BEGIN;

-- ============================================================================
-- (a) model_task_index — 模型 × 任务类型 × 时间桶 指数
-- ============================================================================
CREATE TABLE IF NOT EXISTS model_task_index (
    bucket               TIMESTAMPTZ NOT NULL,
    canonical_id         INT NOT NULL REFERENCES models_canonical(id),
    task_type            TEXT NOT NULL,    -- chat/reasoning/code/agent/creative/long_context/vision/function_call
    sample_count         INT NOT NULL DEFAULT 0,
    success_rate         NUMERIC(5,4),
    avg_latency_ms       INT,
    p95_latency_ms       INT,
    avg_cost_per_1k_usd  NUMERIC(10,6),
    primary_credential_id BIGINT REFERENCES credentials(id),
    updated_at           TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (bucket, canonical_id, task_type)
);
CREATE INDEX IF NOT EXISTS idx_mti_task_bucket    ON model_task_index (task_type, bucket DESC);
CREATE INDEX IF NOT EXISTS idx_mti_canonical_bkt  ON model_task_index (canonical_id, bucket DESC);
COMMENT ON TABLE model_task_index IS 'Auto route: per-model-per-task 5min rolled-up performance (success/latency/cost)';

-- ============================================================================
-- (b) credential_model_index — 凭据实时指数（含三模式预计算分）
-- ============================================================================
CREATE TABLE IF NOT EXISTS credential_model_index (
    bucket                  TIMESTAMPTZ NOT NULL,
    credential_id           BIGINT NOT NULL REFERENCES credentials(id),
    raw_model               TEXT NOT NULL,
    canonical_id            INT,
    -- 静态属性（从 credentials / model_offers 复制）
    billing_mode            TEXT,
    unit_price_in_per_1m    NUMERIC(10,4),
    unit_price_out_per_1m   NUMERIC(10,4),
    context_window          INT,
    -- 动态属性
    success_rate            NUMERIC(5,4),
    p95_latency_ms          INT,
    active_sessions         INT DEFAULT 0,
    concurrency_limit       INT,
    pressure_ratio          NUMERIC(5,4),  -- active_sessions / concurrency_limit
    -- 三模式预计算分（0-100）
    score_smart             NUMERIC(8,4),
    score_speed_first       NUMERIC(8,4),
    score_cost_first        NUMERIC(8,4),
    updated_at              TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (bucket, credential_id, raw_model)
);
CREATE INDEX IF NOT EXISTS idx_cmi_pressure ON credential_model_index (bucket, pressure_ratio DESC);
CREATE INDEX IF NOT EXISTS idx_cmi_score    ON credential_model_index (canonical_id, score_smart DESC, bucket DESC);
COMMENT ON TABLE credential_model_index IS 'Auto route: per-credential 5min rolled-up live score with 3 profile precomputed';

-- ============================================================================
-- (c) api_key_auto_profile — API Key profile 粘性记忆
-- ============================================================================
CREATE TABLE IF NOT EXISTS api_key_auto_profile (
    api_key_id       INT PRIMARY KEY REFERENCES api_keys(id),
    profile          TEXT NOT NULL DEFAULT 'smart'
                       CHECK (profile IN ('smart', 'speed_first', 'cost_first')),
    first_chosen_at  TIMESTAMPTZ DEFAULT NOW(),
    last_used_at     TIMESTAMPTZ DEFAULT NOW(),
    updated_at       TIMESTAMPTZ DEFAULT NOW()
);
COMMENT ON TABLE api_key_auto_profile IS 'Auto route: per-API-Key profile preference (sticky 30min)';

-- ============================================================================
-- (d) request_logs 新增 5 列
-- ============================================================================
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS is_auto_request BOOLEAN DEFAULT FALSE;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS task_type       TEXT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS auto_profile    TEXT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS auto_decision   JSONB;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS auto_confidence NUMERIC(4,3);
CREATE INDEX IF NOT EXISTS idx_request_logs_auto ON request_logs (is_auto_request, task_type, ts DESC);
COMMENT ON COLUMN request_logs.is_auto_request IS 'Auto route: was this request model=auto?';
COMMENT ON COLUMN request_logs.task_type       IS 'Auto route: classified task type (chat/reasoning/code/...)';
COMMENT ON COLUMN request_logs.auto_profile    IS 'Auto route: profile used (smart/speed_first/cost_first)';
COMMENT ON COLUMN request_logs.auto_decision   IS 'Auto route: top-N candidates + chosen model + scoring breakdown';
COMMENT ON COLUMN request_logs.auto_confidence IS 'Auto route: classification confidence 0-1';

COMMIT;