--
-- Name: v_dashboard_slow_queries; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_dashboard_slow_queries AS
 SELECT api_path,
    api_method,
    tenant_id,
    user_id,
    response_time_ms,
    "timestamp",
    error_code,
    error_message
   FROM public.dashboard_access_events_hot
  WHERE ((response_time_ms > 1000) AND ("timestamp" > (now() - '24:00:00'::interval)))
  ORDER BY response_time_ms DESC
 LIMIT 100;

