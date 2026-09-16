--
-- Name: v_model_health_dashboard; Type: VIEW; Schema: public; Owner: -
--
-- 2026-09-17 数据源统一:改读 v_node_probe_state_compat(node_probe_state 投影)。
-- 旧定义读 model_probe_state,在 useNewProbeMode 下停更,probe-health 页曾显示
-- 冻结数月的 healthy(与热力图/路由不一致)。真实请求 24h 成功/失败改为从
-- request_logs 实时聚合(请求结果反馈展示闭环)。列顺序与旧契约完全一致
-- (admin/probe_dashboard.go SELECT * 按位置 Scan)。
-- SSOT: db.ensureProbeHealthDashboardViews(db/db.go),此文件为基线镜像。
--

CREATE VIEW public.v_model_health_dashboard AS
 WITH real24 AS (
         SELECT rl.credential_id,
            lower(coalesce(rl.outbound_model, rl.client_model)) AS model_name,
            count(*) FILTER (WHERE rl.success) AS ok_24h,
            count(*) FILTER (WHERE NOT rl.success) AS fail_24h,
            max(rl.ts) AS last_real_request_at
           FROM public.request_logs_with_current_month rl
          WHERE (rl.ts > (now() - '24:00:00'::interval))
            AND (coalesce(rl.task_type, '') <> 'probe_triggered')
            AND NOT ('probe' = ANY (rl.quality_flags))
          GROUP BY rl.credential_id, lower(coalesce(rl.outbound_model, rl.client_model))
        ),
        model_stats AS (
         SELECT mps.raw_model_name,
            mps.raw_model_name AS outbound_model_name,
            'openai-completions'::text AS protocol,
            p.display_name AS provider_name,
            count(*) AS total_credentials,
            count(*) FILTER (WHERE (mps.state = ANY (ARRAY['healthy_confirmed'::text, 'healthy'::text]))) AS healthy_count,
            count(*) FILTER (WHERE (mps.state = 'suspicious'::text)) AS suspicious_count,
            count(*) FILTER (WHERE (mps.state = ANY (ARRAY['failing'::text, 'recovering'::text, 'broken_confirmed'::text]))) AS failing_count,
            count(*) FILTER (WHERE (mps.state = 'probing'::text)) AS probing_count,
            sum(
                CASE
                    WHEN (mps.consecutive_failures >= 3) THEN 1
                    ELSE 0
                END) AS urgent_count,
            count(*) FILTER (WHERE (mps.state = 'suspicious'::text)) AS suspicious_priority_count,
            count(*) FILTER (WHERE (mps.state = ANY (ARRAY['failing'::text, 'recovering'::text, 'broken_confirmed'::text]))) AS failing_priority_count,
            count(*) FILTER (WHERE (mps.state = 'healthy_confirmed'::text)) AS watchdog_count,
            avg(
                CASE
                    WHEN (mps.total_attempts > 0) THEN (((mps.consecutive_successes)::double precision / (mps.total_attempts)::double precision) * (100)::double precision)
                    ELSE NULL::double precision
                END) AS avg_success_rate_7d,
            avg((EXTRACT(epoch FROM (mps.next_retry_at - now())) / (3600)::numeric)) AS avg_verification_hours,
            avg(mps.consecutive_successes) AS avg_consecutive_successes,
            coalesce(sum(real24.ok_24h), 0) AS total_real_success_24h,
            coalesce(sum(real24.fail_24h), 0) AS total_real_failure_24h,
            max(mps.last_attempt_at) AS last_verified_at,
            max(real24.last_real_request_at) AS last_real_request_at,
            min(mps.next_retry_at) AS next_probe_at,
            sum(
                CASE
                    WHEN ((mps.state = ANY (ARRAY['failing'::text, 'recovering'::text, 'broken_confirmed'::text])) AND (mps.consecutive_failures >= 3)) THEN 1
                    ELSE 0
                END) AS critical_nodes,
            count(*) FILTER (WHERE ((mps.next_retry_at <= (now() + '00:05:00'::interval)) AND (mps.state <> 'probing'::text) AND (mps.paused = false))) AS pending_probes_5min
           FROM ((public.v_node_probe_state_compat mps
             JOIN public.credentials c ON ((c.id = mps.credential_id)))
             JOIN public.providers p ON ((p.id = c.provider_id)))
             LEFT JOIN real24 ON ((real24.credential_id = mps.credential_id) AND (real24.model_name = lower(mps.raw_model_name)))
          WHERE ((COALESCE(c.status, 'active'::text) = 'active'::text) AND (COALESCE(c.lifecycle_status, 'active'::text) = 'active'::text) AND (COALESCE(c.manual_disabled, false) = false))
          GROUP BY mps.raw_model_name, p.display_name
        )
 SELECT 0 AS provider_model_id,
    raw_model_name,
    outbound_model_name,
    protocol,
    provider_name,
    total_credentials,
    healthy_count,
    suspicious_count,
    failing_count,
    probing_count,
    round((((healthy_count)::numeric * 100.0) / (NULLIF(total_credentials, 0))::numeric), 1) AS healthy_percentage,
    round((((failing_count)::numeric * 100.0) / (NULLIF(total_credentials, 0))::numeric), 1) AS failing_percentage,
    urgent_count,
    suspicious_priority_count,
    failing_priority_count,
    watchdog_count,
    round((avg_success_rate_7d)::numeric, 2) AS avg_success_rate_7d,
    round(avg_verification_hours, 1) AS avg_verification_hours,
    round(avg_consecutive_successes, 1) AS avg_consecutive_successes,
    total_real_success_24h,
    total_real_failure_24h,
        CASE
            WHEN ((total_real_success_24h + total_real_failure_24h) > 0) THEN round((((total_real_success_24h)::numeric * 100.0) / ((total_real_success_24h + total_real_failure_24h))::numeric), 2)
            ELSE NULL::numeric
        END AS real_success_rate_24h,
    last_verified_at,
    last_real_request_at,
    next_probe_at,
    critical_nodes,
    pending_probes_5min,
        CASE
            WHEN (critical_nodes > 0) THEN 'critical'::text
            WHEN (round((((failing_count)::numeric * 100.0) / (NULLIF(total_credentials, 0))::numeric), 1) > (20)::numeric) THEN 'warning'::text
            WHEN (round((((failing_count)::numeric * 100.0) / (NULLIF(total_credentials, 0))::numeric), 1) > (10)::numeric) THEN 'degraded'::text
            WHEN (round((((healthy_count)::numeric * 100.0) / (NULLIF(total_credentials, 0))::numeric), 1) >= (90)::numeric) THEN 'healthy'::text
            ELSE 'normal'::text
        END AS overall_health
   FROM model_stats
  ORDER BY
        CASE
            WHEN (critical_nodes > 0) THEN 1
            WHEN (urgent_count > 0) THEN 2
            WHEN (round((((failing_count)::numeric * 100.0) / (NULLIF(total_credentials, 0))::numeric), 1) > (20)::numeric) THEN 3
            ELSE 4
        END, total_credentials DESC, raw_model_name;
