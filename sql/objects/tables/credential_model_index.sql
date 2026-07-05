--
-- Name: credential_model_index; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_model_index (
    bucket timestamp with time zone NOT NULL,
    credential_id bigint NOT NULL,
    raw_model text NOT NULL,
    canonical_id integer,
    billing_mode text,
    unit_price_in_per_1m numeric(10,4),
    unit_price_out_per_1m numeric(10,4),
    context_window integer,
    success_rate numeric(5,4),
    p95_latency_ms integer,
    active_sessions integer DEFAULT 0,
    concurrency_limit integer,
    pressure_ratio numeric(5,4),
    score_smart numeric(8,4),
    score_speed_first numeric(8,4),
    score_cost_first numeric(8,4),
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE credential_model_index; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.credential_model_index IS 'Auto route: per-credential 5min rolled-up live score with 3 profile precomputed';

