--
-- Name: v_dashboard_errors; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_dashboard_errors AS
 SELECT api_path,
    error_code,
    count(*) AS error_count,
    max("timestamp") AS last_error_at,
    array_agg(DISTINCT error_message) FILTER (WHERE (error_message IS NOT NULL)) AS error_messages
   FROM public.dashboard_access_events_hot
  WHERE ((status_code >= 400) AND ("timestamp" > (now() - '24:00:00'::interval)))
  GROUP BY api_path, error_code
 HAVING (count(*) > 0)
  ORDER BY (count(*)) DESC;

