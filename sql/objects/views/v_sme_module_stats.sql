--
-- Name: v_sme_module_stats; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_sme_module_stats AS
 SELECT module_name,
    status,
    count(*) AS execution_count,
    (avg(duration_ms))::integer AS avg_duration_ms,
    (percentile_cont((0.5)::double precision) WITHIN GROUP (ORDER BY ((duration_ms)::double precision)))::integer AS p50_duration_ms,
    (percentile_cont((0.95)::double precision) WITHIN GROUP (ORDER BY ((duration_ms)::double precision)))::integer AS p95_duration_ms,
    (percentile_cont((0.99)::double precision) WITHIN GROUP (ORDER BY ((duration_ms)::double precision)))::integer AS p99_duration_ms,
    count(DISTINCT gw_session_id) AS unique_sessions,
    count(*) FILTER (WHERE (created_at > (now() - '01:00:00'::interval))) AS executions_last_hour
   FROM public.session_module_executions_hot
  WHERE (created_at > (now() - '24:00:00'::interval))
  GROUP BY module_name, status;

