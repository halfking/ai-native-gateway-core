--
-- Name: v_recent_model_probe_failures; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_recent_model_probe_failures AS
 SELECT raw_model_name,
    credential_id,
    count(*) AS failed_count,
    max(created_at) AS last_failed_at,
    min(error_code) AS sample_error_code
   FROM public.model_probe_runs
  WHERE ((status <> 'ok'::text) AND (status <> 'skipped'::text) AND (created_at > (now() - '06:00:00'::interval)))
  GROUP BY raw_model_name, credential_id;


--
-- Name: VIEW v_recent_model_probe_failures; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.v_recent_model_probe_failures IS 'Last 6h failed probe count, grouped by (model, credential). Used by model discovery UI badge.';

