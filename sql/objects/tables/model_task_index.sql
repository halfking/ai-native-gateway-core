--
-- Name: model_task_index; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_task_index (
    bucket timestamp with time zone NOT NULL,
    canonical_id integer NOT NULL,
    task_type text NOT NULL,
    sample_count integer DEFAULT 0 NOT NULL,
    success_rate numeric(5,4),
    avg_latency_ms integer,
    p95_latency_ms integer,
    avg_cost_per_1k_usd numeric(10,6),
    primary_credential_id bigint,
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE model_task_index; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.model_task_index IS 'Auto route: per-model-per-task 5min rolled-up performance (success/latency/cost)';

