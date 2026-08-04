--
-- Name: tuning_signals_daily; Type: MATERIALIZED VIEW; Schema: public; Owner: -
--

CREATE MATERIALIZED VIEW public.tuning_signals_daily AS
 SELECT date_trunc('day'::text, ts) AS bucket,
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
  WHERE (ts >= (now() - '90 days'::interval))
  GROUP BY (date_trunc('day'::text, ts)), task_type, classifier
  WITH NO DATA;

