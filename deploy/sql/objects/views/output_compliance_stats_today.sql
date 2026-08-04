--
-- Name: output_compliance_stats_today; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.output_compliance_stats_today AS
 SELECT tenant_id,
    count(*) AS total_issues,
    count(*) FILTER (WHERE (redacted = true)) AS redacted_count,
    count(*) FILTER (WHERE (blocked = true)) AS blocked_count,
    count(*) FILTER (WHERE ((issue_type)::text = 'pii'::text)) AS pii_count,
    count(*) FILTER (WHERE ((issue_type)::text = 'toxic'::text)) AS toxic_count,
    count(*) FILTER (WHERE ((issue_type)::text = 'bias'::text)) AS bias_count,
    count(*) FILTER (WHERE ((issue_type)::text = 'hallucination'::text)) AS hallucination_count,
    avg(severity) AS avg_severity,
    max(severity) AS max_severity
   FROM public.output_compliance_audit
  WHERE (detected_at >= CURRENT_DATE)
  GROUP BY tenant_id;

