--
-- Name: v_node_probe_state_compat; Type: VIEW; Schema: public; Owner: -
--
-- 单一节点状态事实源(node_probe_state)到旧 model_probe_state 词汇的兼容投影。
-- 2026-09-17 起 probe-health 视图族(v_model_health_dashboard /
-- v_probe_queue_snapshot / v_model_priority_details / v_probe_system_health /
-- get_model_state_summary)与凭据列表 probe_state、热力图 node_status 统一走
-- 本视图;旧 model_probe_state 在 useNewProbeMode 下停更,曾导致 probe-health
-- 显示冻结的 healthy 而真实请求已 410/403。
--
-- SSOT 说明:网关每次启动会由 db.ensureProbeHealthDashboardViews DROP+重建
-- 本视图(以 db/db.go 内嵌 SQL 为准),此文件为 schema 基线镜像。
--

CREATE VIEW public.v_node_probe_state_compat AS
 SELECT nps.credential_id,
    nps.raw_model_name,
    CASE
        WHEN nps.paused = TRUE AND nps.last_err_code = 'manual_offline' THEN 'unknown'
        WHEN nps.in_flight_until IS NOT NULL AND nps.in_flight_until > NOW() THEN 'probing'
        WHEN nps.last_direct_ok IS NULL THEN 'probing'
        WHEN nps.last_direct_ok THEN 'healthy_confirmed'
        WHEN nps.consecutive_failures >= 3 THEN 'broken_confirmed'
        ELSE 'suspicious'
    END AS state,
    CASE
        WHEN (nps.consecutive_failures >= 3) THEN 'urgent'
        WHEN (nps.consecutive_failures >= 1) THEN 'suspicious'
        ELSE 'watchdog'
    END AS probe_priority,
    nps.consecutive_successes,
    nps.consecutive_failures,
    (nps.consecutive_successes + nps.consecutive_failures) AS total_attempts,
    nps.last_attempt_at,
    nps.next_retry_at,
    nps.last_err_code AS last_status,
    nps.updated_at AS last_state_change_at,
    nps.last_run_id AS last_state_change_run,
    nps.paused,
    nps.in_flight_until,
    nps.last_direct_ok,
    nps.last_gateway_ok,
    nps.last_err_detail,
    nps.updated_at
 FROM public.node_probe_state nps;
