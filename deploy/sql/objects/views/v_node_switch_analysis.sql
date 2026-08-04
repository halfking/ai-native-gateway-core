--
-- Name: v_node_switch_analysis; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_node_switch_analysis AS
 SELECT date_trunc('hour'::text, ts) AS time_bucket,
    node_switch_count AS switches,
    count(*) AS request_count,
    count(*) FILTER (WHERE (success = true)) AS success_count,
    round(((100.0 * (count(*) FILTER (WHERE (success = true)))::numeric) / (count(*))::numeric), 2) AS success_rate,
    round((avg(latency_ms) / 1000.0), 2) AS avg_latency_seconds
   FROM public.request_logs
  WHERE ((ts > (now() - '24:00:00'::interval)) AND (node_switch_count >= 0))
  GROUP BY (date_trunc('hour'::text, ts)), node_switch_count
  ORDER BY (date_trunc('hour'::text, ts)) DESC, node_switch_count;

