--
-- Name: v_session_cache_stats; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_session_cache_stats AS
 SELECT last_request_status AS status,
    count(*) AS session_count,
    count(*) FILTER (WHERE (last_response_cached IS NOT NULL)) AS cached_count,
    round(avg(last_response_chunks), 2) AS avg_chunks,
    round((avg(last_latency_ms) / 1000.0), 2) AS avg_latency_seconds,
    round(avg((EXTRACT(epoch FROM (now() - updated_at)) / 60.0)), 2) AS avg_age_minutes
   FROM public.session_last_requests
  WHERE (expires_at > now())
  GROUP BY last_request_status
  ORDER BY (count(*)) DESC;

