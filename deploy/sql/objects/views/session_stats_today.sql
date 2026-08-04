--
-- Name: session_stats_today; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.session_stats_today AS
 SELECT tenant_id,
    count(*) AS session_count,
    count(*) FILTER (WHERE (last_request_at > (now() - '01:00:00'::interval))) AS active_sessions,
    sum(request_count) AS total_requests,
    sum(total_cost_usd) AS total_cost,
    avg(total_cost_usd) AS avg_cost_per_session,
    avg(total_tokens) AS avg_tokens_per_session,
    avg(avg_latency_ms) AS avg_latency,
    (((count(*) FILTER (WHERE ((compliance_status)::text = 'compliant'::text)))::numeric * 100.0) / (NULLIF(count(*), 0))::numeric) AS compliance_rate,
    (((count(*) FILTER (WHERE (quality_score >= 8)))::numeric * 100.0) / (NULLIF(count(*) FILTER (WHERE (quality_score IS NOT NULL)), 0))::numeric) AS high_quality_rate
   FROM public.session_summaries
  WHERE (first_request_at >= CURRENT_DATE)
  GROUP BY tenant_id;

