--
-- Name: request_logs_bodies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_logs_bodies (
    request_id text NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    request_body jsonb,
    outbound_body jsonb,
    response_body jsonb
)
PARTITION BY RANGE (ts);

