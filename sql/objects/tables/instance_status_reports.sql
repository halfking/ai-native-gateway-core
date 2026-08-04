--
-- Name: instance_status_reports; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.instance_status_reports (
    instance_id text NOT NULL,
    "timestamp" timestamp with time zone DEFAULT now() NOT NULL,
    state text NOT NULL,
    active_licenses integer DEFAULT 0 NOT NULL,
    active_devices integer DEFAULT 0 NOT NULL,
    requests_total bigint DEFAULT 0 NOT NULL,
    requests_ok bigint DEFAULT 0 NOT NULL,
    requests_err bigint DEFAULT 0 NOT NULL,
    avg_latency_ms double precision DEFAULT 0 NOT NULL,
    p99_latency_ms double precision DEFAULT 0 NOT NULL
);

