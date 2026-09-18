-- =============================================================================
-- 凭据健康检查查询（R44 重写版，2026-09-19）
-- 识别有问题的凭据：高错误率、配额耗尽、性能异常、状态异常
--
-- 使用说明：
--   * psql -f 直跑（无 psql 元命令依赖）；建议在只读副本或低峰期执行。
--   * 文件头 SET statement_timeout 防误操作长事务，可按需调整。
--   * 读面 = request_logs_with_current_month（request_logs_hot ∪ 月度分区母表，
--     迁移 448/510/710 维护重建）。直接查 request_logs 母表会漏掉 hot 表最近
--     8h 的写入（promote 周期内），健康检查恰恰最该看到"刚出事"的时段。
--   * error_kind 取值 = errorsx.ErrorKind 真实分类法（errorsx/classify.go）；
--     凭据状态词汇 = credentials 表 CHECK 约束（availability_state/quota_state）。
--   * §2 为供应商错误表视角（supplier_errors_unified，V371，D08 闭环）。
-- =============================================================================

SET statement_timeout = '5min';

-- ── §1: 凭据健康画像（近 24h，hot ∪ 母表）──────────────────────────────────

WITH time_range AS (
  SELECT
    NOW() - INTERVAL '24 hours' AS start_time,
    NOW() AS end_time
),
recent_stats AS (
  SELECT
    rl.credential_id,
    c.label AS credential_label,
    p.display_name AS provider_name,
    c.status,
    c.lifecycle_status,
    c.availability_state,
    c.quota_state,
    c.balance_usd,
    c.is_free_tier,

    COUNT(*) AS request_count,
    COUNT(*) FILTER (WHERE rl.success = false) AS error_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE rl.success = false) / NULLIF(COUNT(*), 0), 2) AS error_rate_pct,

    COUNT(*) FILTER (WHERE rl.client_timeout = true) AS timeout_count,
    -- errorsx 真实分类法：rate_limit / quota 族 / auth 族 / upstream_down。
    -- （旧版模板的 quota_exceeded/invalid_auth/service_unavailable 均为虚构
    -- 值，对应计数恒 0，auth/quota 告警全失效 —— R44 F3 修正。）
    COUNT(*) FILTER (WHERE rl.error_kind = 'rate_limit') AS rate_limit_count,
    COUNT(*) FILTER (WHERE rl.error_kind IN ('quota', 'quota_periodic', 'quota_balance', 'quota_permanent')) AS quota_error_count,
    COUNT(*) FILTER (WHERE rl.error_kind IN ('auth', 'auth_revoked')) AS auth_error_count,
    COUNT(*) FILTER (WHERE rl.error_kind = 'upstream_down') AS upstream_down_count,

    ROUND(AVG(rl.latency_ms)::numeric, 0) AS avg_latency_ms,
    PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY rl.latency_ms) AS p95_latency_ms,

    SUM(rl.cost_usd) AS total_cost_usd,
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
    p.display_name,
    c.status,
    c.lifecycle_status,
    c.availability_state,
    c.quota_state,
    c.balance_usd,
    c.is_free_tier
),
health_issues AS (
  SELECT
    credential_id,
    credential_label,
    provider_name,
    status,
    lifecycle_status,
    availability_state,
    quota_state,
    balance_usd,
    is_free_tier,
    request_count,
    error_count,
    error_rate_pct,
    avg_latency_ms,
    p95_latency_ms,
    total_cost_usd,
    last_request_time,

    -- 状态词汇对齐 credentials CHECK 约束：
    --   availability_state ∈ {ready,cooling,rate_limited,auth_failed,unreachable,suspended}
    --   quota_state 运行时值 ∈ {ok,balance_exhausted,permanently_exhausted,
    --                            periodic_exhausted,suspended,unknown}
    --   lifecycle_status ∈ {active,disabled,suspended,retired}（'stable' 是死词汇）
    -- （旧版的 ('available','online') / ('exhausted','depleted','suspended')
    --  与真实词汇错配：健康值 'ready' 被打成 unavailable，三种真实耗尽态全漏
    --  —— R44 F4 修正。）
    ARRAY_REMOVE(ARRAY[
      CASE WHEN error_rate_pct > 20 THEN 'high_error_rate' END,
      CASE WHEN error_rate_pct > 50 THEN 'critical_error_rate' END,
      CASE WHEN timeout_count > request_count * 0.1 THEN 'frequent_timeouts' END,
      CASE WHEN rate_limit_count > 10 THEN 'rate_limited' END,
      CASE WHEN quota_error_count > 0 THEN 'quota_exhausted_family' END,
      CASE WHEN auth_error_count > 0 THEN 'auth_failed' END,
      CASE WHEN upstream_down_count > request_count * 0.2 THEN 'upstream_unstable' END,
      CASE WHEN avg_latency_ms > 10000 THEN 'slow_response' END,
      CASE WHEN p95_latency_ms > 20000 THEN 'p95_latency_high' END,
      CASE WHEN status NOT IN ('active', 'cooling') THEN 'inactive_status' END,
      CASE WHEN lifecycle_status <> 'active' THEN 'lifecycle_issue' END,
      CASE WHEN availability_state <> 'ready' THEN 'state_not_ready' END,
      CASE WHEN quota_state IN ('balance_exhausted', 'permanently_exhausted', 'periodic_exhausted', 'suspended') THEN 'quota_depleted' END,
      CASE WHEN balance_usd IS NOT NULL AND balance_usd < 1.0 THEN 'low_balance' END,
      CASE WHEN balance_usd IS NOT NULL AND balance_usd < 0.1 THEN 'critical_balance' END,
      -- 窗口内有像样流量但最近 2h 完全静默：流量可能已漂移/被熔断/探针冷却。
      -- （旧版 ">1h 即 possibly_stalled" 在只查母表时对每行恒真 —— R44 F1 修正
      --   读面 + 提高门槛降噪。）
      CASE WHEN request_count >= 10 AND NOW() - last_request_time > INTERVAL '2 hours' THEN 'no_recent_traffic' END
    ], NULL) AS issues,

    CASE
      WHEN error_rate_pct > 50 OR auth_error_count > 0
        OR quota_state IN ('balance_exhausted', 'permanently_exhausted', 'periodic_exhausted') THEN 'critical'
      WHEN error_rate_pct > 20 OR rate_limit_count > 10 OR balance_usd < 1.0 THEN 'warning'
      WHEN error_rate_pct > 10 OR avg_latency_ms > 10000 THEN 'notice'
      ELSE 'healthy'
    END AS severity

  FROM recent_stats
)
SELECT
  credential_id,
  credential_label,
  provider_name,
  severity,
  status,
  lifecycle_status,
  availability_state,
  quota_state,
  is_free_tier,
  balance_usd,
  request_count,
  error_count,
  error_rate_pct,
  avg_latency_ms,
  p95_latency_ms,
  total_cost_usd,
  last_request_time,
  ARRAY_LENGTH(issues, 1) AS issue_count,
  ARRAY_TO_STRING(issues, ', ') AS issues_list,

  -- 建议操作
  CASE severity
    WHEN 'critical' THEN 'URGENT: Disable credential, investigate auth/quota issues, contact provider'
    WHEN 'warning' THEN 'Review credential health, check rate limits and balance, consider backup'
    WHEN 'notice' THEN 'Monitor closely, optimize usage patterns if latency is high'
    ELSE 'No action needed'
  END AS recommended_action

