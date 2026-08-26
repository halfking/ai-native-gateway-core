--
-- Name: request_logs_bodies_hot; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_logs_bodies_hot (
    request_id text NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    request_body jsonb,
    outbound_body jsonb,
    response_body jsonb
)
WITH (fillfactor='90', autovacuum_enabled='true', autovacuum_vacuum_scale_factor='0.05', autovacuum_vacuum_threshold='10', autovacuum_analyze_scale_factor='0.02', autovacuum_analyze_threshold='50');

