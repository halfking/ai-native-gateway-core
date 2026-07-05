--
-- Name: tuning_signals_5m; Type: MATERIALIZED VIEW; Schema: public; Owner: -
--

CREATE MATERIALIZED VIEW public.tuning_signals_5m AS
 SELECT (date_trunc('hour'::text, tuning_signals.ts) + (floor((((EXTRACT(minute FROM tuning_signals.ts))::integer / 5))::double precision) * '00:05:00'::interval)) AS bucket,
    tuning_signals.task_type,
    tuning_signals.classifier,
    count(*) AS total,
    avg(tuning_signals.quality_score) AS avg_quality,
    avg(tuning_signals.success_score) AS avg_success,
    avg(tuning_signals.latency_score) AS avg_latency,
    avg(tuning_signals.cost_score) AS avg_cost,
    ((sum(
        CASE
            WHEN tuning_signals.drift_flag THEN 1
            ELSE 0
        END))::double precision / (NULLIF(count(*), 0))::double precision) AS drift_rate
   FROM public.tuning_signals
  WHERE (tuning_signals.ts >= (now() - '7 days'::interval))
  GROUP BY (date_trunc('hour'::text, tuning_signals.ts) + (floor((((EXTRACT(minute FROM tuning_signals.ts))::integer / 5))::double precision) * '00:05:00'::interval)), tuning_signals.task_type, tuning_signals.classifier
  WITH NO DATA;

