--
-- Name: v_dashboard_user_activity; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_dashboard_user_activity AS
 SELECT user_id,
    tenant_id,
    user_role,
    count(*) AS request_count,
    count(DISTINCT api_path) AS unique_apis,
    max("timestamp") AS last_activity_at,
    (now() - max("timestamp")) AS idle_duration
   FROM public.dashboard_access_events_hot
  WHERE (("timestamp" > (now() - '7 days'::interval)) AND (user_id IS NOT NULL))
  GROUP BY user_id, tenant_id, user_role
  ORDER BY (max("timestamp")) DESC;

