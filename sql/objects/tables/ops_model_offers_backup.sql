--
-- Name: ops_model_offers_backup; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ops_model_offers_backup (
    backup_id bigint NOT NULL,
    run_tag text NOT NULL,
    backed_at timestamp with time zone DEFAULT now() NOT NULL,
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    canonical_id bigint,
    raw_model_name text NOT NULL,
    p95_latency_ms integer,
    success_rate numeric(5,4),
    available boolean NOT NULL,
    last_seen_at timestamp with time zone NOT NULL
);

