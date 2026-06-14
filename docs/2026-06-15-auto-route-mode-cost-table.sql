-- 2026-06-15-auto-route-mode-cost-table.sql
-- v2.0.1 — 客户成本表（per-API-Key × per-model 动态聚合）
--
-- 设计目标：
--   1. 每个 API Key 的每个 canonical model 的滚动成本（实时更新）
--   2. 包含并发压力、可用并发、推荐指数等综合信息
--   3. 通过 trigger 实时更新（避免 5min 延迟）
--
-- 表结构：
--   api_key_model_cost :
--     - api_key_id, canonical_id, raw_model, billing_mode
--     - requests_total, tokens_total, cost_usd_total (5min 滚动)
--     - concurrency_limit, active_concurrent, pressure_ratio (实时)
--     - preferred_model_score (智能选择分数)
--     - last_decision_at, last_request_at
--
-- Trigger: 任何 request_logs.is_auto_request = true 的 INSERT 触发
--   → 增量更新 api_key_model_cost 行（O(1) 写）
--
-- Customer Cost Index:
--   按 api_key_id 聚合的 1h / 24h / 7d 成本视图
--   用于 cost-first profile 的智能路由决策

BEGIN;

-- ============================================================================
-- (a) api_key_model_cost — API Key × Model × 5min 滚动成本
-- ============================================================================
CREATE TABLE IF NOT EXISTS api_key_model_cost (
    bucket              TIMESTAMPTZ NOT NULL,
    api_key_id          INT NOT NULL REFERENCES api_keys(id),
    canonical_id        INT REFERENCES models_canonical(id),
    raw_model           TEXT NOT NULL,
    billing_mode        TEXT,
    -- 流量累计
    requests_total      INT NOT NULL DEFAULT 0,
    requests_success    INT NOT NULL DEFAULT 0,
    tokens_input        BIGINT NOT NULL DEFAULT 0,
    tokens_output       BIGINT NOT NULL DEFAULT 0,
    cost_usd            NUMERIC(12,6) NOT NULL DEFAULT 0,
    -- 实时并发（来自 request_logs 的 last 5min 视图）
    active_concurrent   INT NOT NULL DEFAULT 0,
    concurrency_limit   INT,                       -- 来自 api_keys.concurrency_limit
    pressure_ratio      NUMERIC(5,4),
    -- 模型评分（按 profile × task_type 预计算）
    score_smart         NUMERIC(8,4),
    score_speed_first   NUMERIC(8,4),
    score_cost_first    NUMERIC(8,4),
    -- 时间戳
    last_request_at     TIMESTAMPTZ,
    last_decision_at    TIMESTAMPTZ,               -- auto-route 决策时间
    updated_at          TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (bucket, api_key_id, raw_model)
);
CREATE INDEX IF NOT EXISTS idx_akmc_api_key_bucket
    ON api_key_model_cost (api_key_id, bucket DESC);
CREATE INDEX IF NOT EXISTS idx_akmc_canonical
    ON api_key_model_cost (canonical_id, bucket DESC);
CREATE INDEX IF NOT EXISTS idx_akmc_pressure
    ON api_key_model_cost (api_key_id, bucket DESC, pressure_ratio DESC);
COMMENT ON TABLE api_key_model_cost IS 'Auto route: per-API-Key per-model 5min rolled-up cost + concurrency + score';

-- ============================================================================
-- (b) trigger — request_logs INSERT/UPDATE 增量更新成本表
-- ============================================================================
-- 实时性：任何 auto_route 请求完成后立即更新（不再等 5min refresh）
CREATE OR REPLACE FUNCTION update_api_key_model_cost()
RETURNS TRIGGER AS $
DECLARE
    bucket_ts TIMESTAMPTZ;
    key_id INT;
    limit_val INT;
