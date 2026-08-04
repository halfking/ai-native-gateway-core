--
-- Name: stage_performance_recent; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.stage_performance_recent AS
 SELECT stage,
    count(*) AS total_events,
    avg(duration_ms) AS avg_duration_ms,
    percentile_cont((0.50)::double precision) WITHIN GROUP (ORDER BY ((duration_ms)::double precision)) AS p50_duration_ms,
    percentile_cont((0.99)::double precision) WITHIN GROUP (ORDER BY ((duration_ms)::double precision)) AS p99_duration_ms,
    count(*) FILTER (WHERE (status = 'failed'::text)) AS failed_count,
    count(*) FILTER (WHERE (status = 'timeout'::text)) AS timeout_count,
    count(*) FILTER (WHERE (status = 'success'::text)) AS success_count,
    (((count(*) FILTER (WHERE (status = 'failed'::text)))::double precision / (NULLIF(count(*), 0))::double precision) * (100)::double precision) AS failure_rate_pct,
    count(*) FILTER (WHERE (redis_hit = true)) AS redis_hit_count,
    count(*) FILTER (WHERE (redis_hit = false)) AS redis_miss_count,
    (((count(*) FILTER (WHERE (redis_hit = true)))::double precision / (NULLIF(count(*), 0))::double precision) * (100)::double precision) AS redis_hit_rate_pct
   FROM public.request_stage_events
  WHERE (event_timestamp >= (now() - '01:00:00'::interval))
  GROUP BY stage
  ORDER BY (avg(duration_ms)) DESC NULLS LAST;


--
-- Name: VIEW stage_performance_recent; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.stage_performance_recent IS '最近 1 小时各阶段性能统计（平均耗时、P50/P99、失败率、缓存命中率）。';

