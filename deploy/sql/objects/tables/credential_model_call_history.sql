--
-- Name: credential_model_call_history; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_model_call_history (
    credential_id bigint NOT NULL,
    raw_model text NOT NULL,
    window_start timestamp with time zone NOT NULL,
    total_calls integer DEFAULT 0 NOT NULL,
    success_calls integer DEFAULT 0 NOT NULL,
    failed_calls integer DEFAULT 0 NOT NULL,
    avg_latency_ms numeric(8,2),
    p95_latency_ms integer,
    p99_latency_ms integer,
    error_rate_limit_count integer DEFAULT 0 NOT NULL,
    error_quota_count integer DEFAULT 0 NOT NULL,
    error_concurrent_count integer DEFAULT 0 NOT NULL,
    error_network_count integer DEFAULT 0 NOT NULL,
    error_auth_count integer DEFAULT 0 NOT NULL,
    error_other_count integer DEFAULT 0 NOT NULL,
    avg_concurrent numeric(5,2),
    peak_concurrent integer,
    created_at timestamp with time zone DEFAULT now()
);

