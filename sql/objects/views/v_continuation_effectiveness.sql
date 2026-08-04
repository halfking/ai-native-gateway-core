--
-- Name: v_continuation_effectiveness; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_continuation_effectiveness AS
 SELECT date_trunc('hour'::text, ts) AS time_bucket,
    count(*) FILTER (WHERE (is_continuation = true)) AS continuation_requests,
    count(*) FILTER (WHERE (cached_response_id IS NOT NULL)) AS cache_hits,
    count(*) FILTER (WHERE ((is_continuation = true) AND (cached_response_id IS NULL))) AS cache_misses,
    round(((100.0 * (count(*) FILTER (WHERE (cached_response_id IS NOT NULL)))::numeric) / (NULLIF(count(*) FILTER (WHERE (is_continuation = true)), 0))::numeric), 2) AS cache_hit_rate,
    sum(COALESCE(context_size_tokens, 0)) FILTER (WHERE (cached_response_id IS NOT NULL)) AS tokens_saved
   FROM public.request_logs
  WHERE (ts > (now() - '24:00:00'::interval))
  GROUP BY (date_trunc('hour'::text, ts))
 HAVING (count(*) FILTER (WHERE (is_continuation = true)) > 0)
  ORDER BY (date_trunc('hour'::text, ts)) DESC;

