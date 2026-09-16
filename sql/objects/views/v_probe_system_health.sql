--
-- Name: v_probe_system_health; Type: VIEW; Schema: public; Owner: -
--
-- 2026-09-17 数据源统一:改读 v_node_probe_state_compat(node_probe_state 投影)。
-- 真实请求 24h 成功/失败改为从 request_logs 实时聚合(排除探测流量)。
-- 列顺序与旧契约完全一致。SSOT: db.ensureProbeHealthDashboardViews(db/db.go)。
--

CREATE VIEW public.v_probe_system_health AS
 SELECT ( SELECT count(*) AS count
           FROM public.v_node_probe_state_compat) AS total_nodes,
    ( SELECT count(*) AS count
           FROM public.v_node_probe_state_compat
          WHERE (v_node_probe_state_compat.state = ANY (ARRAY['healthy_confirmed'::text, 'healthy'::text]))) AS healthy_nodes,
    ( SELECT count(*) AS count
           FROM public.v_node_probe_state_compat
          WHERE (v_node_probe_state_compat.state = ANY (ARRAY['failing'::text, 'broken_confirmed'::text]))) AS failing_nodes,
    ( SELECT count(*) AS count
           FROM public.v_node_probe_state_compat
          WHERE (v_node_probe_state_compat.state = 'suspicious'::text)) AS suspicious_nodes,
    ( SELECT count(*) AS count
           FROM public.v_node_probe_state_compat
          WHERE (v_node_probe_state_compat.state = 'probing'::text)) AS probing_nodes,
    ( SELECT count(*) AS count
           FROM public.v_node_probe_state_compat
          WHERE (v_node_probe_state_compat.consecutive_failures >= 3)) AS urgent_queue_size,
    ( SELECT count(*) AS count
           FROM public.v_node_probe_state_compat
          WHERE (v_node_probe_state_compat.state = 'suspicious'::text)) AS suspicious_queue_size,
    ( SELECT count(*) AS count
           FROM public.v_node_probe_state_compat
          WHERE (v_node_probe_state_compat.state = ANY (ARRAY['failing'::text, 'broken_confirmed'::text]))) AS failing_queue_size,
    ( SELECT count(*) AS count
           FROM public.v_node_probe_state_compat
          WHERE (v_node_probe_state_compat.state = 'healthy_confirmed'::text)) AS watchdog_queue_size,
    ( SELECT count(*) AS count
           FROM public.v_node_probe_state_compat
          WHERE ((v_node_probe_state_compat.next_retry_at <= now()) AND (v_node_probe_state_compat.state <> 'probing'::text) AND (v_node_probe_state_compat.paused = false))) AS ready_probes,
    ( SELECT count(*) AS count
           FROM public.v_node_probe_state_compat
          WHERE (v_node_probe_state_compat.state = 'probing'::text)) AS current_probing,
    ( SELECT count(DISTINCT v_node_probe_state_compat.credential_id) AS count
           FROM public.v_node_probe_state_compat
          WHERE (v_node_probe_state_compat.state = 'probing'::text)) AS credentials_being_probed,
    ( SELECT round(avg(
                CASE
                    WHEN (v_node_probe_state_compat.total_attempts > 0) THEN (((v_node_probe_state_compat.consecutive_successes)::double precision / (v_node_probe_state_compat.total_attempts)::double precision) * (100)::double precision)
                    ELSE NULL::double precision
                END)::numeric, 2)) AS avg_success_rate_7d,
    ( SELECT max(v_node_probe_state_compat.last_attempt_at) AS max
           FROM public.v_node_probe_state_compat) AS last_probe_at,
    ( SELECT max(v_node_probe_state_compat.last_state_change_at) AS max
           FROM public.v_node_probe_state_compat) AS last_real_request_at,
    ( SELECT COALESCE(sum(s.ok_24h), 0) AS coalesce
           FROM ( SELECT count(*) FILTER (WHERE (rl.success)) AS ok_24h
                   FROM public.request_logs_with_current_month rl
                  WHERE ((rl.ts > (now() - '24:00:00'::interval)) AND (coalesce(rl.task_type, '') <> 'probe_triggered') AND NOT ('probe' = ANY (rl.quality_flags)))) s) AS total_real_success_24h,
    ( SELECT COALESCE(sum(s.fail_24h), 0) AS coalesce
           FROM ( SELECT count(*) FILTER (WHERE (NOT rl.success)) AS fail_24h
                   FROM public.request_logs_with_current_month rl
                  WHERE ((rl.ts > (now() - '24:00:00'::interval)) AND (coalesce(rl.task_type, '') <> 'probe_triggered') AND NOT ('probe' = ANY (rl.quality_flags)))) s) AS total_real_failure_24h,
    ( SELECT count(*) AS count
           FROM public.v_node_probe_state_compat
          WHERE ((v_node_probe_state_compat.state = ANY (ARRAY['failing'::text, 'broken_confirmed'::text])) AND (v_node_probe_state_compat.consecutive_failures >= 5))) AS critical_nodes,
    ( SELECT count(*) AS count
           FROM public.v_node_probe_state_compat
          WHERE ((v_node_probe_state_compat.next_retry_at <= (now() + '00:05:00'::interval)) AND (v_node_probe_state_compat.state <> 'probing'::text) AND (v_node_probe_state_compat.paused = false))) AS pending_probes_5min,
    now() AS snapshot_at;
