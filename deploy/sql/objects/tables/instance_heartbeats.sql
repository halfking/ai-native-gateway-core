--
-- Name: instance_heartbeats; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.instance_heartbeats (
    instance_id text NOT NULL,
    "timestamp" timestamp with time zone DEFAULT now() NOT NULL,
    uptime_secs bigint NOT NULL,
    num_goroutine integer NOT NULL,
    alloc_mb double precision NOT NULL,
    status text NOT NULL,
    metrics jsonb
);

