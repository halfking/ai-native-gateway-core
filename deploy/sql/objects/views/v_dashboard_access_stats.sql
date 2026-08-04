--
-- Name: v_dashboard_access_stats; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_dashboard_access_stats AS
 SELECT api_path,
    event_type,
    count(*) AS request_count,
    count(DISTINCT user_id) AS unique_users,
    count(DISTINCT tenant_id) AS unique_tenants,
    (avg(response_time_ms))::double precision AS avg_response_ms,
    percentile_cont((0.5)::double precision) WITHIN GROUP (ORDER BY ((response_time_ms)::double precision)) AS p50_ms,
    percentile_cont((0.95)::double precision) WITHIN GROUP (ORDER BY ((response_time_ms)::double precision)) AS p95_ms,
    percentile_cont((0.99)::double precision) WITHIN GROUP (ORDER BY ((response_time_ms)::double precision)) AS p99_ms,
    (((count(*) FILTER (WHERE (cache_hit = true)))::numeric * 100.0) / (NULLIF(count(*), 0))::numeric) AS cache_hit_rate,
    (((count(*) FILTER (WHERE (status_code >= 400)))::numeric * 100.0) / (NULLIF(count(*), 0))::numeric) AS error_rate,
    count(*) FILTER (WHERE (response_time_ms > 1000)) AS slow_query_count,
    max("timestamp") AS last_access_at
   FROM public.dashboard_access_events_hot
  WHERE ("timestamp" > (now() - '24:00:00'::interval))
  GROUP BY api_path, event_type
  ORDER BY (count(*)) DESC;

