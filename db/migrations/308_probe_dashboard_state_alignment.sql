-- Migration 305: Probe dashboard state alignment and null-safe aggregates
--
-- Why:
--   1. /probe-health reads PostgreSQL dashboard views, while Redis cache-state
--      uses the newer state set: healthy_confirmed / broken_confirmed /
--      recovering / suspicious / probing.
--   2. The original dashboard views still counted legacy states like
--      healthy/failing, which caused misleading totals.
--   3. Several SUM/AVG expressions returned NULL for sparse datasets, which
--      could make admin handlers fail their Scan and render the page blank.

CREATE OR REPLACE VIEW v_model_health_dashboard AS
WITH model_stats AS (
    SELECT
        pm.id AS provider_model_id,
        pm.raw_model_name,
        pm.outbound_model_name,
        pm.protocol,
        p.name AS provider_name,

        COUNT(*) AS total_credentials,
        COUNT(*) FILTER (WHERE mps.state IN ('healthy', 'healthy_confirmed', 'available')) AS healthy_count,
        COUNT(*) FILTER (WHERE mps.state = 'suspicious') AS suspicious_count,
        COUNT(*) FILTER (WHERE mps.state IN ('failing', 'broken_confirmed', 'unavailable', 'recovering')) AS failing_count,
        COUNT(*) FILTER (WHERE mps.state = 'probing') AS probing_count,

        COUNT(*) FILTER (WHERE mps.probe_priority = 'urgent') AS urgent_count,
        COUNT(*) FILTER (WHERE mps.probe_priority = 'suspicious') AS suspicious_priority_count,
        COUNT(*) FILTER (WHERE mps.probe_priority = 'failing') AS failing_priority_count,
        COUNT(*) FILTER (WHERE mps.probe_priority = 'watchdog') AS watchdog_count,

        AVG(mps.success_rate_7d) AS avg_success_rate_7d,
        AVG(EXTRACT(EPOCH FROM mps.verification_interval) / 3600) AS avg_verification_hours,
        AVG(mps.consecutive_watchdog_successes) AS avg_consecutive_successes,

        COALESCE(SUM(mps.real_request_success_count), 0) AS total_real_success_24h,
        COALESCE(SUM(mps.real_request_failure_count), 0) AS total_real_failure_24h,

        MAX(mps.last_verified_at) AS last_verified_at,
        MAX(mps.last_real_request_at) AS last_real_request_at,
        MIN(mps.next_retry_at) AS next_probe_at,

        COUNT(*) FILTER (
            WHERE mps.state IN ('failing', 'broken_confirmed', 'unavailable')
              AND mps.consecutive_failures >= 3
        ) AS critical_nodes,

        COUNT(*) FILTER (
            WHERE mps.next_retry_at <= NOW() + INTERVAL '5 minutes'
              AND mps.state != 'probing'
        ) AS pending_probes_5min
    FROM provider_models pm
    LEFT JOIN credential_model_bindings cmb ON cmb.provider_model_id = pm.id
    LEFT JOIN model_probe_state mps ON mps.credential_id = cmb.credential_id
        AND mps.raw_model_name = pm.raw_model_name
    LEFT JOIN credentials c ON c.id = cmb.credential_id
    LEFT JOIN providers p ON p.id = pm.provider_id
    WHERE COALESCE(c.status, 'active') = 'active'
      AND COALESCE(c.lifecycle_status, 'active') = 'active'
      AND COALESCE(c.manual_disabled, FALSE) = FALSE
    GROUP BY pm.id, pm.raw_model_name, pm.outbound_model_name, pm.protocol, p.name
)
SELECT
    provider_model_id,
    raw_model_name,
    outbound_model_name,
    protocol,
    provider_name,
    total_credentials,
    healthy_count,
    suspicious_count,
    failing_count,
    probing_count,
    ROUND(COALESCE(healthy_count * 100.0 / NULLIF(total_credentials, 0), 0), 1) AS healthy_percentage,
    ROUND(COALESCE(failing_count * 100.0 / NULLIF(total_credentials, 0), 0), 1) AS failing_percentage,
    urgent_count,
    suspicious_priority_count,
    failing_priority_count,
    watchdog_count,
    ROUND(avg_success_rate_7d, 2) AS avg_success_rate_7d,
    ROUND(avg_verification_hours, 1) AS avg_verification_hours,
    ROUND(avg_consecutive_successes, 1) AS avg_consecutive_successes,
    total_real_success_24h,
    total_real_failure_24h,
    CASE
        WHEN (total_real_success_24h + total_real_failure_24h) > 0
        THEN ROUND(total_real_success_24h * 100.0 / (total_real_success_24h + total_real_failure_24h), 2)
        ELSE NULL
    END AS real_success_rate_24h,
    last_verified_at,
    last_real_request_at,
    next_probe_at,
    critical_nodes,
    pending_probes_5min,
    CASE
        WHEN critical_nodes > 0 THEN 'critical'
        WHEN ROUND(COALESCE(failing_count * 100.0 / NULLIF(total_credentials, 0), 0), 1) > 20 THEN 'warning'
        WHEN ROUND(COALESCE(failing_count * 100.0 / NULLIF(total_credentials, 0), 0), 1) > 10 THEN 'degraded'
        WHEN ROUND(COALESCE(healthy_count * 100.0 / NULLIF(total_credentials, 0), 0), 1) >= 90 THEN 'healthy'
        ELSE 'unknown'
    END AS overall_health