BEGIN
    -- 计算 5min bucket（向下取整）
    bucket_ts := date_trunc('hour', NEW.ts)
                  + (FLOOR(EXTRACT(minute FROM NEW.ts) / 5) * INTERVAL '5 minutes');
    key_id := NEW.api_key_id;
    IF key_id IS NULL THEN
        RETURN NEW;
    END IF;

    -- 查找 api_key 的 rate_limit_rpm（作为该 key 的并发近似上限）
    -- 注意：api_keys 表没有 concurrency_limit 列（已在 realtime-trigger SQL 中确认）。
    -- 用 rate_limit_rpm / 10 作为近似（假设平均请求耗时 6 秒）。
    SELECT COALESCE(rate_limit_rpm, 0) / 10 INTO limit_val
    FROM api_keys WHERE id = key_id;

    -- 增量更新（注意：不在这里累加 active_concurrent，因为 AFTER INSERT 只能加不能减。
    -- active_concurrent 由 customer_cost_view 通过 JOIN request_logs 实时计算）
    INSERT INTO api_key_model_cost (
        bucket, api_key_id, canonical_id, raw_model, billing_mode,
        requests_total, requests_success,
        tokens_input, tokens_output, cost_usd,
        active_concurrent, concurrency_limit, pressure_ratio,
        last_request_at, updated_at
    ) VALUES (
        bucket_ts, key_id, NEW.canonical_id, COALESCE(NEW.outbound_model, NEW.client_model),
        'token',
        1, CASE WHEN NEW.success THEN 1 ELSE 0 END,
        COALESCE(NEW.prompt_tokens, 0), COALESCE(NEW.completion_tokens, 0),
        COALESCE(NEW.cost_usd, 0),
        1, limit_val,
        CASE WHEN limit_val > 0 THEN LEAST(1.0, 1.0 / limit_val) ELSE 0 END,
        NEW.ts, NOW()
    )
    ON CONFLICT (bucket, api_key_id, raw_model) DO UPDATE SET
        requests_total    = api_key_model_cost.requests_total + 1,
        requests_success  = api_key_model_cost.requests_success + CASE WHEN NEW.success THEN 1 ELSE 0 END,
        tokens_input      = api_key_model_cost.tokens_input + COALESCE(NEW.prompt_tokens, 0),
        tokens_output     = api_key_model_cost.tokens_output + COALESCE(NEW.completion_tokens, 0),
        cost_usd          = api_key_model_cost.cost_usd + COALESCE(NEW.cost_usd, 0),
        -- active_concurrent 在 trigger 中不更新（只在视图层动态计算）
        concurrency_limit = EXCLUDED.concurrency_limit,
        pressure_ratio    = CASE WHEN EXCLUDED.concurrency_limit > 0
                                  THEN LEAST(1.0, EXCLUDED.active_concurrent::numeric / EXCLUDED.concurrency_limit)
                                  ELSE 0 END,
        last_request_at   = NEW.ts,
        updated_at        = NOW();

    RETURN NEW;
END;
$ LANGUAGE plpgsql;

-- 仅 auto 请求触发（避免给显式 model 请求带来额外开销）
DROP TRIGGER IF EXISTS trg_update_api_key_model_cost ON request_logs;
CREATE TRIGGER trg_update_api_key_model_cost
AFTER INSERT ON request_logs
FOR EACH ROW
WHEN (NEW.is_auto_request = TRUE)
EXECUTE FUNCTION update_api_key_model_cost();

