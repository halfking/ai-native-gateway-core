--
-- Name: request_logs_bodies_with_current_month; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.request_logs_bodies_with_current_month AS
 SELECT request_logs_bodies_hot.request_id,
    request_logs_bodies_hot.ts,
    request_logs_bodies_hot.request_body,
    request_logs_bodies_hot.outbound_body,
    request_logs_bodies_hot.response_body
   FROM public.request_logs_bodies_hot
UNION ALL
 SELECT request_logs_bodies.request_id,
    request_logs_bodies.ts,
    request_logs_bodies.request_body,
    request_logs_bodies.outbound_body,
    request_logs_bodies.response_body
   FROM public.request_logs_bodies;

