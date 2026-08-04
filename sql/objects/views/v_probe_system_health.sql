--
-- Name: v_probe_system_health; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_probe_system_health AS
 SELECT ( SELECT count(*) AS count
           FROM public.model_probe_state) AS total_nodes,
    ( SELECT count(*) AS count
           FROM public.model_probe_state
          WHERE (model_probe_state.state = ANY (ARRAY['healthy_confirmed'::text, 'healthy'::text]))) AS healthy_nodes,
    ( SELECT count(*) AS count
           FROM public.model_probe_state
          WHERE (model_probe_state.state = ANY (ARRAY['failing'::text, 'broken_confirmed'::text]))) AS failing_nodes,
    ( SELECT count(*) AS count
           FROM public.model_probe_state
          WHERE (model_probe_state.state = 'suspicious'::text)) AS suspicious_nodes,
    ( SELECT count(*) AS count
           FROM public.model_probe_state
          WHERE (model_probe_state.state = 'probing'::text)) AS probing_nodes,
    ( SELECT count(*) AS count
           FROM public.model_probe_state
          WHERE (model_probe_state.consecutive_failures >= 3)) AS urgent_queue_size,
    ( SELECT count(*) AS count
           FROM public.model_probe_state
          WHERE (model_probe_state.state = 'suspicious'::text)) AS suspicious_queue_size,
    ( SELECT count(*) AS count
           FROM public.model_probe_state
          WHERE (model_probe_state.state = ANY (ARRAY['failing'::text, 'recovering'::text]))) AS failing_queue_size,
    ( SELECT count(*) AS count
           FROM public.model_probe_state
          WHERE (model_probe_state.state = 'healthy_confirmed'::text)) AS watchdog_queue_size,
    ( SELECT count(*) AS count
           FROM public.model_probe_state
          WHERE ((model_probe_state.next_retry_at <= now()) AND (model_probe_state.state <> 'probing'::text))) AS ready_probes,
    ( SELECT count(*) AS count
           FROM public.model_probe_state
          WHERE (model_probe_state.state = 'probing'::text)) AS current_probing,
    ( SELECT count(DISTINCT model_probe_state.credential_id) AS count
           FROM public.model_probe_state
          WHERE (model_probe_state.state = 'probing'::text)) AS credentials_being_probed,
    ( SELECT round((avg(
                CASE
                    WHEN (model_probe_state.total_attempts > 0) THEN (((model_probe_state.consecutive_successes)::double precision / (model_probe_state.total_attempts)::double precision) * (100)::double precision)
                    ELSE NULL::double precision
                END))::numeric, 2) AS round
           FROM public.model_probe_state) AS avg_success_rate_7d,
    ( SELECT max(model_probe_state.last_attempt_at) AS max
           FROM public.model_probe_state) AS last_probe_at,
    ( SELECT max(model_probe_state.last_attempt_at) AS max
           FROM public.model_probe_state) AS last_real_request_at,
    0 AS total_real_success_24h,
    0 AS total_real_failure_24h,
    ( SELECT count(*) AS count
           FROM public.model_probe_state
          WHERE ((model_probe_state.state = ANY (ARRAY['failing'::text, 'broken_confirmed'::text])) AND (model_probe_state.consecutive_failures >= 5))) AS critical_nodes,
    ( SELECT count(*) AS count
           FROM public.model_probe_state
          WHERE ((model_probe_state.next_retry_at <= (now() + '00:05:00'::interval)) AND (model_probe_state.state <> 'probing'::text))) AS pending_probes_5min,
    now() AS snapshot_at;

