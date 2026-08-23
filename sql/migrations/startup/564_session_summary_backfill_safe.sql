-- 564_session_summary_backfill_safe.sql
-- 审计修复（2026-08-23）：563 回填曾用 REPLACE 覆盖累计，重跑/并发会低估。
--
-- 本迁移：
--   1) 提供可安全重跑的 backfill：仅抬升 request_count=0 的行，或用 GREATEST
--      把 hot 窗口计数抬到不低于当前 hot 聚合（不压扁更大累计）。
--   2) 不改触发器语义（仍仅挂 request_logs_hot）。
--
-- Idempotent: YES（GREATEST / zero-only 语义）

BEGIN;

-- 抬升：hot 聚合计数 > 当前 summary 时取 GREATEST（不降低已有累计）
WITH hot_agg AS (
    SELECT
        h.gw_session_id AS session_key,
        h.tenant_id,
        MIN(h.ts) AS first_ts,
        MAX(h.ts) AS last_ts,
        COUNT(*)::INT AS req_n,
        COUNT(*) FILTER (WHERE COALESCE(h.success, false))::INT AS ok_n,
        COUNT(*) FILTER (WHERE NOT COALESCE(h.success, false))::INT AS err_n,
        COALESCE(SUM(h.cost_usd), 0)::NUMERIC(12,6) AS cost_n,
        COALESCE(SUM(h.prompt_tokens), 0)::BIGINT AS prompt_n,
        COALESCE(SUM(h.completion_tokens), 0)::BIGINT AS completion_n,
        COALESCE(AVG(h.latency_ms), 0)::INT AS avg_lat,
        MIN(h.latency_ms) AS min_lat,
        MAX(h.latency_ms) AS max_lat
    FROM request_logs_hot h
    WHERE h.gw_session_id IS NOT NULL AND h.gw_session_id <> ''
    GROUP BY h.gw_session_id, h.tenant_id
)
UPDATE session_summaries ss
SET
    first_request_at = LEAST(ss.first_request_at, a.first_ts),
    last_request_at = GREATEST(ss.last_request_at, a.last_ts),
    request_count = GREATEST(ss.request_count, a.req_n),
    success_count = GREATEST(ss.success_count, a.ok_n),
    error_count = GREATEST(ss.error_count, a.err_n),
    total_cost_usd = GREATEST(ss.total_cost_usd, a.cost_n),
    total_prompt_tokens = GREATEST(ss.total_prompt_tokens, a.prompt_n),
    total_completion_tokens = GREATEST(ss.total_completion_tokens, a.completion_n),
    avg_latency_ms = CASE
        WHEN ss.request_count = 0 THEN a.avg_lat
        ELSE ss.avg_latency_ms
    END,
    min_latency_ms = LEAST(ss.min_latency_ms, a.min_lat),
    max_latency_ms = GREATEST(ss.max_latency_ms, a.max_lat),
    updated_at = NOW()
FROM hot_agg a
WHERE ss.session_key = a.session_key
  AND (
    ss.request_count < a.req_n
    OR ss.total_prompt_tokens < a.prompt_n
    OR ss.total_cost_usd < a.cost_n
  );

-- 仍无 summary 行的 hot 会话：插入（与 563 零计数插入同形，仅补缺）
INSERT INTO session_summaries (
    session_key, tenant_id,
    first_request_at, last_request_at,
    request_count, success_count, error_count,
    total_cost_usd, input_cost_usd, output_cost_usd,
    total_prompt_tokens, total_completion_tokens,
    avg_latency_ms, min_latency_ms, max_latency_ms,
    models_used, work_types, providers, client_models,
    updated_at
)
SELECT
    h.gw_session_id,
    h.tenant_id,
    MIN(h.ts),
    MAX(h.ts),
    COUNT(*)::INT,
    COUNT(*) FILTER (WHERE COALESCE(h.success, false))::INT,
    COUNT(*) FILTER (WHERE NOT COALESCE(h.success, false))::INT,
    COALESCE(SUM(h.cost_usd), 0)::NUMERIC(12,6),
    0::NUMERIC(12,6),
    0::NUMERIC(12,6),
    COALESCE(SUM(h.prompt_tokens), 0)::BIGINT,
    COALESCE(SUM(h.completion_tokens), 0)::BIGINT,
    COALESCE(AVG(h.latency_ms), 0)::INT,
    MIN(h.latency_ms),
    MAX(h.latency_ms),
    '{}'::TEXT[],
    '{}'::TEXT[],
    '{}'::TEXT[],
    '{}'::TEXT[],
    NOW()
FROM request_logs_hot h
WHERE h.gw_session_id IS NOT NULL AND h.gw_session_id <> ''
  AND NOT EXISTS (
      SELECT 1 FROM session_summaries ss WHERE ss.session_key = h.gw_session_id
  )
GROUP BY h.gw_session_id, h.tenant_id
ON CONFLICT (session_key) DO NOTHING;

COMMIT;
