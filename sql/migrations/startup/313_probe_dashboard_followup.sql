-- Migration 313: Probe dashboard follow-up
--
-- 编号历史（迁移链追溯）：
--   原编号 309 → Round-48 审计发现与 309_intent_aggregates 冲突，改为 311
--   311        → 又与 311_prompt_injection_detection 冲突，改为 313
-- 逻辑内容未变。
--
-- Why:
--   Migration 308 fixed v_model_health_dashboard, v_probe_system_health and
--   get_model_state_summary, but left two views the /probe-health page still
--   reads in the same payload:
--
--     - v_probe_queue_snapshot     -> GET /api/admin/probe/queue-snapshot
--     - v_model_priority_details   -> GET /api/admin/probe/model/{model}/nodes
--
--   Both still assumed the legacy `healthy/failing/suspicious/probing` state
--   set and referenced `providers.name`, a column that does not exist
--   (providers has `display_name`). As a result:
--
--     - the priority queue strip at the top of the page is empty for any
--       credential/model that the probe runner now writes as
--       `healthy_confirmed / broken_confirmed / recovering`;
--     - clicking "详情" on a model row returns HTTP 500 because
--       v_model_priority_details references p.name and fails at runtime.
--
--   This migration aligns those two views with the same state set and
--   provider-column conventions used by migration 308. It is intentionally
--   limited to view changes so it can be applied without downtime.
--
--   It does NOT change the probe worker SQL — those remain canonical.

CREATE OR REPLACE VIEW v_probe_queue_snapshot AS
SELECT
    mps.probe_priority,
    mps.state,
    COUNT(*) AS queue_size,
    COUNT(*) FILTER (WHERE mps.next_retry_at <= NOW()) AS ready_now,
    COUNT(*) FILTER (WHERE mps.next_retry_at <= NOW() + INTERVAL '1 minute') AS ready_1min,
    COUNT(*) FILTER (WHERE mps.next_retry_at <= NOW() + INTERVAL '5 minutes') AS ready_5min,
    MIN(mps.next_retry_at) AS earliest_retry_at,
    MAX(mps.next_retry_at) AS latest_retry_at,
    AVG(EXTRACT(EPOCH FROM (NOW() - COALESCE(mps.marked_suspicious_at, mps.last_attempt_at, mps.created_at)))) AS avg_wait_seconds,
    MAX(EXTRACT(EPOCH FROM (NOW() - COALESCE(mps.marked_suspicious_at, mps.last_attempt_at, mps.created_at)))) AS max_wait_seconds
FROM model_probe_state mps
JOIN credentials c ON c.id = mps.credential_id
WHERE mps.state IN (
        'suspicious', 'failing',
        'broken_confirmed', 'recovering',
        'unknown', 'probing'
    )
  AND COALESCE(c.status, 'active') = 'active'
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND COALESCE(c.manual_disabled, FALSE) = FALSE
GROUP BY mps.probe_priority, mps.state
ORDER BY
    CASE mps.probe_priority
        WHEN 'urgent' THEN 1
        WHEN 'suspicious' THEN 2
        WHEN 'failing' THEN 3
        WHEN 'recovering' THEN 4
        WHEN 'watchdog' THEN 5
        ELSE 6
    END,
    CASE mps.state
        WHEN 'failing' THEN 1
        WHEN 'broken_confirmed' THEN 2
        WHEN 'suspicious' THEN 3
        WHEN 'recovering' THEN 4
        WHEN 'probing' THEN 5
        WHEN 'unknown' THEN 6
        ELSE 7
    END;

COMMENT ON VIEW v_probe_queue_snapshot IS
    '探测队列快照：使用 model_probe_state 当前状态集 (suspicious/failing/broken_confirmed/recovering/unknown/probing)；与 v_model_health_dashboard 一致口径。';

CREATE OR REPLACE VIEW v_model_priority_details AS
SELECT
    pm.raw_model_name,
    pm.outbound_model_name,
    mps.probe_priority,
    mps.state,
    c.id AS credential_id,
    c.label AS credential_label,
    COALESCE(p.display_name, p.catalog_code, '') AS provider_name,

    mps.last_verified_at,
    mps.next_retry_at,
    mps.marked_suspicious_at,
    mps.probing_started_at,

    mps.consecutive_successes,
    mps.consecutive_failures,
    mps.consecutive_watchdog_successes,
    mps.success_rate_7d,
    mps.verification_interval,

    COALESCE(mps.real_request_success_count, 0) AS real_success_24h,
    COALESCE(mps.real_request_failure_count, 0) AS real_failure_24h,
    mps.last_real_request_at,

    mps.last_unavailable_reason,
    mps.last_err_code,

    CASE
        WHEN mps.next_retry_at IS NULL THEN 'unknown'
        WHEN mps.next_retry_at <= NOW() THEN 'ready'
        WHEN mps.next_retry_at <= NOW() + INTERVAL '1 minute' THEN '<1min'
        WHEN mps.next_retry_at <= NOW() + INTERVAL '5 minutes' THEN '<5min'
        WHEN mps.next_retry_at <= NOW() + INTERVAL '1 hour' THEN '<1h'
        ELSE '>1h'
    END AS retry_in,

    EXTRACT(EPOCH FROM (NOW() - COALESCE(mps.last_attempt_at, mps.created_at))) / 60 AS state_duration_minutes
FROM model_probe_state mps
JOIN credentials c ON c.id = mps.credential_id
JOIN provider_models pm ON pm.raw_model_name = mps.raw_model_name
JOIN providers p ON p.id = c.provider_id
WHERE COALESCE(c.status, 'active') = 'active'
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND COALESCE(c.manual_disabled, FALSE) = FALSE
ORDER BY
    pm.raw_model_name,
    CASE mps.probe_priority
        WHEN 'urgent' THEN 1
        WHEN 'suspicious' THEN 2
        WHEN 'failing' THEN 3
        WHEN 'recovering' THEN 4
        WHEN 'watchdog' THEN 5
        ELSE 6
    END,
    CASE mps.state
        WHEN 'broken_confirmed' THEN 1
        WHEN 'failing' THEN 2
        WHEN 'unavailable' THEN 3
        WHEN 'recovering' THEN 4
        WHEN 'suspicious' THEN 5
        WHEN 'probing' THEN 6
        WHEN 'healthy_confirmed' THEN 7
        WHEN 'available' THEN 8
        WHEN 'healthy' THEN 9
        ELSE 10
    END,
    c.id;

COMMENT ON VIEW v_model_priority_details IS
    '模型优先级详情：每个凭据×模型节点的状态与统计；provider_name 取自 providers.display_name，与其它视图一致。';