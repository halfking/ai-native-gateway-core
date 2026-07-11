-- extract_session_test_data.sql
-- 从252数据库提取真实会话数据用于三层缓存测试
--
-- 用途：
--   1. 提取包含多轮对话的完整会话
--   2. 提取请求/响应的完整内容
--   3. 按时间顺序排列，模拟真实交互流程
--
-- 使用方法：
--   docker exec -i pg-252-pg17 psql -U llm_gateway -d llm_gateway < scripts/extract_session_test_data.sql > test-data/session_test_data.jsonl

\set QUIET on
\pset format unaligned
\pset tuples_only on
\pset fieldsep ''

-- 选择最近7天内有5轮以上对话的会话（排除单轮会话）
WITH active_sessions AS (
  SELECT 
    gw_session_id,
    tenant_id,
    COUNT(*) as turn_count,
    MIN(ts) as first_ts,
    MAX(ts) as last_ts
  FROM request_logs
  WHERE ts > NOW() - INTERVAL '30 days'
    AND gw_session_id IS NOT NULL
    AND gw_session_id != ''
    AND success = true  -- 只取成功的请求
  GROUP BY gw_session_id, tenant_id
  HAVING COUNT(*) >= 2 AND COUNT(*) <= 20  -- 2-20轮对话
  ORDER BY COUNT(*) DESC, MAX(ts) DESC
  LIMIT 10  -- 提取10个会话
),
session_requests AS (
  SELECT 
    r.gw_session_id,
    r.tenant_id,
    r.request_id,
    r.ts,
  r.request_body,
  r.outbound_body,
  r.response_body,
  r.outbound_msg_count,
  r.outbound_token_est,
    r.compression_strategy,
    r.compression_meta,
    ROW_NUMBER() OVER (PARTITION BY r.gw_session_id ORDER BY r.ts) as turn_number
  FROM request_logs r
  INNER JOIN active_sessions s ON r.gw_session_id = s.gw_session_id AND r.tenant_id = s.tenant_id
  WHERE r.success = true
    AND r.request_body IS NOT NULL
    AND r.outbound_body IS NOT NULL
  ORDER BY r.gw_session_id, r.ts
)
SELECT json_build_object(
  'session_id', gw_session_id,
  'tenant_id', tenant_id,
  'turn_number', turn_number,
  'request_id', request_id,
  'timestamp', ts,
  'request_body', request_body::jsonb,
  'outbound_body', outbound_body::jsonb,
  'response_body', response_body::jsonb,
  'outbound_msg_count', outbound_msg_count,
  'outbound_token_est', outbound_token_est,
  'compression_strategy', compression_strategy,
  'compression_meta', compression_meta::jsonb
)
FROM session_requests
ORDER BY gw_session_id, turn_number;
