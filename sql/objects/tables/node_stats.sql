--
-- Name: node_stats; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.node_stats (
    id bigint NOT NULL,
    credential_id bigint,
    raw_model_name text,
    success_rate double precision DEFAULT 0.95,
    p95_latency_ms integer DEFAULT 1000,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now()
);

