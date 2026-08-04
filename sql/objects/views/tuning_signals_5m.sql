--
-- Name: tuning_signals_5m; Type: MATERIALIZED VIEW; Schema: public; Owner: -
--

CREATE MATERIALIZED VIEW public.tuning_signals_5m AS
 SELECT (date_trunc('hour'::text, ts) + (floor((((EXTRACT(minute FROM ts))::integer / 5))::double precision) * '00:05:00'::interval)) AS bucket,
    task_type,
    classifier,
    count(*) AS total,
    avg(quality_score) AS avg_quality,
    avg(success_score) AS avg_success,
    avg(latency_score) AS avg_latency,
    avg(cost_score) AS avg_cost,
    ((sum(
        CASE
            WHEN drift_flag THEN 1
            ELSE 0
        END))::double precision / (NULLIF(count(*), 0))::double precision) AS drift_rate
   FROM public.tuning_signals
  WHERE (ts >= (now() - '7 days'::interval))
  GROUP BY (date_trunc('hour'::text, ts) + (floor((((EXTRACT(minute FROM ts))::integer / 5))::double precision) * '00:05:00'::interval)), task_type, classifier
  WITH NO DATA;

