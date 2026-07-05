--
-- Name: v_recent_model_probe_failures; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_recent_model_probe_failures AS
 SELECT model_probe_runs.raw_model_name,
    model_probe_runs.credential_id,
    count(*) AS failed_count,
    max(model_probe_runs.created_at) AS last_failed_at,
    min(model_probe_runs.error_code) AS sample_error_code
   FROM public.model_probe_runs
  WHERE ((model_probe_runs.status <> 'ok'::text) AND (model_probe_runs.status <> 'skipped'::text) AND (model_probe_runs.created_at > (now() - '06:00:00'::interval)))
  GROUP BY model_probe_runs.raw_model_name, model_probe_runs.credential_id;

