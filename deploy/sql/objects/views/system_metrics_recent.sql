--
-- Name: system_metrics_recent; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.system_metrics_recent AS
 SELECT instance_id,
    date_trunc('minute'::text, "timestamp") AS time_bucket,
    avg(cpu_usage_pct) AS avg_cpu_pct,
    max(cpu_usage_pct) AS max_cpu_pct,
    avg((((mem_used_mb)::double precision / (NULLIF(mem_total_mb, 0))::double precision) * (100)::double precision)) AS avg_mem_pct,
    max((((mem_used_mb)::double precision / (NULLIF(mem_total_mb, 0))::double precision) * (100)::double precision)) AS max_mem_pct,
    avg((((disk_used_gb)::double precision / (NULLIF(disk_total_gb, 0))::double precision) * (100)::double precision)) AS avg_disk_pct,
    max(current_concurrency) AS max_concurrency,
    avg(last_5min_tps) AS avg_tps,
    percentile_cont((0.50)::double precision) WITHIN GROUP (ORDER BY ((last_5min_p50_ms)::double precision)) AS p50_latency_ms,
    percentile_cont((0.99)::double precision) WITHIN GROUP (ORDER BY ((last_5min_p99_ms)::double precision)) AS p99_latency_ms,
    avg(last_5min_success_pct) AS avg_success_pct,
    count(*) AS sample_count
   FROM public.runtime_metrics
  WHERE ("timestamp" >= (now() - '24:00:00'::interval))
  GROUP BY instance_id, (date_trunc('minute'::text, "timestamp"))
  ORDER BY (date_trunc('minute'::text, "timestamp")) DESC;

