--
-- Name: v_session_cache_by_model; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_session_cache_by_model AS
 SELECT last_model AS model,
    count(*) AS session_count,
    count(*) FILTER (WHERE (last_response_cached IS NOT NULL)) AS cached_count,
    round(((100.0 * (count(*) FILTER (WHERE (last_response_cached IS NOT NULL)))::numeric) / (count(*))::numeric), 2) AS cache_rate,
    round((avg(last_latency_ms) / 1000.0), 2) AS avg_latency_seconds
   FROM public.session_last_requests
  WHERE ((expires_at > now()) AND (last_model IS NOT NULL))
  GROUP BY last_model
  ORDER BY (count(*)) DESC;

