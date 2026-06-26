-- ===========================================================================
-- scripts/verify_request_logs.sql
-- request_log 验证：
-- 1. 基本字段完整性
-- 2. 请求/响应体保存
-- 3. 无重复记录
-- 4. request_wal 新表
-- ===========================================================================

\echo '=========================================='
\echo '📋 验证 1: request_logs 基本字段完整性'
\echo '=========================================='

SELECT
    client_model,
    request_status,
    success,
    error_kind,
    latency_ms,
    tenant_id,
    failure_stage,
    failure_detail_code,
    count(*) AS count
FROM request_logs
WHERE ts > now() - interval '5 minutes'
GROUP BY client_model, request_status, success, error_kind, latency_ms, tenant_id, failure_stage, failure_detail_code
ORDER BY client_model, count DESC
LIMIT 10;

\echo ''
\echo '=========================================='
\echo '📋 验证 2: 请求/响应体保存'
\echo '=========================================='

SELECT
    request_id,
    client_model,
    request_status,
    length(request_preview) AS preview_len,
    length(response_preview) AS resp_preview_len,
    request_body IS NOT NULL AS has_request_body,
    response_body IS NOT NULL AS has_response_body,
    request_preview IS NOT NULL AS has_request_preview,
    response_preview IS NOT NULL AS has_response_preview
FROM request_logs
WHERE ts > now() - interval '5 minutes'
ORDER BY ts DESC
LIMIT 5;

\echo ''
\echo '📋 验证 2.1: 实际请求/响应内容'
SELECT
    request_id,
    client_model,
    request_preview AS sample_request_preview,
    response_preview AS sample_response_preview,
    substring(request_body::text, 1, 200) AS sample_request_body
FROM request_logs
WHERE ts > now() - interval '5 minutes'
ORDER BY ts DESC
LIMIT 2;

\echo ''
\echo '=========================================='
\echo '📋 验证 3: 无重复记录（CHANGELOG_request_logs_fix 回归测试）'
\echo '=========================================='

\echo '3.1 同一 request_id 出现多次的：'
SELECT request_id, COUNT(*) AS dup_count
FROM request_logs
WHERE ts > now() - interval '5 minutes'
GROUP BY request_id
HAVING COUNT(*) > 1
ORDER BY dup_count DESC
LIMIT 5;

\echo ''
\echo '3.2 唯一约束验证（应当无返回行）:'
SELECT request_id, COUNT(*)
FROM request_logs
WHERE ts > now() - interval '5 minutes'
GROUP BY request_id
HAVING COUNT(*) > 1;

\echo ''
\echo '=========================================='
\echo '📋 验证 4: 各字段非空率'
\echo '=========================================='

SELECT
    count(*) AS total,
    count(client_model) AS has_client_model,
    count(outbound_model) AS has_outbound_model,
    count(request_status) AS has_request_status,
    count(success) AS has_success,
    count(latency_ms) AS has_latency,
    count(tenant_id) AS has_tenant,
    count(ts) AS has_ts,
    count(error_kind) AS has_error_kind,
    count(failure_stage) AS has_failure_stage,
    count(failure_detail_code) AS has_failure_detail_code,
    count(api_key_prefix) AS has_api_key_prefix,
    count(identity_hash) AS has_identity_hash
FROM request_logs
WHERE ts > now() - interval '5 minutes';

\echo ''
\echo '=========================================='
\echo '📋 验证 5: 时序一致性（最近 5 分钟）'
\echo '=========================================='

SELECT
    date_trunc('minute', ts) AS minute,
    count(*) AS count,
    avg(latency_ms)::int AS avg_lat
FROM request_logs
WHERE ts > now() - interval '5 minutes'
GROUP BY 1
ORDER BY 1 DESC
LIMIT 10;

\echo ''
\echo '=========================================='
\echo '📋 验证 6: request_wal 新表（应当无数据，因为 WAL 是新流程）'
\echo '=========================================='

SELECT count(*) AS wal_total FROM request_wal WHERE created_at > now() - interval '5 minutes';

SELECT
    request_id,
    status,
    stage,
    client_model,
    upstream_provider_id,
    upstream_credential_id,
    completion_tokens,
    prompt_tokens,
    created_at,
    completed_at,
    error
FROM request_wal
WHERE created_at > now() - interval '5 minutes'
ORDER BY created_at DESC
LIMIT 5;

\echo ''
\echo '=========================================='
\echo '✅ 验证完成'
\echo '=========================================='