FROM health_issues
WHERE severity IN ('critical', 'warning', 'notice')
  OR ARRAY_LENGTH(issues, 1) > 0
ORDER BY
  CASE severity
    WHEN 'critical' THEN 1
    WHEN 'warning' THEN 2
    WHEN 'notice' THEN 3
    ELSE 4
  END,
  error_rate_pct DESC,
  request_count DESC;

-- ── §2: 供应商错误表视角（D08：凭据维度错误集合，评估供应商服务质量）────────
-- supplier_errors_unified = supplier_errors_hot(8h) ∪ 月度 columnar 分区
-- （V371；唯一写入入口 CandidateFailureWriter；error_type = errorsx.ErrorKind；
--   error_message 已脱敏，此处不做明细展示只做聚合）。
SELECT
  se.credential_id,
  c.label AS credential_label,
  p.display_name AS provider_name,
  COUNT(*) AS error_count,
  COUNT(*) FILTER (WHERE se.is_retryable) AS retryable_count,
  COUNT(*) FILTER (WHERE se.error_type = 'rate_limit') AS rate_limit_count,
  COUNT(*) FILTER (WHERE se.error_type IN ('auth', 'auth_revoked')) AS auth_error_count,
  COUNT(*) FILTER (WHERE se.error_type IN ('quota', 'quota_periodic', 'quota_balance', 'quota_permanent')) AS quota_error_count,
  COUNT(*) FILTER (WHERE se.error_type = 'upstream_down') AS upstream_down_count,
  MODE() WITHIN GROUP (ORDER BY se.stage) AS dominant_stage,
  MAX(se.occurred_at) AS last_error_at
FROM supplier_errors_unified se
LEFT JOIN credentials c ON se.credential_id = c.id
LEFT JOIN providers p ON c.provider_id = p.id
WHERE se.occurred_at >= NOW() - INTERVAL '24 hours'
  AND se.credential_id IS NOT NULL
GROUP BY se.credential_id, c.label, p.display_name
HAVING COUNT(*) >= 3
ORDER BY error_count DESC
LIMIT 50;

RESET statement_timeout;
