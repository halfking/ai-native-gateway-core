--
-- Name: system_metrics_local; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.system_metrics_local AS
 SELECT instance_id,
    "timestamp",
    cpu_usage_pct,
    mem_used_mb,
    mem_total_mb,
    ((((mem_used_mb)::double precision / (NULLIF(mem_total_mb, 0))::double precision) * (100)::double precision))::real AS mem_usage_pct,
    disk_used_gb,
    disk_total_gb,
    ((((disk_used_gb)::double precision / (NULLIF(disk_total_gb, 0))::double precision) * (100)::double precision))::real AS disk_usage_pct,
    current_concurrency,
    last_5min_tps,
    last_5min_p50_ms,
    last_5min_p99_ms,
    last_5min_success_pct
   FROM public.runtime_metrics
  WHERE ("timestamp" >= (now() - '24:00:00'::interval))
  ORDER BY "timestamp" DESC;


--
-- Name: VIEW system_metrics_local; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.system_metrics_local IS '最近 24 小时本地系统指标汇总。用于与 request_logs 时间戳对齐分析负载与性能关系。';

