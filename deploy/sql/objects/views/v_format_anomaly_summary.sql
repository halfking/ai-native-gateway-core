--
-- Name: v_format_anomaly_summary; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_format_anomaly_summary AS
 SELECT date_trunc('hour'::text, detected_at) AS hour,
    provider_code,
    client_model,
    anomaly_type,
    severity,
    count(*) AS anomaly_count,
    count(DISTINCT request_id) AS affected_requests,
    avg(content_size_bytes) AS avg_content_size,
    avg(expected_tokens) AS avg_expected_tokens,
    avg(actual_tokens) AS avg_actual_tokens,
    count(*) FILTER (WHERE resolved) AS resolved_count
   FROM public.response_format_anomalies
  WHERE (detected_at > (now() - '7 days'::interval))
  GROUP BY (date_trunc('hour'::text, detected_at)), provider_code, client_model, anomaly_type, severity;

