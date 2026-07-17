-- sql/diagnostics/probe_failure_diagnosis.sql
-- 2026-07-17: 诊断 probe 行真实失败原因的查询集合。
--
-- 背景: probe 行(emitter 写入 request_logs)在 dashboard 上长期只显示
-- "probe direct failed / Token 1/0 / 失败阶段:上游", 这些字段大部分是占位符。
-- 真实的失败原因(HTTP 状态码 / err_code / 请求与响应体)记录在:
--   1. request_logs.auto_decision JSONB (元数据: err_code/http_status/via_proxy)
--   2. node_probe_runs 表 (完整审计: request_url/request_body/response_body)
--
-- 本文件给出按优先级排列的三组查询, 直接复制粘贴即可定位根因。

-- ============================================================================
-- 查询 1: 单条 probe 行的真实诊断(按 request_id)
-- 适用: 已知 request_id(如 dashboard 点进去的那条), 想看它到底为什么失败。
-- ============================================================================

SELECT
    request_id,
    error_kind,
    latency_ms,
    auto_decision->>'probe_err_code'      AS err_code,       -- http_429 / endpoint_build / network_error ...
    auto_decision->>'probe_http_status'   AS http_status,    -- 上游真实 HTTP 状态码
    auto_decision->>'probe_status'        AS probe_status,   -- success/timeout/network/http_5xx ...
    auto_decision->>'probe_failure_stage' AS failure_stage,  -- 真实失败点(upstream/gateway); 2026-07-17 后才有
    auto_decision->>'probe_via_proxy'     AS via_proxy,      -- 是否走代理
    auto_decision->>'probe_attempt'       AS attempt,
    auto_decision->>'probe_origin'        AS origin          -- direct / gateway
FROM request_logs
WHERE request_id = 'probe-direct-c16-mglm-5.2-a5-fail-1784225551431410131';
-- 替换为你要查的任意 probe request_id。

-- ============================================================================
-- 查询 2: 某凭据+模型最近 7 次探测的完整审计(含请求/响应体)
-- 适用: 想看一个渠道反复失败的完整轨迹(如 c16/glm-5.2 连续失败 5 轮)。
-- 数据来源: node_probe_runs 表(2026-07-16 migration 419 起记录完整字段)。
-- ============================================================================

SELECT
    attempt,
    direct_ok,
    direct_http_status,
    direct_err_code,          -- endpoint_build / http_429 / network_error ...
    direct_latency_ms,
    LEFT(direct_err_detail, 200) AS direct_err_detail,
    gateway_ok,
    gateway_http_status,
    gateway_err_code,
    request_url,              -- 探测发往的完整 URL
    request_body,             -- 探测请求体(约80字节)
    LEFT(response_body, 256)  AS response_body,  -- 响应体前512字节
    via_proxy,                -- true=走了 HTTP_PROXY(2026-07-16 修复后的正确行为)
    started_at,
    duration_ms
FROM node_probe_runs
WHERE credential_id = 16
  AND raw_model_name = 'glm-5.2'
ORDER BY started_at DESC
LIMIT 10;

-- err_code 解读速查:
--   endpoint_build    → 网关侧失败(解密/base_url/JOIN 失败), 请求没发出去。检查 keyring/credentials。
--   body_build        → 网关侧失败(请求体构造), 一般是 model 名为空。
--   network_error     → 网络层失败。若 latency≈15000ms 则是超时(15s), 查代理/连通性。
--   http_429          → 上游限流(tokenhub.market 等聚合器常见)。
--   http_5xx          → 上游不稳定。
--   http_401/403      → 凭据失效/无权限。

-- ============================================================================
-- 查询 3: 全局最近 6 小时探测失败汇总(按 err_code 分桶)
-- 适用: 想看当前哪些渠道在持续失败、失败模式分布。
-- ============================================================================

SELECT
    outbound_model,
    credential_id,
    auto_decision->>'probe_err_code' AS err_code,
    COUNT(*)                         AS fail_count,
    MAX(ts)                          AS last_fail,
    MIN(latency_ms)                  AS min_lat,
    MAX(latency_ms)                  AS max_lat
FROM request_logs
WHERE task_type = 'probe_triggered'
  AND success = FALSE
  AND ts > NOW() - INTERVAL '6 hours'
GROUP BY outbound_model, credential_id, auto_decision->>'probe_err_code'
ORDER BY fail_count DESC, last_fail DESC
LIMIT 50;

-- ============================================================================
-- 查询 4: 当前探测状态机(node_probe_state)
-- 适用: 看某渠道下一轮探测什么时候跑、已连续失败几次。
-- ============================================================================

SELECT
    credential_id,
    raw_model_name,
    consecutive_failures,
    consecutive_successes,
    last_direct_ok,
    last_gateway_ok,
    last_err_code,
    next_retry_at,
    next_retry_seconds,
    EXTRACT(EPOCH FROM (next_retry_at - NOW()))::int AS seconds_until_next,
    paused
FROM node_probe_state
WHERE credential_id = 16
ORDER BY next_retry_at;
