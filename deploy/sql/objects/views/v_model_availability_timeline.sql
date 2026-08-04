--
-- Name: v_model_availability_timeline; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_model_availability_timeline AS
 SELECT raw_model_name,
    raw_model_name AS outbound_model_name,
    date_trunc('hour'::text, created_at) AS hour_bucket,
    count(*) AS total_probes,
    count(*) FILTER (WHERE (status = 'ok'::text)) AS successful_probes,
    count(*) FILTER (WHERE (status <> 'ok'::text)) AS failed_probes,
    round((((count(*) FILTER (WHERE (status = 'ok'::text)))::numeric * 100.0) / (count(*))::numeric), 2) AS success_rate,
    avg(latency_ms) FILTER (WHERE (status = 'ok'::text)) AS avg_latency_ms,
    count(DISTINCT credential_id) AS probed_credentials,
    count(DISTINCT credential_id) FILTER (WHERE (status = 'ok'::text)) AS successful_credentials,
    count(DISTINCT credential_id) FILTER (WHERE (status <> 'ok'::text)) AS failed_credentials
   FROM public.model_probe_runs_with_current_month mpr
  WHERE (created_at >= (now() - '24:00:00'::interval))
  GROUP BY raw_model_name, (date_trunc('hour'::text, created_at))
  ORDER BY raw_model_name, (date_trunc('hour'::text, created_at)) DESC;