FROM model_stats
ORDER BY
    CASE
        WHEN critical_nodes > 0 THEN 1
        WHEN urgent_count > 0 THEN 2
        WHEN ROUND(COALESCE(failing_count * 100.0 / NULLIF(total_credentials, 0), 0), 1) > 20 THEN 3
        ELSE 4
    END,
    total_credentials DESC,
    raw_model_name;

CREATE OR REPLACE VIEW v_probe_system_health AS
SELECT
    (SELECT COUNT(*) FROM model_probe_state) AS total_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state IN ('healthy', 'healthy_confirmed', 'available')) AS healthy_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state IN ('failing', 'broken_confirmed', 'unavailable', 'recovering')) AS failing_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'suspicious') AS suspicious_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'probing') AS probing_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE probe_priority = 'urgent') AS urgent_queue_size,
    (SELECT COUNT(*) FROM model_probe_state WHERE probe_priority = 'suspicious') AS suspicious_queue_size,
    (SELECT COUNT(*) FROM model_probe_state WHERE probe_priority = 'failing') AS failing_queue_size,
    (SELECT COUNT(*) FROM model_probe_state WHERE probe_priority = 'watchdog') AS watchdog_queue_size,
    (SELECT COUNT(*) FROM model_probe_state WHERE next_retry_at <= NOW() AND state != 'probing') AS ready_probes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'probing') AS current_probing,
    (SELECT COUNT(DISTINCT credential_id) FROM model_probe_state WHERE state = 'probing') AS credentials_being_probed,
    (SELECT ROUND(AVG(success_rate_7d), 2) FROM model_probe_state WHERE success_rate_7d IS NOT NULL) AS avg_success_rate_7d,
    (SELECT MAX(last_verified_at) FROM model_probe_state) AS last_probe_at,
    (SELECT MAX(last_real_request_at) FROM model_probe_state) AS last_real_request_at,
    (SELECT COALESCE(SUM(real_request_success_count), 0) FROM model_probe_state) AS total_real_success_24h,
    (SELECT COALESCE(SUM(real_request_failure_count), 0) FROM model_probe_state) AS total_real_failure_24h,
    (SELECT COUNT(*) FROM model_probe_state WHERE state IN ('failing', 'broken_confirmed', 'unavailable') AND consecutive_failures >= 5) AS critical_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE next_retry_at <= NOW() + INTERVAL '5 minutes' AND state != 'probing') AS pending_probes_5min,
    NOW() AS snapshot_at;

CREATE OR REPLACE FUNCTION get_model_state_summary(p_raw_model_name TEXT)
RETURNS TABLE (
    state TEXT,
    priority TEXT,
    count BIGINT,
    avg_success_rate NUMERIC,
    next_probe_in_seconds INTEGER
)
LANGUAGE SQL
STABLE
AS $$
    SELECT
        mps.state::TEXT,
        mps.probe_priority::TEXT,
        COUNT(*) AS count,
        ROUND(AVG(mps.success_rate_7d), 2) AS avg_success_rate,
        EXTRACT(EPOCH FROM MIN(mps.next_retry_at - NOW()))::INTEGER AS next_probe_in_seconds
    FROM model_probe_state mps
    JOIN credentials c ON c.id = mps.credential_id
    WHERE mps.raw_model_name = p_raw_model_name
      AND COALESCE(c.status, 'active') = 'active'
      AND COALESCE(c.lifecycle_status, 'active') = 'active'
      AND COALESCE(c.manual_disabled, FALSE) = FALSE
    GROUP BY mps.state, mps.probe_priority
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
        END;
$$;