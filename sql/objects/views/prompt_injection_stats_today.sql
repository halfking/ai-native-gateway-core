--
-- Name: prompt_injection_stats_today; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.prompt_injection_stats_today AS
 SELECT tenant_id,
    count(*) AS total_detections,
    count(*) FILTER (WHERE (blocked = true)) AS blocked_count,
    count(*) FILTER (WHERE ((risk_level = 10) OR (risk_level = 9))) AS critical_count,
    count(*) FILTER (WHERE ((risk_level >= 7) AND (risk_level <= 8))) AS high_count,
    count(*) FILTER (WHERE ((risk_level >= 4) AND (risk_level <= 6))) AS medium_count,
    count(*) FILTER (WHERE (risk_level <= 3)) AS low_count,
    avg(risk_level) AS avg_score,
    max(risk_level) AS max_score
   FROM public.prompt_injection_detections
  WHERE (detected_at >= CURRENT_DATE)
  GROUP BY tenant_id;

