-- 凭据健康检查查询
-- 识别有问题的凭据：高错误率、配额耗尽、性能异常、状态异常

WITH time_range AS (
  SELECT 
    NOW() - INTERVAL '24 hours' AS start_time,
    NOW() AS end_time
),
recent_stats AS (
  SELECT 
    rl.credential_id,
    c.label AS credential_label,
    p.name AS provider_name,
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
    COUNT(*) FILTER (WHERE rl.error_kind = 'rate_limit') AS rate_limit_count,
    COUNT(*) FILTER (WHERE rl.error_kind = 'quota_exceeded') AS quota_exceeded_count,
    COUNT(*) FILTER (WHERE rl.error_kind = 'invalid_auth') AS auth_error_count,
    COUNT(*) FILTER (WHERE rl.error_kind = 'service_unavailable') AS service_unavail_count,
    
    ROUND(AVG(rl.latency_ms)::numeric, 0) AS avg_latency_ms,
    PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY rl.latency_ms) AS p95_latency_ms,
    
    SUM(rl.cost_usd) AS total_cost_usd,
    MAX(rl.ts) AS last_request_time
    
  FROM request_logs rl
  CROSS JOIN time_range tr
  LEFT JOIN credentials c ON rl.credential_id = c.id
  LEFT JOIN providers p ON c.provider_id = p.id
  WHERE rl.ts BETWEEN tr.start_time AND tr.end_time
    AND rl.credential_id IS NOT NULL
    AND rl.request_type = 'main'
  GROUP BY 
    rl.credential_id,
    c.label,
    p.name,
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
    
    ARRAY_REMOVE(ARRAY[
      CASE WHEN error_rate_pct > 20 THEN 'high_error_rate' END,
      CASE WHEN error_rate_pct > 50 THEN 'critical_error_rate' END,
      CASE WHEN timeout_count > request_count * 0.1 THEN 'frequent_timeouts' END,
      CASE WHEN rate_limit_count > 10 THEN 'rate_limited' END,
      CASE WHEN quota_exceeded_count > 0 THEN 'quota_exceeded' END,
      CASE WHEN auth_error_count > 0 THEN 'auth_failed' END,
      CASE WHEN service_unavail_count > request_count * 0.2 THEN 'service_unstable' END,
      CASE WHEN avg_latency_ms > 10000 THEN 'slow_response' END,
      CASE WHEN p95_latency_ms > 20000 THEN 'p95_latency_high' END,
      CASE WHEN status != 'active' THEN 'inactive_status' END,
      CASE WHEN lifecycle_status NOT IN ('active', 'stable') THEN 'lifecycle_issue' END,
      CASE WHEN availability_state NOT IN ('available', 'online') THEN 'unavailable' END,
      CASE WHEN quota_state IN ('exhausted', 'depleted', 'suspended') THEN 'quota_depleted' END,
      CASE WHEN balance_usd IS NOT NULL AND balance_usd < 1.0 THEN 'low_balance' END,
      CASE WHEN balance_usd IS NOT NULL AND balance_usd < 0.1 THEN 'critical_balance' END,
      CASE WHEN NOW() - last_request_time > INTERVAL '1 hour' AND request_count > 0 THEN 'possibly_stalled' END
    ], NULL) AS issues,
    
    CASE 
      WHEN error_rate_pct > 50 OR auth_error_count > 0 OR quota_state = 'exhausted' THEN 'critical'
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
