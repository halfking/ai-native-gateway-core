--
-- Name: request_logs_bodies_progress; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.request_logs_bodies_progress AS
 SELECT ( SELECT count(*) AS count
           FROM public.request_logs
          WHERE ((request_logs.request_body IS NOT NULL) OR (request_logs.outbound_body IS NOT NULL) OR (request_logs.response_body IS NOT NULL))) AS source_rows_with_body,
    ( SELECT count(*) AS count
           FROM public.request_logs_bodies) AS bodies_rows,
    ( SELECT count(*) AS count
           FROM public.request_logs
          WHERE (((request_logs.request_body IS NOT NULL) OR (request_logs.outbound_body IS NOT NULL) OR (request_logs.response_body IS NOT NULL)) AND (NOT (EXISTS ( SELECT 1
                   FROM public.request_logs_bodies b
                  WHERE ((b.request_id = request_logs.request_id) AND (b.ts = request_logs.ts))))))) AS rows_pending_backfill;

