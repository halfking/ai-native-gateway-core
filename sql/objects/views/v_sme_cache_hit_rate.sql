--
-- Name: v_sme_cache_hit_rate; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_sme_cache_hit_rate AS
 SELECT module_name,
    count(*) FILTER (WHERE ((status)::text = 'completed'::text)) AS total_executions,
    count(*) FILTER (WHERE ((status)::text = 'skipped'::text)) AS cache_skips,
    round((((count(*) FILTER (WHERE ((status)::text = 'skipped'::text)))::numeric * 100.0) / (NULLIF(count(*), 0))::numeric), 2) AS skip_rate_pct
   FROM public.session_module_executions_hot
  WHERE (created_at > (now() - '24:00:00'::interval))
  GROUP BY module_name;

