--
-- Name: tuning_signals; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tuning_signals (
    id bigint NOT NULL,
    request_id text NOT NULL,
    session_id text,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    task_type text NOT NULL,
    classifier text NOT NULL,
    confidence numeric(4,3),
    chosen_model text,
    canonical_id integer,
    success_score numeric(3,2) DEFAULT 0.5 NOT NULL,
    latency_score numeric(3,2) DEFAULT 0.5 NOT NULL,
    cost_score numeric(3,2) DEFAULT 0.5 NOT NULL,
    drift_flag boolean DEFAULT false NOT NULL,
    quality_score numeric(3,2) DEFAULT 0.5 NOT NULL,
    latency_ms integer,
    cost_usd numeric(10,6),
    prompt_tokens integer,
    completion_tokens integer,
    signal_payload jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    strategy text DEFAULT 'pattern_layered'::text NOT NULL,
    CONSTRAINT tuning_signals_strategy_check CHECK ((strategy = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text])))
);

