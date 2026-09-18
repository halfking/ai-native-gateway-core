-- =============================================================================
-- 凭据使用情况分析查询（R44 重写版，2026-09-19）
-- 分析时间范围内每个凭据的请求量、成功率、成本和性能指标
--
-- 使用说明：
--   * psql -f 直跑（无 psql 元命令依赖）；建议在只读副本或低峰期执行。
--   * 文件头 SET statement_timeout 防误操作长事务，可按需调整。
--   * 读面 = request_logs_with_current_month（hot ∪ 母表，448/510/710）。
--     直接查 request_logs 母表会漏掉 hot 表最近 8h 的写入 —— 7 天总量/成功率/
--     成本全部少计最近 8h（R44 F1 修正；本机实测 24h 窗口母表少 13.6% 行）。
--   * top_models / error_breakdown 均为一次性 GROUP BY 预聚合后 JOIN 回主表，
--     不再使用按行执行的相关子查询（旧版对每行凭据重复全窗口扫描，母表无
--     credential_id 索引，生产直跑有拖库风险 —— R44 F5 修正）。
-- =============================================================================

SET statement_timeout = '10min';

WITH time_range AS (
  SELECT
    NOW() - INTERVAL '7 days' AS start_time,
    NOW() AS end_time
),
credential_stats AS (
  SELECT
    rl.credential_id,
    c.label AS credential_label,
    c.provider_id,
    p.display_name AS provider_name,
    c.status AS credential_status,
    c.lifecycle_status,
    c.availability_state,
    c.quota_state,
    c.concurrency_limit,
    c.fp_slot_limit,
    c.rpm_limit,
    c.tpm_limit,
    c.balance_usd,
    c.is_free_tier,
    c.plan_type,

    -- 请求量统计
    COUNT(*) AS total_requests,
    COUNT(*) FILTER (WHERE rl.success = true) AS successful_requests,
    COUNT(*) FILTER (WHERE rl.success = false) AS failed_requests,
    ROUND(100.0 * COUNT(*) FILTER (WHERE rl.success = true) / NULLIF(COUNT(*), 0), 2) AS success_rate_pct,

    -- Token 统计
    SUM(rl.prompt_tokens) AS total_prompt_tokens,
    SUM(rl.completion_tokens) AS total_completion_tokens,
    SUM(rl.total_tokens) AS total_tokens,
    ROUND(AVG(rl.prompt_tokens)::numeric, 0) AS avg_prompt_tokens,
    ROUND(AVG(rl.completion_tokens)::numeric, 0) AS avg_completion_tokens,

    -- 成本统计
    SUM(rl.cost_usd) AS total_cost_usd,
    ROUND(AVG(rl.cost_usd)::numeric, 6) AS avg_cost_usd,
    MAX(rl.cost_usd) AS max_cost_usd,

    -- 性能统计
    ROUND(AVG(rl.latency_ms)::numeric, 0) AS avg_latency_ms,
    PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY rl.latency_ms) AS p50_latency_ms,
    PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY rl.latency_ms) AS p95_latency_ms,
    PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY rl.latency_ms) AS p99_latency_ms,
    MAX(rl.latency_ms) AS max_latency_ms,

    -- 流式请求统计
    COUNT(*) FILTER (WHERE rl.stream_chunk_count > 0) AS stream_requests,
    ROUND(AVG(rl.stream_first_chunk_ms) FILTER (WHERE rl.stream_chunk_count > 0)::numeric, 0) AS avg_first_chunk_ms,
    COUNT(*) FILTER (WHERE rl.stream_interrupted = true) AS interrupted_streams,

    -- 错误统计
    COUNT(DISTINCT rl.error_kind) FILTER (WHERE rl.error_kind IS NOT NULL) AS distinct_error_kinds,
    COUNT(*) FILTER (WHERE rl.client_timeout = true) AS timeout_count,

    -- 时间范围
    MIN(rl.ts) AS first_request_time,
    MAX(rl.ts) AS last_request_time

  FROM request_logs_with_current_month rl
  CROSS JOIN time_range tr
  LEFT JOIN credentials c ON rl.credential_id = c.id
  LEFT JOIN providers p ON c.provider_id = p.id
  WHERE rl.ts BETWEEN tr.start_time AND tr.end_time
    AND rl.credential_id IS NOT NULL
    AND rl.request_type = 'main'
  GROUP BY
    rl.credential_id,
    c.label,
    c.provider_id,
    p.display_name,
    c.status,
    c.lifecycle_status,
    c.availability_state,
    c.quota_state,
    c.concurrency_limit,
    c.fp_slot_limit,
    c.rpm_limit,
    c.tpm_limit,
    c.balance_usd,
    c.is_free_tier,
    c.plan_type
),
-- 模型分布 Top3：一次全窗口 GROUP BY + ROW_NUMBER，替代旧版按行相关子查询。
model_usage AS (
  SELECT
    rl.credential_id,
    rl.outbound_model,
    COUNT(*) AS request_count,
    ROW_NUMBER() OVER (PARTITION BY rl.credential_id ORDER BY COUNT(*) DESC) AS rn
  FROM request_logs_with_current_month rl
  CROSS JOIN time_range tr
  WHERE rl.ts BETWEEN tr.start_time AND tr.end_time
    AND rl.credential_id IS NOT NULL
    AND rl.outbound_model IS NOT NULL
    AND rl.request_type = 'main'   -- 与外层 total_requests 口径对齐（R44 F6）
  GROUP BY rl.credential_id, rl.outbound_model
),
top_models AS (
  SELECT
    credential_id,
    STRING_AGG(outbound_model || ' (' || request_count || ')', ', ' ORDER BY request_count DESC) AS top_models
  FROM model_usage
  WHERE rn <= 3
  GROUP BY credential_id
),
error_breakdown AS (
  SELECT
    rl.credential_id,
    rl.error_kind,
    COUNT(*) AS error_count,
    ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (PARTITION BY rl.credential_id), 2) AS error_pct
  FROM request_logs_with_current_month rl
  CROSS JOIN time_range tr
  WHERE rl.ts BETWEEN tr.start_time AND tr.end_time
    AND rl.credential_id IS NOT NULL
    AND rl.success = false
    AND rl.error_kind IS NOT NULL
    AND rl.request_type = 'main'
  GROUP BY rl.credential_id, rl.error_kind
),
top_errors AS (
  SELECT
    credential_id,
    STRING_AGG(
      error_kind || ' (' || error_count || ', ' || error_pct || '%)',
      ', '
      ORDER BY error_count DESC
    ) AS top_errors
  FROM (
    SELECT
      credential_id,
      error_kind,
      error_count,
      error_pct,
      ROW_NUMBER() OVER (PARTITION BY credential_id ORDER BY error_count DESC) AS rn
    FROM error_breakdown
  ) ranked_errors
  WHERE rn <= 3
  GROUP BY credential_id
)
SELECT
  cs.credential_id,
  cs.credential_label,
  cs.provider_name,
  cs.credential_status,
  cs.lifecycle_status,
  cs.availability_state,
  cs.quota_state,
  cs.is_free_tier,
  cs.plan_type,

  -- 配额限制
  cs.concurrency_limit,
  cs.fp_slot_limit,
  cs.rpm_limit,
  cs.tpm_limit,
  cs.balance_usd,

  -- 请求统计
  cs.total_requests,
  cs.successful_requests,
  cs.failed_requests,
  cs.success_rate_pct,

  -- Token 统计
  cs.total_tokens,
  cs.total_prompt_tokens,
  cs.total_completion_tokens,
  cs.avg_prompt_tokens,
  cs.avg_completion_tokens,

  -- 成本统计
  cs.total_cost_usd,
  cs.avg_cost_usd,
  cs.max_cost_usd,

  -- 性能统计
  cs.avg_latency_ms,
  cs.p50_latency_ms,
  cs.p95_latency_ms,
  cs.p99_latency_ms,
  cs.max_latency_ms,

  -- 流式统计
  cs.stream_requests,
  cs.avg_first_chunk_ms,
  cs.interrupted_streams,

  -- 错误统计
  cs.distinct_error_kinds,
  cs.timeout_count,
  te.top_errors,

  -- 时间信息
  cs.first_request_time,
  cs.last_request_time,

  -- 模型分布
  tm.top_models,

  -- 利用率指标（7d 平均口径；峰值口径可改查 credential_model_peak_1m）
  CASE
    WHEN cs.rpm_limit IS NOT NULL THEN
      ROUND(100.0 * (cs.total_requests::numeric / 7 / 24 / 60) / cs.rpm_limit, 2)
    ELSE NULL
  END AS rpm_utilization_pct,

  CASE
    WHEN cs.tpm_limit IS NOT NULL THEN
      ROUND(100.0 * (cs.total_tokens::numeric / 7 / 24 / 60) / cs.tpm_limit, 2)
    ELSE NULL
  END AS tpm_utilization_pct,

  -- 健康评分（简单计算）
  CASE
    WHEN cs.success_rate_pct >= 99 AND cs.avg_latency_ms < 2000 THEN 'excellent'
    WHEN cs.success_rate_pct >= 95 AND cs.avg_latency_ms < 5000 THEN 'good'
    WHEN cs.success_rate_pct >= 90 AND cs.avg_latency_ms < 10000 THEN 'fair'
    ELSE 'poor'
  END AS health_score

FROM credential_stats cs
LEFT JOIN top_errors te ON cs.credential_id = te.credential_id
LEFT JOIN top_models tm ON cs.credential_id = tm.credential_id
ORDER BY cs.total_requests DESC;

RESET statement_timeout;