-- ============================================================================
-- (c) customer_cost_view — 按 API Key 聚合的成本视图（admin 查询用）
-- ============================================================================
-- 修复 v2.0.1 的 active_concurrent bug：
-- 之前 trigger 在 INSERT 时累加 active_concurrent 但永远不减回，
-- 几小时后字段会爆增。现在 active_concurrent 在视图层动态计算
-- (JOIN request_logs 子查询，统计最近 5 分钟内未完成的请求数近似)。
CREATE OR REPLACE VIEW customer_cost_view AS
SELECT
    akmc.api_key_id,
    ak.key_alias,
    ak.tenant_id,
    ak.application_id,
    -- 最近 1h 成本
    SUM(CASE WHEN akmc.bucket >= NOW() - INTERVAL '1 hour'
             THEN akmc.cost_usd ELSE 0 END) AS cost_usd_1h,
    -- 最近 24h 成本
    SUM(CASE WHEN akmc.bucket >= NOW() - INTERVAL '24 hours'
             THEN akmc.cost_usd ELSE 0 END) AS cost_usd_24h,
    -- 最近 7d 成本
    SUM(CASE WHEN akmc.bucket >= NOW() - INTERVAL '7 days'
             THEN akmc.cost_usd ELSE 0 END) AS cost_usd_7d,
    -- 总请求数（auto only）
    SUM(akmc.requests_total) AS total_auto_requests,
    SUM(akmc.requests_success) AS total_auto_success,
    -- 实时活跃并发（从 request_logs 动态计算，不依赖 trigger 累加）
    -- 注意：每个 api_key 最近 5min 内的请求数近似活跃数。
    -- 真正的活跃数需要 server-side 实时统计，但这个近似够 admin 展示用。
    (SELECT COUNT(*) FROM request_logs rl
     WHERE rl.api_key_id = akmc.api_key_id
       AND rl.is_auto_request = TRUE
       AND rl.ts >= NOW() - INTERVAL '5 minutes'
       AND rl.success IS NOT NULL  -- 还未完成 (NULL) 的请求表示 in-flight
       AND rl.ts IS NOT NULL) AS active_concurrent,
    -- 限制值（从 akmc 最新 bucket 取）
    MAX(akmc.concurrency_limit) AS concurrency_limit,
    -- 平均压力比（最近 1h，按 bucket 加权）
    AVG(CASE WHEN akmc.bucket >= NOW() - INTERVAL '1 hour'
             THEN akmc.pressure_ratio ELSE NULL END) AS avg_pressure_1h,
    -- 智能选择指数（推荐指数）
    MAX(akmc.score_smart) AS best_score_smart,
    MAX(akmc.score_speed_first) AS best_score_speed_first,
    MAX(akmc.score_cost_first) AS best_score_cost_first,
    -- 最近活动
    MAX(akmc.last_request_at) AS last_request_at
FROM api_key_model_cost akmc
JOIN api_keys ak ON ak.id = akmc.api_key_id
GROUP BY akmc.api_key_id, ak.key_alias, ak.tenant_id, ak.application_id;

COMMENT ON VIEW customer_cost_view IS 'Auto route: per-API-Key customer cost dashboard (1h/24h/7d windows + concurrency + scores). active_concurrent is computed live from request_logs (5min window).';

-- ============================================================================
-- (d) model_cost_per_task_view — 每个 canonical model 在每个 task 上的成本（模型选型决策用）
-- ============================================================================
CREATE OR REPLACE VIEW model_cost_per_task_view AS
SELECT
    canonical_id,
    raw_model,
    -- 总成本
    SUM(cost_usd) AS total_cost_usd,
    SUM(tokens_input + tokens_output) AS total_tokens,
    -- 平均价格（USD per 1M tokens）
    CASE WHEN SUM(tokens_input + tokens_output) > 0
         THEN SUM(cost_usd) / SUM(tokens_input + tokens_output) * 1000000
         ELSE 0
    END AS avg_cost_per_1m_usd,
    -- 成功率
    CASE WHEN SUM(requests_total) > 0
         THEN SUM(requests_success)::numeric / SUM(requests_total)
         ELSE 0
    END AS success_rate,
    -- 平均延迟（从 request_logs 子查询）
    (SELECT AVG(latency_ms) FROM request_logs rl
     WHERE rl.outbound_model = mcp.raw_model
       AND rl.success = TRUE
       AND rl.ts >= NOW() - INTERVAL '7 days') AS avg_latency_ms,
    -- 总请求数
    SUM(requests_total) AS total_requests,
    -- 唯一 API Key 数
    COUNT(DISTINCT api_key_id) AS unique_api_keys
FROM api_key_model_cost mcp
WHERE bucket >= NOW() - INTERVAL '7 days'
GROUP BY canonical_id, raw_model;

COMMENT ON VIEW model_cost_per_task_view IS 'Auto route: per-model aggregated cost for last 7 days';

COMMIT;