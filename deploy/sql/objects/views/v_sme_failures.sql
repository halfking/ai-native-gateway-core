--
-- Name: v_sme_failures; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_sme_failures AS
 SELECT module_name,
    count(*) AS failure_count,
    max(created_at) AS last_failure_at,
    array_agg(DISTINCT error_message) FILTER (WHERE (error_message IS NOT NULL)) AS error_messages
   FROM public.session_module_executions_hot
  WHERE (((status)::text = 'failed'::text) AND (created_at > (now() - '24:00:00'::interval)))
  GROUP BY module_name
 HAVING (count(*) > 0);

