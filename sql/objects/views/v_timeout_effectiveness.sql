--
-- Name: v_timeout_effectiveness; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_timeout_effectiveness AS
 SELECT date_trunc('hour'::text, ts) AS time_bucket,
    timeout_mode,
    count(*) AS total_requests,
    count(*) FILTER (WHERE (success = true)) AS success_count,
    count(*) FILTER (WHERE ((success = false) AND (error_kind ~~ '%timeout%'::text))) AS timeout_count,
    round(((100.0 * (count(*) FILTER (WHERE ((success = false) AND (error_kind ~~ '%timeout%'::text))))::numeric) / (count(*))::numeric), 2) AS timeout_rate,
    round(avg(effective_timeout_seconds), 2) AS avg_effective_timeout,
    round((avg(latency_ms) / 1000.0), 2) AS avg_latency_seconds
   FROM public.request_logs
  WHERE ((ts > (now() - '24:00:00'::interval)) AND (effective_timeout_seconds IS NOT NULL))
  GROUP BY (date_trunc('hour'::text, ts)), timeout_mode
  ORDER BY (date_trunc('hour'::text, ts)) DESC;

